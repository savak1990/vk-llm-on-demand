package mgmt

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeKeyFile(t *testing.T, dir, name string, keys ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := ""
	for _, k := range keys {
		content += k + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func newFullServer(t *testing.T, fc *fakeController, keys *auth.Keys, clk *fakeclock.Clock, rpm int) (http.Handler, *auth.Limiter) {
	t.Helper()
	limiter := auth.NewLimiter(rpm, clk)
	reg := prometheus.NewRegistry()
	h := New(Deps{Controller: fc, Keys: keys, Limiter: limiter, Clock: clk, Config: config.Mgmt{}, Reg: reg, Log: noopLogger()})
	return h, limiter
}

func authKeys(t *testing.T, clientKey, mgmtKey string) *auth.Keys {
	t.Helper()
	dir := t.TempDir()
	cp := writeKeyFile(t, dir, "client-keys", clientKey)
	mp := writeKeyFile(t, dir, "management-tokens", mgmtKey)
	k, err := auth.Load(cp, mp, fakeclock.New(time.Unix(0, 0)), noopLogger())
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	return k
}

func TestAuthMatrix(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Public: "READY"}
	fc.stopSnap = controller.Snapshot{Public: "STOPPED"}
	h, _ := newFullServer(t, fc, keys, clk, 10000)

	routes := []struct{ method, path string }{
		{"GET", "/api/v1/gpu/status"}, {"GET", "/api/v1/gpu/cost"},
		{"POST", "/api/v1/gpu/start"}, {"POST", "/api/v1/gpu/stop"},
	}
	for _, rt := range routes {
		for _, c := range []struct {
			header string
			want   int
		}{
			{"", 401}, {"Basic x", 401}, {"Bearer sk-vkllm-REPLACE_ME", 401},
			{"Bearer sk-vkllm-clientkey1", 403}, {"Bearer sk-vkllm-mgmttoken1", -1}, // -1: any 2xx
		} {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if c.want == -1 {
				if rec.Code < 200 || rec.Code >= 300 {
					t.Errorf("%s %s with mgmt token: code = %d, want 2xx", rt.method, rt.path, rec.Code)
				}
				continue
			}
			if rec.Code != c.want {
				t.Errorf("%s %s with %q: code = %d, want %d", rt.method, rt.path, c.header, rec.Code, c.want)
			}
		}
	}
	if fc.Calls("start")+fc.Calls("stop") != 2 { // only the two 2xx calls reached the controller
		t.Fatalf("total controller calls = %d, want 2", fc.Calls("start")+fc.Calls("stop"))
	}
}

func TestRateLimit(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Public: "READY"}
	h, _ := newFullServer(t, fc, keys, clk, 60)

	for i := 0; i < 60; i++ {
		req := httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
		req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("call #%d: code = %d, want 200", i, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("call #61: code=%d Retry-After=%q, want 429/1", rec.Code, rec.Header().Get("Retry-After"))
	}

	clk.Advance(time.Second)
	req = httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("after 1s: code = %d, want 200", rec.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	h, _ := newFullServer(t, fc, keys, clk, 10000)

	big := strings.Repeat("a", 65537)
	req := httptest.NewRequest("POST", "/api/v1/gpu/stop", strings.NewReader(`{"x":"`+big+`"}`))
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 || fc.Calls("stop") != 0 {
		t.Fatalf("code = %d, stop calls = %d, want 413/0", rec.Code, fc.Calls("stop"))
	}

	req = httptest.NewRequest("POST", "/api/v1/gpu/stop", strings.NewReader(`{"x":"`+big+`"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("same oversized body without a token: code = %d, want 401 (auth runs before body limit)", rec.Code)
	}
}
