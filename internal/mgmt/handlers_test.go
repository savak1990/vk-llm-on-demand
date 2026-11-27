package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
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
	rec := doReq(s, "GET", "/api/v1/gpu/bogus", "")
	if rec.Code != 404 {
		t.Fatalf("unknown path: code = %d, want 404", rec.Code)
	}
	var body map[string]map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"]["code"] != "not_found" {
		t.Fatalf("unknown path body = %v, want error.code = not_found", body)
	}
	if rec := doReq(s, "DELETE", "/api/v1/gpu/start", ""); rec.Code != 405 {
		t.Fatalf("wrong method: code = %d, want 405", rec.Code)
	}
}

func TestStartRejectsMalformedBody(t *testing.T) {
	s := newTestServer(t, newFakeController())
	rec := doReq(s, "POST", "/api/v1/gpu/start", `{"x":1}`)
	if rec.Code != 400 {
		t.Fatalf("unknown field: code = %d, want 400", rec.Code)
	}
	rec = doReq(s, "POST", "/api/v1/gpu/start", `not json`)
	if rec.Code != 400 {
		t.Fatalf("malformed json: code = %d, want 400", rec.Code)
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
	if fc.StopForce() {
		t.Fatal("StopForce() = true, want false for {\"force\":false}")
	}

	s.d.Config.ShutdownAllowForce = true
	rec = doReq(s, "POST", "/api/v1/gpu/stop", `{"force":true}`)
	if rec.Code != 202 || fc.Calls("stop") != 2 {
		t.Fatalf("code = %d, stop calls = %d, want 202/2", rec.Code, fc.Calls("stop"))
	}
	if !fc.StopForce() {
		t.Fatal("StopForce() = false, want true for {\"force\":true} with allowForce enabled")
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

func TestStopCoalescesConcurrentCalls(t *testing.T) {
	fc := newFakeController()
	fc.stopBlock = make(chan struct{})
	fc.stopSnap = controller.Snapshot{Public: "DRAINING"}
	s := newTestServer(t, fc)

	var wg sync.WaitGroup
	codes := make([]int, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = doReq(s, "POST", "/api/v1/gpu/stop", "").Code
		}(i)
	}
	waitForWaiters(t, s.stop, 49)
	close(fc.stopBlock)
	wg.Wait()

	if fc.Calls("stop") != 1 {
		t.Fatalf("Calls(stop) = %d, want 1", fc.Calls("stop"))
	}
	for i, code := range codes {
		if code != 202 {
			t.Fatalf("codes[%d] = %d, want 202", i, code)
		}
	}
}

func TestStopReleaseCache(t *testing.T) {
	fc := newFakeController()
	fc.stopSnap = controller.Snapshot{Public: "DRAINING"}
	s := newTestServer(t, fc)
	for _, body := range []string{"", `{"force":false}`, `{"releaseCache":false}`} {
		if rec := doReq(s, "POST", "/api/v1/gpu/stop", body); rec.Code != 202 {
			t.Fatalf("body %q: code = %d, want 202", body, rec.Code)
		}
	}
	if fc.Calls("release") != 0 {
		t.Fatalf("release calls = %d after plain stops, want 0", fc.Calls("release"))
	}
	if rec := doReq(s, "POST", "/api/v1/gpu/stop", `{"releaseCache":true}`); rec.Code != 202 || fc.Calls("release") != 1 || fc.Calls("stop") != 4 {
		t.Fatalf("code = %d, release %d, stop %d; want 202/1/4", rec.Code, fc.Calls("release"), fc.Calls("stop"))
	}

	fc.stopSnap = controller.Snapshot{Public: "STOPPED"}
	rec := doReq(s, "POST", "/api/v1/gpu/stop", `{"releaseCache":true}`)
	if rec.Code != 200 || fc.Calls("release") != 2 || !strings.Contains(rec.Body.String(), `"result":"already_stopped"`) {
		t.Fatalf("already stopped: code = %d, release %d, body %s; want 200, 2, already_stopped", rec.Code, fc.Calls("release"), rec.Body)
	}

	if rec := doReq(s, "POST", "/api/v1/gpu/stop", `{"force":true,"releaseCache":true}`); rec.Code != 400 || fc.Calls("release") != 2 {
		t.Fatalf("refused force: code = %d, release %d; want 400 and no release", rec.Code, fc.Calls("release"))
	}

	if rec := doReq(s, "POST", "/api/v1/gpu/stop", `{"releaseCache":"yes"}`); rec.Code != 400 || fc.Calls("release") != 2 ||
		!strings.Contains(rec.Body.String(), "invalid_json") {
		t.Fatalf(`{"releaseCache":"yes"}: code = %d, release %d, body %s; want 400 invalid_json`, rec.Code, fc.Calls("release"), rec.Body)
	}

	fc.releaseErr = errors.New("store down")
	rec = doReq(s, "POST", "/api/v1/gpu/stop", `{"releaseCache":true}`)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), "stop accepted; cache release failed") {
		t.Fatalf("release error: code = %d, body %s; want 500 naming the accepted stop", rec.Code, rec.Body)
	}
}

func TestStopReleaseCacheOutlivesClientDisconnect(t *testing.T) {
	fc := newFakeController()
	fc.stopSnap = controller.Snapshot{Public: "DRAINING"}
	s := newTestServer(t, fc)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/api/v1/gpu/stop", strings.NewReader(`{"releaseCache":true}`)).WithContext(ctx)
	s.mux.ServeHTTP(httptest.NewRecorder(), req)
	if fc.Calls("release") != 1 || fc.releaseCtxErr != nil {
		t.Fatalf("release calls %d ctx err %v; want 1 call on a live context", fc.Calls("release"), fc.releaseCtxErr)
	}
	if _, ok := fc.releaseDeadline(); !ok {
		t.Fatal("release context has no deadline; want a bounded write")
	}
}

func TestStopReleaseCacheSurvivesCoalescing(t *testing.T) {
	fc := newFakeController()
	fc.stopBlock = make(chan struct{})
	fc.stopSnap = controller.Snapshot{Public: "DRAINING"}
	s := newTestServer(t, fc)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); doReq(s, "POST", "/api/v1/gpu/stop", "") }()
	deadline := time.Now().Add(5 * time.Second)
	for fc.Calls("stop") < 1 {
		if time.Now().After(deadline) {
			t.Fatal("plain stop never reached the controller")
		}
		time.Sleep(time.Millisecond)
	}
	code := 0
	go func() { defer wg.Done(); code = doReq(s, "POST", "/api/v1/gpu/stop", `{"releaseCache":true}`).Code }()
	waitForWaiters(t, s.stop, 1)
	close(fc.stopBlock)
	wg.Wait()

	if fc.Calls("stop") != 1 || fc.Calls("release") != 1 || code != 202 {
		t.Fatalf("stop %d, release %d, code %d; want 1/1/202", fc.Calls("stop"), fc.Calls("release"), code)
	}
}

func TestStartBudgetExhaustedMessage(t *testing.T) {
	fc := newFakeController()
	fc.startErr = controller.ErrGPUUnavailable
	fc.startSnap = controller.Snapshot{Unavailable: "budget_exhausted", BudgetMonthUSD: 30, SpendMTDUSD: 30.12}
	rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/start", "")
	var body map[string]map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	want := "monthly budget of $30.00 reached ($30.12 spent); raise it with vk-llm budget set"
	if body["error"]["message"] != want {
		t.Fatalf("message = %q, want %q", body["error"]["message"], want)
	}
}

func TestStartOtherDenialsKeepEmptyMessage(t *testing.T) {
	fc := newFakeController()
	fc.startErr = controller.ErrGPUUnavailable
	fc.startSnap = controller.Snapshot{Unavailable: "no_offer"}
	rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/start", "")
	var body map[string]map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"]["message"] != "" {
		t.Fatalf("message = %q, want empty for no_offer", body["error"]["message"])
	}
}

func TestRentals200(t *testing.T) {
	fc := newFakeController()
	fc.statusSnap = controller.Snapshot{Rentals: []store.Rental{{Gen: 7, Geo: "Quebec, CA"}}}
	rec := doReq(newTestServer(t, fc), "GET", "/api/v1/gpu/rentals", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"geo":"Quebec, CA"`) {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(newTestServer(t, fc), "POST", "/api/v1/gpu/rentals", ""); rec.Code != 405 {
		t.Fatalf("POST code = %d, want 405", rec.Code)
	}
}
