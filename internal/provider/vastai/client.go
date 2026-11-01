// Package vastai implements the Vast.ai provider client: HTTP core, offer
// search and instance lifecycle calls.
package vastai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

const maxBodyBytes = 8 << 20

// Client is the Vast.ai HTTP client: timeouts, error classification and
// 429/5xx backoff.
type Client struct {
	baseURL    string
	httpClient *http.Client
	apiKey     func() (string, error)
	clock      clock.Clock
	rndMu      sync.Mutex
	rnd        *rand.Rand
	logger     *slog.Logger
	metrics    *metrics
}

// New builds a Client; apiKey is called on every request so a rotated key
// takes effect without rebuilding the client.
func New(baseURL string, httpClient *http.Client, apiKey func() (string, error),
	c clock.Clock, rnd *rand.Rand, logger *slog.Logger, reg prometheus.Registerer) *Client {
	return &Client{
		baseURL: baseURL, httpClient: httpClient, apiKey: apiKey,
		clock: c, rnd: rnd, logger: logger, metrics: newMetrics(reg),
	}
}

// sleep waits d or ctx.Done(). A Ticker (not After) guarantees Stop() runs
// on every exit, so the fake clock never keeps a waiter past this call.
func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	t := c.clock.NewTicker(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoff returns a jittered delay for retry attempt n, capped at 30s.
func (c *Client) backoff(n int) time.Duration {
	if n > 4 { // 2s<<4 already exceeds the cap; clamping avoids overflow for large n
		n = 4
	}
	d := 2 * time.Second << n
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	half := d / 2
	c.rndMu.Lock()
	jitter := c.rnd.Int63n(int64(half))
	c.rndMu.Unlock()
	return half + time.Duration(jitter)
}

// do retries a transport error, 429 or 5xx up to maxRetries times with
// jittered backoff; other statuses are returned for the caller to classify.
func (c *Client) do(ctx context.Context, op string, timeout time.Duration, maxRetries int,
	build func() (*http.Request, error)) (int, []byte, error) {

	start := c.clock.Now()
	defer func() {
		c.metrics.requestSeconds.WithLabelValues(op).Observe(c.clock.Now().Sub(start).Seconds())
	}()

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		key, err := c.apiKey()
		if err != nil {
			return 0, nil, &provider.Error{Op: op, Class: provider.Fatal, Err: fmt.Errorf("read api key: %w", err)}
		}
		req, err := build()
		if err != nil {
			return 0, nil, &provider.Error{Op: op, Class: provider.Terminal, Err: err}
		}
		req.Header.Set("Authorization", "Bearer "+key)

		status, body, transportErr := c.roundTrip(ctx, req, timeout)
		if transportErr != nil {
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			c.metrics.requestsTotal.WithLabelValues(op, "retryable").Inc()
			if attempt >= maxRetries {
				return 0, nil, &provider.Error{Op: op, Class: provider.Retryable, Err: transportErr}
			}
			if sleepErr := c.sleep(ctx, c.backoff(attempt)); sleepErr != nil {
				return 0, nil, sleepErr
			}
			continue
		}

		if status == http.StatusTooManyRequests || status >= 500 {
			if status == http.StatusTooManyRequests {
				c.metrics.rateLimited.Inc()
			}
			c.metrics.requestsTotal.WithLabelValues(op, "retryable").Inc()
			if attempt >= maxRetries {
				return status, body, &provider.Error{Op: op, Class: provider.Retryable, Status: status, Err: fmt.Errorf("provider returned %d", status)}
			}
			if sleepErr := c.sleep(ctx, c.backoff(attempt)); sleepErr != nil {
				return 0, nil, sleepErr
			}
			continue
		}

		c.metrics.requestsTotal.WithLabelValues(op, "ok").Inc()
		return status, body, nil
	}
}

// roundTrip cancels its per-attempt timeout only after the body is read,
// or a deadline firing right after headers return would truncate it.
func (c *Client) roundTrip(ctx context.Context, req *http.Request, timeout time.Duration) (int, []byte, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req = req.WithContext(attemptCtx)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

// fatalOrTerminal classifies a non-retryable status: 401/403 are Fatal (the
// key is bad, retrying never helps), anything else is Terminal.
func fatalOrTerminal(op string, status int) *provider.Error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &provider.Error{Op: op, Class: provider.Fatal, Status: status, Err: fmt.Errorf("auth failed")}
	}
	return &provider.Error{Op: op, Class: provider.Terminal, Status: status, Err: fmt.Errorf("request failed")}
}

func decodeJSON(body []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	return d.Decode(v)
}

// SearchOffers queries /bundles with the given filter and applies the
// local price cap and denylist (the server filter is not the guarantee).
func (c *Client) SearchOffers(ctx context.Context, f provider.OfferFilter) ([]provider.Offer, error) {
	body, err := json.Marshal(buildFilterBody(f))
	if err != nil {
		return nil, &provider.Error{Op: "search", Class: provider.Terminal, Err: err}
	}
	status, respBody, err := c.do(ctx, "search", 30*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, c.baseURL+"/api/v0/bundles", bytes.NewReader(body))
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fatalOrTerminal("search", status)
	}
	var decoded struct {
		Offers []struct {
			ID          int64   `json:"id"`
			MachineID   int64   `json:"machine_id"`
			GPUName     string  `json:"gpu_name"`
			Geolocation string  `json:"geolocation"`
			NumGPUs     int     `json:"num_gpus"`
			DphTotal    float64 `json:"dph_total"`
			Reliability float64 `json:"reliability2"`
			InetDown    float64 `json:"inet_down"`
			DiskBw      float64 `json:"disk_bw"`
			CudaMaxGood float64 `json:"cuda_max_good"`
		} `json:"offers"`
	}
	if err := decodeJSON(respBody, &decoded); err != nil {
		return nil, &provider.Error{Op: "search", Class: provider.Terminal, Err: fmt.Errorf("decode offers: %w", err)}
	}
	out := make([]provider.Offer, 0, len(decoded.Offers))
	for _, o := range decoded.Offers {
		out = append(out, provider.Offer{
			ID: o.ID, MachineID: o.MachineID, GPUName: o.GPUName, Geolocation: o.Geolocation,
			NumGPUs: o.NumGPUs, Hourly: provider.Money(o.DphTotal*1e6 + 0.5),
			Reliability: o.Reliability, InetDownMBps: o.InetDown, DiskBwMBps: o.DiskBw, CUDAMaxGood: o.CudaMaxGood,
		})
	}
	return provider.Select(out, f), nil
}

type instanceDTO struct {
	ID           provider.InstanceID `json:"id"`
	Label        string              `json:"label"`
	ActualStatus string              `json:"actual_status"`
	StatusMsg    string              `json:"status_msg"`
	MachineID    int64               `json:"machine_id"`
	PublicIP     string              `json:"public_ipaddr"`
	SSHHost      string              `json:"ssh_host"`
	SSHPort      int                 `json:"ssh_port"`
	DphTotal     float64             `json:"dph_total"`
	StartDate    float64             `json:"start_date"`
}

func (d instanceDTO) toInstance() provider.Instance {
	return provider.Instance{
		ID: d.ID, Label: d.Label, StatusMsg: d.StatusMsg,
		Status: provider.MapStatus(d.ActualStatus), RawStatus: d.ActualStatus,
		MachineID: d.MachineID, PublicIP: d.PublicIP, SSHHost: d.SSHHost, SSHPort: d.SSHPort,
		Hourly:    provider.Money(d.DphTotal*1e6 + 0.5),
		StartedAt: time.Unix(int64(d.StartDate), 0),
	}
}

// GetInstance fetches one instance by id.
func (c *Client) GetInstance(ctx context.Context, id provider.InstanceID) (provider.Instance, error) {
	status, body, err := c.do(ctx, "get", 15*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v0/instances/%d", c.baseURL, id), nil)
	})
	if err != nil {
		return provider.Instance{}, err
	}
	if status == http.StatusNotFound {
		return provider.Instance{}, &provider.Error{Op: "get", Class: provider.Terminal, Status: 404, Err: fmt.Errorf("instance %d: %w", id, provider.ErrNotFound)}
	}
	if status < 200 || status >= 300 {
		return provider.Instance{}, fatalOrTerminal("get", status)
	}
	var dto instanceDTO
	if err := decodeJSON(body, &dto); err != nil {
		return provider.Instance{}, &provider.Error{Op: "get", Class: provider.Terminal, Err: fmt.Errorf("decode instance: %w", err)}
	}
	return dto.toInstance(), nil
}

// listPage fetches one page of /api/v1/instances; cursor is the opaque
// pagination token from the previous page's response, empty for page 1.
func (c *Client) listPage(ctx context.Context, cursor string) ([]instanceDTO, string, error) {
	status, body, err := c.do(ctx, "list", 15*time.Second, 5, func() (*http.Request, error) {
		reqURL := c.baseURL + "/api/v1/instances?limit=25"
		if cursor != "" {
			reqURL += "&cursor=" + url.QueryEscape(cursor)
		}
		return http.NewRequest(http.MethodGet, reqURL, nil)
	})
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		return nil, "", fatalOrTerminal("list", status)
	}
	var decoded struct {
		Instances  []instanceDTO `json:"instances"`
		NextCursor string        `json:"next_cursor"`
	}
	if err := decodeJSON(body, &decoded); err != nil {
		return nil, "", &provider.Error{Op: "list", Class: provider.Terminal, Err: fmt.Errorf("decode page: %w", err)}
	}
	return decoded.Instances, decoded.NextCursor, nil
}

// ListInstances pages through /api/v1/instances and filters by label
// prefix; any page failure returns an error and a nil slice, never a
// partial list.
func (c *Client) ListInstances(ctx context.Context, labelPrefix string) ([]provider.Instance, error) {
	var out []provider.Instance
	cursor := ""
	for {
		page, next, err := c.listPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, d := range page {
			if labelPrefix == "" || strings.HasPrefix(d.Label, labelPrefix) {
				out = append(out, d.toInstance())
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return out, nil
}

// DestroyInstance deletes an instance by id.
func (c *Client) DestroyInstance(ctx context.Context, id provider.InstanceID) error {
	status, _, err := c.do(ctx, "destroy", 30*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/v0/instances/%d", c.baseURL, id), nil)
	})
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fatalOrTerminal("destroy", status)
	}
	return nil
}

// Balance reports the account's current balance.
func (c *Client) Balance(ctx context.Context) (provider.Money, error) {
	status, body, err := c.do(ctx, "balance", 15*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, c.baseURL+"/api/v0/users/current", nil)
	})
	if err != nil {
		return 0, err
	}
	if status < 200 || status >= 300 {
		return 0, fatalOrTerminal("balance", status)
	}
	var decoded struct {
		Balance float64 `json:"balance"`
	}
	if err := decodeJSON(body, &decoded); err != nil {
		return 0, &provider.Error{Op: "balance", Class: provider.Terminal, Err: fmt.Errorf("decode balance: %w", err)}
	}
	return provider.Money(decoded.Balance*1e6 + 0.5), nil
}
