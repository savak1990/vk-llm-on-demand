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

// clampMaxTokens clamps each field independently and mutates body in
// place; <= 0 is treated as absent because llama-server reads that as
// "unlimited", the exact bypass this cap exists to prevent.
func clampMaxTokens(body map[string]json.RawMessage, maxCap int) (clamped bool, requested int, err error) {
	mtPresent, mtVal, mtClamped, err := clampField(body, "max_tokens", maxCap)
	if err != nil {
		return false, 0, err
	}
	mctPresent, mctVal, mctClamped, err := clampField(body, "max_completion_tokens", maxCap)
	if err != nil {
		return false, 0, err
	}
	switch {
	case !mtPresent && !mctPresent:
		body["max_tokens"] = json.RawMessage(strconv.Itoa(maxCap))
		return true, 0, nil
	case !mtPresent:
		// llama-server's support for max_completion_tokens is unconfirmed
		// (open question), so also send max_tokens with the same already
		// clamped value rather than leaving the cap unenforceable.
		body["max_tokens"] = body["max_completion_tokens"]
	}
	requested = mctVal
	if mtPresent {
		requested = mtVal
	}
	return mtClamped || mctClamped, requested, nil
}

// clampField clamps one field of body in place, if present.
func clampField(body map[string]json.RawMessage, key string, maxCap int) (present bool, original int, clamped bool, err error) {
	raw, ok := body[key]
	if !ok {
		return false, 0, false, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return true, 0, false, errors.New(key + " must be an integer")
	}
	want := v
	if v <= 0 || v > maxCap {
		want = maxCap
	}
	if want == v {
		return true, v, false, nil
	}
	body[key] = json.RawMessage(strconv.Itoa(want))
	return true, v, true, nil
}

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", "request body exceeds the size limit")
		case r.Context().Err() != nil:
			w.WriteHeader(499)
		default:
			writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", "could not read request body")
		}
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
	rec := recorderOf(w)
	rec.stream = stream

	clamped, requested, err := clampMaxTokens(body, g.deps.Config.MaxTokensCap)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
		return
	}
	rec.clamped = clamped
	rec.maxTokensRequested = requested

	stripProgress(body)

	status := g.deps.Controller.Status()
	gen := status.Generation
	rt, err := g.deps.Controller.Admit(gen)
	switch {
	case g.onDemand() && errors.Is(err, controller.ErrBackendUnavailable):
		if stream {
			g.holdAndProxy(w, r, body)
		} else {
			g.refuseColdStart(w, r)
		}
		return
	case g.onDemand() && errors.Is(err, controller.ErrDraining):
		g.writeShuttingDownOnDemand(w)
		return
	case errors.Is(err, controller.ErrBackendUnavailable), errors.Is(err, controller.ErrDraining):
		// No response headers have been sent yet, so an Admit failure is
		// always a plain status — even for stream:true — since the SSE
		// content-type only gets set once a 2xx actually arrives upstream.
		rec.outcome = "backend_unavailable"
		writeBackendUnavailable(w)
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "internal error")
		return
	}
	rec.gen = gen
	defer g.deps.Controller.RequestEnd(gen)

	g.proxyChat(w, r, gen, rt, body, stream, false)
}

func (g *Gateway) proxyChat(w http.ResponseWriter, r *http.Request, gen int64, rt http.RoundTripper, body map[string]json.RawMessage, stream, held bool) {
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
	id := g.registerActive(cancel)
	defer func() {
		g.unregisterActive(id)
		cancel()
	}()

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
		// Outside held mode no headers have gone out yet, so this is a plain status even for stream:true.
		outcome := g.classify(r, err)
		if outcome == outcomeUpstreamError {
			g.deps.Controller.ReportUpstreamFailure(gen)
		}
		if held {
			g.writeInStreamOutcome(w, http.NewResponseController(w), outcome, true)
			return
		}
		g.writePreHeaderOutcome(w, outcome)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden, resp.StatusCode >= 500,
		resp.StatusCode >= 300 && resp.StatusCode < 400:
		// A status never forwarded as-is; outside held mode no headers have gone out, so it is plain.
		g.deps.Controller.ReportUpstreamFailure(gen)
		if held {
			g.writeHeldTail(w, http.NewResponseController(w), "upstream_error", "the worker rejected the request")
			return
		}
		writeError(w, http.StatusBadGateway, "server_error", "upstream_error", "the worker rejected the request")
	case resp.StatusCode >= 400:
		if held {
			_, message, _ := g.readUpstream4xx(resp)
			g.writeHeldTail(w, http.NewResponseController(w), "upstream_rejected", message)
			return
		}
		g.copyUpstream4xx(w, resp)
	case stream:
		g.streamUpstream(w, resp, gen, r, start, held)
	default:
		g.copyUpstream2xx(w, resp, r, start)
	}
}

// cappedBuffer tees into a bounded buffer for a usage parse; io.Copy
// requires a tee target to report every byte written, so Write always
// returns len(p), silently discarding bytes once the cap is reached.
type cappedBuffer struct {
	buf bytes.Buffer
	cap int64
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.cap - int64(c.buf.Len()); room > 0 {
		n := int64(len(p))
		if n > room {
			n = room
		}
		c.buf.Write(p[:n])
	}
	return len(p), nil
}

// copyUpstream2xx passes a confirmed 2xx plain response straight through,
// teeing it into a capped buffer to read usage.completion_tokens once the
// copy finishes, for the plain-request tokens/s metric.
func (g *Gateway) copyUpstream2xx(w http.ResponseWriter, resp *http.Response, r *http.Request, start time.Time) {
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)

	tee := &cappedBuffer{cap: g.deps.Config.MaxBodyBytes}
	if _, err := io.Copy(io.MultiWriter(w, tee), resp.Body); err != nil {
		// The client may already have a truncated body; that matches
		// ordinary HTTP body-copy behaviour, but the failure still needs
		// to be visible, so the access log's outcome field carries it.
		recorderOf(w).outcome = outcomeLabel(g.classify(r, err))
		return
	}

	var parsed struct {
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(tee.buf.Bytes(), &parsed) == nil && parsed.Usage.CompletionTokens > 0 {
		if elapsed := g.deps.Clock.Now().Sub(start).Seconds(); elapsed > 0 {
			g.metrics.outputTokensPerSecond.Observe(float64(parsed.Usage.CompletionTokens) / elapsed)
		}
	}
}

// upstream4xxProbe tolerates a non-string type/code (e.g. llama-server
// sending an integer code), which errorBody's strict string fields would
// otherwise reject outright, losing the upstream message for no reason.
type upstream4xxProbe struct {
	Error struct {
		Message string          `json:"message"`
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
	} `json:"error"`
}

func isJSONString(raw json.RawMessage) bool {
	var s string
	return len(raw) > 0 && json.Unmarshal(raw, &s) == nil && s != ""
}

// copyUpstream4xx passes a client-error body through only when it
// already has all three fields as non-empty strings; anything else is
// normalized, keeping the upstream message where there is one.
func (g *Gateway) copyUpstream4xx(w http.ResponseWriter, resp *http.Response) {
	raw, message, complete := g.readUpstream4xx(resp)
	if complete {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return
	}
	writeError(w, resp.StatusCode, "invalid_request_error", "upstream_rejected", message)
}

// readUpstream4xx returns the body, its message (or a fallback), and whether all three fields are strings.
func (g *Gateway) readUpstream4xx(resp *http.Response) (raw []byte, message string, complete bool) {
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, g.deps.Config.MaxBodyBytes))
	var probe upstream4xxProbe
	ok := json.Unmarshal(raw, &probe) == nil
	message = probe.Error.Message
	if message == "" {
		message = "the worker rejected the request"
	}
	return raw, message, ok && probe.Error.Message != "" && isJSONString(probe.Error.Type) && isJSONString(probe.Error.Code)
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

// outcomeLabel names an upstreamOutcome for the access log's outcome
// field, independent of the HTTP status or in-stream code a given path
// writes for the same outcome.
func outcomeLabel(o upstreamOutcome) string {
	switch o {
	case outcomeClientGone:
		return "client_gone"
	case outcomeShuttingDown:
		return "shutting_down"
	case outcomeTimeout:
		return "upstream_timeout"
	case outcomeDrainCutoff:
		return "backend_unavailable"
	default:
		return "upstream_error"
	}
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
func (g *Gateway) streamUpstream(w http.ResponseWriter, resp *http.Response, gen int64, r *http.Request, start time.Time, held bool) {
	if !held {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}
	rc := http.NewResponseController(w)

	firstByte := true
	chunks := 0
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if firstByte {
				ttft := g.deps.Clock.Now().Sub(start)
				g.deps.Controller.ObserveFirstToken(gen, ttft)
				g.metrics.ttftSeconds.Observe(ttft.Seconds())
				firstByte = false
			}
			chunks += bytes.Count(buf[:n], []byte("data: "))
			if _, werr := w.Write(buf[:n]); werr != nil {
				setCodeOverride(w, "499")
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			if err == io.EOF {
				if elapsed := g.deps.Clock.Now().Sub(start).Seconds(); elapsed > 0 && chunks > 1 {
					g.metrics.outputTokensPerSecond.Observe(float64(chunks-1) / elapsed) // -1 for the final [DONE] row
				}
			} else {
				outcome := g.classify(r, err)
				if outcome == outcomeUpstreamError {
					g.deps.Controller.ReportUpstreamFailure(gen)
				}
				g.writeInStreamOutcome(w, rc, outcome, held)
			}
			return
		}
	}
}

// writeInStreamOutcome writes the in-stream error row for an outcome
// discovered after headers already went out; a second WriteHeader call
// would be silently ignored by net/http, so setCodeOverride is how the real status reaches metrics.
func (g *Gateway) writeInStreamOutcome(w http.ResponseWriter, rc *http.ResponseController, outcome upstreamOutcome, held bool) {
	if outcome == outcomeClientGone {
		setCodeOverride(w, "499")
		return // nothing to write, the client is already gone
	}
	code, msg := "upstream_error", "connection to the worker was lost"
	switch outcome {
	case outcomeShuttingDown:
		code, msg = "shutting_down", "the server is shutting down"
	case outcomeTimeout:
		code, msg = "upstream_timeout", "the worker did not respond in time"
	case outcomeDrainCutoff:
		code, msg = "backend_unavailable", "the GPU stopped before it could answer"
	}
	if held {
		g.writeHeldTail(w, rc, code, msg)
		return
	}
	setCodeOverride(w, "stream_error")
	if outcome == outcomeDrainCutoff {
		_, _ = io.WriteString(w, backendUnavailableSSE())
	} else {
		_, _ = io.WriteString(w, inStreamError(code, msg)+"data: [DONE]\n\n")
	}
	_ = rc.Flush()
}
