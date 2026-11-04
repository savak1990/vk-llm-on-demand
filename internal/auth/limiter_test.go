package auth

import (
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func TestLimiterAllowsUpToCapacityThenBlocks(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	l := NewLimiter(60, clk) // 60 rpm -> capacity 60, refill 1/s
	for i := 0; i < 60; i++ {
		if ok, _ := l.Allow("key1"); !ok {
			t.Fatalf("Allow #%d: want true (within capacity)", i)
		}
	}
	ok, retryAfter := l.Allow("key1")
	if ok {
		t.Fatal("Allow #61: want false (capacity exhausted)")
	}
	if retryAfter < time.Second {
		t.Fatalf("retryAfter = %v, want at least 1s (minimum 1)", retryAfter)
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	l := NewLimiter(60, clk)
	for i := 0; i < 60; i++ {
		l.Allow("key1")
	}
	clk.Advance(time.Second)
	if ok, _ := l.Allow("key1"); !ok {
		t.Fatal("Allow after 1s refill: want true")
	}
}

func TestLimiterBucketsAreIndependentPerKey(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	l := NewLimiter(1, clk)
	l.Allow("key1")
	if ok, _ := l.Allow("key2"); !ok {
		t.Fatal("a different keyID must have its own, unaffected bucket")
	}
}

func TestLimiterEnforces1SecondFloor(t *testing.T) {
	clk := fakeclock.New(time.Unix(0, 0))
	l := NewLimiter(120, clk) // 120 rpm -> perSec = 2 tokens/sec
	// Drain the bucket completely
	for i := 0; i < 120; i++ {
		if ok, _ := l.Allow("key1"); !ok {
			t.Fatalf("Allow #%d: want true (draining capacity)", i)
		}
	}
	// Now bucket is empty. Natural formula: (1-0)/2 * 1s = 0.5s
	// But we expect 1s floor to bite, so retryAfter must be >= 1s
	ok, retryAfter := l.Allow("key1")
	if ok {
		t.Fatal("Allow on empty bucket: want false")
	}
	if retryAfter < time.Second {
		t.Fatalf("retryAfter = %v, want >= 1s (floor enforced)", retryAfter)
	}
}
