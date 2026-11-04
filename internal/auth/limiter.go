package auth

import (
	"sync"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
)

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// Limiter is a per-keyID token bucket; its own instance, never shared
// with another surface's limiter (ADR 0012 §1).
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity float64
	perSec   float64
	clock    clock.Clock
}

func NewLimiter(rpm int, c clock.Clock) *Limiter {
	return &Limiter{
		buckets:  make(map[string]*bucket),
		capacity: float64(rpm),
		perSec:   float64(rpm) / 60,
		clock:    c,
	}
}

// Allow reports whether keyID has a token now; on false, retryAfter is
// the time to the next token, floored at 1 second (spec's own minimum).
func (l *Limiter) Allow(keyID string) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	b, exists := l.buckets[keyID]
	if !exists {
		b = &bucket{tokens: l.capacity, lastRefill: now}
		l.buckets[keyID] = b
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = min(l.capacity, b.tokens+elapsed*l.perSec)
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.perSec * float64(time.Second))
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}
