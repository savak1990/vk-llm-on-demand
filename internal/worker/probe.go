package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

// Readiness is where the worker is in its startup sequence, as seen
// through Probe.
type Readiness int

const (
	Unreachable Readiness = iota // nothing on 127.0.0.1:8080 yet (downloading)
	Loading                      // /health 503, or /v1/models without the alias
	Ready                        // /health 200 and the alias is in /v1/models
)

const workerBaseURL = "http://127.0.0.1:8080"

const probeTimeout = 5 * time.Second

// Client talks to the worker over the tunnel's loopback transport.
type Client struct {
	Transport http.RoundTripper // from tunnel.Dialer
	Key       string
	Alias     string
}

func (c Client) httpClient() *http.Client {
	return &http.Client{Transport: c.Transport}
}

// isUnreachable reports whether err is the local or tunnel-shaped
// connection-refused/reset error, i.e. nothing is listening yet. It does
// not match timeouts or DNS failures, which are real errors.
func isUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var chanErr *ssh.OpenChannelError
	return errors.As(err, &chanErr) && chanErr.Reason == ssh.ConnectionFailed
}

// Probe reports the worker's readiness.
func (c Client) Probe(ctx context.Context) (Readiness, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	hc := c.httpClient()

	healthReq, err := http.NewRequestWithContext(ctx, http.MethodGet, workerBaseURL+"/health", nil)
	if err != nil {
		return Unreachable, err
	}
	healthResp, err := hc.Do(healthReq)
	if err != nil {
		if isUnreachable(err) {
			return Unreachable, nil
		}
		return Unreachable, err
	}
	defer func() { _ = healthResp.Body.Close() }()

	switch healthResp.StatusCode {
	case 503:
		return Loading, nil
	case 200:
		// fall through to the models check
	default:
		return Unreachable, fmt.Errorf("worker: unexpected /health status %d", healthResp.StatusCode)
	}

	modelsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, workerBaseURL+"/v1/models", nil)
	if err != nil {
		return Unreachable, err
	}
	modelsReq.Header.Set("Authorization", "Bearer "+c.Key)
	modelsResp, err := hc.Do(modelsReq)
	if err != nil {
		if isUnreachable(err) {
			return Unreachable, nil
		}
		return Unreachable, err
	}
	defer func() { _ = modelsResp.Body.Close() }()

	switch modelsResp.StatusCode {
	case 401, 403:
		return Unreachable, fmt.Errorf("worker: worker key rejected")
	case 200:
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(modelsResp.Body).Decode(&body); err != nil {
			return Loading, fmt.Errorf("worker: decoding /v1/models response: %w", err)
		}
		for _, m := range body.Data {
			if m.ID == c.Alias {
				return Ready, nil
			}
		}
		return Loading, fmt.Errorf("worker: alias missing from /v1/models")
	default:
		return Unreachable, fmt.Errorf("worker: unexpected /v1/models status %d", modelsResp.StatusCode)
	}
}
