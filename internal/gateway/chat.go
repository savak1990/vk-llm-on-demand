package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

// clampMaxTokens returns the value to send upstream for max_tokens:
// cap if absent or over cap, otherwise the requested value. clamped
// is true whenever the returned value differs from what was requested.
func clampMaxTokens(body map[string]json.RawMessage, maxCap int) (clamped bool, value int, err error) {
	raw, ok := body["max_tokens"]
	if !ok {
		raw, ok = body["max_completion_tokens"]
	}
	if !ok {
		return true, maxCap, nil
	}
	var requested int
	if err := json.Unmarshal(raw, &requested); err != nil {
		return false, 0, errors.New("max_tokens must be an integer")
	}
	if requested > maxCap {
		return true, maxCap, nil
	}
	return false, requested, nil
}

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body exceeds the size limit")
		return
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "request body is not valid json")
		return
	}

	var model string
	if raw, ok := body["model"]; ok {
		_ = json.Unmarshal(raw, &model)
	}
	if model != g.deps.Config.ModelAlias {
		writeError(w, http.StatusNotFound, "invalid_request_error", "model_not_found", "the requested model does not exist")
		return
	}

	stream := false
	if raw, ok := body["stream"]; ok {
		if err := json.Unmarshal(raw, &stream); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "stream must be a boolean")
			return
		}
	}

	_, maxTokens, err := clampMaxTokens(body, g.deps.Config.MaxTokensCap)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	body["max_tokens"] = json.RawMessage(strconv.Itoa(maxTokens))
	delete(body, "max_completion_tokens")

	status := g.deps.Controller.Status()
	gen := status.Generation
	rt, err := g.deps.Controller.Admit(gen)
	switch {
	case errors.Is(err, controller.ErrBackendUnavailable), errors.Is(err, controller.ErrDraining):
		// No response headers have been sent yet, so an Admit failure is
		// always a plain status — even for stream:true — since the SSE
		// content-type only gets set once a 2xx actually arrives upstream.
		writeBackendUnavailable(w)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "internal error")
		return
	}
	defer g.deps.Controller.RequestEnd(gen)

	g.proxyChat(w, r, gen, rt, body, stream)
}

func (g *Gateway) proxyChat(w http.ResponseWriter, r *http.Request, gen int64, rt http.RoundTripper, body map[string]json.RawMessage, stream bool) {
	payload, err := json.Marshal(body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "internal error")
		return
	}

	timeout := g.deps.Config.GenerationTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	key := worker.WorkerKey(g.deps.Seed.Bytes(), g.deps.InstallationID, gen)

	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://worker/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "internal error")
		return
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+key)
	if stream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	}

	client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	start := g.deps.Clock.Now() // captured before the round trip, so ttft_seconds measures the real wait, not just time inside streamUpstream
	resp, err := client.Do(upstreamReq)
	if err != nil {
		// No response headers have been sent to the client yet, so
		// this is always a plain status — never SSE, regardless of
		// stream — even for a request that asked to stream.
		outcome := g.classify(r, err)
		if outcome == outcomeUpstreamError {
			g.deps.Controller.ReportUpstreamFailure(gen)
		}
		g.writePreHeaderOutcome(w, outcome)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden, resp.StatusCode >= 500,
		resp.StatusCode >= 300 && resp.StatusCode < 400:
		// The upstream answered, but with a status this gateway never
		// forwards as-is. Headers still haven't been sent to the
		// client, so this is plain too, regardless of stream.
		g.deps.Controller.ReportUpstreamFailure(gen)
		writeError(w, http.StatusBadGateway, "server_error", "upstream_error", "the worker rejected the request")
	case resp.StatusCode >= 400:
		g.copyUpstream4xx(w, resp)
	case stream:
		g.streamUpstream(w, resp, gen, r, start)
	default:
		g.copyUpstream2xx(w, resp)
	}
}

// copyUpstream2xx passes a confirmed 2xx plain response straight through.
func (g *Gateway) copyUpstream2xx(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// copyUpstream4xx passes through a client-error body that already looks
// like this gateway's own {"error":...} shape unchanged; any other 4xx
// body is wrapped so the client still gets a parseable error.
func (g *Gateway) copyUpstream4xx(w http.ResponseWriter, resp *http.Response) {
	raw, _ := io.ReadAll(resp.Body)
	var probe errorBody
	if json.Unmarshal(raw, &probe) == nil && probe.Error.Message != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}
	writeError(w, resp.StatusCode, "invalid_request_error", "upstream_rejected", "the worker rejected the request")
}

type upstreamOutcome int

const (
	outcomeClientGone upstreamOutcome = iota
	outcomeShuttingDown
	outcomeTimeout
	outcomeDrainCutoff
	outcomeUpstreamError
)

// classify decides why an upstream call failed, checking a disconnected
// client, this gateway's own shutdown, the deadline, and a drain cutoff
// before falling back to a genuine upstream failure worth reporting.
func (g *Gateway) classify(r *http.Request, err error) upstreamOutcome {
	if r.Context().Err() != nil {
		return outcomeClientGone
	}
	if g.shuttingDown.Load() {
		return outcomeShuttingDown
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return outcomeTimeout
	}
	if g.isDrainCutoff() {
		return outcomeDrainCutoff
	}
	return outcomeUpstreamError
}

// isDrainCutoff checks the internal State enum, never the Public string:
// PublicState(Destroying, failing=true) reports "FAILED", which a
// "DRAINING"/"DELETING" string compare would silently miss.
func (g *Gateway) isDrainCutoff() bool {
	state := g.deps.Controller.Status().State
	return state == controller.Draining || state == controller.Destroying
}

// writePreHeaderOutcome writes a plain status for an outcome that
// happened before any response headers were sent to the client.
func (g *Gateway) writePreHeaderOutcome(w http.ResponseWriter, outcome upstreamOutcome) {
	switch outcome {
	case outcomeClientGone:
		w.WriteHeader(499)
	case outcomeShuttingDown:
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "server_error", "shutting_down", "the server is shutting down")
	case outcomeTimeout:
		writeError(w, http.StatusGatewayTimeout, "server_error", "upstream_timeout", "the worker did not respond in time")
	case outcomeDrainCutoff:
		writeBackendUnavailable(w)
	default:
		writeError(w, http.StatusBadGateway, "server_error", "upstream_error", "failed to reach the worker")
	}
}

func inStreamError(code, message string) string {
	b, _ := json.Marshal(errorBody{Error: errorDetail{Message: message, Type: "server_error", Code: code}})
	return "data: " + string(b) + "\n\n"
}

// streamUpstream streams a confirmed 2xx SSE response to the client,
// observing the first-token latency and forwarding every error after
// the 200/text-event-stream headers go out through writeInStreamOutcome.
func (g *Gateway) streamUpstream(w http.ResponseWriter, resp *http.Response, gen int64, r *http.Request, start time.Time) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)

	firstByte := true
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if firstByte {
				g.deps.Controller.ObserveFirstToken(gen, g.deps.Clock.Now().Sub(start))
				firstByte = false
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			if err != io.EOF {
				outcome := g.classify(r, err)
				if outcome == outcomeUpstreamError {
					g.deps.Controller.ReportUpstreamFailure(gen)
				}
				g.writeInStreamOutcome(w, rc, outcome)
			}
			return
		}
	}
}

// writeInStreamOutcome writes the in-stream error row for an outcome
// discovered after headers already went out; a second WriteHeader call
// would be silently ignored by net/http, so setCodeOverride is how the real status reaches metrics.
func (g *Gateway) writeInStreamOutcome(w http.ResponseWriter, rc *http.ResponseController, outcome upstreamOutcome) {
	switch outcome {
	case outcomeClientGone:
		setCodeOverride(w, "499")
		return // nothing to write, the client is already gone
	case outcomeShuttingDown:
		setCodeOverride(w, "stream_error")
		_, _ = io.WriteString(w, inStreamError("shutting_down", "the server is shutting down")+"data: [DONE]\n\n")
	case outcomeTimeout:
		setCodeOverride(w, "stream_error")
		_, _ = io.WriteString(w, inStreamError("upstream_timeout", "the worker did not respond in time")+"data: [DONE]\n\n")
	case outcomeDrainCutoff:
		setCodeOverride(w, "stream_error")
		_, _ = io.WriteString(w, backendUnavailableSSE())
	default:
		setCodeOverride(w, "stream_error")
		_, _ = io.WriteString(w, inStreamError("upstream_error", "connection to the worker was lost")+"data: [DONE]\n\n")
	}
	_ = rc.Flush()
}
