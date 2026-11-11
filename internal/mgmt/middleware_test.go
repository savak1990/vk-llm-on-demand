package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
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

func TestStartRejectsOversizedBody(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
	fc := newFakeController()
	h, _ := newFullServer(t, fc, keys, clk, 10000)

	big := strings.Repeat("a", 65537)
	req := httptest.NewRequest("POST", "/api/v1/gpu/start", strings.NewReader(`{"x":"`+big+`"}`))
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 || fc.Calls("start") != 0 {
		t.Fatalf("code = %d, start calls = %d, want 413/0", rec.Code, fc.Calls("start"))
	}
}

// TestRejectedRequestsAreObserved proves the observe wrapper sits
// outermost: a 401/403/429 — none of which ever reach a handler — must
// still each produce one metric increment and one log line.
func TestRejectedRequestsAreObserved(t *testing.T) {
	cases := []struct {
		name           string
		header         string
		rpm, preCalls  int
		wantCode       int
		wantKeyIDEmpty bool
	}{
		{name: "401 bad auth", header: "", rpm: 10000, wantCode: 401, wantKeyIDEmpty: true},
		{name: "403 wrong surface", header: "Bearer sk-vkllm-clientkey1", rpm: 10000, wantCode: 403, wantKeyIDEmpty: true},
		{name: "429 rate limited", header: "Bearer sk-vkllm-mgmttoken1", rpm: 1, preCalls: 1, wantCode: 429, wantKeyIDEmpty: false},
	}
	const op = "status"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := fakeclock.New(time.Unix(0, 0))
			keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
			fc := newFakeController()
			fc.statusSnap = controller.Snapshot{Public: "READY"}
			limiter := auth.NewLimiter(tc.rpm, clk)
			reg := prometheus.NewRegistry()
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
			h := New(Deps{Controller: fc, Keys: keys, Limiter: limiter, Clock: clk, Config: config.Mgmt{}, Reg: reg, Log: logger})

			newReq := func() *http.Request {
				req := httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
				if tc.header != "" {
					req.Header.Set("Authorization", tc.header)
				}
				return req
			}
			for i := 0; i < tc.preCalls; i++ {
				h.ServeHTTP(httptest.NewRecorder(), newReq())
			}
			logBuf.Reset() // only the call under test's line matters

			before := testutil.ToFloat64(mgmtRequestsTotal.WithLabelValues(op, strconv.Itoa(tc.wantCode)))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, newReq())
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tc.wantCode)
			}
			after := testutil.ToFloat64(mgmtRequestsTotal.WithLabelValues(op, strconv.Itoa(tc.wantCode)))
			if after-before != 1 {
				t.Fatalf("mgmtRequestsTotal{op=%s,code=%d} increased by %v, want 1", op, tc.wantCode, after-before)
			}

			lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("got %d log lines, want 1: %q", len(lines), logBuf.String())
			}
			var entry struct {
				Msg        string `json:"msg"`
				Op         string `json:"op"`
				StatusCode int    `json:"status_code"`
				KeyID      string `json:"key_id"`
			}
			if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
				t.Fatalf("Unmarshal log line: %v (%q)", err, lines[0])
			}
			if entry.Msg != "mgmt_request" || entry.Op != op || entry.StatusCode != tc.wantCode {
				t.Fatalf("log line = %+v, want msg=mgmt_request op=%s status_code=%d", entry, op, tc.wantCode)
			}
			if (entry.KeyID == "") != tc.wantKeyIDEmpty {
				t.Fatalf("log line key_id = %q, want empty=%v", entry.KeyID, tc.wantKeyIDEmpty)
			}
		})
	}
}

// TestAC13_RotatingOneManagementTokenKeepsTheOther proves removing one
// management token from the file, after a reload, leaves the other valid.
func TestAC13_RotatingOneManagementTokenKeepsTheOther(t *testing.T) {
	dir := t.TempDir()
	clientPath := writeKeyFile(t, dir, "client-keys", "sk-vkllm-clientkey1")
	mgmtPath := writeKeyFile(t, dir, "management-tokens",
		"sk-vkllm-mgmttoken1", "sk-vkllm-mgmttoken2")
	clk := fakeclock.New(time.Unix(0, 0))
	keys, err := auth.Load(clientPath, mgmtPath, clk, noopLogger())
	if err != nil {
		t.Fatal(err)
	}
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Public: "READY"}
	h, _ := newFullServer(t, fc, keys, clk, 60)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go keys.Run(ctx)
	clk.BlockUntil(1)

	// Remove mgmttoken2's line, keeping mgmttoken1 — a Secret-volume
	// file rewrite, the same shape as the rotation runbook's last step.
	writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken1")
	clk.Advance(10 * time.Second)
	clk.BlockUntil(1)

	req := httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("remaining token: code = %d, want 200", rec.Code)
	}

	req2 := httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
	req2.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken2")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != 401 {
		t.Fatalf("removed token: code = %d, want 401", rec2.Code)
	}
}

// TestAC2_StartDenialMetric drives all 4 start-denial reasons through a
// real HTTP request each (via the full New(...) handler, so the observe
// wrapper actually runs) and checks vkllm_mgmt_requests_total{op="start",
// code="503"} increased by exactly 4.
func TestAC2_StartDenialMetric(t *testing.T) {
	before := testutil.ToFloat64(mgmtRequestsTotal.WithLabelValues("start", "503"))

	for _, reason := range []string{"no_offer", "budget_exhausted", "balance_low", "attempts_exhausted"} {
		clk := fakeclock.New(time.Unix(0, 0))
		keys := authKeys(t, "sk-vkllm-clientkey1", "sk-vkllm-mgmttoken1")
		fc := newFakeController()
		fc.startErr = controller.ErrGPUUnavailable
		fc.startSnap = controller.Snapshot{Unavailable: reason}
		reg := prometheus.NewRegistry()
		h := New(Deps{Controller: fc, Keys: keys, Limiter: auth.NewLimiter(10000, clk), Clock: clk, Config: config.Mgmt{}, Reg: reg, Log: noopLogger()})

		req := httptest.NewRequest("POST", "/api/v1/gpu/start", nil)
		req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 503 {
			t.Fatalf("reason=%s: code = %d, want 503", reason, rec.Code)
		}
	}

	after := testutil.ToFloat64(mgmtRequestsTotal.WithLabelValues("start", "503"))
	if after-before != 4 {
		t.Fatalf("mgmtRequestsTotal{op=start,code=503} increased by %v, want 4", after-before)
	}
}
