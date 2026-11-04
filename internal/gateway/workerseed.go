// internal/gateway/workerseed.go

package gateway

import (
	"context"
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

// reload stores the file's bytes exactly as read, no trimming or
// encoding conversion. Once a good 32-byte value is loaded, a reload
// that reads something else is rejected rather than swapped in.
func (w *WorkerSeed) reload() error {
	raw, err := os.ReadFile(w.path)
	if err != nil {
		return fmt.Errorf("gateway: worker seed: %w", err)
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
