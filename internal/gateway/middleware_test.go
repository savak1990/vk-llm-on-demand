package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestGateway(t *testing.T) *Gateway {
	t.Helper()
	g, err := New(testDeps(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestAuthMatrixV1(t *testing.T) {
	g := newTestGateway(t)
	h := g.Handler()

	cases := []struct {
		name       string
		header     string
		want       int
		authReason string // empty for the one success case, which increments no auth_failures counter
		wantCode   string // empty for the one success case, which has no error body
	}{
		{"no header", "", http.StatusUnauthorized, "missing", "invalid_api_key"},
		{"not bearer", "Basic xyz", http.StatusUnauthorized, "malformed", "invalid_api_key"},
		{"unknown key", "Bearer sk-vkllm-doesnotexist", http.StatusUnauthorized, "unknown_key", "invalid_api_key"},
		{"management token on v1", "Bearer sk-vkllm-mgmtmgmt9999", http.StatusForbidden, "wrong_surface", "permission_denied"},
		{"inference key", "Bearer sk-vkllm-aaaaaaaa1111", http.StatusOK, "", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (body %q)", c.name, rec.Code, c.want, rec.Body.String())
		}
		if c.authReason != "" {
			if got := testutil.ToFloat64(g.metrics.authFailuresTotal.WithLabelValues(c.authReason)); got != 1 {
				t.Errorf("%s: auth_failures_total{reason=%q} = %v, want 1", c.name, c.authReason, got)
			}
		}
		if c.wantCode != "" {
			var body errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("%s: body %q does not parse as errorBody: %v", c.name, rec.Body.String(), err)
			} else if body.Error.Code != c.wantCode {
				t.Errorf("%s: body code = %q, want %q", c.name, body.Error.Code, c.wantCode)
			}
		}
	}

	// /v1/models never calls Admit by design, so the controller-reach
	// check above needs a route that does: a fresh gateway per case
	// keeps each one's Admit count independent of the loop above.
	for _, c := range cases {
		if c.authReason == "" {
			continue
		}
		d2 := testDeps(t)
		fc2 := &fakeController{}
		d2.Controller = fc2
		g2, err := New(d2)
		if err != nil {
			t.Fatalf("%s: New: %v", c.name, err)
		}
		req := chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`)
		if c.header == "" {
			req.Header.Del("Authorization")
		} else {
			req.Header.Set("Authorization", c.header)
		}
		rec := httptest.NewRecorder()
		g2.Handler().ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: chat completions status = %d, want %d (body %q)", c.name, rec.Code, c.want, rec.Body.String())
		}
		if calls, _ := fc2.snapshot(); len(calls) != 0 {
			t.Errorf("%s: Admit called %d times, want 0 (a non-2xx auth result must never reach the controller)", c.name, len(calls))
		}
	}
}
