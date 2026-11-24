package gpucontrol

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const clientKey = "client-key-xyz789"

func smokeEnv(f *fake, extra map[string]string) map[string]string {
	env := map[string]string{
		"ENDPOINT":          f.URL(),
		"VK_LLM_CLIENT_KEY": clientKey,
		"SMOKE_INTERVAL":    "1",
		"SMOKE_MAX_WAIT":    "5",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func scriptAllOK(f *fake) {
	f.script("GET /v1/models", fakeResp{code: 200, body: `{"object":"list","data":[]}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"state":"STOPPED"}`})
	f.script("GET /api/v1/gpu/cost", fakeResp{code: 200, body: `{}`})
}

func TestDevSmokeAllOK(t *testing.T) {
	f := newFake(t)
	scriptAllOK(f)
	r := runNamed(t, "dev-smoke.sh", smokeEnv(f, nil))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	if c := f.callLog("GET /v1/models"); len(c) != 1 || c[0].authHeader != "Bearer "+clientKey {
		t.Errorf("models calls = %+v, want one with the client key", c)
	}
	for _, route := range []string{"GET /api/v1/gpu/status", "GET /api/v1/gpu/cost"} {
		if c := f.callLog(route); len(c) != 1 || c[0].authHeader != "Bearer test-token-abc123" {
			t.Errorf("%s calls = %+v, want one with the management token", route, c)
		}
	}
}

func TestDevSmokeRetriesUntil200(t *testing.T) {
	f := newFake(t)
	f.script("GET /v1/models", fakeResp{code: 503, body: `{}`}, fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/cost", fakeResp{code: 200, body: `{}`})
	r := runNamed(t, "dev-smoke.sh", smokeEnv(f, nil))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	if n := len(f.callLog("GET /v1/models")); n != 2 {
		t.Errorf("models calls = %d, want 2 (one retry)", n)
	}
}

func TestDevSmokeFailsAfterDeadline(t *testing.T) {
	f := newFake(t)
	f.script("GET /v1/models", fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 503, body: `{}`})
	r := runNamed(t, "dev-smoke.sh", smokeEnv(f, map[string]string{"SMOKE_MAX_WAIT": "2"}))
	if r.exitCode != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stderr, "/api/v1/gpu/status") {
		t.Errorf("stderr = %q, want it to name the failing path", r.stderr)
	}
	if n := len(f.callLog("GET /api/v1/gpu/cost")); n != 0 {
		t.Errorf("cost calls = %d, want 0 after status failed", n)
	}
}

func TestDevSmokeRejectsPlainHTTP(t *testing.T) {
	f := newFake(t)
	r := runNamed(t, "dev-smoke.sh", smokeEnv(f, map[string]string{"ENDPOINT": "http://example.test"}))
	if r.exitCode != 1 {
		t.Fatalf("exit = %d, want 1", r.exitCode)
	}
}

func TestDevSmokeTokensNeverInArgv(t *testing.T) {
	f := newFake(t)
	scriptAllOK(f)
	dir, argvLog, _ := writeCurlStub(t)
	r := runNamed(t, "dev-smoke.sh", smokeEnv(f, map[string]string{"PATH": dir + ":" + os.Getenv("PATH")}))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	for _, secret := range []string{clientKey, "test-token-abc123"} {
		if strings.Contains(string(argv), secret) {
			t.Errorf("curl argv contains a token")
		}
	}
}

func TestDevSmokeIsReadOnly(t *testing.T) {
	raw, err := os.ReadFile(scriptPath(t, "dev-smoke.sh"))
	if err != nil {
		t.Fatalf("reading dev-smoke.sh: %v", err)
	}
	forbidden := regexp.MustCompile(`/gpu/(start|stop|budget)|chat/completions|-X |--request|--data|-d `)
	if m := forbidden.FindString(string(raw)); m != "" {
		t.Errorf("dev-smoke.sh contains %q; the smoke check must only GET", m)
	}
}
