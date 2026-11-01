package fake

import (
	"testing"
	"time"
)

func TestAdvanceFiresTicker(t *testing.T) {
	c := New(time.Unix(0, 0))
	ticker := c.NewTicker(time.Minute)
	defer ticker.Stop()

	c.BlockUntil(1)
	c.Advance(time.Minute)

	select {
	case <-ticker.C:
	case <-time.After(time.Second):
		t.Fatal("ticker did not fire after Advance")
	}
}

func TestAfterFiresOnAdvance(t *testing.T) {
	c := New(time.Unix(0, 0))
	ch := c.After(30 * time.Minute)

	c.BlockUntil(1)
	c.Advance(30 * time.Minute)

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("After channel did not fire after Advance")
	}
}
