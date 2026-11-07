package obs

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestServerEndpoints(t *testing.T) {
	reg := NewRegistry()
	failing := false
	checks := []func() error{func() error {
		if failing {
			return errors.New("not ready yet")
		}
		return nil
	}}
	srv := NewServer(reg, checks, Config{Addr: ":0"})

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("/healthz = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("/readyz (no failures) = %d, want 200", rec.Code)
	}

	failing = true
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 || rec.Body.String() != "not ready yet\n" {
		t.Fatalf("/readyz (failing) = %d %q, want 503 \"not ready yet\\n\"", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
}
