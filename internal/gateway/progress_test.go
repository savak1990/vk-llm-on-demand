package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

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
	if d["reasoning_content"] != "vk-llm › Ready.\n\n" || d["content"] != "" {
		t.Fatalf("delta = %v", d)
	}
}

func TestVisibleStatusChunkShape(t *testing.T) {
	line := visibleStatusChunk("qwen", time.Unix(100, 0))
	raw, ok := strings.CutPrefix(line, "data: ")
	if !ok || !strings.HasSuffix(line, "\n\n") {
		t.Fatalf("not an SSE data line: %q", line)
	}
	var c struct {
		Choices []struct {
			Delta        map[string]string `json:"delta"`
			FinishReason *string           `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	d := c.Choices[0].Delta
	want := "⏳ Starting the GPU (3–6 min). Progress is in the thinking block.\n\n"
	if d["content"] != want || d["reasoning_content"] != "" || d["role"] != "assistant" {
		t.Fatalf("delta = %v", d)
	}
	if c.Choices[0].FinishReason != nil {
		t.Fatalf("finish_reason = %v, want nil", c.Choices[0].FinishReason)
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
