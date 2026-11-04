package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestUpstreamErrorMappingPlain(t *testing.T) {
	cases := []struct {
		name           string
		state          controller.State
		rt             fakeRoundTripper
		wantStatus     int
		wantCode       string
		wantFailureInc bool
	}{
		{
			name:  "500 response",
			state: controller.Ready,
			rt: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
			},
			wantStatus:     http.StatusBadGateway,
			wantFailureInc: true,
		},
		{
			name:  "401 from worker",
			state: controller.Ready,
			rt: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 401, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
			},
			wantStatus:     http.StatusBadGateway,
			wantFailureInc: true,
		},
		{
			name:  "transport error while Serving",
			state: controller.Serving,
			rt: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			wantStatus:     http.StatusBadGateway,
			wantFailureInc: true,
		},
		{
			name:  "transport error while Draining",
			state: controller.Draining,
			rt: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			wantStatus:     http.StatusServiceUnavailable,
			wantFailureInc: false,
		},
		{
			name:  "transport error while Destroying (failing=false still counts as cutoff)",
			state: controller.Destroying,
			rt: func(_ *http.Request) (*http.Response, error) {
				return nil, errors.New("connection reset")
			},
			wantStatus:     http.StatusServiceUnavailable,
			wantFailureInc: false,
		},
		{
			name:  "context deadline exceeded",
			state: controller.Ready,
			rt: func(_ *http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			},
			wantStatus:     http.StatusGatewayTimeout,
			wantCode:       "upstream_timeout",
			wantFailureInc: false,
		},
	}

	for _, c := range cases {
		d := testDeps(t)
		fc := &fakeController{admitRT: c.rt, status: controller.Snapshot{Generation: 1, State: c.state}}
		d.Controller = fc
		g, err := New(d)
		if err != nil {
			t.Fatalf("%s: New: %v", c.name, err)
		}

		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

		if rec.Code != c.wantStatus {
			t.Errorf("%s: status = %d, want %d (body %q)", c.name, rec.Code, c.wantStatus, rec.Body.String())
		}
		if c.wantCode != "" {
			var body errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("%s: body %q does not parse as errorBody: %v", c.name, rec.Body.String(), err)
			} else if body.Error.Code != c.wantCode {
				t.Errorf("%s: body code = %q, want %q", c.name, body.Error.Code, c.wantCode)
			}
		}
		fc.mu.Lock()
		gotFailure := len(fc.upstreamErr) == 1
		fc.mu.Unlock()
		if gotFailure != c.wantFailureInc {
			t.Errorf("%s: ReportUpstreamFailure called=%v, want %v", c.name, gotFailure, c.wantFailureInc)
		}
	}
}

// PublicState(Destroying, failing=true) reports "FAILED", not "DRAINING"
// or "DELETING", so a drain-cutoff check keyed on that string would miss
// this case; isDrainCutoff must key on the State enum instead.
func TestDrainCutoffIgnoresPublicStringAndFailingFlag(t *testing.T) {
	if got := controller.PublicState(controller.Destroying, true); got != "FAILED" {
		t.Fatalf("PublicState(Destroying, true) = %q, want %q (if this ever changes, isDrainCutoff's State-based check still holds; a Public-string-based one would not)", got, "FAILED")
	}

	d := testDeps(t)
	fc := &fakeController{
		admitRT: fakeRoundTripper(func(_ *http.Request) (*http.Response, error) { return nil, errors.New("connection reset") }),
		status:  controller.Snapshot{Generation: 1, State: controller.Destroying, Public: "FAILED"},
	}
	d.Controller = fc
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (State-based drain-cutoff check must still catch this even though Public says FAILED)", rec.Code)
	}
}
