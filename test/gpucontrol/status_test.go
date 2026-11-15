package gpucontrol

import (
	"strings"
	"testing"
)

func TestStatus(t *testing.T) {
	f := newFake(t)
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: mustJSON(t, map[string]any{
		"provider": "vastai", "state": "READY", "ready": true,
		"instance":                 map[string]any{"gpu": "L40S", "priceUSDPerHour": 0.71},
		"model":                    map[string]any{"alias": "qwen3.6-35b-a3b"},
		"uptimeSeconds":            1830,
		"cost":                     map[string]any{"estimatedInstanceUSD": 0.36, "estimatedMonthUSD": 12.1},
		"secondsUntilIdleShutdown": 1440,
	})})

	r := runScript(t, map[string]string{"ACTION": "status", "ENDPOINT": f.URL()})

	if r.exitCode != 0 {
		t.Fatalf("exit %d, stderr: %s", r.exitCode, r.stderr)
	}
	for _, want := range []string{
		"provider=vastai", "state=READY", "gpu=L40S", "model=qwen3.6-35b-a3b",
		"ready=true", "priceUSDPerHour=0.71", "uptimeSeconds=1830",
		"estimatedInstanceUSD=0.36", "estimatedMonthUSD=12.1", "secondsUntilIdleShutdown=1440",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q; got: %s", want, r.stdout)
		}
	}
	if !strings.Contains(r.summary, "state=READY secondsUntilIdleShutdown=1440") {
		t.Errorf("bad summary: %s", r.summary)
	}
	if got := len(f.callLog("GET /api/v1/gpu/status")); got != 1 {
		t.Errorf("call count = %d, want 1", got)
	}
}

func TestEndpointMasked(t *testing.T) {
	// An endpoint nothing listens on: connection refused, no real network dependency.
	r := runScript(t, map[string]string{"ACTION": "status", "ENDPOINT": "https://llm.lab.example.com"})

	if r.exitCode != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", r.exitCode, r.stderr)
	}
	if !strings.HasPrefix(r.stderr, "::add-mask::llm.lab.example.com") {
		t.Errorf("expected ::add-mask:: as first line of stderr, got: %s", r.stderr)
	}
	rest := strings.TrimPrefix(r.stderr, "::add-mask::llm.lab.example.com")
	if strings.Contains(rest, "llm.lab.example.com") {
		t.Errorf("hostname leaked unmasked after the mask line: %s", rest)
	}
	if strings.Contains(r.stdout, "llm.lab.example.com") {
		t.Errorf("hostname leaked unmasked on stdout: %s", r.stdout)
	}
}
