package controller

import "testing"

func TestPublicStateMap(t *testing.T) {
	cases := []struct {
		s       State
		failing bool
		want    string
	}{
		{Idle, false, "STOPPED"},
		{Searching, false, "ALLOCATING"},
		{Creating, false, "ALLOCATING"},
		{Booting, false, "STARTING"},
		{Loading, false, "LOADING_MODEL"},
		{Ready, false, "READY"},
		{Serving, false, "READY"},
		{Draining, false, "DRAINING"},
		{Destroying, false, "DELETING"},
		{Destroying, true, "FAILED"},
	}
	for _, c := range cases {
		if got := PublicState(c.s, c.failing); got != c.want {
			t.Errorf("PublicState(%v, %v) = %q, want %q", c.s, c.failing, got, c.want)
		}
	}
}

func TestStateString(t *testing.T) {
	if Ready.String() != "Ready" || Destroying.String() != "Destroying" {
		t.Errorf("String() = %q / %q", Ready.String(), Destroying.String())
	}
}
