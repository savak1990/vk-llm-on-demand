package clock

import "testing"

func TestRealNowAdvances(t *testing.T) {
	c := Real{}
	t1 := c.Now()
	t2 := c.Now()
	if t2.Before(t1) {
		t.Fatalf("time went backwards: %v then %v", t1, t2)
	}
}
