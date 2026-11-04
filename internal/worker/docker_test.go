//go:build integration

package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const stubImage = "vkllm-worker-stub-test:latest"

// buildStubImage copies the stub build context into a temp dir, writes
// the current onstart script there (regenerated from Render() on every
// run, so the harness always tests the real artifact), and builds the
// image from that copy. Building from a temp dir, rather than writing
// into testdata/stub/ directly, keeps the source tree clean.
func buildStubImage(t *testing.T) {
	t.Helper()
	srcDir := "testdata/stub"
	dir := t.TempDir()

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, info.Mode()); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "onstart.sh"), []byte(Render()), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("docker", "build", "-t", stubImage, dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker build failed: %v\n%s", err, out)
	}
}

// runStubContainer runs the stub image with env, waits up to timeout for
// it to exit, and returns its exit code, combined stdout, and name (the
// caller must remove it).
func runStubContainer(t *testing.T, name string, env map[string]string, timeout time.Duration) (exitCode int, stdout string, cleanup func()) {
	t.Helper()
	args := []string{"run", "--name", name}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, stubImage)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	cleanup = func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	}

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("container %s did not exit within %s; output:\n%s", name, timeout, buf.String())
	}

	inspect := exec.Command("docker", "inspect", "-f", "{{.State.ExitCode}}", name)
	out, err := inspect.Output()
	if err != nil {
		t.Fatalf("docker inspect failed: %v (run error was: %v)", err, runErr)
	}
	exitCode, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("docker inspect exit code not a number: %q", out)
	}
	return exitCode, buf.String(), cleanup
}

// copyFromContainer reads path out of the (possibly exited) container's
// filesystem. `docker cp ... -` writes a tar stream, not raw bytes, so
// this copies into a temp dir and reads the real file back.
func copyFromContainer(t *testing.T, name, path string) []byte {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, "out")
	cmd := exec.Command("docker", "cp", name+":"+path, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker cp %s:%s failed: %v\n%s", name, path, err, out)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading copied file: %v", err)
	}
	return data
}

// lsFromContainer lists dir from the (possibly exited) container's
// filesystem. The container may already have exited by the time this
// runs, so it copies the directory out rather than using `docker exec`,
// which requires a running container.
func lsFromContainer(t *testing.T, name, dir string) string {
	t.Helper()
	tmp := t.TempDir()
	out, err := exec.Command("docker", "cp", name+":"+dir, tmp).CombinedOutput()
	if err != nil {
		t.Fatalf("docker cp %s:%s failed: %v\n%s", name, dir, err, out)
	}
	entries, err := os.ReadDir(filepath.Join(tmp, filepath.Base(dir)))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		sb.WriteString(e.Name())
		sb.WriteString(" ")
		sb.WriteString(strconv.FormatInt(info.Size(), 10))
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestWorkerScriptChecksumMismatch(t *testing.T) {
	buildStubImage(t)
	name := "vkllm-worker-test-checksum"
	start := time.Now()
	const key = "test-secret-key-value"
	exitCode, stdout, cleanup := runStubContainer(t, name, map[string]string{
		"MODEL_URL":        "file:///src.bin",
		"MODEL_SHA256":     strings.Repeat("0", 64), // deliberately wrong
		"MODEL_ALIAS":      "test-alias",
		"CTX":              "4096",
		"PARALLEL":         "1",
		"WORKER_API_KEY":   key,
		"DEADMAN_SECONDS":  "60",
		"POLL_SECONDS":     "1",
		"REASONING_FORMAT": "deepseek",
	}, 10*time.Second)
	defer cleanup()
	elapsed := time.Since(start)

	if exitCode != 3 {
		t.Errorf("exit code = %d, want 3\noutput:\n%s", exitCode, stdout)
	}
	if elapsed >= 10*time.Second {
		t.Errorf("container took %s to exit, want < 10s", elapsed)
	}
	if strings.Contains(stdout, "server_spawn") {
		t.Errorf("stdout contains server_spawn, want none before a checksum failure")
	}
	if !strings.Contains(stdout, "VKLLM_STAGE fail_checksum") {
		t.Errorf("stdout does not contain the fail_checksum stage line:\n%s", stdout)
	}
	if !strings.Contains(stdout, "WRAPPER_GOT_TERM") {
		t.Errorf("stdout does not show PID 1's TERM trap firing:\n%s", stdout)
	}
	if strings.Contains(stdout, key) {
		t.Errorf("stdout leaks the worker API key")
	}
}

func TestWorkerScriptDeadMan(t *testing.T) {
	buildStubImage(t)
	fixture, err := os.ReadFile("testdata/stub/src.bin")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(fixture)
	goodSHA := hex.EncodeToString(h[:])

	name := "vkllm-worker-test-deadman"
	const key = "test-secret-key-value"
	exitCode, stdout, cleanup := runStubContainer(t, name, map[string]string{
		"MODEL_URL":        "file:///src.bin",
		"MODEL_SHA256":     goodSHA,
		"MODEL_ALIAS":      "test-alias",
		"CTX":              "4096",
		"PARALLEL":         "1",
		"WORKER_API_KEY":   key,
		"DEADMAN_SECONDS":  "3",
		"POLL_SECONDS":     "1",
		"REASONING_FORMAT": "deepseek",
	}, 15*time.Second)
	defer cleanup()

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\noutput:\n%s", exitCode, stdout)
	}
	if !strings.Contains(stdout, "WRAPPER_GOT_TERM") {
		t.Errorf("stdout does not show PID 1's TERM trap firing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "STUB_GONE") {
		t.Errorf("stdout does not confirm the stub llama-server process was killed:\n%s", stdout)
	}

	stages, err := ParseStages(strings.NewReader(stdout))
	if err != nil {
		t.Fatalf("ParseStages() on the container's stdout: %v", err)
	}
	spawn, ok := stages["server_spawn"]
	if !ok {
		t.Fatalf("stdout does not contain the server_spawn stage line:\n%s", stdout)
	}
	idle, ok := stages["idle_exit"]
	if !ok {
		t.Fatalf("stdout does not contain the idle_exit stage line:\n%s", stdout)
	}
	if d := idle.Sub(spawn); d >= 6*time.Second {
		t.Errorf("idle_exit came %s after server_spawn, want < 6s", d)
	}

	modelOut := copyFromContainer(t, name, "/models/model.gguf")
	gotSHA := sha256.Sum256(modelOut)
	if hex.EncodeToString(gotSHA[:]) != goodSHA {
		t.Errorf("reassembled /models/model.gguf does not match the source file's sha256")
	}

	stageLog := copyFromContainer(t, name, "/var/log/vkllm-stages.log")
	modelsListing := lsFromContainer(t, name, "/models")
	for _, haystack := range []struct{ name, data string }{
		{"stdout", stdout},
		{"stage log", string(stageLog)},
		{"/models listing", modelsListing},
	} {
		if strings.Contains(haystack.data, key) {
			t.Errorf("%s leaks the worker API key", haystack.name)
		}
	}
	if !strings.Contains(string(stageLog), "idle_exit") {
		t.Errorf("stage log does not contain idle_exit:\n%s", stageLog)
	}
}
