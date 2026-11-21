package config

import "testing"

func clearBudgetEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_BUDGET_MONTH_USD", "VKLLM_BUDGET_WARN_FRACTION",
		"VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD", "VKLLM_BUDGET_MAX_MONTHLY_USD",
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

func TestLoadBudgetMaxMonthlyDefault(t *testing.T) {
	clearBudgetEnv(t)
	b, err := LoadBudget()
	if err != nil {
		t.Fatalf("LoadBudget: %v", err)
	}
	if b.MaxMonthlyUSD != 100 {
		t.Errorf("MaxMonthlyUSD = %v, want 100", b.MaxMonthlyUSD)
	}
}

func TestLoadBudgetMaxMonthlyOverride(t *testing.T) {
	clearBudgetEnv(t)
	t.Setenv("VKLLM_BUDGET_MAX_MONTHLY_USD", "250")
	b, err := LoadBudget()
	if err != nil {
		t.Fatalf("LoadBudget: %v", err)
	}
	if b.MaxMonthlyUSD != 250 {
		t.Errorf("MaxMonthlyUSD = %v, want 250", b.MaxMonthlyUSD)
	}
}

func TestLoadBudgetRejectsBadCeiling(t *testing.T) {
	for _, tc := range []struct{ monthly, ceiling string }{
		{"30", "0"},
		{"30", "-1"},
		{"150", "100"},
	} {
		clearBudgetEnv(t)
		t.Setenv("VKLLM_BUDGET_MONTH_USD", tc.monthly)
		t.Setenv("VKLLM_BUDGET_MAX_MONTHLY_USD", tc.ceiling)
		if _, err := LoadBudget(); err == nil {
			t.Errorf("LoadBudget(monthly=%s, ceiling=%s) = nil error, want an error", tc.monthly, tc.ceiling)
		}
	}
}
