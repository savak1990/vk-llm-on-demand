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
