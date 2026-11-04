// internal/config/gateway_test.go
package config

import (
	"testing"
	"time"
)

func TestLoadGatewayDefaults(t *testing.T) {
	g, err := LoadGateway()
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	// Same literal default as config.LoadMgmt's own VKLLM_MODEL_ALIAS
	// default (CORE-055, internal/config/mgmt.go) — both loaders read
	// the same env var and must agree on what "unset" means, or the
	// gateway could report a different model id than the management
	// API's own status/cost documents.
	if g.ModelAlias != "qwen3.6-35b-a3b" {
		t.Fatalf("ModelAlias = %q", g.ModelAlias)
	}
	if g.MaxBodyBytes != 1<<20 {
		t.Fatalf("MaxBodyBytes = %d, want %d", g.MaxBodyBytes, 1<<20)
	}
	if g.MaxTokensCap != 8192 {
		t.Fatalf("MaxTokensCap = %d, want 8192", g.MaxTokensCap)
	}
	if g.RateLimitPerKeyRPM != 60 {
		t.Fatalf("RateLimitPerKeyRPM = %d, want 60", g.RateLimitPerKeyRPM)
	}
	if g.GenerationTimeout != 10*time.Minute {
		t.Fatalf("GenerationTimeout = %v, want 10m", g.GenerationTimeout)
	}
}

func TestLoadGatewayRespectsModelAliasOverride(t *testing.T) {
	t.Setenv("VKLLM_MODEL_ALIAS", "custom-alias")
	g, err := LoadGateway()
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.ModelAlias != "custom-alias" {
		t.Fatalf("ModelAlias = %q, want %q", g.ModelAlias, "custom-alias")
	}
}
