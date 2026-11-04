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
}
