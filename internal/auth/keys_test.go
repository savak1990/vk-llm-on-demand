package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func captureLogs() (*bytes.Buffer, *slog.Logger) {
	buf := &bytes.Buffer{}
	return buf, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func containsMsg(buf *bytes.Buffer, msg string) bool {
	return strings.Contains(buf.String(), msg)
}

func writeKeyFile(t *testing.T, dir, name string, keys ...string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := ""
	for _, k := range keys {
		content += k + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadAndCheckBothSurfaces(t *testing.T) {
	dir := t.TempDir()
	clientPath := writeKeyFile(t, dir, "client-keys", "sk-vkllm-clientkey1")
	mgmtPath := writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken1")
	clk := fakeclock.New(time.Unix(0, 0))
	k, err := Load(clientPath, mgmtPath, clk, noopLogger())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !k.Loaded() {
		t.Fatal("Loaded() = false after a successful Load with both files non-empty")
	}

	keyID, err := k.Check("Bearer sk-vkllm-clientkey1", Inference)
	if err != nil || keyID != "clientke" { // first 8 chars after "sk-vkllm-"
		t.Fatalf("Check(client key, Inference) = %q, %v", keyID, err)
	}
	if _, err := k.Check("Bearer sk-vkllm-mgmttoken1", Inference); !errors.Is(err, ErrWrongSurface) {
		t.Fatalf("Check(mgmt token, Inference) = %v, want ErrWrongSurface", err)
	}
	if _, err := k.Check("Bearer sk-vkllm-mgmttoken1", Management); err != nil {
		t.Fatalf("Check(mgmt token, Management): %v", err)
	}
}

func TestCheckMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()
	clientPath := writeKeyFile(t, dir, "client-keys", "sk-vkllm-clientkey1")
	mgmtPath := writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken1")
	k, err := Load(clientPath, mgmtPath, fakeclock.New(time.Unix(0, 0)), noopLogger())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := k.Check("", Inference); !errors.Is(err, ErrMissing) {
		t.Fatalf("Check(empty) = %v, want ErrMissing", err)
	}
	if _, err := k.Check("Basic x", Inference); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Check(Basic x) = %v, want ErrMalformed", err)
	}
	if _, err := k.Check("Bearer sk-vkllm-unknownkey", Inference); !errors.Is(err, ErrUnknown) {
		t.Fatalf("Check(unknown) = %v, want ErrUnknown", err)
	}
}

func TestLoadRejectsKeyInBothLists(t *testing.T) {
	dir := t.TempDir()
	clientPath := writeKeyFile(t, dir, "client-keys", "sk-vkllm-shared")
	mgmtPath := writeKeyFile(t, dir, "management-tokens", "sk-vkllm-shared")
	if _, err := Load(clientPath, mgmtPath, fakeclock.New(time.Unix(0, 0)), noopLogger()); err == nil {
		t.Fatal("Load: want error when a value is present in both lists")
	}
}

func TestRunReloadsEvery10sAndKeepsOldSetsOnFailure(t *testing.T) {
	dir := t.TempDir()
	clientPath := writeKeyFile(t, dir, "client-keys", "sk-vkllm-clientkey1")
	mgmtPath := writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken1")
	clk := fakeclock.New(time.Unix(0, 0))
	k, err := Load(clientPath, mgmtPath, clk, noopLogger())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go k.Run(ctx)
	clk.BlockUntil(1)

	// Add a key, bump mtime, advance past the poll — new key accepted.
	writeKeyFile(t, dir, "client-keys", "sk-vkllm-clientkey1", "sk-vkllm-clientkey2")
	clk.Advance(10 * time.Second)
	clk.BlockUntil(1)
	if _, err := k.Check("Bearer sk-vkllm-clientkey2", Inference); err != nil {
		t.Fatalf("Check(new key) after reload: %v", err)
	}

	// Corrupt the file (key duplicated across lists) — old sets must survive.
	writeKeyFile(t, dir, "management-tokens", "sk-vkllm-mgmttoken1", "sk-vkllm-clientkey2")
	buf, log := captureLogs()
	k.log = log
	clk.Advance(10 * time.Second)
	clk.BlockUntil(1)
	if _, err := k.Check("Bearer sk-vkllm-clientkey2", Inference); err != nil {
		t.Fatalf("Check(key from before failed reload): %v, want still valid", err)
	}
	if !containsMsg(buf, "keys_reload_failed") {
		t.Fatal("want a keys_reload_failed log line after the bad reload")
	}
}
