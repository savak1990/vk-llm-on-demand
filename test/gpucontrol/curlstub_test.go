package gpucontrol

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeCurlStub creates an executable named "curl" that logs its own argv
// and stdin, then forwards both to the real curl so request handling still
// works end to end — AC8 needs to see where the token travels, not fake it.
func writeCurlStub(t *testing.T) (dir, argvLog, stdinLog string) {
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatalf("no real curl on PATH: %v", err)
	}
	dir = t.TempDir()
	argvLog = filepath.Join(t.TempDir(), "argv.log")
	stdinLog = filepath.Join(t.TempDir(), "stdin.log")

	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' "$@" >> %q
tee -a %q | %q "$@"
`, argvLog, stdinLog, realCurl)

	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing curl stub: %v", err)
	}
	return dir, argvLog, stdinLog
}

func TestTokenNeverOnArgv(t *testing.T) {
	stubDir, argvLog, stdinLog := writeCurlStub(t)
	token := "sk-vkllm-supersecrettoken"

	f := newFake(t)
	f.script("GET /api/v1/gpu/status", fakeResp{code: 200, body: `{"provider":"vastai","state":"READY","ready":true}`})
	f.script("POST /api/v1/gpu/start", fakeResp{code: 503, body: `{"error":{"code":"gpu_unavailable","reason":"no_offer","message":"no capacity"}}`})

	for action, wantExit := range map[string]int{"status": 0, "start": 1} {
		r := runScript(t, map[string]string{
			"ACTION": action, "ENDPOINT": f.URL(), "VK_LLM_TOKEN": token,
			"PATH": stubDir + ":" + os.Getenv("PATH"),
		})
		if r.exitCode != wantExit {
			t.Fatalf("%s: exit %d, want %d; stderr: %s", action, r.exitCode, wantExit, r.stderr)
		}
	}

	argvBytes, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("reading argv log: %v", err)
	}
	if strings.Contains(string(argvBytes), token) {
		t.Fatalf("token leaked into curl argv:\n%s", argvBytes)
	}

	stdinBytes, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatalf("reading stdin log: %v", err)
	}
	if !strings.Contains(string(stdinBytes), "Authorization: Bearer "+token) {
		t.Fatalf("token not found on stdin where it belongs:\n%s", stdinBytes)
	}
}
