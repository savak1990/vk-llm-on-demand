package vastai

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	requestsTotal  *prometheus.CounterVec
	requestSeconds *prometheus.HistogramVec
	rateLimited    prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vkllm_provider_requests_total",
		}, []string{"op", "outcome"}),
		requestSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "vkllm_provider_request_seconds",
		}, []string{"op"}),
		rateLimited: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_provider_rate_limited_total",
		}),
	}
	reg.MustRegister(m.requestsTotal, m.requestSeconds, m.rateLimited)
	return m
}
