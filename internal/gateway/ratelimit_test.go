package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestRateLimitMiddleware(t *testing.T) {
	d := testDeps(t)
	d.Clock = fakeclock.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	d.Config.RateLimitPerKeyRPM = 60
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := g.Handler()

	for i := 0; i < 60; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("61st request: status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("61st request: Retry-After = %q, want %q", got, "1")
	}

	// Advancing the fake clock by exactly one second must refill one
	// token, letting one more request through.
	fc := d.Clock.(*fakeclock.Clock)
	fc.Advance(time.Second)
	req3 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req3.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("request after advancing 1s: status = %d, want 200", rec3.Code)
	}

	// A different key still has its own bucket.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req2.Header.Set("Authorization", "Bearer sk-vkllm-bbbbbbbb2222")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("other key: status = %d, want 200", rec2.Code)
	}
}

// TestRateLimitMiddlewareChatCompletions confirms the per-key Admit
// ceiling on the route that actually calls Admit: GET /v1/models
// never does, so that route alone cannot prove this budget.
func TestRateLimitMiddlewareChatCompletions(t *testing.T) {
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader([]byte(`{}`)))}, nil
	})
	d := testDeps(t)
	d.Clock = fakeclock.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	d.Config.RateLimitPerKeyRPM = 60
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1, State: controller.Ready}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := g.Handler()

	for i := 0; i < 60; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 (body %q)", i, rec.Code, rec.Body.String())
		}
	}
	if admitCalls, _ := fc.snapshot(); len(admitCalls) != 60 {
		t.Fatalf("Admit calls = %d, want 60", len(admitCalls))
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("61st request: status = %d, want 429", rec.Code)
	}
	if admitCalls, _ := fc.snapshot(); len(admitCalls) != 60 {
		t.Fatalf("Admit calls after 61st request = %d, want still 60 (the 61st must never reach Admit)", len(admitCalls))
	}

	// Advancing the fake clock by exactly one second refills one token,
	// so the next request reaches Admit again.
	d.Clock.(*fakeclock.Clock).Advance(time.Second)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if rec2.Code != http.StatusOK {
		t.Fatalf("request after advancing 1s: status = %d, want 200 (body %q)", rec2.Code, rec2.Body.String())
	}
	if admitCalls, _ := fc.snapshot(); len(admitCalls) != 61 {
		t.Fatalf("Admit calls after advancing 1s = %d, want 61", len(admitCalls))
	}
}
