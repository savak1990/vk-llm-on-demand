package deploydev

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoScript(t *testing.T, name string) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "scripts", name)
}

// stubAWS puts an `aws` on PATH that writes stderr and exits 254, like a failed get-parameter.
func stubAWS(t *testing.T, stderr string) string {
	dir := t.TempDir()
	body := "#!/usr/bin/env bash\necho '" + stderr + "' >&2\nexit 254\n"
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(body), 0o755); err != nil {
		t.Fatalf("writing aws stub: %v", err)
	}
	return dir
}

func run(t *testing.T, pathDir string, args ...string) (int, string) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = []string{
		"PATH=" + pathDir + ":" + os.Getenv("PATH"),
		"PROJECT_NAME=p",
		"RUNNER_TEMP=" + t.TempDir(),
		"HOME=" + t.TempDir(),
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stderr.String()
	default:
		t.Fatalf("running %v: %v", args, err)
		return -1, ""
	}
}

func TestKubeconfigMissingParameterIsUnreachable(t *testing.T) {
	dir := stubAWS(t, "An error occurred (ParameterNotFound) when calling the GetParameter operation")
	code, stderr := run(t, dir, repoScript(t, "kubeconfig.sh"))
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "/p/cluster/llm-deploy/endpoint") {
		t.Errorf("stderr = %q, want the parameter path", stderr)
	}
}

func TestKubeconfigAccessDeniedFails(t *testing.T) {
	dir := stubAWS(t, "An error occurred (AccessDeniedException): User is not authorized to perform: ssm:GetParameter")
	code, stderr := run(t, dir, repoScript(t, "kubeconfig.sh"))
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr)
	}
	if !strings.Contains(stderr, "vk-llm-ci-role") {
		t.Errorf("stderr = %q, want it to name the role to fix", stderr)
	}
}

func TestDeployDevPassesUnreachableThrough(t *testing.T) {
	dir := stubAWS(t, "An error occurred (ParameterNotFound)")
	code, stderr := run(t, dir, repoScript(t, "deploy-dev.sh"), "0.2.1-main.abc1234", strings.Repeat("a", 40))
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr = %s", code, stderr)
	}
}

func TestDeployDevNeedsBothArguments(t *testing.T) {
	code, _ := run(t, t.TempDir(), repoScript(t, "deploy-dev.sh"), "0.2.1-main.abc1234")
	if code == 0 || code == 2 {
		t.Fatalf("exit = %d, want a usage failure that is not 0 or 2", code)
	}
}
