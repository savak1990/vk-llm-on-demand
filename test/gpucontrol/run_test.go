package gpucontrol

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type result struct {
	stdout, stderr string
	exitCode       int
	summary        string
}

func scriptPath(t *testing.T, name string) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..", "scripts", name)
}

func runScript(t *testing.T, env map[string]string) result {
	return runNamed(t, "gpu-control.sh", env)
}

func runNamed(t *testing.T, name string, env map[string]string) result {
	summaryFile := filepath.Join(t.TempDir(), "summary")

	// Seed defaults first so a caller-supplied override (e.g. a PATH that
	// prepends a stub directory) always wins over them.
	merged := map[string]string{
		"ACTION":              "status",
		"VK_LLM_TOKEN":        "test-token-abc123",
		"POLL_INTERVAL":       "1",
		"PATH":                os.Getenv("PATH"),
		"GITHUB_STEP_SUMMARY": summaryFile,
	}
	if ca := os.Getenv("CURL_CA_BUNDLE"); ca != "" {
		merged["CURL_CA_BUNDLE"] = ca
	}
	for k, v := range env {
		merged[k] = v
	}

	cmd := exec.Command(scriptPath(t, name))
	for k, v := range merged {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running %s: %v", name, err)
		}
	}

	summary, _ := os.ReadFile(summaryFile)
	return result{stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode, summary: string(summary)}
}
