package gateway

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestModelsNoController(t *testing.T) {
	d := testDeps(t)
	fc := &fakeController{status: controller.Snapshot{Public: "STOPPED"}}
	fc.admitErr = controller.ErrBackendUnavailable
	d.Controller = fc

	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	g.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls, _ := fc.snapshot(); len(calls) != 0 {
		t.Fatalf("Admit called %d times, want 0", len(calls))
	}
	want := `"id":"qwen3.6-35b-a3b"`
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body %q does not contain %q", rec.Body.String(), want)
	}
}

// TestModelsWithPanickingController wires in a controller whose Status
// panics, so a future change that made /v1/models call it would fail
// this test immediately instead of silently passing.
func TestModelsWithPanickingController(t *testing.T) {
	d := testDeps(t)
	fc := &fakeController{panicOnStatus: true}
	d.Controller = fc

	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	g.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}

func TestModelsLatency(t *testing.T) {
	g := newTestGateway(t)
	const n = 1000
	durations := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		start := time.Now()
		g.mux.ServeHTTP(rec, req)
		durations[i] = time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("iteration %d: status = %d, want 200", i, rec.Code)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[989] // 990th-fastest of 1000
	if p99 > 50*time.Millisecond {
		t.Fatalf("p99 latency = %v, want under 50ms", p99)
	}
}
