package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/budget"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func gatherValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			m := f.GetMetric()[0]
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %s not found in Gather()", name)
	return 0
}

func TestColdstartStagePreInitialized(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_coldstart_stage_seconds" {
			if len(f.GetMetric()) != 8 {
				t.Fatalf("vkllm_coldstart_stage_seconds has %d label combos, want 8", len(f.GetMetric()))
			}
			return
		}
	}
	t.Fatal("vkllm_coldstart_stage_seconds not registered")
}

func TestPublishCostMetricsAccumulatesAndClampsOnRollover(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs.RegisterMetrics(reg)
	c := &Controller{deps: Deps{Budget: budget.New(budget.Config{MonthlyUSD: 30})}}

	var doc store.Document
	doc.Ledger.TotalUSD = 5.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 5.0 {
		t.Fatalf("after first publish = %v, want 5.0", v)
	}
	if v := gatherValue(t, reg, "vkllm_budget_used_ratio"); v != 5.0/30 {
		t.Fatalf("used ratio = %v, want %v", v, 5.0/30)
	}

	doc.Ledger.TotalUSD = 8.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 8.0 {
		t.Fatalf("after second publish = %v, want 8.0 (5 + delta 3)", v)
	}

	// A monthly rollover drops the raw ledger total; the exported
	// counter must never decrease.
	doc.Ledger.TotalUSD = 1.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 8.0 {
		t.Fatalf("after rollover drop = %v, want unchanged 8.0", v)
	}

	// Growth resumes from the new, lower baseline.
	doc.Ledger.TotalUSD = 4.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 11.0 {
		t.Fatalf("after post-rollover growth = %v, want 11.0 (8 + delta 3)", v)
	}
}

func TestPublishCostMetricsNoOpenEntryIsZeroInstanceSpend(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs.RegisterMetrics(reg)
	c := &Controller{deps: Deps{Budget: budget.New(budget.Config{MonthlyUSD: 30})}}

	c.publishCostMetrics(store.Document{})
	if v := gatherValue(t, reg, "vkllm_cost_instance_usd"); v != 0 {
		t.Fatalf("instance spend with no open entry = %v, want 0", v)
	}
}
