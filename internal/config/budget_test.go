package config

import "testing"

func clearBudgetEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_BUDGET_MONTH_USD", "VKLLM_BUDGET_WARN_FRACTION",
		"VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadBudgetDefaults(t *testing.T) {
	clearBudgetEnv(t)
	b, err := LoadBudget()
	if err != nil {
		t.Fatalf("LoadBudget: %v", err)
	}
	if b.MonthlyUSD != 30 {
		t.Errorf("MonthlyUSD = %v, want 30", b.MonthlyUSD)
	}
	if b.WarnFraction != 0.8 {
		t.Errorf("WarnFraction = %v, want 0.8", b.WarnFraction)
	}
	if b.MaxHourlyPriceUSD != 0.80 {
		t.Errorf("MaxHourlyPriceUSD = %v, want 0.80", b.MaxHourlyPriceUSD)
	}
}

func TestLoadBudgetOverrides(t *testing.T) {
	clearBudgetEnv(t)
	t.Setenv("VKLLM_BUDGET_MONTH_USD", "45.5")
	b, err := LoadBudget()
	if err != nil {
		t.Fatalf("LoadBudget: %v", err)
	}
	if b.MonthlyUSD != 45.5 {
		t.Errorf("MonthlyUSD = %v, want 45.5", b.MonthlyUSD)
	}
}

func TestLoadBudgetMalformedFloat(t *testing.T) {
	clearBudgetEnv(t)
	t.Setenv("VKLLM_BUDGET_MONTH_USD", "not-a-number")
	if _, err := LoadBudget(); err == nil {
		t.Fatal("LoadBudget: want error for malformed float, got nil")
	}
}
