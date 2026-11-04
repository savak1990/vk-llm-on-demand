package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
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

// proxyChat does not call upstream yet; it exists only so admission
// above it (already returning 503 correctly) has somewhere to hand
// off to once streaming and upstream error handling land here.
func (g *Gateway) proxyChat(w http.ResponseWriter, r *http.Request, gen int64, rt http.RoundTripper, body map[string]json.RawMessage, stream bool) {
	_ = r
	_ = gen
	_ = rt
	_ = body
	_ = stream
	writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "upstream proxying not yet implemented")
}
