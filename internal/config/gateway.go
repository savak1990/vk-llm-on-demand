package config

import (
	"fmt"
	"strings"
	"time"
)

// Gateway configures internal/gateway's request limits and the model
// identity it reports; the monthly budget cap lives in Controller, not
// here.
type Gateway struct {
	ModelAlias         string
	MaxBodyBytes       int64
	MaxTokensCap       int
	RateLimitPerKeyRPM int
	GenerationTimeout  time.Duration
	ProvisioningMode   string
	OnDemand           OnDemand
}

// OnDemand holds the held-request limits; read only when ProvisioningMode is on-demand.
type OnDemand struct {
	MaxWait           time.Duration
	MaxPending        int
	HeartbeatInterval time.Duration
	TickerInterval    time.Duration
	DemandGrace       time.Duration
	TickerPhrases     []string
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
	if maxBody <= 0 {
		return Gateway{}, fmt.Errorf("config: VKLLM_GATEWAY_MAX_BODY_BYTES must be positive, got %d", maxBody)
	}
	g.MaxBodyBytes = int64(maxBody)

	if g.MaxTokensCap, err = envInt("VKLLM_GATEWAY_MAX_TOKENS_CAP", 8192); err != nil {
		return Gateway{}, err
	}
	if g.MaxTokensCap <= 0 {
		return Gateway{}, fmt.Errorf("config: VKLLM_GATEWAY_MAX_TOKENS_CAP must be positive, got %d", g.MaxTokensCap)
	}
	if g.RateLimitPerKeyRPM, err = envInt("VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM", 60); err != nil {
		return Gateway{}, err
	}
	if g.RateLimitPerKeyRPM <= 0 {
		return Gateway{}, fmt.Errorf("config: VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM must be positive, got %d", g.RateLimitPerKeyRPM)
	}
	if g.GenerationTimeout, err = envDuration("VKLLM_MODEL_GENERATION_TIMEOUT", 10*time.Minute); err != nil {
		return Gateway{}, err
	}

	g.ProvisioningMode = envString("VKLLM_LIFECYCLE_PROVISIONING_MODE", "explicit")
	if g.ProvisioningMode != "explicit" && g.ProvisioningMode != "on-demand" {
		return Gateway{}, fmt.Errorf("config: VKLLM_LIFECYCLE_PROVISIONING_MODE must be explicit or on-demand, got %q", g.ProvisioningMode)
	}
	if g.OnDemand.MaxWait, err = envDuration("VKLLM_ONDEMAND_MAX_WAIT", 600*time.Second); err != nil {
		return Gateway{}, err
	}
	if g.OnDemand.MaxPending, err = envInt("VKLLM_ONDEMAND_MAX_PENDING", 32); err != nil {
		return Gateway{}, err
	}
	if g.OnDemand.HeartbeatInterval, err = envDuration("VKLLM_ONDEMAND_HEARTBEAT_INTERVAL", 15*time.Second); err != nil {
		return Gateway{}, err
	}
	if g.OnDemand.TickerInterval, err = envDuration("VKLLM_ONDEMAND_TICKER_INTERVAL", 30*time.Second); err != nil {
		return Gateway{}, err
	}
	if g.OnDemand.DemandGrace, err = envDuration("VKLLM_ONDEMAND_DEMAND_GRACE", 60*time.Second); err != nil {
		return Gateway{}, err
	}
	for _, p := range strings.Split(envString("VKLLM_ONDEMAND_TICKER_PHRASES", ""), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			g.OnDemand.TickerPhrases = append(g.OnDemand.TickerPhrases, p)
		}
	}
	if g.OnDemand.MaxWait <= 0 || g.OnDemand.MaxPending <= 0 || g.OnDemand.HeartbeatInterval <= 0 || g.OnDemand.TickerInterval <= 0 || g.OnDemand.DemandGrace <= 0 {
		return Gateway{}, fmt.Errorf("config: every VKLLM_ONDEMAND_* duration and count must be positive")
	}
	if g.OnDemand.HeartbeatInterval >= g.OnDemand.DemandGrace {
		return Gateway{}, fmt.Errorf("config: VKLLM_ONDEMAND_HEARTBEAT_INTERVAL (%s) must be shorter than VKLLM_ONDEMAND_DEMAND_GRACE (%s)", g.OnDemand.HeartbeatInterval, g.OnDemand.DemandGrace)
	}
	return g, nil
}
