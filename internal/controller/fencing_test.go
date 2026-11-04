package controller

import "testing"

func TestParseLabel(t *testing.T) {
	cases := []struct {
		label   string
		wantID  string
		wantGen int64
		wantOK  bool
	}{
		{"vkllm:iid:7", "iid", 7, true},
		{"vkllm:iid:0", "iid", 0, true},
		{"notvkllm:iid:7", "", 0, false},
		{"vkllm:iid", "", 0, false},
		{"vkllm:iid:abc", "", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		id, gen, ok := parseLabel(c.label)
		if ok != c.wantOK || (ok && (id != c.wantID || gen != c.wantGen)) {
			t.Errorf("parseLabel(%q) = (%q, %d, %v), want (%q, %d, %v)", c.label, id, gen, ok, c.wantID, c.wantGen, c.wantOK)
		}
	}
}

func TestMayDestroy(t *testing.T) {
	cases := []struct {
		name          string
		label         string
		installID     string
		docGeneration int64
		want          bool
	}{
		{"foreign installation id", "vkllm:other:3", "iid", 5, false},
		{"generation above current", "vkllm:iid:6", "iid", 5, false},
		{"generation equal to current", "vkllm:iid:5", "iid", 5, true},
		{"generation below current", "vkllm:iid:2", "iid", 5, true},
		{"unparsable label", "garbage", "iid", 5, false},
		{"empty label", "", "iid", 5, false},
	}
	for _, c := range cases {
		if got := mayDestroy(c.label, c.installID, c.docGeneration); got != c.want {
			t.Errorf("%s: mayDestroy(%q, %q, %d) = %v, want %v", c.name, c.label, c.installID, c.docGeneration, got, c.want)
		}
	}
}
