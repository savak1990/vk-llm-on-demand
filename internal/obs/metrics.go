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

// RegisterMetrics registers obs's 5 owned metrics into reg. Safe to
// call more than once — each call builds fresh collectors, so no
// state leaks across repeated calls (e.g. one per test).
func RegisterMetrics(reg prometheus.Registerer) {
	m := &ownedMetrics{
		budgetUsedRatio: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_budget_used_ratio",
			Help: "Month-to-date estimated spend divided by the configured monthly budget.",
		}),
		costEstimatedUSDTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_cost_estimated_usd_total",
			Help: "Monotonic estimated spend; never decremented within this process's lifetime. A restart resets the baseline to 0, so the next tick jumps by the current month-to-date spend — expected, not a real cost event.",
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

// AddCostEstimatedUSD adds delta to the monotonic spend counter;
// Add panics on a negative value, so this guards the floor itself
// as a defensive backstop against a caller that forgets to clamp.
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
