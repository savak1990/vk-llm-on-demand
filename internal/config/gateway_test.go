// internal/config/gateway_test.go
package config

import (
	"testing"
	"time"
)

func clearGatewayEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_MODEL_ALIAS",
		"VKLLM_GATEWAY_MAX_BODY_BYTES", "VKLLM_GATEWAY_MAX_TOKENS_CAP",
		"VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM",
		"VKLLM_MODEL_GENERATION_TIMEOUT",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadGatewayDefaults(t *testing.T) {
	clearGatewayEnv(t)
	g, err := LoadGateway()
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	// Two loaders reading the same env var must agree on defaults,
	// or the gateway could report a different model id than the management API.
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

func TestLoadGatewayRejectsNonPositiveMaxBodyBytes(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		clearGatewayEnv(t)
		t.Setenv("VKLLM_GATEWAY_MAX_BODY_BYTES", v)
		if _, err := LoadGateway(); err == nil {
			t.Fatalf("LoadGateway with MAX_BODY_BYTES=%s: want error, got nil", v)
		}
	}
}

func TestLoadGatewayRejectsNonPositiveMaxTokensCap(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		clearGatewayEnv(t)
		t.Setenv("VKLLM_GATEWAY_MAX_TOKENS_CAP", v)
		if _, err := LoadGateway(); err == nil {
			t.Fatalf("LoadGateway with MAX_TOKENS_CAP=%s: want error, got nil", v)
		}
	}
}

func TestLoadGatewayRejectsNonPositiveRateLimit(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		clearGatewayEnv(t)
		t.Setenv("VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM", v)
		if _, err := LoadGateway(); err == nil {
			t.Fatalf("LoadGateway with RATE_LIMIT_PER_KEY_RPM=%s: want error, got nil", v)
		}
	}
}

func TestLoadGatewayRespectsModelAliasOverride(t *testing.T) {
	clearGatewayEnv(t)
	t.Setenv("VKLLM_MODEL_ALIAS", "custom-alias")
	g, err := LoadGateway()
	if err != nil {
		t.Fatalf("LoadGateway: %v", err)
	}
	if g.ModelAlias != "custom-alias" {
		t.Fatalf("ModelAlias = %q, want %q", g.ModelAlias, "custom-alias")
	}
}
