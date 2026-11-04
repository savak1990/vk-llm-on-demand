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
