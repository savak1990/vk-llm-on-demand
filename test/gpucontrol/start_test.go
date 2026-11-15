package gpucontrol

import (
	"strings"
	"testing"
	"time"
)

func TestStartWalk(t *testing.T) {
	f := newFake(t)
	for _, s := range []string{"ALLOCATING", "STARTING", "LOADING_MODEL"} {
		f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: mustJSON(t, map[string]any{
			"state": s, "ready": false, "idleDeadline": "2026-11-15T12:00:00Z",
		})})
	}
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: mustJSON(t, map[string]any{
		"state": "READY", "ready": true, "idleDeadline": "2026-11-15T12:00:00Z",
		"secondsUntilIdleShutdown": 1800,
	})})
	f.script("POST /api/v1/gpu/start", fakeResp{code: 200, body: `{"result":"accepted"}`})

	start := time.Now()
	r := runScript(t, map[string]string{"ACTION": "start", "ENDPOINT": f.URL(), "POLL_INTERVAL": "1"})
	elapsed := time.Since(start)

	if r.exitCode != 0 {
		t.Fatalf("exit %d, stderr: %s", r.exitCode, r.stderr)
	}
	wantOrder := []string{"ALLOCATING", "STARTING", "LOADING_MODEL", "READY"}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if len(lines) != len(wantOrder) {
		t.Fatalf("stdout lines = %v, want states %v", lines, wantOrder)
	}
	for i, want := range wantOrder {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s, want <= 10s", elapsed)
	}
	calls := f.callLog("GET /api/v1/gpu/status")
	if len(calls) != 4 {
		t.Fatalf("got %d status calls, want 4", len(calls))
	}
	for i, c := range calls {
		if !strings.Contains(c.body, `"idleDeadline":"2026-11-15T12:00:00Z"`) {
			t.Errorf("status call %d idleDeadline changed: %s", i, c.body)
		}
	}
}

func TestStartTimeout(t *testing.T) {
	f := newFake(t)
	for i := 0; i < 5; i++ {
		f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"state":"ALLOCATING","ready":false}`})
	}
	f.script("POST /api/v1/gpu/start", fakeResp{code: 200, body: `{"result":"accepted"}`})

	r := runScript(t, map[string]string{
		"ACTION": "start", "ENDPOINT": f.URL(), "POLL_INTERVAL": "1", "START_MAX_WAIT": "2",
	})

	if r.exitCode != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", r.exitCode, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if last := lines[len(lines)-1]; last != "ALLOCATING" {
		t.Errorf("last printed state = %q, want ALLOCATING", last)
	}
	if !strings.Contains(r.stderr, "timed out") {
		t.Errorf("stderr missing timeout reason: %s", r.stderr)
	}
}

func TestStartDenied(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResp{code: 503, body: `{"error":{"code":"gpu_unavailable","reason":"no_offer","message":"no capacity right now"}}`})

	r := runScript(t, map[string]string{"ACTION": "start", "ENDPOINT": f.URL()})

	if r.exitCode != 1 {
		t.Fatalf("exit %d, want 1", r.exitCode)
	}
	if got := len(f.callLog("GET /api/v1/gpu/status")); got != 0 {
		t.Errorf("status calls = %d, want 0 (denial is on POST start itself)", got)
	}
	if !strings.Contains(r.stderr, "gpu_unavailable") || !strings.Contains(r.stderr, "no_offer") {
		t.Errorf("stderr missing error fields: %s", r.stderr)
	}
	if strings.Contains(r.stderr, `{"error"`) {
		t.Errorf("raw error body leaked to stderr: %s", r.stderr)
	}
}

func TestStartFailsMidPoll(t *testing.T) { // start denied after allocating, not on the first POST
	f := newFake(t)
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"state":"ALLOCATING","ready":false}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"state":"STOPPED","ready":false,"unavailable":"no_offer: no capacity"}`})
	f.script("POST /api/v1/gpu/start", fakeResp{code: 200, body: `{"result":"accepted"}`})

	r := runScript(t, map[string]string{"ACTION": "start", "ENDPOINT": f.URL(), "POLL_INTERVAL": "1"})

	if r.exitCode != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stderr, "no_offer") {
		t.Errorf("stderr missing unavailable reason: %s", r.stderr)
	}
}
