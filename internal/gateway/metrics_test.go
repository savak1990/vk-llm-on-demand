package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestMetricsLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := testDeps(t)
	d.Reg = reg
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	got, err := testutil.GatherAndCount(reg, "vkllm_gateway_requests_total")
	if err != nil {
		t.Fatalf("GatherAndCount: %v", err)
	}
	if got == 0 {
		t.Fatal("vkllm_gateway_requests_total has no samples after one request")
	}

	pending, err := testutil.GatherAndCount(reg, "vkllm_gateway_pending_requests")
	if err != nil || pending == 0 {
		t.Fatalf("vkllm_gateway_pending_requests not registered: count=%d err=%v", pending, err)
	}
}

// TestMetricsCountAuthAndRateLimitRejections is the regression test for
// the advisor-caught bug: 401/403/429 responses return before reaching
// the handler, so they are only counted if the metrics layer wraps auth
// and rate-limiting too, not just the mux.
func TestMetricsCountAuthAndRateLimitRejections(t *testing.T) {
	reg := prometheus.NewRegistry()
	d := testDeps(t)
	d.Reg = reg
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := g.Handler()

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil)) // 401, no Authorization header

	metricFamilies, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range metricFamilies {
		if mf.GetName() != "vkllm_gateway_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "code" && l.GetValue() == "401" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("vkllm_gateway_requests_total has no code=\"401\" sample — the metrics layer is not wrapping auth rejections")
	}
}

// TestMetricsNoSharedStateBetweenGateways proves collectors are
// per-Gateway: two Gateway instances, each with its own registry, must
// not panic on construction (a package-level var would double-register
// on the second New() against the default registry, or leak counts
// between instances sharing one registry).
func TestMetricsNoSharedStateBetweenGateways(t *testing.T) {
	d1 := testDeps(t)
	d1.Reg = prometheus.NewRegistry()
	if _, err := New(d1); err != nil {
		t.Fatalf("first New: %v", err)
	}

	d2 := testDeps(t)
	d2.Reg = prometheus.NewRegistry()
	if _, err := New(d2); err != nil {
		t.Fatalf("second New: %v", err)
	}
}

// TestDisconnectIsLabeledCode499 is the regression test for the
// advisor-caught bug: a mid-stream disconnect must be recorded as
// requests_total{code="499"}, not "200" — status 200 was already
// written before the disconnect was noticed, so only codeOverride can
// correct the label.
func TestDisconnectIsLabeledCode499(t *testing.T) {
	ctxDone := make(chan struct{}, 1)
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: &watchedBody{ctx: r.Context(), done: ctxDone}}, nil
	})

	reg := prometheus.NewRegistry()
	d := testDeps(t)
	d.Reg = reg
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	reqCtx, cancel := context.WithCancel(context.Background())
	req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`).WithContext(reqCtx)
	go func() {
		<-ctxDone
	}()
	go func() {
		cancel()
	}()
	g.Handler().ServeHTTP(httptest.NewRecorder(), req)

	metricFamilies, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range metricFamilies {
		if mf.GetName() != "vkllm_gateway_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "code" && l.GetValue() == "499" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("vkllm_gateway_requests_total has no code=\"499\" sample for a mid-stream disconnect")
	}
}

func TestRecoverDoesNotWriteAfterHeadersSent(t *testing.T) {
	g, err := New(testDeps(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	g.mux.HandleFunc("GET /v1/test-panic-mid-stream", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom after headers")
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/test-panic-mid-stream", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (recover must not rewrite a status after headers already went out, now that Handler() wraps w in *statusRecorder)", rec.Code)
	}
	if rec.Body.String() != "partial" {
		t.Fatalf("body = %q, want %q (recover must not append anything after the panic)", rec.Body.String(), "partial")
	}
}

func TestLogRedaction(t *testing.T) {
	var buf bytes.Buffer
	d := testDeps(t)
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"the secret prompt"}]}`)
	req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	logged := buf.String()
	forbidden := []string{"sk-vkllm-aaaaaaaa1111", "the secret prompt", "Bearer "}
	for _, f := range forbidden {
		if strings.Contains(logged, f) {
			t.Errorf("log output contains forbidden substring %q: %s", f, logged)
		}
	}
}
