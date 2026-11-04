package gateway

import (
	"encoding/json"
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

func TestClampMaxTokensPure(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantVal int
		wantOK  bool
	}{
		{"absent", `{}`, 8192, true},
		{"above cap", `{"max_tokens": 100000}`, 8192, true},
		{"within cap", `{"max_tokens": 10}`, 10, false},
	}
	for _, c := range cases {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.body), &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", c.name, err)
		}
		clamped, value, err := clampMaxTokens(m, 8192)
		if err != nil {
			t.Fatalf("%s: clampMaxTokens: %v", c.name, err)
		}
		if value != c.wantVal || clamped != c.wantOK {
			t.Errorf("%s: got (clamped=%v, value=%d), want (clamped=%v, value=%d)", c.name, clamped, value, c.wantOK, c.wantVal)
		}
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
