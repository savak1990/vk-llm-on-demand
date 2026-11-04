package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBodyLimit413(t *testing.T) {
	g := newTestGateway(t)
	g.mux.HandleFunc("POST /v1/test-echo", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large", err.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := g.Handler()

	body := bytes.Repeat([]byte("a"), int(g.deps.Config.MaxBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/test-echo", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", rec.Code, rec.Body.String())
	}
}

// TestBodyLimit413OnChatRoute covers the real chat route, not just a
// test-only echo handler: handleChatCompletions must map MaxBytesReader's
// error to 413 request_too_large, not swallow it into a 400.
func TestBodyLimit413OnChatRoute(t *testing.T) {
	g := newTestGateway(t)
	h := g.Handler()

	body := bytes.Repeat([]byte("a"), int(g.deps.Config.MaxBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", rec.Code, rec.Body.String())
	}
	var body2 errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body2); err != nil {
		t.Fatalf("body %q does not parse as errorBody: %v", rec.Body.String(), err)
	}
	if body2.Error.Code != "request_too_large" {
		t.Fatalf("body code = %q, want %q", body2.Error.Code, "request_too_large")
	}
}

func TestBodyLimitRequiresAuthFirst(t *testing.T) {
	g := newTestGateway(t)
	h := g.Handler()

	body := bytes.Repeat([]byte("a"), int(g.deps.Config.MaxBodyBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (auth must run before the body is read)", rec.Code)
	}
}
