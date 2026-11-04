package mgmt

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func newTestServer(t *testing.T, fc *fakeController) *server {
	t.Helper()
	s := &server{
		d:   Deps{Controller: fc, Clock: fakeclock.New(time.Unix(0, 0)), Config: config.Mgmt{}},
		mux: http.NewServeMux(), start: newCoalescer(), stop: newCoalescer(),
	}
	s.routes()
	return s
}

func doReq(s *server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func waitForWaiters(t *testing.T, c *coalescer, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.waiterCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("waiterCount() did not reach %d in time", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStartReadyReturns200AlreadyRunning(t *testing.T) {
	fc := newFakeController()
	fc.startSnap = controller.Snapshot{Public: "READY"}
	rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/start", "")
	if rec.Code != 200 {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var doc statusDocument
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Result != "already_running" {
		t.Fatalf("Result = %q, want already_running", doc.Result)
	}
}

func TestStartAllocatingReturns202Accepted(t *testing.T) {
	fc := newFakeController()
	fc.startSnap = controller.Snapshot{Public: "ALLOCATING", Desired: "RUNNING"}
	rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/start", "")
	if rec.Code != 202 {
		t.Fatalf("code = %d, want 202", rec.Code)
	}
}

func TestStartDenialReturns503WithReason(t *testing.T) {
	for _, reason := range []string{"no_offer", "budget_exhausted", "balance_low", "attempts_exhausted"} {
		fc := newFakeController()
		fc.startErr = controller.ErrGPUUnavailable
		fc.startSnap = controller.Snapshot{Unavailable: reason}
		rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/start", "")
		if rec.Code != 503 {
			t.Fatalf("reason=%s: code = %d, want 503", reason, rec.Code)
		}
		var body map[string]map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["error"]["code"] != "gpu_unavailable" || body["error"]["reason"] != reason {
			t.Fatalf("reason=%s: body = %v", reason, body)
		}
		if rec.Header().Get("Retry-After") != "" {
			t.Fatalf("reason=%s: want no Retry-After on 503", reason)
		}
	}
}

func TestStopReadyReturns202Draining(t *testing.T) {
	fc := newFakeController()
	fc.stopSnap = controller.Snapshot{Public: "DRAINING"}
	rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/stop", "")
	if rec.Code != 202 {
		t.Fatalf("code = %d, want 202", rec.Code)
	}
}

func TestStatusAndCost200(t *testing.T) {
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Public: "READY"}
	s := newTestServer(t, fc)
	if rec := doReq(s, "GET", "/api/v1/gpu/status", ""); rec.Code != 200 {
		t.Fatalf("status code = %d", rec.Code)
	}
	if rec := doReq(s, "GET", "/api/v1/gpu/cost", ""); rec.Code != 200 {
		t.Fatalf("cost code = %d", rec.Code)
	}
}

func TestUnknownPathAnd405(t *testing.T) {
	s := newTestServer(t, newFakeController())
	if rec := doReq(s, "GET", "/api/v1/gpu/bogus", ""); rec.Code != 404 {
		t.Fatalf("unknown path: code = %d, want 404", rec.Code)
	}
	if rec := doReq(s, "DELETE", "/api/v1/gpu/start", ""); rec.Code != 405 {
		t.Fatalf("wrong method: code = %d, want 405", rec.Code)
	}
}

func TestInvalidJSONBody(t *testing.T) {
	s := newTestServer(t, newFakeController())
	rec := doReq(s, "POST", "/api/v1/gpu/stop", `{"force":"yes"}`)
	if rec.Code != 400 {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	rec = doReq(s, "POST", "/api/v1/gpu/stop", `{"x":1}`)
	if rec.Code != 400 {
		t.Fatalf("unknown field: code = %d, want 400", rec.Code)
	}
}

func TestForceNotEnabled(t *testing.T) {
	fc := newFakeController()
	s := newTestServer(t, fc)
	s.d.Config.ShutdownAllowForce = false
	rec := doReq(s, "POST", "/api/v1/gpu/stop", `{"force":true}`)
	if rec.Code != 400 || fc.Calls("stop") != 0 {
		t.Fatalf("code = %d, stop calls = %d, want 400/0", rec.Code, fc.Calls("stop"))
	}
	var body map[string]map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"]["code"] != "force_not_enabled" {
		t.Fatalf("body = %v", body)
	}

	rec = doReq(s, "POST", "/api/v1/gpu/stop", `{"force":false}`)
	if rec.Code != 202 || fc.Calls("stop") != 1 {
		t.Fatalf("code = %d, stop calls = %d, want 202/1", rec.Code, fc.Calls("stop"))
	}

	s.d.Config.ShutdownAllowForce = true
	rec = doReq(s, "POST", "/api/v1/gpu/stop", `{"force":true}`)
	if rec.Code != 202 || fc.Calls("stop") != 2 {
		t.Fatalf("code = %d, stop calls = %d, want 202/2", rec.Code, fc.Calls("stop"))
	}
}

func TestStartCoalescesConcurrentCalls(t *testing.T) {
	fc := newFakeController()
	fc.startBlock = make(chan struct{})
	fc.startSnap = controller.Snapshot{Public: "READY"}
	s := newTestServer(t, fc)

	var wg sync.WaitGroup
	codes := make([]int, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = doReq(s, "POST", "/api/v1/gpu/start", "").Code
		}(i)
	}
	waitForWaiters(t, s.start, 49)
	close(fc.startBlock)
	wg.Wait()

	if fc.Calls("start") != 1 {
		t.Fatalf("Calls(start) = %d, want 1", fc.Calls("start"))
	}
	for i, code := range codes {
		if code != 200 {
			t.Fatalf("codes[%d] = %d, want 200", i, code)
		}
	}
}
