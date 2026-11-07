package obs

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestRequestIDRoundTrip(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	got := rec.Header().Get("X-Request-Id")
	if !hex32.MatchString(got) {
		t.Fatalf("no inbound header: got %q, want 32 lowercase hex", got)
	}
	if seen != got {
		t.Fatalf("context id %q != response header %q", seen, got)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", "not-valid")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") == "not-valid" {
		t.Fatal("malformed inbound id was echoed back instead of replaced")
	}

	valid := NewRequestID()
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", valid)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") != valid {
		t.Fatalf("valid inbound id was replaced: got %q, want %q", rec.Header().Get("X-Request-Id"), valid)
	}
}
