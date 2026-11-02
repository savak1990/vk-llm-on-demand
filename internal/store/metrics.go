package store

import "github.com/prometheus/client_golang/prometheus"

var (
	conflictsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vkllm_store_conflicts_total",
	})
	opsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_store_ops_total",
	}, []string{"op", "outcome"})
)

// RegisterMetrics registers this package's collectors; main calls it once.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(conflictsTotal, opsTotal)
}

// RecordOp lets the configmap implementation record load/save outcomes.
func RecordOp(op, outcome string) { opsTotal.WithLabelValues(op, outcome).Inc() }
