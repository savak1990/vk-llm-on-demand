package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

func TestProxyPlain(t *testing.T) {
	var gotReq *http.Request
	var gotBody []byte
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotReq = r
		gotBody, _ = io.ReadAll(r.Body)
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(bytes.NewReader([]byte(`{"choices":[{"message":{"content":"hi"}}],"usage":{"completion_tokens":3}}`))),
		}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 7}}
	d.Controller = fc
	d.InstallationID = "test-install"
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if gotReq.URL.Host != "worker" {
		t.Fatalf("upstream host = %q, want %q", gotReq.URL.Host, "worker")
	}
	wantKey := "Bearer " + worker.WorkerKey(d.Seed.Bytes(), "test-install", 7)
	if got := gotReq.Header.Get("Authorization"); got != wantKey {
		t.Fatalf("upstream Authorization = %q, want %q", got, wantKey)
	}
	var sentBody map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal sent body: %v", err)
	}
	var maxTokens int
	_ = json.Unmarshal(sentBody["max_tokens"], &maxTokens)
	if maxTokens != 8192 {
		t.Fatalf("upstream max_tokens = %d, want 8192 (clamp did not reach the proxy)", maxTokens)
	}
	if _, endCalls := fc.snapshot(); len(endCalls) != 1 || endCalls[0] != 7 {
		t.Fatalf("RequestEnd calls = %v, want [7]", endCalls)
	}
}

// TestProxyOverhead answers instantly from the fake upstream, so total
// handler time reduces to this gateway's own overhead, which p99
// must keep under 5ms.
func TestProxyOverhead(t *testing.T) {
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader([]byte(`{"choices":[{"message":{"content":"hi"}}]}`)))}, nil
	})
	d := testDeps(t)
	d.Config.RateLimitPerKeyRPM = 1_000_000 // isolate overhead from the rate limiter, covered separately in ratelimit_test.go
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := g.Handler()

	const n = 10000
	durations := make([]time.Duration, n)
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
		durations[i] = time.Since(start)
		if rec.Code != http.StatusOK {
			t.Fatalf("iteration %d: status = %d, want 200", i, rec.Code)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[9899] // 9900th-fastest of 10000
	if p99 > 5*time.Millisecond {
		t.Fatalf("p99 overhead = %v, want under 5ms", p99)
	}
}

func TestAtomicAdmissionAtDeadline(t *testing.T) {
	rtA := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader([]byte(`{}`)))}, nil
	})

	d := testDeps(t)
	fc := &fakeController{admitRT: rtA, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	recA := httptest.NewRecorder()
	g.Handler().ServeHTTP(recA, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if recA.Code != http.StatusOK {
		t.Fatalf("request A: status = %d, want 200", recA.Code)
	}

	fc.mu.Lock()
	fc.admitErr = controller.ErrBackendUnavailable
	fc.mu.Unlock()

	recB := httptest.NewRecorder()
	g.Handler().ServeHTTP(recB, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	if recB.Code != http.StatusServiceUnavailable {
		t.Fatalf("request B: status = %d, want 503", recB.Code)
	}

	admitCalls, endCalls := fc.snapshot()
	if len(admitCalls) != 2 {
		t.Fatalf("Admit calls = %d, want 2", len(admitCalls))
	}
	if len(endCalls) != 1 {
		t.Fatalf("RequestEnd calls = %d, want 1", len(endCalls))
	}
}

func TestUpstream4xxMapping(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantStatus     int
		wantCode       string
		wantExactBody  bool
		wantFailureInc bool
	}{
		{
			name:          "openai-shaped 4xx passes through unchanged",
			body:          `{"error":{"message":"bad request","type":"invalid_request_error","code":"invalid_request"}}`,
			wantStatus:    http.StatusBadRequest,
			wantCode:      "invalid_request",
			wantExactBody: true,
		},
		{
			name:       "non-conforming 4xx body gets wrapped as upstream_rejected",
			body:       `plain text, not json`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "upstream_rejected",
		},
	}
	for _, c := range cases {
		rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(c.body))}, nil
		})
		d := testDeps(t)
		fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
		d.Controller = fc
		g, err := New(d)
		if err != nil {
			t.Fatalf("%s: New: %v", c.name, err)
		}

		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

		if rec.Code != c.wantStatus {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.wantStatus)
		}
		if !strings.Contains(rec.Body.String(), c.wantCode) {
			t.Errorf("%s: body %q does not contain code %q", c.name, rec.Body.String(), c.wantCode)
		}
		if c.wantExactBody && rec.Body.String() != c.body {
			t.Errorf("%s: body = %q, want upstream body passed through unchanged: %q", c.name, rec.Body.String(), c.body)
		}
		fc.mu.Lock()
		gotFailure := len(fc.upstreamErr) != 0
		fc.mu.Unlock()
		if gotFailure {
			t.Errorf("%s: ReportUpstreamFailure called, want 0 calls (a 4xx-with-body is not reported, only 401/403/5xx and transport failures are)", c.name)
		}
	}
}

// TestUpstream3xxReportedAsError guards against a 3xx upstream response
// falling through to the stream/default cases, which would otherwise
// get copied as if it were a confirmed 2xx.
func TestUpstream3xxReportedAsError(t *testing.T) {
	rt := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": {"http://elsewhere"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	d := testDeps(t)
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (never SSE for a 3xx)", ct)
	}
	if !strings.Contains(rec.Body.String(), "upstream_error") {
		t.Fatalf("body %q does not contain code %q", rec.Body.String(), "upstream_error")
	}
	fc.mu.Lock()
	gotFailure := len(fc.upstreamErr) != 0
	fc.mu.Unlock()
	if !gotFailure {
		t.Fatal("ReportUpstreamFailure not called, want 1 call for a 3xx upstream response")
	}
}
