package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"not bearer", "Basic xyz", http.StatusUnauthorized},
		{"unknown key", "Bearer sk-vkllm-doesnotexist", http.StatusUnauthorized},
		{"management token on v1", "Bearer sk-vkllm-mgmtmgmt9999", http.StatusForbidden},
		{"inference key", "Bearer sk-vkllm-aaaaaaaa1111", http.StatusOK},
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
	}
}
