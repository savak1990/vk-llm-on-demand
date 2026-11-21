// internal/gateway/workerseed.go

package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
)

const workerSeedReloadInterval = 10 * time.Second

// WorkerSeed holds the worker-signing seed read from one file,
// independent of internal/auth (that package never loads this file).
// Exported because cmd/gpu-on-demand/main.go constructs one directly.
type WorkerSeed struct {
	path  string
	clk   clock.Clock
	bytes atomic.Pointer[[]byte]
}

func LoadWorkerSeed(path string, clk clock.Clock) (*WorkerSeed, error) {
	w := &WorkerSeed{path: path, clk: clk}
	if err := w.reload(); err != nil {
		return nil, err
	}
	return w, nil
}

// reload stores 32 raw bytes exactly as read; a file that is not 32 bytes
// is tried as the base64 form the install docs show, with or without a
// trailing newline. A reload that still yields something else is rejected.
func (w *WorkerSeed) reload() error {
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return fmt.Errorf("gateway: worker seed: %w", err)
	}
	if len(raw) != 32 {
		trimmed := bytes.TrimRight(raw, "\r\n")
		if dec, err := base64.StdEncoding.DecodeString(string(trimmed)); err == nil && len(dec) == 32 {
			raw = dec
		} else if len(trimmed) == 32 {
			raw = trimmed
		}
	}
	if len(raw) != 32 && len(w.Bytes()) == 32 {
		return fmt.Errorf("gateway: worker seed: reload read %d bytes, want 32; keeping the last good value", len(raw))
	}
	w.bytes.Store(&raw)
	return nil
}

func (w *WorkerSeed) Bytes() []byte {
	b := w.bytes.Load()
	if b == nil {
		return nil
	}
	return *b
}

func (w *WorkerSeed) Loaded() error {
	if len(w.Bytes()) != 32 {
		return fmt.Errorf("gateway: worker seed must be 32 bytes, got %d", len(w.Bytes()))
	}
	return nil
}

// Run reloads the seed file every workerSeedReloadInterval until ctx is
// done; a failed reload keeps serving the last good value.
func (w *WorkerSeed) Run(ctx context.Context) {
	if w.clk == nil {
		return
	}
	t := w.clk.NewTicker(workerSeedReloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = w.reload()
		}
	}
}
