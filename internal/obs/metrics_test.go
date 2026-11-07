// internal/obs/metrics_test.go
package obs

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObsOwnedMetricsPreInitialized(t *testing.T) {
	reg := NewRegistry()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := map[string]bool{
		"vkllm_budget_used_ratio": true, "vkllm_cost_estimated_usd_total": true,
		"vkllm_cost_instance_usd": true, "vkllm_provider_balance_usd": true,
		"vkllm_inference_scrape_errors_total": true,
	}
	got := map[string]bool{}
	for _, f := range families {
		if !want[f.GetName()] {
			continue
		}
		got[f.GetName()] = true
		m := f.GetMetric()[0]
		if g := m.GetGauge(); g != nil && g.GetValue() != 0 {
			t.Errorf("%s = %v, want 0", f.GetName(), g.GetValue())
		}
		if c := m.GetCounter(); c != nil && c.GetValue() != 0 {
			t.Errorf("%s = %v, want 0", f.GetName(), c.GetValue())
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing metric family %s", name)
		}
	}
}

func TestSettersUpdateValuesAndNegativeDeltaIsIgnored(t *testing.T) {
	reg := NewRegistry()
	SetBudgetUsedRatio(0.42)
	SetCostInstanceUSD(1.5)
	SetProviderBalanceUSD(12.5)
	IncInferenceScrapeErrors()
	AddCostEstimatedUSD(3.0)
	AddCostEstimatedUSD(-1.0) // must not panic, must not apply

	families, _ := reg.Gather()
	values := map[string]float64{}
	for _, f := range families {
		m := f.GetMetric()[0]
		switch {
		case m.GetGauge() != nil:
			values[f.GetName()] = m.GetGauge().GetValue()
		case m.GetCounter() != nil:
			values[f.GetName()] = m.GetCounter().GetValue()
		}
	}
	if values["vkllm_budget_used_ratio"] != 0.42 {
		t.Errorf("budget_used_ratio = %v, want 0.42", values["vkllm_budget_used_ratio"])
	}
	if values["vkllm_cost_instance_usd"] != 1.5 {
		t.Errorf("cost_instance_usd = %v, want 1.5", values["vkllm_cost_instance_usd"])
	}
	if values["vkllm_provider_balance_usd"] != 12.5 {
		t.Errorf("provider_balance_usd = %v, want 12.5", values["vkllm_provider_balance_usd"])
	}
	if values["vkllm_inference_scrape_errors_total"] != 1 {
		t.Errorf("inference_scrape_errors_total = %v, want 1", values["vkllm_inference_scrape_errors_total"])
	}
	if values["vkllm_cost_estimated_usd_total"] != 3.0 {
		t.Errorf("cost_estimated_usd_total = %v, want 3.0 (negative delta must be ignored)", values["vkllm_cost_estimated_usd_total"])
	}
	_ = testutil.ToFloat64 // kept available for a future, more targeted assertion
}

func TestRegisterMetricsIsTestIsolated(t *testing.T) {
	regA := NewRegistry()
	SetBudgetUsedRatio(0.9)
	_ = regA

	regB := NewRegistry() // a second RegisterMetrics call must start from zero
	families, _ := regB.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_budget_used_ratio" {
			if v := f.GetMetric()[0].GetGauge().GetValue(); v != 0 {
				t.Fatalf("budget_used_ratio on a fresh registry = %v, want 0 (state leaked across RegisterMetrics calls)", v)
			}
		}
	}
}
