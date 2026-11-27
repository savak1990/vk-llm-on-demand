package gpucontrol

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const smokeClientKey = "client-key-xyz789"

func smokePath(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "deploy", "helm", "gpu-on-demand", "files", "smoke.sh")
}

func keysDir(t *testing.T) string {
	dir := t.TempDir()
	for name, v := range map[string]string{
		"client-keys":       smokeClientKey + "\nsecond-client-key\n",
		"management-tokens": "test-token-abc123\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func smokeEnv(t *testing.T, f *fake, extra map[string]string) map[string]string {
	env := map[string]string{"BASE": f.URL(), "KEYS_DIR": keysDir(t), "SMOKE_INTERVAL": "1", "SMOKE_MAX_WAIT": "5"}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func scriptAllOK(f *fake) {
	f.script("GET /v1/models", fakeResp{code: 200, body: `{"object":"list","data":[]}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/cost", fakeResp{code: 200, body: `{}`})
}

func TestSmokeAllOK(t *testing.T) {
	f := newFake(t)
	scriptAllOK(f)
	r := runPath(t, smokePath(t), smokeEnv(t, f, nil))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	if c := f.callLog("GET /v1/models"); len(c) != 1 || c[0].authHeader != "Bearer "+smokeClientKey {
		t.Errorf("models calls = %+v, want one with the first client key", c)
	}
	for _, route := range []string{"GET /api/v1/gpu/status", "GET /api/v1/gpu/cost"} {
		if c := f.callLog(route); len(c) != 1 || c[0].authHeader != "Bearer test-token-abc123" {
			t.Errorf("%s calls = %+v, want one with the management token", route, c)
		}
	}
}

func TestSmokeRetriesUntil200(t *testing.T) {
	f := newFake(t)
	f.script("GET /v1/models", fakeResp{code: 503, body: `{}`}, fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/cost", fakeResp{code: 200, body: `{}`})
	r := runPath(t, smokePath(t), smokeEnv(t, f, nil))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	if n := len(f.callLog("GET /v1/models")); n != 2 {
		t.Errorf("models calls = %d, want 2", n)
	}
}

func TestSmokeFailsAfterDeadline(t *testing.T) {
	f := newFake(t)
	f.script("GET /v1/models", fakeResp{code: 200, body: `{}`})
	f.script("GET /api/v1/gpu/status", fakeResp{code: 503, body: `{}`})
	r := runPath(t, smokePath(t), smokeEnv(t, f, map[string]string{"SMOKE_MAX_WAIT": "2"}))
	if r.exitCode != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", r.exitCode, r.stderr)
	}
	if !strings.Contains(r.stderr, "/api/v1/gpu/status") {
		t.Errorf("stderr = %q, want the failing path", r.stderr)
	}
	if n := len(f.callLog("GET /api/v1/gpu/cost")); n != 0 {
		t.Errorf("cost calls = %d, want 0", n)
	}
}

func TestSmokeNeedsKeys(t *testing.T) {
	f := newFake(t)
	r := runPath(t, smokePath(t), smokeEnv(t, f, map[string]string{"KEYS_DIR": t.TempDir()}))
	if r.exitCode != 1 {
		t.Fatalf("exit = %d, want 1", r.exitCode)
	}
}

func TestSmokeTokensNeverInArgv(t *testing.T) {
	f := newFake(t)
	scriptAllOK(f)
	dir, argvLog, _ := writeCurlStub(t)
	r := runPath(t, smokePath(t), smokeEnv(t, f, map[string]string{"PATH": dir + ":" + os.Getenv("PATH")}))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	argv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{smokeClientKey, "test-token-abc123"} {
		if strings.Contains(string(argv), s) {
			t.Error("curl argv contains a token")
		}
	}
}

func TestSmokeSkipsBlankLinesAndCRLF(t *testing.T) {
	f := newFake(t)
	scriptAllOK(f)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "client-keys"), []byte("\r\n"+smokeClientKey+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "management-tokens"), []byte("test-token-abc123\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := runPath(t, smokePath(t), smokeEnv(t, f, map[string]string{"KEYS_DIR": dir}))
	if r.exitCode != 0 {
		t.Fatalf("exit = %d, stderr = %s", r.exitCode, r.stderr)
	}
	if c := f.callLog("GET /v1/models"); len(c) != 1 || c[0].authHeader != "Bearer "+smokeClientKey {
		t.Errorf("models calls = %+v, want one with %q", c, "Bearer "+smokeClientKey)
	}
}

func TestSmokeIsReadOnly(t *testing.T) {
	raw, err := os.ReadFile(smokePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`/gpu/(start|stop|budget)|chat/completions|-X |--request|--data|-d `).FindString(string(raw)); m != "" {
		t.Errorf("smoke.sh contains %q; it must only GET", m)
	}
}
