package gateway

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

const progressPrefix = "[vk-llm] "

var defaultTickerPhrases = []string{
	"Still warming up the GPU…",
	"Almost there…",
	"The GPU is still getting ready…",
	"Still working on it…",
	"Hang on, nearly ready…",
}

// progressTracker emits each milestone once, in order, from successive snapshots.
type progressTracker struct {
	sent    map[string]bool
	lastPct int
	lastGB  int64
}

func (p *progressTracker) once(key string, cond bool, text string, out []string) []string {
	if p.sent == nil {
		p.sent = map[string]bool{}
	}
	if !cond || p.sent[key] {
		return out
	}
	p.sent[key] = true
	return append(out, text)
}

func gbOf(b int64) float64 { return float64(b) / 1e9 }

func (p *progressTracker) lines(s controller.Snapshot) []string {
	var out []string
	out = p.once("alloc", s.Public == "ALLOCATING" && s.MachineID == 0, "Looking for a GPU offer…", out)
	where := ""
	if s.Geolocation != "" {
		where = " in " + s.Geolocation
	}
	out = p.once("offer", s.MachineID != 0 && s.GPUName != "",
		fmt.Sprintf("Renting an %s%s at $%.2f/h (machine %d); booting the worker…", s.GPUName, where, s.Hourly.USD(), s.MachineID), out)
	size := ""
	if s.ModelBytes > 0 {
		size = fmt.Sprintf(" (%.1f GB)", gbOf(s.ModelBytes))
	}
	out = p.once("loading", s.Public == "LOADING_MODEL", "Worker up; downloading the model"+size+"…", out)
	downloaded := slices.Contains(s.WorkerStages, "download_end")
	if s.Public == "LOADING_MODEL" && !downloaded && s.DownloadedBytes > 0 {
		if s.ModelBytes > 0 {
			pct := int(min(s.DownloadedBytes*100/s.ModelBytes, 99)) / 10 * 10
			if pct > p.lastPct {
				p.lastPct = pct
				out = append(out, fmt.Sprintf("Downloading the model: %d%% (%.1f of %.1f GB)", pct, gbOf(s.DownloadedBytes), gbOf(s.ModelBytes)))
			}
		} else if gb := s.DownloadedBytes / 2e9 * 2; gb > p.lastGB {
			p.lastGB = gb
			out = append(out, fmt.Sprintf("Downloading the model: %d GB so far", gb))
		}
	}
	out = p.once("verify", downloaded, "Download done; verifying the checksum…", out)
	out = p.once("spawn", slices.Contains(s.WorkerStages, "server_spawn"), "Checksum OK; loading the model into the GPU…", out)
	return p.once("ready", s.Ready, "Ready.", out)
}

type chunkDelta struct {
	Role             string `json:"role"`
	ReasoningContent string `json:"reasoning_content"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type progressChunkBody struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
}

func progressChunk(alias, text string, now time.Time) string {
	b, _ := json.Marshal(progressChunkBody{
		ID: "vkllm-progress", Object: "chat.completion.chunk", Created: now.Unix(), Model: alias,
		Choices: []chunkChoice{{Delta: chunkDelta{Role: "assistant", ReasoningContent: progressPrefix + text + "\n\n"}}},
	})
	return "data: " + string(b) + "\n\n"
}

// errorTail ends a held stream; some clients reject a stream with no finish_reason.
func errorTail(code, message string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"error":   errorDetail{Message: message, Type: "server_error", Code: code},
	})
	return "data: " + string(b) + "\n\ndata: [DONE]\n\n"
}

func nextPhrase(phrases []string, last int) int {
	if len(phrases) < 2 {
		return 0
	}
	i := rand.IntN(len(phrases) - 1) //nolint:gosec // ticker phrase selection, not cryptographic
	if i >= last && last >= 0 {
		i++
	}
	return i
}
