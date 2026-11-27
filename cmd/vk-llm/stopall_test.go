package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

const (
	twoVols = `"cacheVolumes":[{"machineId":42,"state":"deleting"},{"machineId":43,"state":"deleting"}]`
	oneVol  = `"cacheVolumes":[{"machineId":42,"state":"deleting"}]`
)

// runStopAll drives the poll loop with the fake clock until run returns.
func runStopAll(t *testing.T, f *fakeServer, args ...string) (int, string, string) {
	t.Helper()
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(append([]string{"stop-all"}, args...), testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk)
	}()
	for {
		select {
		case code := <-done:
			return code, stdout.String(), stderr.String()
		case <-time.After(time.Millisecond):
			if _, ok := clk.NextDeadline(); ok {
				clk.Advance(5 * time.Second)
			}
		}
	}
}

func TestStopAllWaitsForGPUAndVolumes(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING",` + twoVols + `}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"DELETING",` + twoVols + `}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED",` + oneVol + `}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED"}`},
	)
	code, stdout, stderr := runStopAll(t, f, "--json")
	if code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
	if body := f.callsFor("POST /api/v1/gpu/stop")[0].body; body != `{"releaseCache":true}` {
		t.Fatalf("stop body = %q", body)
	}
	for _, want := range []string{"Stopping the GPU...", "GPU stopped.", "Deleting 1 cache volume...", "All Vast.ai resources released."} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if stdout != `{"state":"STOPPED"}` {
		t.Errorf("stdout = %q, want the final status document", stdout)
	}
}

func TestStopAllAlreadyStoppedStillWaitsForVolumes(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 200, body: `{"state":"STOPPED",` + oneVol + `}`})
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"STOPPED"}`})
	code, stdout, stderr := runStopAll(t, f)
	if code != 0 || f.callCount("GET /api/v1/gpu/status") != 1 {
		t.Fatalf("exit %d status calls %d stderr %s", code, f.callCount("GET /api/v1/gpu/status"), stderr)
	}
	if strings.Contains(stdout+stderr, "already stopped") || strings.Contains(stderr, "Stopping the GPU") ||
		!strings.Contains(stderr, "Deleting 1 cache volume...") || !strings.Contains(stderr, "All Vast.ai resources released.") {
		t.Fatalf("stdout %q stderr %q", stdout, stderr)
	}
}

func TestStopAllDetach(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DELETING",` + twoVols + `}`})
	code, stdout, stderr := runStopAll(t, f, "--detach")
	if code != 0 || f.callCount("GET /api/v1/gpu/status") != 0 {
		t.Fatalf("exit %d status calls %d stderr %s", code, f.callCount("GET /api/v1/gpu/status"), stderr)
	}
	if body := f.callsFor("POST /api/v1/gpu/stop")[0].body; body != `{"releaseCache":true}` {
		t.Fatalf("stop body = %q", body)
	}
	if !strings.Contains(stdout, "2 cache volumes") {
		t.Fatalf("stdout = %q, want what was requested", stdout)
	}
}

func TestStopAllRejectsForce(t *testing.T) {
	f := newFake(t)
	code, _, stderr := runStopAll(t, f, "--force")
	if code != 2 || f.callCount("POST /api/v1/gpu/stop") != 0 || !strings.Contains(stderr, "--force") {
		t.Fatalf("exit %d stop calls %d stderr %q; want 2, no request", code, f.callCount("POST /api/v1/gpu/stop"), stderr)
	}
}

func TestStopAllPollTokenRefused(t *testing.T) {
	for _, status := range []int{401, 403} {
		f := newFake(t)
		f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING"}`})
		f.script("GET /api/v1/gpu/status", fakeResponse{code: status, body: `{"error":{"code":"invalid_token"}}`})
		if code, _, stderr := runStopAll(t, f); code != 3 || f.callCount("GET /api/v1/gpu/status") != 1 {
			t.Fatalf("%d while polling: exit %d status calls %d stderr %q; want 3 after one poll", status, code, f.callCount("GET /api/v1/gpu/status"), stderr)
		}
	}
}

func TestStopAllPollRetries429And5xx(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING",` + oneVol + `}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 429, body: `{"error":{"code":"rate_limited"}}`, headers: map[string]string{"Retry-After": "10"}},
		fakeResponse{code: 500, body: `{"error":{"code":"internal_error"}}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED"}`},
	)
	code, _, stderr := runStopAll(t, f)
	if code != 0 || f.callCount("GET /api/v1/gpu/status") != 3 || !strings.Contains(stderr, "All Vast.ai resources released.") {
		t.Fatalf("exit %d status calls %d stderr %q; want 0 after 3 polls", code, f.callCount("GET /api/v1/gpu/status"), stderr)
	}
}

func TestStopForceRefused(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 400, body: `{"error":{"code":"force_not_enabled"}}`})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"stop", "--force"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0))); code != 1 ||
		!strings.Contains(stderr.String(), "force_not_enabled") {
		t.Fatalf("exit %d stderr %q; want 1 and force_not_enabled", code, stderr.String())
	}
}

func TestStopForceSendsForce(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DELETING"}`})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"stop", "--force", "--detach"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0))); code != 0 {
		t.Fatalf("exit %d stderr %s", code, stderr.String())
	}
	if body := f.callsFor("POST /api/v1/gpu/stop")[0].body; body != `{"force":true}` {
		t.Fatalf("stop body = %q", body)
	}
}

func TestStopAllTimeoutWithVolumeLeft(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING",` + oneVol + `}`})
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"STOPPED",` + oneVol + `}`})
	code, _, stderr := runStopAll(t, f, "--timeout", "1m")
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr %s", code, stderr)
	}
	if !strings.Contains(stderr, "timed out after 4m0s") || !strings.Contains(stderr, "1 cache volume left") {
		t.Fatalf("stderr = %q", stderr)
	}
	if n := f.callCount("GET /api/v1/gpu/status"); n < 45 || f.callCount("POST /api/v1/gpu/stop") != 1 {
		t.Fatalf("status calls %d stop calls %d, want polling for 4m and one stop", n, f.callCount("POST /api/v1/gpu/stop"))
	}
}

func TestStopAllTimeoutWhileGPURuns(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING"}`})
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"DELETING"}`})
	code, _, stderr := runStopAll(t, f, "--timeout", "1m")
	if code != 1 || !strings.Contains(stderr, "timed out after 1m0s in state DELETING") ||
		!strings.Contains(stderr, "run vk-llm stop-all") || strings.Contains(stderr, "run vk-llm stop,") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}
