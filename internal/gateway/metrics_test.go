package gateway

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

func TestStatusLabelBucketsUnenumeratedCodes(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{200, "200"}, {201, "2xx"}, {400, "400"}, {404, "404"},
		{422, "4xx"}, {409, "4xx"}, {500, "500"}, {503, "503"}, {599, "5xx"},
	}
	for _, c := range cases {
		if got := statusLabel(c.code); got != c.want {
			t.Errorf("statusLabel(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestUnenumeratedUpstreamCodeMetricBucketed: an upstream 4xx outside
// the enumerated label set must not grow the requests_total series,
// even though the client still receives the exact status sent upstream.
func TestUnenumeratedUpstreamCodeMetricBucketed(t *testing.T) {
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 422, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"m","type":"invalid_request_error","code":"unprocessable"}}`))}, nil
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

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

	if rec.Code != 422 {
		t.Fatalf("status = %d, want 422 (the client must still see the upstream's exact code)", rec.Code)
	}
	metricFamilies, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range metricFamilies {
		if mf.GetName() != "vkllm_gateway_requests_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "code" && l.GetValue() == "422" {
					t.Fatal("requests_total has code=\"422\", want it bucketed to \"4xx\"")
				}
			}
		}
	}
}

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

// A 401/403/429 response returns before reaching a route handler, so
// it is only counted if the metrics layer wraps auth and rate-limiting
// too, not just the mux.
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

// Two Gateways, each with its own registry, must not panic on
// construction: a package-level var would double-register the second
// one against the default registry.
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

// A mid-stream disconnect must be recorded as code="499", not "200":
// 200 was already written before the disconnect was noticed, so only
// codeOverride can correct the label.
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
	g.mux.HandleFunc("GET /v1/test-panic-mid-stream", func(w http.ResponseWriter, _ *http.Request) {
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

// A panic after headers go out is a post-header failure like the
// other writeInStreamOutcome cases, not a client disconnect, so it
// must not default to codeOverride="" and read back as a 200.
func TestPanicAfterHeadersSentLabeledStreamError(t *testing.T) {
	g, err := New(testDeps(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sr := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	handler := g.recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic("boom after headers")
	}))
	handler.ServeHTTP(sr, httptest.NewRequest(http.MethodGet, "/v1/test-panic-mid-stream", nil))

	if sr.codeOverride != "stream_error" {
		t.Fatalf("codeOverride = %q, want %q (a panic after headers go out must not read back as a 200)", sr.codeOverride, "stream_error")
	}
}

// authMiddleware stores keyID on a derived *http.Request its callees
// see, but accessLogAndMetrics sits above it with the original
// request, so only *statusRecorder.keyID can carry the value up.
func TestAccessLogIncludesKeyID(t *testing.T) {
	var buf bytes.Buffer
	d := testDeps(t)
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	if !strings.Contains(buf.String(), "key_id=aaaaaaaa") {
		t.Fatalf("access log missing key_id=aaaaaaaa: %s", buf.String())
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

// TestLogRedactionFullSuite runs a batch of request scenarios against
// one Gateway sharing one log buffer, then sweeps the whole
// accumulated output for the same forbidden substrings.
func TestLogRedactionFullSuite(t *testing.T) {
	var buf bytes.Buffer
	reg := prometheus.NewRegistry()
	d := testDeps(t)
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	d.Reg = reg
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(string(raw), "trigger-500"):
			return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case strings.Contains(string(raw), `"stream":true`):
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"the secret reply\"}}]}\n\ndata: [DONE]\n\n"))}, nil
		default:
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"the secret reply"}}]}`))}, nil
		}
	})
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := g.Handler()

	secretReq := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"the secret prompt"}]}`)
	secretReq.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	h.ServeHTTP(httptest.NewRecorder(), secretReq)

	badAuthReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	badAuthReq.Header.Set("Authorization", "Bearer sk-vkllm-doesnotexist")
	h.ServeHTTP(httptest.NewRecorder(), badAuthReq)

	mismatchReq := chatReq(`{"model":"gpt-4o","messages":[{"role":"user","content":"the secret prompt"}]}`)
	mismatchReq.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	h.ServeHTTP(httptest.NewRecorder(), mismatchReq)

	mgmtReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	mgmtReq.Header.Set("Authorization", "Bearer sk-vkllm-mgmtmgmt9999")
	h.ServeHTTP(httptest.NewRecorder(), mgmtReq)

	basicReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	basicReq.Header.Set("Authorization", "Basic xyz")
	h.ServeHTTP(httptest.NewRecorder(), basicReq)

	streamReq := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"the secret prompt"}],"stream":true}`)
	streamReq.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	h.ServeHTTP(httptest.NewRecorder(), streamReq)

	upstream500Req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"trigger-500"}]}`)
	upstream500Req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	h.ServeHTTP(httptest.NewRecorder(), upstream500Req)

	logged := buf.String()
	if logged == "" {
		t.Fatal("log buffer is empty after the request batch")
	}
	workerKey := worker.WorkerKey(d.Seed.Bytes(), d.InstallationID, 1)
	forbidden := []string{
		"sk-vkllm-aaaaaaaa1111", "sk-vkllm-doesnotexist", "sk-vkllm-mgmtmgmt9999",
		"the secret prompt", "the secret reply", "Bearer ",
		string(d.Seed.Bytes()), workerKey,
	}
	for _, f := range forbidden {
		if strings.Contains(logged, f) {
			t.Errorf("log output contains forbidden substring %q: %s", f, logged)
		}
	}

	if got := testutil.ToFloat64(g.metrics.pendingRequests); got != 0 {
		t.Errorf("pending_requests = %v, want 0 (on-demand-only metric, explicit mode never touches it)", got)
	}
	// Pre-init touches every outcome's child so the family shows up in
	// /metrics, but explicit mode must never increment any of them.
	for _, o := range []string{"ready", "gpu_cold_start", "queue_full", "gpu_unavailable", "client_gone", "shutting_down"} {
		if got := testutil.ToFloat64(g.metrics.coldStartOutcomesTotal.WithLabelValues(o)); got != 0 {
			t.Errorf("cold_start_outcomes_total{outcome=%q} = %v, want 0 (on-demand-only metric)", o, got)
		}
	}

	allowedRoute := map[string]bool{"chat_completions": true, "models": true, "other": true}
	allowedCode := map[string]bool{
		"200": true, "400": true, "401": true, "403": true, "404": true, "405": true,
		"413": true, "429": true, "499": true, "500": true, "502": true, "503": true,
		"504": true, "stream_error": true,
	}
	allowedReason := map[string]bool{"missing": true, "malformed": true, "unknown_key": true, "wrong_surface": true}
	metricFamilies, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range metricFamilies {
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "route":
					if !allowedRoute[l.GetValue()] {
						t.Errorf("%s: route label %q outside the enumerated set", mf.GetName(), l.GetValue())
					}
				case "code":
					if !allowedCode[l.GetValue()] {
						t.Errorf("%s: code label %q outside the enumerated set", mf.GetName(), l.GetValue())
					}
				case "reason":
					if !allowedReason[l.GetValue()] {
						t.Errorf("%s: reason label %q outside the enumerated set", mf.GetName(), l.GetValue())
					}
				}
			}
		}
	}
}
