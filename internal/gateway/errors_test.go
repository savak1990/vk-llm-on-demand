package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func TestErrorBodyShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusNotFound, "invalid_request_error", "model_not_found", "the model does not exist")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Message, Type, Code string
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Message == "" || body.Error.Type == "" || body.Error.Code == "" {
		t.Fatalf("got empty field(s) in %+v", body.Error)
	}
}

func TestBackendUnavailableBodyIsVerbatim(t *testing.T) {
	rec := httptest.NewRecorder()
	writeBackendUnavailable(rec)

	want := `{"error": {"type": "server_error", "code": "backend_unavailable", "message": "GPU backend is stopped; start it with ` + "`vk-llm start`" + `"}}`
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatal("backend_unavailable must not set Retry-After")
	}
	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}
}

// TestAllErrorBodiesConformToSchema sweeps every non-2xx response this
// package can produce and asserts each one parses into
// {"error":{"message","type","code"}} with all three fields set.
func TestAllErrorBodiesConformToSchema(t *testing.T) {
	cases := []struct {
		name string
		rec  *httptest.ResponseRecorder
	}{}

	record := func(name string, g *Gateway, r *http.Request) {
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, r)
		cases = append(cases, struct {
			name string
			rec  *httptest.ResponseRecorder
		}{name, rec})
	}

	authGateway := newTestGateway(t)
	for _, c := range []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"not bearer", "Basic xyz"},
		{"unknown key", "Bearer sk-vkllm-doesnotexist"},
		{"management token on v1", "Bearer sk-vkllm-mgmtmgmt9999"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		record("auth: "+c.name, authGateway, req)
	}

	bodyLimitGateway := newTestGateway(t)
	oversized := bytes.Repeat([]byte("a"), int(bodyLimitGateway.deps.Config.MaxBodyBytes)+1)
	bodyLimitReq := chatReq("")
	bodyLimitReq.Body = io.NopCloser(bytes.NewReader(oversized))
	record("body limit 413", bodyLimitGateway, bodyLimitReq)

	record("model mismatch 404", newTestGateway(t), chatReq(`{"model":"gpt-4o","messages":[]}`))

	for _, admitErr := range []error{controller.ErrBackendUnavailable, controller.ErrDraining} {
		d := testDeps(t)
		d.Controller = &fakeController{admitErr: admitErr}
		g, err := New(d)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		record("backend unavailable", g, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	}

	rlDeps := testDeps(t)
	rlDeps.Clock = fakeclock.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	rlDeps.Config.RateLimitPerKeyRPM = 60
	rlGateway, err := New(rlDeps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 60; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
		rlGateway.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	rlReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rlReq.Header.Set("Authorization", "Bearer sk-vkllm-aaaaaaaa1111")
	record("rate limit 429", rlGateway, rlReq)

	upstreamCases := []struct {
		name string
		rt   fakeRoundTripper
	}{
		{"upstream 500", func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}},
		{"upstream 401", func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 401, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}},
		{"transport error", func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("connection reset")
		}},
	}
	for _, c := range upstreamCases {
		d := testDeps(t)
		d.Controller = &fakeController{admitRT: c.rt, status: controller.Snapshot{Generation: 1, State: controller.Ready}}
		g, err := New(d)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		record(c.name, g, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	}

	nonConformingRT := fakeRoundTripper(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader("plain text, not json"))}, nil
	})
	wrapDeps := testDeps(t)
	wrapDeps.Controller = &fakeController{admitRT: nonConformingRT, status: controller.Snapshot{Generation: 1}}
	wrapGateway, err := New(wrapDeps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	record("4xx wrapped", wrapGateway, chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

	for _, c := range cases {
		if c.rec.Code < 300 {
			t.Errorf("%s: status = %d, want non-2xx (test setup bug)", c.name, c.rec.Code)
			continue
		}
		var body errorBody
		if err := json.Unmarshal(c.rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: body %q does not parse as errorBody: %v", c.name, c.rec.Body.String(), err)
			continue
		}
		if body.Error.Message == "" || body.Error.Type == "" || body.Error.Code == "" {
			t.Errorf("%s: got empty field(s) in %+v (status %d, body %q)", c.name, body.Error, c.rec.Code, c.rec.Body.String())
		}
	}
}
