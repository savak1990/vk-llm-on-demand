// internal/gateway/workerseed_test.go
package gateway

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func TestLoadWorkerSeed(t *testing.T) {
	w, err := LoadWorkerSeed("testdata/worker-seed", nil)
	if err != nil {
		t.Fatalf("LoadWorkerSeed: %v", err)
	}
	if len(w.Bytes()) != 32 {
		t.Fatalf("Bytes length = %d, want 32", len(w.Bytes()))
	}
	if err := w.Loaded(); err != nil {
		t.Fatalf("Loaded: %v", err)
	}
}

func TestLoadWorkerSeedRequires32Bytes(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/seed"
	if err := os.WriteFile(path, []byte("tooshort"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	w, err := LoadWorkerSeed(path, nil)
	if err != nil {
		t.Fatalf("LoadWorkerSeed: %v", err)
	}
	if err := w.Loaded(); err == nil {
		t.Fatal("Loaded: want error for a non-32-byte seed")
	}
}

func TestWorkerSeedBytesAreNeverTransformed(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/seed"
	raw := make([]byte, 32)
	raw[0], raw[31] = ' ', '\n' // deliberately shaped like something TrimSpace would alter
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	w, err := LoadWorkerSeed(path, nil)
	if err != nil {
		t.Fatalf("LoadWorkerSeed: %v", err)
	}
	if got := w.Bytes(); len(got) != 32 || got[0] != ' ' || got[31] != '\n' {
		t.Fatalf("Bytes() = %v, want the 32 bytes on disk untouched", got)
	}
}

func TestWorkerSeedReload(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/seed"
	first := make([]byte, 32)
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	clk := fakeclock.New(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	w, err := LoadWorkerSeed(path, clk)
	if err != nil {
		t.Fatalf("LoadWorkerSeed: %v", err)
	}

	second := make([]byte, 32)
	second[0] = 0xFF
	if err := os.WriteFile(path, second, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	clk.BlockUntil(1)
	clk.Advance(workerSeedReloadInterval)

	rotated := make(chan struct{})
	go func() {
		for w.Bytes()[0] != 0xFF {
			runtime.Gosched()
		}
		close(rotated)
	}()

	select {
	case <-rotated:
	case <-time.After(2 * time.Second):
		t.Fatal("rotated seed never took effect after one reload tick")
	}
	cancel()
	<-done
}
