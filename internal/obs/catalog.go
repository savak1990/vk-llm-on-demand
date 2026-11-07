// internal/obs/catalog.go
package obs

import "sort"

// Spec is one entry in the metric catalog — the single source of
// truth every metric name in this codebase is checked against.
type Spec struct {
	Name string
	Type string // counter, gauge, histogram
}

func Catalog() []Spec {
	c := []Spec{
		{"vkllm_gateway_requests_total", "counter"},
		{"vkllm_gateway_active_requests", "gauge"},
		{"vkllm_gateway_pending_requests", "gauge"},
		{"vkllm_gateway_wait_seconds", "histogram"},
		{"vkllm_gateway_request_seconds", "histogram"},
		{"vkllm_gateway_ttft_seconds", "histogram"},
		{"vkllm_gateway_output_tokens_per_second", "histogram"},
		{"vkllm_gateway_cold_start_outcomes_total", "counter"},
		{"vkllm_gateway_auth_failures_total", "counter"},
		{"vkllm_gateway_cancelled_streams_total", "counter"},
		{"vkllm_mgmt_requests_total", "counter"},
		{"vkllm_mgmt_request_seconds", "histogram"},
		{"vkllm_gpu_state", "gauge"},
		{"vkllm_provision_attempts_total", "counter"},
		{"vkllm_coldstart_stage_seconds", "histogram"},
		{"vkllm_gpu_uptime_seconds_total", "counter"},
		{"vkllm_gpu_idle_seconds", "gauge"},
		{"vkllm_gpu_idle_deadline_seconds", "gauge"},
		{"vkllm_gpu_last_inference_finished_seconds", "gauge"},
		{"vkllm_shutdowns_total", "counter"},
		{"vkllm_destroy_attempts_total", "counter"},
		{"vkllm_orphans_destroyed_total", "counter"},
		{"vkllm_reconcile_runs_total", "counter"},
		{"vkllm_instance_hourly_price_usd", "gauge"},
		{"vkllm_provider_requests_total", "counter"},
		{"vkllm_provider_request_seconds", "histogram"},
		{"vkllm_provider_rate_limited_total", "counter"},
		{"vkllm_store_conflicts_total", "counter"},
		{"vkllm_store_ops_total", "counter"},
		{"vkllm_inference_prompt_tokens_total", "counter"},
		{"vkllm_inference_prompt_seconds_total", "counter"},
		{"vkllm_inference_generated_tokens_total", "counter"},
		{"vkllm_inference_generation_seconds_total", "counter"},
		{"vkllm_inference_prompt_tokens_per_second", "gauge"},
		{"vkllm_inference_generated_tokens_per_second", "gauge"},
		{"vkllm_inference_requests_processing", "gauge"},
		{"vkllm_inference_requests_deferred", "gauge"},
		{"vkllm_inference_up", "gauge"},
		{"vkllm_inference_scrape_errors_total", "counter"},
		{"vkllm_budget_used_ratio", "gauge"},
		{"vkllm_cost_estimated_usd_total", "counter"},
		{"vkllm_cost_instance_usd", "gauge"},
		{"vkllm_provider_balance_usd", "gauge"},
	}
	sort.Slice(c, func(i, j int) bool { return c[i].Name < c[j].Name })
	return c
}

// VastaiOnlyMetrics names the 3 catalog entries that only register
// when the real Vast.ai provider is constructed, not the fake one —
// a caller using the fake provider sees Catalog() minus these 3.
func VastaiOnlyMetrics() []string {
	return []string{
		"vkllm_provider_requests_total",
		"vkllm_provider_request_seconds",
		"vkllm_provider_rate_limited_total",
	}
}
