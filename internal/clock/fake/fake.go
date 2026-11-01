// Package fake provides a manually-controlled Clock implementation for deterministic testing.
package fake

import (
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
)

type waiter struct {
	deadline time.Time
	fire     func(time.Time)
	period   time.Duration // zero for a one-shot After
}

// Clock is a manually-advanced implementation of clock.Clock for deterministic tests.
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
	pending chan struct{} // closed and replaced each time len(waiters) changes
}

// New creates a Clock starting at the given time.
func New(start time.Time) *Clock {
	c := &Clock{now: start}
	c.pending = make(chan struct{})
	return c
}

var _ clock.Clock = (*Clock)(nil)

// Now returns the current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After returns a channel that sends the current time after d has elapsed on the clock.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.waiters = append(c.waiters, &waiter{
		deadline: c.now.Add(d),
		fire:     func(t time.Time) { ch <- t },
	})
	c.notifyLocked()
	c.mu.Unlock()
	return ch
}

// NewTicker creates a Ticker that fires on the clock.
func (c *Clock) NewTicker(d time.Duration) clock.Ticker {
	ch := make(chan time.Time, 1)
	w := &waiter{period: d}
	c.mu.Lock()
	w.deadline = c.now.Add(d)
	w.fire = func(t time.Time) {
		select {
		case ch <- t:
		default:
		}
	}
	c.waiters = append(c.waiters, w)
	c.notifyLocked()
	c.mu.Unlock()
	stop := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, cur := range c.waiters {
			if cur == w {
				c.waiters = append(c.waiters[:i], c.waiters[i+1:]...)
				break
			}
		}
	}
	return clock.NewTickerFrom(ch, stop)
}

// Advance moves the clock forward by d, firing every waiter whose deadline falls at or before the new time.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	remaining := c.waiters[:0:0]
	for _, w := range c.waiters {
		if !w.deadline.After(now) {
			w.fire(now)
			if w.period > 0 {
				w.deadline = now.Add(w.period)
				remaining = append(remaining, w)
			}
			continue
		}
		remaining = append(remaining, w)
	}
	c.waiters = remaining
	c.mu.Unlock()
}

// BlockUntil blocks until at least n timers/tickers are pending, for race-safe test setup.
func (c *Clock) BlockUntil(n int) {
	for {
		c.mu.Lock()
		count := len(c.waiters)
		ch := c.pending
		c.mu.Unlock()
		if count >= n {
			return
		}
		<-ch
	}
}

func (c *Clock) notifyLocked() {
	close(c.pending)
	c.pending = make(chan struct{})
}
