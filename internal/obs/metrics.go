// internal/obs/metrics.go
package obs

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

type ownedMetrics struct {
	budgetUsedRatio            prometheus.Gauge
	costEstimatedUSDTotal      prometheus.Counter
	costInstanceUSD            prometheus.Gauge
	providerBalanceUSD         prometheus.Gauge
	inferenceScrapeErrorsTotal prometheus.Counter
}

var current atomic.Pointer[ownedMetrics]

// RegisterMetrics registers the 5 metrics obs owns directly into reg,
// so the catalog is complete independent of which other packages are
// wired. Safe to call more than once (e.g. once per test): it builds
// fresh collectors each time rather than reusing package-level
// singletons, so no state leaks across calls.
func RegisterMetrics(reg prometheus.Registerer) {
	m := &ownedMetrics{
		budgetUsedRatio: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_budget_used_ratio",
			Help: "Month-to-date estimated spend divided by the configured monthly budget.",
		}),
		costEstimatedUSDTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_cost_estimated_usd_total",
			Help: "Monotonic estimated spend; never decremented within this process's lifetime.",
		}),
		costInstanceUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_cost_instance_usd",
			Help: "Estimated cost of the current instance so far; 0 when no instance exists.",
		}),
		providerBalanceUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_provider_balance_usd",
			Help: "Last observed provider account balance.",
		}),
		inferenceScrapeErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_inference_scrape_errors_total",
			Help: "Failed GET /metrics pulls over the tunnel.",
		}),
	}
	reg.MustRegister(m.budgetUsedRatio, m.costEstimatedUSDTotal, m.costInstanceUSD,
		m.providerBalanceUSD, m.inferenceScrapeErrorsTotal)
	current.Store(m)
}

func SetBudgetUsedRatio(v float64)    { current.Load().budgetUsedRatio.Set(v) }
func SetCostInstanceUSD(v float64)    { current.Load().costInstanceUSD.Set(v) }
func SetProviderBalanceUSD(v float64) { current.Load().providerBalanceUSD.Set(v) }
func IncInferenceScrapeErrors()       { current.Load().inferenceScrapeErrorsTotal.Inc() }

// AddCostEstimatedUSD adds delta to the monotonic spend counter. A
// Prometheus counter panics on a negative Add, so a caller computing a
// delta across a value that can drop (e.g. a monthly rollover) must
// clamp to >=0 itself; this still guards the floor defensively.
func AddCostEstimatedUSD(delta float64) {
	if delta > 0 {
		current.Load().costEstimatedUSDTotal.Add(delta)
	}
}

// NewRegistry builds a fresh registry with obs's own metrics already
// registered. cmd/gpu-on-demand/main.go's sole constructor for the
// process-wide registry.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	return reg
}
