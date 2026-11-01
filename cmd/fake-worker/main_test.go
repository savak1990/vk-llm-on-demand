package main

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthBecomesReadyAfterDelay(t *testing.T) {
	now := time.Now()
	h := newHandler(100*time.Millisecond, "qwen3.6-35b-a3b", func() time.Time { return now })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d before READY_AFTER, want 503", rec.Code)
	}

	now = now.Add(150 * time.Millisecond)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("got %d after READY_AFTER, want 200", rec2.Code)
	}
}

func TestModelsListsConfiguredAlias(t *testing.T) {
	h := newHandler(0, "qwen3.6-35b-a3b", time.Now)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if !strings.Contains(rec.Body.String(), "qwen3.6-35b-a3b") {
		t.Fatalf("response %q does not contain configured alias", rec.Body.String())
	}
}

func TestChatCompletionsStreamsAndEnds(t *testing.T) {
	h := newHandler(0, "qwen3.6-35b-a3b", time.Now)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	h.ServeHTTP(rec, req)

	scanner := bufio.NewScanner(rec.Body)
	var lastLine string
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			lastLine = line
		}
	}
	if lastLine != "data: [DONE]" {
		t.Fatalf("stream ended with %q, want %q", lastLine, "data: [DONE]")
	}
}
