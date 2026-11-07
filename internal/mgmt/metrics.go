package mgmt

import "github.com/prometheus/client_golang/prometheus"

var (
	mgmtRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_mgmt_requests_total",
	}, []string{"op", "code"})
	mgmtRequestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "vkllm_mgmt_request_seconds",
	}, []string{"op"})
)

func registerMetrics(reg prometheus.Registerer) {
	if reg == nil {
		return
	}
	reg.MustRegister(mgmtRequestsTotal, mgmtRequestSeconds)
	for _, op := range []string{"start", "status", "stop", "cost", "other"} {
		mgmtRequestSeconds.WithLabelValues(op)
		for _, code := range []string{"200", "202", "400", "401", "403", "404", "405", "413", "429", "500", "503"} {
			mgmtRequestsTotal.WithLabelValues(op, code)
		}
	}
}
