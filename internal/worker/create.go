package worker

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

// CreateParams are the per-instance values BuildCreate turns into a
// provider.CreateRequest for an ssh-launch-mode worker.
type CreateParams struct {
	Image                        string
	DiskGB                       int
	Label                        string
	ModelURL, ModelSHA256, Alias string
	Ctx, Parallel                int
	DeadMan                      time.Duration
	WorkerKey                    string
}

const reasoningFormat = "deepseek"

func isHex64(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// BuildCreate validates p and builds the provider create request:
// runtype ssh, no port-mapping env key.
func BuildCreate(p CreateParams) (provider.CreateRequest, error) {
	switch {
	case !isHex64(p.ModelSHA256):
		return provider.CreateRequest{}, fmt.Errorf("worker: ModelSHA256 must be 64 lowercase hex characters")
	case !strings.HasPrefix(p.ModelURL, "https://"):
		return provider.CreateRequest{}, fmt.Errorf("worker: ModelURL must start with https://")
	case p.Alias == "":
		return provider.CreateRequest{}, fmt.Errorf("worker: Alias must not be empty")
	case p.Ctx < 1:
		return provider.CreateRequest{}, fmt.Errorf("worker: Ctx must be >= 1")
	case p.Parallel < 1:
		return provider.CreateRequest{}, fmt.Errorf("worker: Parallel must be >= 1")
	case p.DeadMan < 60*time.Second:
		return provider.CreateRequest{}, fmt.Errorf("worker: DeadMan must be >= 60s")
	}

	return provider.CreateRequest{
		Image:   p.Image,
		DiskGB:  p.DiskGB,
		RunType: "ssh",
		Label:   p.Label,
		Onstart: Render(),
		Env: map[string]string{
			"MODEL_URL":        p.ModelURL,
			"MODEL_SHA256":     p.ModelSHA256,
			"MODEL_ALIAS":      p.Alias,
			"CTX":              strconv.Itoa(p.Ctx),
			"PARALLEL":         strconv.Itoa(p.Parallel),
			"WORKER_API_KEY":   p.WorkerKey,
			"DEADMAN_SECONDS":  strconv.Itoa(int(p.DeadMan.Seconds())),
			"REASONING_FORMAT": reasoningFormat,
		},
	}, nil
}
