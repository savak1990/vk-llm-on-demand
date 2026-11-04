package config

import (
	"testing"
)

func clearMgmtEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_PROVIDER_NAME", "VKLLM_MODEL_ALIAS", "VKLLM_MODEL_CONTEXT_SIZE",
		"VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE", "VKLLM_MGMT_BUDGET_MONTH_USD",
		"VKLLM_MGMT_RATE_LIMIT_RPM",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadMgmtDefaults(t *testing.T) {
	clearMgmtEnv(t)
	m, err := LoadMgmt()
	if err != nil {
		t.Fatalf("LoadMgmt: %v", err)
	}
	if m.ProviderName != "vastai" {
		t.Errorf("ProviderName = %q, want vastai", m.ProviderName)
	}
	if m.ModelAlias != "qwen3.6-35b-a3b" {
		t.Errorf("ModelAlias = %q", m.ModelAlias)
	}
	if m.ModelContextSize != 32768 {
		t.Errorf("ModelContextSize = %d, want 32768", m.ModelContextSize)
	}
	if m.ShutdownAllowForce {
		t.Error("ShutdownAllowForce = true, want false default")
	}
	if m.BudgetMonthUSD != 30 {
		t.Errorf("BudgetMonthUSD = %v, want 30", m.BudgetMonthUSD)
	}
	if m.RateLimitPerKeyRPM != 60 {
		t.Errorf("RateLimitPerKeyRPM = %v, want 60", m.RateLimitPerKeyRPM)
	}
}

func TestLoadMgmtOverrides(t *testing.T) {
	clearMgmtEnv(t)
	t.Setenv("VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE", "true")
	t.Setenv("VKLLM_MGMT_BUDGET_MONTH_USD", "45.5")
	m, err := LoadMgmt()
	if err != nil {
		t.Fatalf("LoadMgmt: %v", err)
	}
	if !m.ShutdownAllowForce {
		t.Error("ShutdownAllowForce = false, want true")
	}
	if m.BudgetMonthUSD != 45.5 {
		t.Errorf("BudgetMonthUSD = %v, want 45.5", m.BudgetMonthUSD)
	}
}

func TestLoadMgmtMalformedFloat(t *testing.T) {
	clearMgmtEnv(t)
	t.Setenv("VKLLM_MGMT_BUDGET_MONTH_USD", "not-a-number")
	if _, err := LoadMgmt(); err == nil {
		t.Fatal("LoadMgmt: want error for malformed float, got nil")
	}
}

func TestLoadMgmtRejectsNonPositiveRateLimit(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		clearMgmtEnv(t)
		t.Setenv("VKLLM_MGMT_RATE_LIMIT_RPM", v)
		if _, err := LoadMgmt(); err == nil {
			t.Fatalf("LoadMgmt with RPM=%s: want error, got nil", v)
		}
	}
}
