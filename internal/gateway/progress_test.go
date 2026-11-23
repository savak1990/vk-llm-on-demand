package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func TestProgressTrackerSequence(t *testing.T) {
	var p progressTracker
	gb := int64(1_000_000_000)
	steps := []controller.Snapshot{
		{Public: "ALLOCATING"},
		{Public: "ALLOCATING"},
		{Public: "STARTING", MachineID: 117871, GPUName: "RTX 6000Ada", Geolocation: "Sweden", Hourly: provider.Money(550000)},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb, DownloadedBytes: 9 * gb},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb, DownloadedBytes: 28 * gb, WorkerStages: []string{"download_end"}},
		{Public: "LOADING_MODEL", MachineID: 117871, ModelBytes: 28 * gb, DownloadedBytes: 28 * gb, WorkerStages: []string{"download_end", "server_spawn"}},
		{Public: "READY", Ready: true},
	}
	var got []string
	for _, s := range steps {
		got = append(got, p.lines(s)...)
	}
	want := []string{
		"Looking for a GPU offer…",
		"Renting an RTX 6000Ada in Sweden at $0.55/h (machine 117871); booting the worker…",
		"Worker up; downloading the model (28.0 GB)…",
		"Downloading the model: 30% (9.0 of 28.0 GB)",
		"Download done; verifying the checksum…",
		"Checksum OK; loading the model into the GPU…",
		"Ready.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines =\n%q\nwant\n%q", got, want)
	}
}

func TestProgressTrackerWithoutModelSize(t *testing.T) {
	var p progressTracker
	p.lines(controller.Snapshot{Public: "LOADING_MODEL"})
	got := p.lines(controller.Snapshot{Public: "LOADING_MODEL", DownloadedBytes: 4_100_000_000})
	if len(got) != 1 || got[0] != "Downloading the model: 4 GB so far" {
		t.Fatalf("lines = %q", got)
	}
}

func TestProgressChunkShape(t *testing.T) {
	line := progressChunk("qwen", "Ready.", time.Unix(100, 0))
	raw, ok := strings.CutPrefix(line, "data: ")
	if !ok || !strings.HasSuffix(line, "\n\n") {
		t.Fatalf("not an SSE data line: %q", line)
	}
	var c struct {
		Choices []struct {
			Delta map[string]string `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	d := c.Choices[0].Delta
	if d["reasoning_content"] != "[vk-llm] Ready.\n\n" || d["content"] != "" {
		t.Fatalf("delta = %v", d)
	}
}

func TestErrorTailHasFinishReasonAndDone(t *testing.T) {
	tail := errorTail("gpu_unavailable", "x")
	if !strings.Contains(tail, `"finish_reason":"stop"`) || !strings.Contains(tail, `"code":"gpu_unavailable"`) || !strings.HasSuffix(tail, "data: [DONE]\n\n") {
		t.Fatalf("tail = %q", tail)
	}
}

func TestNextPhraseNeverRepeats(t *testing.T) {
	last := -1
	for i := 0; i < 200; i++ {
		n := nextPhrase(defaultTickerPhrases, last)
		if n == last {
			t.Fatalf("phrase %d repeated", n)
		}
		last = n
	}
	if nextPhrase([]string{"only"}, 0) != 0 {
		t.Fatal("a one-phrase set must return 0")
	}
}
