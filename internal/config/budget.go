package config

import (
	"fmt"
	"math"
)

// Budget is internal/budget's own env-parsed configuration.
type Budget struct {
	MonthlyUSD        float64
	MaxMonthlyUSD     float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

// LoadBudget reads internal/budget's configuration. MaxHourlyPriceUSD
// reads the same env var as config.Controller's own field — the balance
// floor in Allow needs the same cap value, not a second copy of it.
func LoadBudget() (Budget, error) {
	var b Budget
	var err error

	if b.MonthlyUSD, err = envFloat("VKLLM_BUDGET_MONTH_USD", 30); err != nil {
		return Budget{}, err
	}
	if b.WarnFraction, err = envFloat("VKLLM_BUDGET_WARN_FRACTION", 0.8); err != nil {
		return Budget{}, err
	}
	if b.MaxHourlyPriceUSD, err = envFloat("VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD", 0.80); err != nil {
		return Budget{}, err
	}
	if b.MaxMonthlyUSD, err = envFloat("VKLLM_BUDGET_MAX_MONTHLY_USD", 100); err != nil {
		return Budget{}, err
	}
	if !(b.MaxMonthlyUSD > 0) || math.IsInf(b.MaxMonthlyUSD, 0) {
		return Budget{}, fmt.Errorf("config: VKLLM_BUDGET_MAX_MONTHLY_USD must be a positive finite number, got %v", b.MaxMonthlyUSD)
	}
	if !(b.MonthlyUSD <= b.MaxMonthlyUSD) {
		return Budget{}, fmt.Errorf("config: VKLLM_BUDGET_MONTH_USD %v must be a number not above VKLLM_BUDGET_MAX_MONTHLY_USD %v", b.MonthlyUSD, b.MaxMonthlyUSD)
	}
	return b, nil
}
