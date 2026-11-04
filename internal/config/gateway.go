package config

import (
	"time"
)

// Gateway configures internal/gateway's request limits and the model
// identity it reports; provisioning mode and the monthly budget cap
// live in Controller, not here.
type Gateway struct {
	ModelAlias         string
	MaxBodyBytes       int64
	MaxTokensCap       int
	RateLimitPerKeyRPM int
	GenerationTimeout  time.Duration
}

func LoadGateway() (Gateway, error) {
	var g Gateway
	var err error

	// Two loaders reading the same env var must agree on the unset case,
	// or the gateway could report a different model id than the management API.
	g.ModelAlias = envString("VKLLM_MODEL_ALIAS", "qwen3.6-35b-a3b")

	maxBody, err := envInt("VKLLM_GATEWAY_MAX_BODY_BYTES", 1<<20)
	if err != nil {
		return Gateway{}, err
	}
	g.MaxBodyBytes = int64(maxBody)

	if g.MaxTokensCap, err = envInt("VKLLM_GATEWAY_MAX_TOKENS_CAP", 8192); err != nil {
		return Gateway{}, err
	}
	if g.RateLimitPerKeyRPM, err = envInt("VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM", 60); err != nil {
		return Gateway{}, err
	}
	if g.GenerationTimeout, err = envDuration("VKLLM_MODEL_GENERATION_TIMEOUT", 10*time.Minute); err != nil {
		return Gateway{}, err
	}
	return g, nil
}
