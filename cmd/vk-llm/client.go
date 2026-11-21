package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

var version = "dev"

type statusDoc struct {
	Provider   string `json:"provider"`
	State      string `json:"state"`
	Desired    string `json:"desired"`
	Generation int64  `json:"generation"`
	Instance   *struct {
		GPU             string     `json:"gpu"`
		MachineID       int64      `json:"machineId"`
		PriceUSDPerHour float64    `json:"priceUSDPerHour"`
		CreatedAt       *time.Time `json:"createdAt"`
		ReadyAt         *time.Time `json:"readyAt"`
	} `json:"instance"`
	Model struct {
		Alias       string `json:"alias"`
		ContextSize int    `json:"contextSize"`
	} `json:"model"`
	Ready                    bool       `json:"ready"`
	UptimeSeconds            int64      `json:"uptimeSeconds"`
	LastInferenceFinishedAt  *time.Time `json:"lastInferenceFinishedAt"`
	IdleDeadline             *time.Time `json:"idleDeadline"`
	SecondsUntilIdleShutdown *int64     `json:"secondsUntilIdleShutdown"`
	Cost                     struct {
		EstimatedInstanceUSD float64 `json:"estimatedInstanceUSD"`
		EstimatedMonthUSD    float64 `json:"estimatedMonthUSD"`
		BudgetMonthUSD       float64 `json:"budgetMonthUSD"`
	} `json:"cost"`
	Unavailable    string     `json:"unavailable"`
	NextCreateAt   *time.Time `json:"nextCreateAt"`
	InstallationID string     `json:"installationId"`
	LabelPrefix    string     `json:"labelPrefix"`
	Result         string     `json:"result,omitempty"`
}

type apiResult struct {
	doc statusDoc
	raw []byte
}

type apiError struct {
	Code    string
	Message string
	Reason  string
	// RetryAfterSeconds is set from a 429 response's Retry-After header,
	// so the caller's poll loop can wait that long instead of the normal tick.
	RetryAfterSeconds int
}

func (e *apiError) Error() string {
	s := e.Code
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

type client struct {
	http     *http.Client
	endpoint string
	token    string
}

func newClient(cfg Config) *client {
	return &client{
		http: &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		endpoint: cfg.Endpoint,
		token:    cfg.Token,
	}
}

func (c *client) start(ctx context.Context) (apiResult, int, error) {
	return c.do(ctx, http.MethodPost, "/api/v1/gpu/start", nil)
}

func (c *client) status(ctx context.Context) (apiResult, int, error) {
	return c.do(ctx, http.MethodGet, "/api/v1/gpu/status", nil)
}

func (c *client) stop(ctx context.Context, force bool) (apiResult, int, error) {
	var body io.Reader
	if force {
		body = bytes.NewReader([]byte(`{"force":true}`))
	}
	return c.do(ctx, http.MethodPost, "/api/v1/gpu/stop", body)
}

func (c *client) setBudget(ctx context.Context, usd float64) (apiResult, int, error) {
	body := bytes.NewReader([]byte(fmt.Sprintf(`{"monthlyUSD":%s}`, strconv.FormatFloat(usd, 'f', -1, 64))))
	return c.do(ctx, http.MethodPost, "/api/v1/gpu/budget", body)
}

func (c *client) do(ctx context.Context, method, path string, body io.Reader) (apiResult, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return apiResult{}, 1, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "vk-llm/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return apiResult{}, 1, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return apiResult{}, 3, decodeAPIError(raw, resp.StatusCode)
	case resp.StatusCode == 429:
		return apiResult{}, 1, decodeRetryAfter(raw, resp)
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return apiResult{}, 1, fmt.Errorf("unexpected redirect: %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return apiResult{}, 1, decodeAPIError(raw, resp.StatusCode)
	}

	var doc statusDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return apiResult{}, 1, fmt.Errorf("bad response body: %w", err)
	}
	return apiResult{doc: doc, raw: raw}, 0, nil
}

func decodeAPIError(raw []byte, statusCode int) error {
	var wire struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Reason  string `json:"reason"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || wire.Error.Code == "" {
		return fmt.Errorf("http %d", statusCode)
	}
	return &apiError{Code: wire.Error.Code, Message: wire.Error.Message, Reason: wire.Error.Reason}
}

// decodeRetryAfter builds the 429 error, carrying the Retry-After header
// even when the body doesn't decode into the usual error shape.
func decodeRetryAfter(raw []byte, resp *http.Response) error {
	err := decodeAPIError(raw, resp.StatusCode)
	var ae *apiError
	if !errors.As(err, &ae) {
		ae = &apiError{Code: "rate_limited"}
	}
	if secs, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil {
		ae.RetryAfterSeconds = secs
	}
	return ae
}
