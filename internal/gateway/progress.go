package gateway

import (
	"encoding/json"
	"math/rand/v2"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/progress"
)

const progressPrefix = "vk-llm › "

// legacyProgressPrefix is the prefix chats started before this change still carry.
const legacyProgressPrefix = "[vk-llm] "

const visibleStatusLine = "⏳ Starting the GPU (3–6 min). Progress is in the thinking block."

var defaultTickerPhrases = []string{
	"Still warming up the GPU…",
	"Almost there…",
	"The GPU is still getting ready…",
	"Still working on it…",
	"Hang on, nearly ready…",
}

func progressState(s controller.Snapshot) progress.State {
	return progress.State{
		Public: s.Public, MachineID: s.MachineID, GPUName: s.GPUName, Geolocation: s.Geolocation,
		HourlyUSD: s.Hourly.USD(), DownloadedBytes: s.DownloadedBytes, ModelBytes: s.ModelBytes,
		WorkerStages: s.WorkerStages, Ready: s.Ready, CacheHit: s.CacheHit,
	}
}

type chunkDelta struct {
	Role             string `json:"role"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
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

// visibleStatusChunk is the one visible content line sent once at the start of a held stream.
func visibleStatusChunk(alias string, now time.Time) string {
	b, _ := json.Marshal(progressChunkBody{
		ID: "vkllm-progress", Object: "chat.completion.chunk", Created: now.Unix(), Model: alias,
		Choices: []chunkChoice{{Delta: chunkDelta{Role: "assistant", Content: visibleStatusLine + "\n\n"}}},
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
