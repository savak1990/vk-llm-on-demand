package config

// Mgmt is internal/mgmt's own env-parsed configuration.
type Mgmt struct {
	ProviderName        string
	ModelAlias          string
	ModelContextSize    int
	ShutdownAllowForce  bool
	BudgetMonthUSD      float64
	RateLimitPerKeyRPM  int
}

// LoadMgmt reads internal/mgmt's configuration. ShutdownAllowForce
// reads the same env var as config.Controller's own field — mgmt must
// reject a disallowed force request itself, before ever calling
// Controller.Stop, so it needs its own copy, not a shared struct.
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
	return m, nil
}
