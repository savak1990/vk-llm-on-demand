package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
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
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("61st request: Retry-After header not set")
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
