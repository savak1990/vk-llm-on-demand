package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func testGetenv(f *fakeServer, token string) func(string) string {
	return func(k string) string {
		switch k {
		case "VK_LLM_TOKEN":
			return token
		case "VK_LLM_API_ENDPOINT":
			return f.URL()
		}
		return ""
	}
}

func TestStartWalk(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"ALLOCATING"}`},
		fakeResponse{code: 200, body: `{"state":"STARTING"}`},
		fakeResponse{code: 200, body: `{"state":"STARTING"}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL"}`},
		fakeResponse{code: 200, body: `{"state":"READY","ready":true,"model":{"alias":"qwen3.6-35b-a3b"},"instance":{"gpu":"L40S"},"secondsUntilIdleShutdown":1800}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	getenv := testGetenv(f, "sk-vkllm-REPLACE_ME")
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, getenv, &stdout, &stderr, clk) }()
	for i := 0; i < 4; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("stderr lines = %d, want 4: %q", len(lines), stderr.String())
	}
	for i, want := range []string{"ALLOCATING", "STARTING", "LOADING_MODEL"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[3], "idle shutdown in 30 min") {
		t.Errorf("final line = %q, want it to contain %q", lines[3], "idle shutdown in 30 min")
	}
	if got := f.callCount("POST /api/v1/gpu/start"); got != 1 {
		t.Errorf("start calls = %d, want 1", got)
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 5 {
		t.Errorf("status calls = %d, want 5", got)
	}
	if got := f.callCount("POST /api/v1/gpu/stop"); got != 0 {
		t.Errorf("stop calls = %d, want 0", got)
	}
}

func TestStartWalkWithProgress(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"STARTING","instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871}}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL","instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871},"progress":{"downloadedBytes":0,"modelBytes":10000000000,"workerStages":[]}}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL","instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871},"progress":{"downloadedBytes":4000000000,"modelBytes":10000000000,"workerStages":[]}}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL","instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871},"progress":{"downloadedBytes":10000000000,"modelBytes":10000000000,"workerStages":["download_end"]}}`},
		fakeResponse{code: 200, body: `{"state":"READY","ready":true,"model":{"alias":"qwen3.6-35b-a3b"},"instance":{"gpu":"RTX 6000Ada"},"secondsUntilIdleShutdown":1800}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	getenv := testGetenv(f, "sk-vkllm-REPLACE_ME")
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, getenv, &stdout, &stderr, clk) }()
	for i := 0; i < 4; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	indexOf := func(substr string) int {
		for i, l := range lines {
			if strings.Contains(l, substr) {
				return i
			}
		}
		return -1
	}
	offer := indexOf("Renting an RTX 6000Ada in Sweden at $0.55/h (machine 117871); booting the worker…")
	pct := indexOf("Downloading the model: 40%")
	verify := indexOf("Download done; verifying the checksum…")
	if offer < 0 || pct < 0 || verify < 0 {
		t.Fatalf("stderr = %q, want offer/pct/verify lines all present (offer=%d pct=%d verify=%d)", lines, offer, pct, verify)
	}
	if offer >= pct || pct >= verify {
		t.Fatalf("stderr = %q, want offer < pct < verify, got offer=%d pct=%d verify=%d", lines, offer, pct, verify)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "ready:") {
		t.Errorf("last line = %q, want it to contain %q", last, "ready:")
	}
}

func TestStartWalkThroughDrainingShowsOnlyTheNewOffer(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"DRAINING","instance":{"gpu":"L40S","machineId":1,"priceUSDPerHour":0.5}}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"DELETING","instance":{"gpu":"L40S","machineId":1,"priceUSDPerHour":0.5}}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED"}`},
		fakeResponse{code: 200, body: `{"state":"ALLOCATING"}`},
		fakeResponse{code: 200, body: `{"state":"STARTING","instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871}}`},
		fakeResponse{code: 200, body: `{"state":"READY","ready":true,"model":{"alias":"qwen3.6-35b-a3b"},"instance":{"gpu":"RTX 6000Ada"},"secondsUntilIdleShutdown":1800}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	getenv := testGetenv(f, "sk-vkllm-REPLACE_ME")
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, getenv, &stdout, &stderr, clk) }()
	for i := 0; i < 4; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	out := stderr.String()
	if strings.Contains(out, "L40S") {
		t.Errorf("stderr = %q, want no mention of the draining instance's GPU", out)
	}
	if !strings.Contains(out, "Renting an RTX 6000Ada in Sweden at $0.55/h (machine 117871); booting the worker…") {
		t.Errorf("stderr = %q, want the new offer line", out)
	}
}

func TestStartWalkAcrossGenerationsResetsTracker(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"LOADING_MODEL","generation":1,"instance":{"gpu":"L40S","machineId":1,"priceUSDPerHour":0.5},"progress":{"downloadedBytes":9000000000,"modelBytes":10000000000,"workerStages":[]}}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"DRAINING","generation":1,"instance":{"gpu":"L40S","machineId":1,"priceUSDPerHour":0.5}}`},
		fakeResponse{code: 200, body: `{"state":"DELETING","generation":1,"instance":{"gpu":"L40S","machineId":1,"priceUSDPerHour":0.5}}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED","generation":1}`},
		fakeResponse{code: 200, body: `{"state":"ALLOCATING","generation":2}`},
		fakeResponse{code: 200, body: `{"state":"STARTING","generation":2,"instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871}}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL","generation":2,"instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871},"progress":{"downloadedBytes":0,"modelBytes":10000000000,"workerStages":[]}}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL","generation":2,"instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871},"progress":{"downloadedBytes":3000000000,"modelBytes":10000000000,"workerStages":[]}}`},
		fakeResponse{code: 200, body: `{"state":"LOADING_MODEL","generation":2,"instance":{"gpu":"RTX 6000Ada","geolocation":"Sweden","priceUSDPerHour":0.55,"machineId":117871},"progress":{"downloadedBytes":6000000000,"modelBytes":10000000000,"workerStages":[]}}`},
		fakeResponse{code: 200, body: `{"state":"READY","ready":true,"model":{"alias":"qwen3.6-35b-a3b"},"instance":{"gpu":"RTX 6000Ada"},"secondsUntilIdleShutdown":1800}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	getenv := testGetenv(f, "sk-vkllm-REPLACE_ME")
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, getenv, &stdout, &stderr, clk) }()
	for i := 0; i < 8; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	indexOf := func(substr string) int {
		for i, l := range lines {
			if strings.Contains(l, substr) {
				return i
			}
		}
		return -1
	}
	workerUpCount := 0
	for _, l := range lines {
		if strings.Contains(l, "Worker up; downloading the model") {
			workerUpCount++
		}
	}
	if workerUpCount != 2 {
		t.Errorf("stderr = %q, want \"Worker up; downloading the model\" twice, got %d", lines, workerUpCount)
	}
	secondOffer := indexOf("Renting an RTX 6000Ada in Sweden at $0.55/h (machine 117871); booting the worker…")
	pct30 := indexOf("Downloading the model: 30%")
	if secondOffer < 0 || pct30 < 0 || pct30 <= secondOffer {
		t.Errorf("stderr = %q, want \"Downloading the model: 30%%\" after the second offer line (secondOffer=%d pct30=%d)", lines, secondOffer, pct30)
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 200, body: `{"state":"READY","ready":true,"secondsUntilIdleShutdown":1440}`})
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	code := run([]string{"start"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already running, idle shutdown in 24 min") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if got := f.callCount("POST /api/v1/gpu/start"); got != 1 {
		t.Errorf("start calls = %d, want 1", got)
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 0 {
		t.Errorf("status calls = %d, want 0", got)
	}
}

func TestStartTimeout(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"LOADING_MODEL"}`})
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"LOADING_MODEL"}`})
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"start", "--timeout", "1m"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk)
	}()
	for i := 0; i < 12; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 1 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "timed out") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "timed out")
	}
	if !strings.Contains(stderr.String(), "not stopped") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "not stopped")
	}
	if got := f.callCount("POST /api/v1/gpu/stop"); got != 0 {
		t.Errorf("stop calls = %d, want 0", got)
	}
}

func TestStartNoOffer(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"ALLOCATING"}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED","unavailable":"no_offer"}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk) }()
	clk.BlockUntil(1)
	clk.Advance(5 * time.Second)
	code := <-done
	if code != 1 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no_offer") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "no_offer")
	}
	if !strings.Contains(stderr.String(), "start failed") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "start failed")
	}
}

func TestStopWalk(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING"}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"DRAINING"}`},
		fakeResponse{code: 200, body: `{"state":"DELETING"}`},
		fakeResponse{code: 200, body: `{"state":"STOPPED"}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"stop"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk) }()
	for i := 0; i < 2; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stderr lines = %d, want 3: %q", len(lines), stderr.String())
	}
	for i, want := range []string{"DRAINING", "DELETING", "STOPPED"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want it to contain %q", i, lines[i], want)
		}
	}

	// Stop on an already-stopped GPU: one request, "already stopped", no polling.
	f2 := newFake(t)
	f2.script("POST /api/v1/gpu/stop", fakeResponse{code: 200, body: `{"state":"STOPPED"}`})
	var stdout2, stderr2 bytes.Buffer
	code2 := run([]string{"stop"}, testGetenv(f2, "sk-vkllm-REPLACE_ME"), &stdout2, &stderr2, fake.New(time.Unix(0, 0)))
	if code2 != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code2, stderr2.String())
	}
	if !strings.Contains(stdout2.String(), "already stopped") {
		t.Errorf("stdout = %q", stdout2.String())
	}
	if got := f2.callCount("POST /api/v1/gpu/stop"); got != 1 {
		t.Errorf("stop calls = %d, want 1", got)
	}
	if got := f2.callCount("GET /api/v1/gpu/status"); got != 0 {
		t.Errorf("status calls = %d, want 0", got)
	}

	// Stuck at DELETING past the default 5m timeout: exit 1, 0 stop retries.
	f3 := newFake(t)
	f3.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING"}`})
	f3.script("GET /api/v1/gpu/status", fakeResponse{code: 200, body: `{"state":"DELETING"}`})
	clk3 := fake.New(time.Unix(0, 0))
	var stdout3, stderr3 bytes.Buffer
	done3 := make(chan int, 1)
	go func() {
		done3 <- run([]string{"stop"}, testGetenv(f3, "sk-vkllm-REPLACE_ME"), &stdout3, &stderr3, clk3)
	}()
	for i := 0; i < 60; i++ {
		clk3.BlockUntil(1)
		clk3.Advance(5 * time.Second)
	}
	code3 := <-done3
	if code3 != 1 {
		t.Fatalf("exit code = %d, stderr = %s", code3, stderr3.String())
	}
	if !strings.Contains(stderr3.String(), "timed out") {
		t.Errorf("stderr = %q, want it to contain %q", stderr3.String(), "timed out")
	}
	if got := f3.callCount("POST /api/v1/gpu/stop"); got != 1 {
		t.Errorf("stop calls = %d, want 1 (never retried)", got)
	}
}

func TestDetach(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 202, body: `{"state":"DRAINING"}`})
	clk := fake.New(time.Unix(0, 0))

	var stdout, stderr bytes.Buffer
	code := run([]string{"start", "--detach"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk)
	if code != 0 {
		t.Fatalf("start --detach exit code = %d, stderr = %s", code, stderr.String())
	}
	if got := f.callCount("POST /api/v1/gpu/start"); got != 1 {
		t.Errorf("start calls = %d, want 1", got)
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 0 {
		t.Errorf("status calls = %d, want 0 (no polling under --detach)", got)
	}

	var stdout2, stderr2 bytes.Buffer
	code2 := run([]string{"stop", "--detach"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout2, &stderr2, clk)
	if code2 != 0 {
		t.Fatalf("stop --detach exit code = %d, stderr = %s", code2, stderr2.String())
	}
	if got := f.callCount("POST /api/v1/gpu/stop"); got != 1 {
		t.Errorf("stop calls = %d, want 1", got)
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 0 {
		t.Errorf("status calls = %d, want 0 (no polling under --detach)", got)
	}
}

// TestWaitForReadyFalseDetaches confirms a config file's waitForReady: false
// is honoured even without --detach, so start makes one call and returns.
func TestWaitForReadyFalseDetaches(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("defaults:\n  waitForReady: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"start", "--config", cfgPath}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, fake.New(time.Unix(0, 0)))
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if got := f.callCount("POST /api/v1/gpu/start"); got != 1 {
		t.Errorf("start calls = %d, want 1", got)
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 0 {
		t.Errorf("status calls = %d, want 0 (waitForReady: false must skip polling)", got)
	}
}

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	getenv := func(string) string { return "" }
	code := run([]string{"version"}, getenv, &stdout, &stderr, fake.New(time.Unix(0, 0)))
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "vk-llm "+version) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), "vk-llm "+version)
	}
}

func TestUsage(t *testing.T) {
	clk := fake.New(time.Unix(0, 0))
	getenv := func(k string) string {
		if k == "VK_LLM_TOKEN" {
			return "sk-vkllm-REPLACE_ME"
		}
		if k == "VK_LLM_API_ENDPOINT" {
			return "http://127.0.0.1:1"
		}
		return ""
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"frobnicate"}, getenv, &stdout, &stderr, clk); code != 2 {
		t.Errorf("unknown subcommand: exit code = %d, want 2", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"start", "--timeout", "abc"}, getenv, &stdout, &stderr, clk); code != 2 {
		t.Errorf("bad --timeout: exit code = %d, want 2, stderr = %s", code, stderr.String())
	}
}

func TestRetryAfter429(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 429, headers: map[string]string{"Retry-After": "7"}, body: `{}`},
		fakeResponse{code: 200, body: `{"state":"READY","ready":true,"model":{"alias":"qwen3.6-35b-a3b"},"instance":{"gpu":"L40S"},"secondsUntilIdleShutdown":1800}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk) }()

	clk.BlockUntil(1)
	clk.Advance(5 * time.Second)
	if got := f.callCount("GET /api/v1/gpu/status"); got != 1 {
		t.Fatalf("status calls after 5s = %d, want 1 (must still be waiting for Retry-After=7s, not the 5s floor)", got)
	}
	clk.Advance(2 * time.Second)

	code := <-done
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ready:") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "ready:")
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 2 {
		t.Errorf("status calls = %d, want 2", got)
	}
}

func TestMidPollForbidden(t *testing.T) {
	f := newFake(t)
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 202, body: `{"state":"ALLOCATING"}`})
	f.script("GET /api/v1/gpu/status",
		fakeResponse{code: 200, body: `{"state":"ALLOCATING"}`},
		fakeResponse{code: 200, body: `{"state":"STARTING"}`},
		fakeResponse{code: 403, body: `{"error":{"code":"permission_denied"}}`},
	)
	clk := fake.New(time.Unix(0, 0))
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run([]string{"start"}, testGetenv(f, "sk-vkllm-REPLACE_ME"), &stdout, &stderr, clk) }()
	for i := 0; i < 2; i++ {
		clk.BlockUntil(1)
		clk.Advance(5 * time.Second)
	}
	code := <-done
	if code != 3 {
		t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "permission_denied") {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), "permission_denied")
	}
	if got := f.callCount("GET /api/v1/gpu/status"); got != 3 {
		t.Errorf("status calls = %d, want 3 (poll loop must not retry past the 403)", got)
	}
}

func TestNoTokenInOutput(t *testing.T) {
	const token = "sk-vkllm-REPLACE_ME"
	f := newFake(t)
	errBody := `{"error":{"code":"invalid_token"}}`
	f.script("POST /api/v1/gpu/start", fakeResponse{code: 401, body: errBody})
	f.script("GET /api/v1/gpu/status", fakeResponse{code: 401, body: errBody})
	f.script("POST /api/v1/gpu/stop", fakeResponse{code: 401, body: errBody})

	for _, args := range [][]string{{"start"}, {"status"}, {"stop"}, {"version"}} {
		var stdout, stderr bytes.Buffer
		clk := fake.New(time.Unix(0, 0))
		run(args, testGetenv(f, token), &stdout, &stderr, clk)
		combined := stdout.String() + stderr.String()
		if strings.Contains(combined, token) {
			t.Errorf("args %v: output contains the token: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}
