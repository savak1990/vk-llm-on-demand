package mgmt

import (
	"context"
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

// coalescer collapses concurrent calls of one op into a single
// in-flight controller call shared by every waiter. The shared call
// always runs on its own context, so one client giving up never cancels it for the others.
type coalescer struct {
	mu      sync.Mutex
	waiters int
	done    chan struct{}
	snap    controller.Snapshot
	err     error
}

func newCoalescer() *coalescer { return &coalescer{} }

func (c *coalescer) waiterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiters
}

func (c *coalescer) do(ctx context.Context, call func(context.Context) (controller.Snapshot, error)) (controller.Snapshot, error) {
	c.mu.Lock()
	if c.done != nil {
		done := c.done
		c.waiters++
		c.mu.Unlock()
		select {
		case <-done:
			c.mu.Lock()
			snap, err := c.snap, c.err
			c.waiters--
			c.mu.Unlock()
			return snap, err
		case <-ctx.Done():
			c.mu.Lock()
			c.waiters--
			c.mu.Unlock()
			return controller.Snapshot{}, ctx.Err()
		}
	}
	done := make(chan struct{})
	c.done = done
	c.mu.Unlock()

	callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	snap, err := call(callCtx)
	cancel()

	c.mu.Lock()
	c.snap, c.err = snap, err
	c.done = nil
	close(done)
	c.mu.Unlock()
	return snap, err
}
