// internal/gateway/workerseed_test.go
package gateway

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/obs/obstest"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
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

// TestWorkerSeedReloadKeepsLastGoodValue closes the gap where a failed
// reload (a truncated or empty file mid-rotation) used to overwrite a
// good 32-byte seed with garbage instead of keeping serving the last one.
func TestWorkerSeedReloadKeepsLastGoodValue(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/seed"
	good := make([]byte, 32)
	good[0] = 0xAA
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	w, err := LoadWorkerSeed(path, nil)
	if err != nil {
		t.Fatalf("LoadWorkerSeed: %v", err)
	}

	if err := os.WriteFile(path, []byte("too short"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := w.reload(); err == nil {
		t.Fatal("reload: want error for a non-32-byte file, got nil")
	}

	if got := w.Bytes(); len(got) != 32 || got[0] != 0xAA {
		t.Fatalf("Bytes() = %v, want the last good 32-byte value kept", got)
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

// TestWorkerSeedAndDerivedKeyNeverLogged proves the per-request key
// worker.WorkerKey derives from the seed (the Authorization header
// chat.go sends upstream) never reaches the gateway's logs.
func TestWorkerSeedAndDerivedKeyNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	d := testDeps(t)
	d.Log = slog.New(slog.NewTextHandler(&buf, nil))
	var gotAuth string
	rt := fakeRoundTripper(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
	})
	d.Controller = &fakeController{admitRT: rt, status: controller.Snapshot{Generation: 1}}
	g, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	g.Handler().ServeHTTP(httptest.NewRecorder(), chatReq(`{"model":"qwen3.6-35b-a3b","messages":[]}`))

	if gotAuth == "" {
		t.Fatal("upstream never received an Authorization header; the leak check below would be vacuous")
	}
	derivedKey := worker.WorkerKey(d.Seed.Bytes(), d.InstallationID, 1)
	obstest.AssertNoSecret(t, buf.Bytes(), derivedKey)
}
