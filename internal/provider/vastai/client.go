// Package vastai implements the Vast.ai provider client: HTTP core, offer
// search and instance lifecycle calls.
package vastai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
// attrs are extra slog fields (e.g. instance_id, label) added to every log
// line this call produces.
func (c *Client) do(ctx context.Context, op string, timeout time.Duration, maxRetries int,
	build func() (*http.Request, error), attrs ...any) (int, []byte, error) {

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

		attemptStart := c.clock.Now()
		status, body, transportErr := c.roundTrip(ctx, req, timeout)
		c.logAttempt(op, attempt, status, c.clock.Now().Sub(attemptStart), attrs)

		if transportErr != nil {
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			c.metrics.requestsTotal.WithLabelValues(op, "retryable").Inc()
			if attempt >= maxRetries {
				c.logGiveUp(op, "retryable", status, attrs)
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
				c.logGiveUp(op, "retryable", status, attrs)
				return status, body, &provider.Error{Op: op, Class: provider.Retryable, Status: status, Err: fmt.Errorf("provider returned %d", status)}
			}
			if sleepErr := c.sleep(ctx, c.backoff(attempt)); sleepErr != nil {
				return 0, nil, sleepErr
			}
			continue
		}

		return status, body, nil
	}
}

// logAttempt emits the spec's required per-attempt debug line. It never
// receives the request body, headers or key, so there is nothing to leak.
func (c *Client) logAttempt(op string, attempt, status int, d time.Duration, attrs []any) {
	fields := append([]any{"op", op, "attempt", attempt, "status_code", status, "duration_ms", d.Milliseconds()}, attrs...)
	c.logger.Debug("provider request attempt", fields...)
}

// logGiveUp emits the spec's required warn line for a call that do() itself
// gives up on after exhausting retries.
func (c *Client) logGiveUp(op, outcome string, status int, attrs []any) {
	fields := append([]any{"op", op, "status_code", status, "outcome", outcome}, attrs...)
	c.logger.Warn("provider call failed", fields...)
}

// recordOutcome increments the business-level outcome counter and, for any
// outcome besides ok/adopted, emits the spec's warn log for the call site's
// final classification (do()'s own give-up logging is separate).
func (c *Client) recordOutcome(op, outcome string, status int, attrs ...any) {
	c.metrics.requestsTotal.WithLabelValues(op, outcome).Inc()
	if outcome == "ok" || outcome == "adopted" {
		return
	}
	fields := append([]any{"op", op, "status_code", status, "outcome", outcome}, attrs...)
	c.logger.Warn("provider call failed", fields...)
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

// outcomeLabel maps a business-level *provider.Error to the metric label
// its class warrants; do() only counts retries, so callers own this split.
func outcomeLabel(err *provider.Error) string {
	if err.Class == provider.Fatal {
		return "fatal"
	}
	return "terminal"
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
		fe := fatalOrTerminal("search", status)
		c.recordOutcome("search", outcomeLabel(fe), status)
		return nil, fe
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
		c.recordOutcome("search", "terminal", status)
		return nil, &provider.Error{Op: "search", Class: provider.Terminal, Err: fmt.Errorf("decode offers: %w", err)}
	}
	c.recordOutcome("search", "ok", status)
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
	}, "instance_id", id)
	if err != nil {
		return provider.Instance{}, err
	}
	if status == http.StatusNotFound {
		c.recordOutcome("get", "not_found", status, "instance_id", id)
		return provider.Instance{}, &provider.Error{Op: "get", Class: provider.Terminal, Status: 404, Err: fmt.Errorf("instance %d: %w", id, provider.ErrNotFound)}
	}
	if status < 200 || status >= 300 {
		fe := fatalOrTerminal("get", status)
		c.recordOutcome("get", outcomeLabel(fe), status, "instance_id", id)
		return provider.Instance{}, fe
	}
	var decoded struct {
		Instances instanceDTO `json:"instances"`
	}
	if err := decodeJSON(body, &decoded); err != nil {
		c.recordOutcome("get", "terminal", status, "instance_id", id)
		return provider.Instance{}, &provider.Error{Op: "get", Class: provider.Terminal, Err: fmt.Errorf("decode instance: %w", err)}
	}
	c.recordOutcome("get", "ok", status, "instance_id", id)
	return decoded.Instances.toInstance(), nil
}

// listPage fetches one page of /api/v1/instances; cursor is the opaque
// pagination token from the previous page's response, empty for page 1.
// The request parameter is after_token, per the real API's OpenAPI schema.
func (c *Client) listPage(ctx context.Context, cursor string) ([]instanceDTO, string, error) {
	status, body, err := c.do(ctx, "list", 15*time.Second, 5, func() (*http.Request, error) {
		reqURL := c.baseURL + "/api/v1/instances?limit=25"
		if cursor != "" {
			reqURL += "&after_token=" + url.QueryEscape(cursor)
		}
		return http.NewRequest(http.MethodGet, reqURL, nil)
	})
	if err != nil {
		return nil, "", err
	}
	if status < 200 || status >= 300 {
		fe := fatalOrTerminal("list", status)
		c.recordOutcome("list", outcomeLabel(fe), status)
		return nil, "", fe
	}
	var decoded struct {
		Instances []instanceDTO `json:"instances"`
		NextToken string        `json:"next_token"`
	}
	if err := decodeJSON(body, &decoded); err != nil {
		c.recordOutcome("list", "terminal", status)
		return nil, "", &provider.Error{Op: "list", Class: provider.Terminal, Err: fmt.Errorf("decode page: %w", err)}
	}
	return decoded.Instances, decoded.NextToken, nil
}

// ListInstances pages through /api/v1/instances and filters by label
// prefix; any page failure returns an error and a nil slice, never a
// partial list.
const maxListPages = 1000

func (c *Client) ListInstances(ctx context.Context, labelPrefix string) ([]provider.Instance, error) {
	var out []provider.Instance
	cursor := ""
	for pages := 0; ; pages++ {
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
		if next == cursor {
			c.recordOutcome("list", "terminal", 0)
			return nil, &provider.Error{Op: "list", Class: provider.Terminal, Err: fmt.Errorf("list: pagination did not advance, got repeated token")}
		}
		if pages+1 >= maxListPages {
			c.recordOutcome("list", "terminal", 0)
			return nil, &provider.Error{Op: "list", Class: provider.Terminal, Err: fmt.Errorf("list: exceeded %d pages", maxListPages)}
		}
		cursor = next
	}
	c.recordOutcome("list", "ok", 0)
	return out, nil
}

// DestroyInstance deletes an instance by id.
func (c *Client) DestroyInstance(ctx context.Context, id provider.InstanceID) error {
	status, _, err := c.do(ctx, "destroy", 30*time.Second, 5, func() (*http.Request, error) {
		return http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/v0/instances/%d", c.baseURL, id), nil)
	}, "instance_id", id)
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		c.recordOutcome("destroy", "not_found", status, "instance_id", id)
		return &provider.Error{Op: "destroy", Class: provider.Terminal, Status: 404, Err: fmt.Errorf("instance %d: %w", id, provider.ErrNotFound)}
	}
	if status < 200 || status >= 300 {
		fe := fatalOrTerminal("destroy", status)
		c.recordOutcome("destroy", outcomeLabel(fe), status, "instance_id", id)
		return fe
	}
	c.recordOutcome("destroy", "ok", status, "instance_id", id)
	return nil
}

// listByExactLabel lists every instance and filters to exact label matches;
// ListInstances with an empty prefix returns everything.
func (c *Client) listByExactLabel(ctx context.Context, label string) ([]provider.Instance, error) {
	all, err := c.ListInstances(ctx, "")
	if err != nil {
		return nil, err
	}
	var out []provider.Instance
	for _, inst := range all {
		if inst.Label == label {
			out = append(out, inst)
		}
	}
	return out, nil
}

// adoptByLabel returns the existing instance for a label (0 if none). More
// than one instance under the same label means a previous create's result
// was never reconciled; that is a Terminal error, not a pick-one.
func (c *Client) adoptByLabel(ctx context.Context, label string) (provider.InstanceID, error) {
	found, err := c.listByExactLabel(ctx, label)
	if err != nil {
		return 0, err
	}
	switch len(found) {
	case 0:
		return 0, nil
	case 1:
		return found[0].ID, nil
	default:
		ids := make([]provider.InstanceID, len(found))
		for i, f := range found {
			ids[i] = f.ID
		}
		return 0, &provider.Error{Op: "create", Class: provider.Terminal,
			Err: fmt.Errorf("label %q has %v: %w", label, ids, provider.ErrDuplicateLabel)}
	}
}

// CreateInstance is idempotent on r.Label: it adopts an existing instance
// under that label instead of creating a second one, both before the PUT
// and after an ambiguous (transport/429/5xx) PUT outcome, since the PUT may
// have succeeded on the provider's side even though the response did not
// reach us.
func (c *Client) CreateInstance(ctx context.Context, r provider.CreateRequest) (provider.InstanceID, error) {
	if len(r.Onstart) > 4048 {
		return 0, &provider.Error{Op: "create", Class: provider.Terminal, Err: fmt.Errorf("onstart too long: %d chars", len(r.Onstart))}
	}
	if r.Image == "" || r.Label == "" {
		return 0, &provider.Error{Op: "create", Class: provider.Terminal, Err: fmt.Errorf("image and label are required")}
	}

	if id, err := c.adoptByLabel(ctx, r.Label); err != nil {
		if errors.Is(err, provider.ErrDuplicateLabel) {
			c.recordOutcome("create", "terminal", 0, "label", r.Label)
		}
		return 0, err
	} else if id != 0 {
		c.recordOutcome("create", "adopted", 0, "label", r.Label, "instance_id", id)
		return id, nil
	}

	body, _ := json.Marshal(map[string]any{
		"image": r.Image, "disk": r.DiskGB, "runtype": r.RunType,
		"env": r.Env, "onstart": r.Onstart, "label": r.Label,
	})
	status, respBody, err := c.do(ctx, "create", 60*time.Second, 0, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPut, fmt.Sprintf("%s/api/v0/asks/%d", c.baseURL, r.OfferID), bytes.NewReader(body))
	}, "label", r.Label)

	if err == nil && status >= 200 && status < 300 {
		var decoded struct {
			Success     bool  `json:"success"`
			NewContract int64 `json:"new_contract"`
		}
		// new_contract == 0 is adoptByLabel's "none found" sentinel; a
		// success response carrying it is malformed, not a real id, so it
		// must not be returned as one.
		if decErr := decodeJSON(respBody, &decoded); decErr == nil && decoded.Success && decoded.NewContract != 0 {
			c.recordOutcome("create", "ok", status, "label", r.Label, "instance_id", decoded.NewContract)
			return provider.InstanceID(decoded.NewContract), nil
		}
		// 2xx but success:false, undecodable, or missing an id: terminal,
		// not ambiguous — the provider told us definitively.
		c.recordOutcome("create", "terminal", status, "label", r.Label)
		return 0, &provider.Error{Op: "create", Class: provider.Terminal, Status: status, Err: fmt.Errorf("create rejected")}
	}
	if err == nil && status < 500 && status != 429 {
		// 4xx other than 429: terminal or fatal, not ambiguous.
		fe := fatalOrTerminal("create", status)
		c.recordOutcome("create", outcomeLabel(fe), status, "label", r.Label)
		return 0, fe
	}

	// Ambiguous: transport error, 5xx, or 429 — back off, then list again and
	// adopt rather than retrying the PUT and risking a duplicate.
	if sleepErr := c.sleep(ctx, c.backoff(0)); sleepErr != nil {
		return 0, sleepErr
	}
	id, listErr := c.adoptByLabel(ctx, r.Label)
	if listErr != nil {
		if errors.Is(listErr, provider.ErrDuplicateLabel) {
			c.recordOutcome("create", "terminal", 0, "label", r.Label)
			return 0, listErr
		}
		return 0, &provider.Error{Op: "create", Class: provider.Retryable,
			Err: fmt.Errorf("list after ambiguous create: %w: %w", listErr, provider.ErrOutcomeUnknown)}
	}
	if id != 0 {
		c.recordOutcome("create", "adopted", 0, "label", r.Label, "instance_id", id)
		return id, nil
	}
	// do() already counted this attempt's "retryable" outcome for the 5xx/429/
	// transport error that got us here; CreateInstance must not count it again.
	return 0, &provider.Error{Op: "create", Class: provider.Retryable, Err: provider.ErrOutcomeUnknown}
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
		fe := fatalOrTerminal("balance", status)
		c.recordOutcome("balance", outcomeLabel(fe), status)
		return 0, fe
	}
	var decoded struct {
		Balance float64 `json:"balance"`
	}
	if err := decodeJSON(body, &decoded); err != nil {
		c.recordOutcome("balance", "terminal", status)
		return 0, &provider.Error{Op: "balance", Class: provider.Terminal, Err: fmt.Errorf("decode balance: %w", err)}
	}
	c.recordOutcome("balance", "ok", status)
	return provider.Money(decoded.Balance*1e6 + 0.5), nil
}
