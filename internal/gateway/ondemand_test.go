package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func onDemandDeps(t *testing.T, fc *fakeController) Deps {
	t.Helper()
	d := testDeps(t)
	d.Controller = fc
	d.Config.ProvisioningMode = "on-demand"
	d.Config.OnDemand = config.OnDemand{MaxWait: 600 * time.Second, MaxPending: 32, HeartbeatInterval: 15 * time.Second, TickerInterval: 30 * time.Second, DemandGrace: 60 * time.Second}
	return d
}

func TestOnDemandNonStreamStartsAndRefuses(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, status: controller.Snapshot{Public: "STOPPED"}}
	g, err := New(onDemandDeps(t, fc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"gpu_cold_start"`) {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "360" {
		t.Fatalf("Retry-After = %q, want 360", ra)
	}
	if len(fc.demandCalls) != 1 {
		t.Fatalf("Demand calls = %d, want 1", len(fc.demandCalls))
	}
}

func TestOnDemandNonStreamDenied(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable, demandErr: controller.ErrGPUUnavailable, demandSnap: controller.Snapshot{Unavailable: "budget_exhausted"}}
	g, _ := New(onDemandDeps(t, fc))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"gpu_unavailable"`) || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("code %d RA %q body %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "monthly GPU budget") {
		t.Fatalf("body lacks the readable reason: %s", rec.Body)
	}
}

func TestOnDemandDrainingAnswersRetryLater(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrDraining}
	g, _ := New(onDemandDeps(t, fc))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("code %d RA %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestOnDemandQueueFull(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable}
	g, _ := New(onDemandDeps(t, fc))
	g.held.Store(32)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "15" || !strings.Contains(rec.Body.String(), `"queue_full"`) {
		t.Fatalf("code %d RA %q body %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
}

func TestExplicitModeBodyUnchanged(t *testing.T) {
	fc := &fakeController{admitErr: controller.ErrBackendUnavailable}
	g, _ := New(testDepsWith(t, fc))
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if rec.Body.String() != backendUnavailableBody || len(fc.demandCalls) != 0 {
		t.Fatalf("body %s demand calls %d", rec.Body, len(fc.demandCalls))
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		s    controller.Snapshot
		want time.Duration
	}{
		{controller.Snapshot{Public: "STOPPED"}, 360 * time.Second},
		{controller.Snapshot{Public: "LOADING_MODEL", CreatedAt: now.Add(-100 * time.Second)}, 260 * time.Second},
		{controller.Snapshot{Public: "LOADING_MODEL", CreatedAt: now.Add(-10 * time.Minute)}, 30 * time.Second},
	}
	for _, tc := range cases {
		if got := retryAfter(tc.s, now); got != tc.want {
			t.Fatalf("retryAfter(%+v) = %v, want %v", tc.s, got, tc.want)
		}
	}
}
