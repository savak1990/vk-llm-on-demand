package mgmt

import (
	"context"
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

// call is one in-flight (or just-finished) controller call; a waiter
// captures a reference to the specific call it coalesced onto, so it
// never reads a later call's result.
type call struct {
	done chan struct{}
	snap controller.Snapshot
	err  error
}

// coalescer collapses concurrent calls of one op into a single
// in-flight controller call shared by every waiter. The shared call
// always runs on its own context, so one client giving up never cancels it for the others.
type coalescer struct {
	mu      sync.Mutex
	waiters int
	current *call
}

func newCoalescer() *coalescer { return &coalescer{} }

func (c *coalescer) waiterCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiters
}

func (c *coalescer) do(ctx context.Context, fn func(context.Context) (controller.Snapshot, error)) (controller.Snapshot, error) {
	c.mu.Lock()
	if cur := c.current; cur != nil {
		c.waiters++
		c.mu.Unlock()
		select {
		case <-cur.done:
			c.mu.Lock()
			c.waiters--
			c.mu.Unlock()
			return cur.snap, cur.err
		case <-ctx.Done():
			c.mu.Lock()
			c.waiters--
			c.mu.Unlock()
			return controller.Snapshot{}, ctx.Err()
		}
	}
	cur := &call{done: make(chan struct{})}
	c.current = cur
	c.mu.Unlock()

	callCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	cur.snap, cur.err = fn(callCtx)
	cancel()

	c.mu.Lock()
	c.current = nil
	c.mu.Unlock()
	close(cur.done)
	return cur.snap, cur.err
}
