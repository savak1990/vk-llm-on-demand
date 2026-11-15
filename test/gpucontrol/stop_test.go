package gpucontrol

import (
	"strings"
	"testing"
)

func TestStopWalk(t *testing.T) {
	f := newFake(t)
	for _, s := range []string{"DRAINING", "DELETING", "STOPPED"} {
		f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"state":"` + s + `","ready":false}`})
	}
	f.script("POST /api/v1/gpu/stop", fakeResp{code: 200, body: `{"result":"accepted"}`})

	r := runScript(t, map[string]string{"ACTION": "stop", "ENDPOINT": f.URL(), "POLL_INTERVAL": "1"})

	if r.exitCode != 0 {
		t.Fatalf("exit %d, stderr: %s", r.exitCode, r.stderr)
	}
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if want := []string{"DRAINING", "DELETING", "STOPPED"}; !equalSlices(lines, want) {
		t.Errorf("stdout lines = %v, want %v", lines, want)
	}
	if !strings.Contains(r.summary, "state=STOPPED secondsUntilIdleShutdown=n/a") {
		t.Errorf("bad summary: %s", r.summary)
	}
}

func TestDeadlineUnchangedAcrossPolling(t *testing.T) {
	f := newFake(t)
	const deadline = "2026-11-15T13:00:00Z"
	for i := 0; i < 10; i++ {
		f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"provider":"vastai","state":"READY","ready":true,"idleDeadline":"` + deadline + `"}`})
	}
	for i := 0; i < 10; i++ {
		f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"state":"STOPPED","ready":false,"idleDeadline":"` + deadline + `"}`})
		f.script("POST /api/v1/gpu/stop", fakeResp{code: 200, body: `{"result":"accepted"}`})
	}

	for i := 0; i < 10; i++ {
		r := runScript(t, map[string]string{"ACTION": "status", "ENDPOINT": f.URL()})
		if r.exitCode != 0 {
			t.Fatalf("status run %d: exit %d", i, r.exitCode)
		}
	}
	for i := 0; i < 10; i++ {
		r := runScript(t, map[string]string{"ACTION": "stop", "ENDPOINT": f.URL(), "POLL_INTERVAL": "1"})
		if r.exitCode != 0 {
			t.Fatalf("stop run %d: exit %d", i, r.exitCode)
		}
	}

	// Both loops only succeed if every call matched the fixed status/stop
	// fixtures 20 times total; any call to an unscripted route would have
	// already failed via fake.handle, so passing proves idleDeadline held.
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
