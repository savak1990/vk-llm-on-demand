package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func chatReq(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	r.Header.Set("Content-Type", "application/json")
	return r
}

// getIntField reads an int field back out of a clamped body map; present
// reports whether the key exists at all, distinguishing "absent" from "0".
func getIntField(t *testing.T, m map[string]json.RawMessage, key string) (value int, present bool) {
	t.Helper()
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("field %s did not unmarshal as int: %v (raw %s)", key, err, raw)
	}
	return value, true
}

func TestClampMaxTokensPure(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantClamped bool
		wantMT      int
		wantMTOK    bool
		wantMCT     int
		wantMCTOK   bool
	}{
		{"absent", `{}`, true, 8192, true, 0, false},
		{"above cap", `{"max_tokens": 100000}`, true, 8192, true, 0, false},
		{"within cap", `{"max_tokens": 10}`, false, 10, true, 0, false},
		{"negative treated as absent", `{"max_tokens": -1}`, true, 8192, true, 0, false},
		{"zero treated as absent", `{"max_tokens": 0}`, true, 8192, true, 0, false},
		{"only max_completion_tokens above cap", `{"max_completion_tokens": 100000}`, true, 8192, true, 8192, true},
		{"only max_completion_tokens within cap, also mirrored to max_tokens", `{"max_completion_tokens": 10}`, false, 10, true, 10, true},
		{"both present, only one over cap", `{"max_tokens": 10, "max_completion_tokens": 100000}`, true, 10, true, 8192, true},
	}
	for _, c := range cases {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.body), &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", c.name, err)
		}
		clamped, _, err := clampMaxTokens(m, 8192)
		if err != nil {
			t.Fatalf("%s: clampMaxTokens: %v", c.name, err)
		}
		if clamped != c.wantClamped {
			t.Errorf("%s: clamped = %v, want %v", c.name, clamped, c.wantClamped)
		}
		if gotMT, okMT := getIntField(t, m, "max_tokens"); okMT != c.wantMTOK || (okMT && gotMT != c.wantMT) {
			t.Errorf("%s: max_tokens = (present=%v, value=%d), want (present=%v, value=%d)", c.name, okMT, gotMT, c.wantMTOK, c.wantMT)
		}
		if gotMCT, okMCT := getIntField(t, m, "max_completion_tokens"); okMCT != c.wantMCTOK || (okMCT && gotMCT != c.wantMCT) {
			t.Errorf("%s: max_completion_tokens = (present=%v, value=%d), want (present=%v, value=%d)", c.name, okMCT, gotMCT, c.wantMCTOK, c.wantMCT)
		}
	}
}

func TestClampMaxTokensRejectsNonInteger(t *testing.T) {
	cases := []string{
		`{"max_tokens": "ten"}`,
		`{"max_completion_tokens": "ten"}`,
		`{"max_tokens": 10, "max_completion_tokens": "ten"}`,
	}
	for _, body := range cases {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", body, err)
		}
		if _, _, err := clampMaxTokens(m, 8192); err == nil {
			t.Errorf("%s: clampMaxTokens: want error for a non-integer field, got nil", body)
		}
	}
}

// TestClampEndToEndLogsAndUpstream: a request over the cap must reach
// the upstream clamped, with every observability field this path owns
// visible on the access log line, not just max_tokens_clamped.
func TestClampEndToEndLogsAndUpstream(t *testing.T) {
	var gotBody []byte
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(r.Body)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader([]byte(`{}`)))}, nil
	})

	var buf bytes.Buffer
	d := testDeps(t)
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	fc := &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"max_tokens":100000}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var sentBody map[string]json.RawMessage
	if err := json.Unmarshal(gotBody, &sentBody); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	var maxTokens int
	_ = json.Unmarshal(sentBody["max_tokens"], &maxTokens)
	if maxTokens != 8192 {
		t.Fatalf("upstream max_tokens = %d, want 8192", maxTokens)
	}

	logged := buf.String()
	for _, want := range []string{
		"max_tokens_clamped=true", "max_tokens_requested=100000",
		"instance_gen=1", "stream=false", "duration_ms=",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("access log missing %q: %s", want, logged)
		}
	}
}

// TestBackendUnavailableLogsOutcome confirms the access log's outcome
// field reports backend_unavailable when Admit refuses a request, not
// just the numeric 503 status every other 503 would also carry.
func TestBackendUnavailableLogsOutcome(t *testing.T) {
	var buf bytes.Buffer
	d := testDeps(t)
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	d.Controller = &fakeController{admitErr: controller.ErrBackendUnavailable}
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

	if !strings.Contains(buf.String(), "outcome=backend_unavailable") {
		t.Fatalf("access log missing outcome=backend_unavailable: %s", buf.String())
	}
}

func TestModelMismatch404(t *testing.T) {
	g := newTestGateway(t)
	h := g.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, chatReq(`{"model":"gpt-4o","messages":[]}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q does not parse as errorBody: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "model_not_found" {
		t.Fatalf("body code = %q, want %q", body.Error.Code, "model_not_found")
	}
	fc := g.deps.Controller.(*fakeController)
	if calls, _ := fc.snapshot(); len(calls) != 0 {
		t.Fatalf("Admit called %d times, want 0", len(calls))
	}
}

func TestBackendUnavailable(t *testing.T) {
	states := []error{controller.ErrBackendUnavailable, controller.ErrDraining}
	for _, admitErr := range states {
		d := testDeps(t)
		fc := &fakeController{admitErr: admitErr}
		d.Controller = fc
		g, err := New(d)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 for %v", rec.Code, admitErr)
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Fatalf("Retry-After set for %v, want unset", admitErr)
		}
		if _, endCalls := fc.snapshot(); len(endCalls) != 0 {
			t.Fatalf("RequestEnd called for %v, want 0 calls", admitErr)
		}

		// A stream:true request must still get a plain JSON 503, never
		// SSE — no 200/text-event-stream header has been written yet.
		fc2 := &fakeController{admitErr: admitErr}
		d2 := testDeps(t)
		d2.Controller = fc2
		g2, err := New(d2)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		recStream := httptest.NewRecorder()
		g2.Handler().ServeHTTP(recStream, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`))
		if recStream.Code != http.StatusServiceUnavailable {
			t.Fatalf("stream request: status = %d, want 503 for %v", recStream.Code, admitErr)
		}
		if ct := recStream.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("stream request: Content-Type = %q, want application/json (never SSE before any upstream response)", ct)
		}
	}
}

// TestBackendUnavailableAcrossEveryState: a pre-admission 503 never
// depends on the reported state, plain or streamed, and the fake
// records no call besides Status and Admit.
func TestBackendUnavailableAcrossEveryState(t *testing.T) {
	cases := []struct {
		state controller.State
		err   error
	}{
		{controller.Idle, controller.ErrBackendUnavailable},
		{controller.Searching, controller.ErrBackendUnavailable},
		{controller.Creating, controller.ErrBackendUnavailable},
		{controller.Booting, controller.ErrBackendUnavailable},
		{controller.Loading, controller.ErrBackendUnavailable},
		{controller.Draining, controller.ErrDraining},
		{controller.Destroying, controller.ErrBackendUnavailable},
	}
	for _, c := range cases {
		for _, stream := range []bool{false, true} {
			d := testDeps(t)
			fc := &fakeController{admitErr: c.err, status: controller.Snapshot{State: c.state}}
			d.Controller = fc
			g, err := New(d)
			if err != nil {
				t.Fatalf("%v/%v/stream=%v: New: %v", c.state, c.err, stream, err)
			}

			body := `{"model":"qwen3.6-35b-a3b","messages":[]}`
			if stream {
				body = `{"model":"qwen3.6-35b-a3b","messages":[],"stream":true}`
			}
			rec := httptest.NewRecorder()
			g.Handler().ServeHTTP(rec, chatReq(body))

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%v/%v/stream=%v: status = %d, want 503", c.state, c.err, stream, rec.Code)
			}
			if rec.Body.String() != backendUnavailableBody {
				t.Fatalf("%v/%v/stream=%v: body = %q, want %q", c.state, c.err, stream, rec.Body.String(), backendUnavailableBody)
			}
			if rec.Header().Get("Retry-After") != "" {
				t.Fatalf("%v/%v/stream=%v: Retry-After set, want unset", c.state, c.err, stream)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("%v/%v/stream=%v: Content-Type = %q, want application/json, never text/event-stream", c.state, c.err, stream, ct)
			}
			fc.mu.Lock()
			endCalls, firstToken, upstreamErr := len(fc.endCalls), len(fc.firstToken), len(fc.upstreamErr)
			fc.mu.Unlock()
			if endCalls != 0 || firstToken != 0 || upstreamErr != 0 {
				t.Fatalf("%v/%v/stream=%v: RequestEnd/ObserveFirstToken/ReportUpstreamFailure = %d/%d/%d, want 0/0/0",
					c.state, c.err, stream, endCalls, firstToken, upstreamErr)
			}
		}
	}
}
