package obs

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServerEndpoints(t *testing.T) {
	reg := NewRegistry()
	failing := false
	checks := []func() error{func() error {
		if failing {
			return errors.New("apiserver 10.0.0.1 forbidden")
		}
		return nil
	}}
	var buf bytes.Buffer
	srv := NewServer(reg, checks, Config{Addr: ":0", Log: slog.New(slog.NewTextHandler(&buf, nil))})

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
	if rec.Code != 503 || rec.Body.String() != "not ready\n" {
		t.Fatalf("/readyz (failing) = %d %q, want 503 \"not ready\\n\"", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), "apiserver 10.0.0.1 forbidden") {
		t.Fatalf("readiness failure must be logged, log = %q", buf.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
}

func TestServerNilLogDefaults(t *testing.T) {
	srv := NewServer(NewRegistry(), []func() error{func() error { return errors.New("x") }}, Config{Addr: ":0"})
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 || rec.Body.String() != "not ready\n" {
		t.Fatalf("/readyz = %d %q, want 503 \"not ready\\n\"", rec.Code, rec.Body.String())
	}
}
