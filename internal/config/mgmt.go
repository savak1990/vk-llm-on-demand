package config

import "fmt"

// Mgmt is internal/mgmt's own env-parsed configuration.
type Mgmt struct {
	ProviderName       string
	ModelAlias         string
	ModelContextSize   int
	ShutdownAllowForce bool
	BudgetMonthUSD     float64
	RateLimitPerKeyRPM int
}

// LoadMgmt reads internal/mgmt's configuration; ShutdownAllowForce
// needs its own copy since mgmt rejects a disallowed force request
// itself, before ever calling Controller.Stop.
func LoadMgmt() (Mgmt, error) {
	var m Mgmt
	var err error

	m.ProviderName = envString("VKLLM_PROVIDER_NAME", "vastai")
	m.ModelAlias = envString("VKLLM_MODEL_ALIAS", "qwen3.6-35b-a3b")
	if m.ModelContextSize, err = envInt("VKLLM_MODEL_CONTEXT_SIZE", 32768); err != nil {
		return Mgmt{}, err
	}
	if m.ShutdownAllowForce, err = envBool("VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE", false); err != nil {
		return Mgmt{}, err
	}
	if m.BudgetMonthUSD, err = envFloat("VKLLM_MGMT_BUDGET_MONTH_USD", 30); err != nil {
		return Mgmt{}, err
	}
	if m.RateLimitPerKeyRPM, err = envInt("VKLLM_MGMT_RATE_LIMIT_RPM", 60); err != nil {
		return Mgmt{}, err
	}
	if m.RateLimitPerKeyRPM <= 0 {
		return Mgmt{}, fmt.Errorf("config: VKLLM_MGMT_RATE_LIMIT_RPM must be positive, got %d", m.RateLimitPerKeyRPM)
	}
	return m, nil
}
