package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

// panicOnAnyCallController panics on every Controller method, so a
// future change that made handleModels reach the controller at all
// would fail immediately instead of silently passing.
type panicOnAnyCallController struct{}

func (panicOnAnyCallController) Admit(int64) (http.RoundTripper, error) {
	panic("Admit must never be called by /v1/models")
}
func (panicOnAnyCallController) RequestEnd(int64) {
	panic("RequestEnd must never be called by /v1/models")
}
func (panicOnAnyCallController) ObserveFirstToken(int64, time.Duration) {
	panic("ObserveFirstToken must never be called by /v1/models")
}
func (panicOnAnyCallController) ReportUpstreamFailure(int64) {
	panic("ReportUpstreamFailure must never be called by /v1/models")
}
func (panicOnAnyCallController) Status() controller.Snapshot {
	panic("Status must never be called by /v1/models")
}

// TestModelsNeverCallsController runs the full middleware chain, not
// just the mux, and wires in a controller that panics on all five
// methods, so this covers "0 calls" on every one of them, not only Status.
func TestModelsNeverCallsController(t *testing.T) {
	d := testDeps(t)
	d.Controller = panicOnAnyCallController{}
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var list modelsList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("body %q does not parse: %v", rec.Body.String(), err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(list.Data))
	}
	if list.Data[0].ID != "qwen3.6-35b-a3b" {
		t.Fatalf("data[0].id = %q, want %q", list.Data[0].ID, "qwen3.6-35b-a3b")
	}
}

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
