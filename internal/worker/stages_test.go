package worker

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseStages(t *testing.T) {
	var lines []string
	for i := range 20 {
		lines = append(lines, "some noise line "+string(rune('a'+i)))
	}
	stages := []struct {
		name string
		ms   int64
	}{
		{"start", 1000},
		{"download_begin", 2000},
		{"download_end", 3000},
		{"verified", 4000},
		{"server_spawn", 5000},
		{"server_healthy", 6000},
		{"idle_exit", 7000},
		{"extra", 8000},
	}
	for _, s := range stages {
		lines = append(lines, "VKLLM_STAGE "+s.name+" "+strconv.FormatInt(s.ms, 10))
	}

	got, err := ParseStages(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatalf("ParseStages() error = %v, want nil", err)
	}
	if len(got) != len(stages) {
		t.Fatalf("ParseStages() returned %d stages, want %d", len(got), len(stages))
	}
	for _, s := range stages {
		want := time.UnixMilli(s.ms)
		ts, ok := got[s.name]
		if !ok {
			t.Errorf("ParseStages() missing stage %q", s.name)
			continue
		}
		if !ts.Equal(want) {
			t.Errorf("ParseStages()[%q] = %v, want %v", s.name, ts, want)
		}
	}
}

func TestParseStagesMalformedLine(t *testing.T) {
	_, err := ParseStages(strings.NewReader("VKLLM_STAGE x notanumber"))
	if err == nil {
		t.Error("ParseStages() error = nil, want an error for a malformed VKLLM_STAGE line")
	}
}
