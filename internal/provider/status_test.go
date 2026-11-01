// internal/provider/status_test.go
package provider

import "testing"

func TestStatusMapping(t *testing.T) {
	cases := []struct {
		raw  string
		want InstanceStatus
		term bool
	}{
		{"running", StatusRunning, false},
		{"exited", StatusExited, true},
		{"offline", StatusOffline, true},
		{"unknown", StatusUnknown, true},
		{"", StatusOther, false},
		{"loading-xyz", StatusOther, false},
	}
	for _, c := range cases {
		got := MapStatus(c.raw)
		if got != c.want {
			t.Errorf("MapStatus(%q) = %v, want %v", c.raw, got, c.want)
		}
		if got.Terminal() != c.term {
			t.Errorf("MapStatus(%q).Terminal() = %v, want %v", c.raw, got.Terminal(), c.term)
		}
	}
}
