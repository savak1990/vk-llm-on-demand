package gateway

import "github.com/prometheus/client_golang/prometheus"

// gatewayMetrics holds one Gateway's own collectors, built fresh in
// New rather than as package-level vars, so two Gateways never share
// state or double-register against the same registry.
type gatewayMetrics struct {
	requestsTotal          *prometheus.CounterVec
	activeRequests         prometheus.Gauge
	requestSeconds         *prometheus.HistogramVec
	authFailuresTotal      *prometheus.CounterVec
	ttftSeconds            prometheus.Histogram
	outputTokensPerSecond  prometheus.Histogram
	pendingRequests        prometheus.Gauge
	waitSeconds            prometheus.Histogram
	coldStartOutcomesTotal *prometheus.CounterVec
}

func newGatewayMetrics(reg prometheus.Registerer) *gatewayMetrics {
	m := &gatewayMetrics{
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vkllm_gateway_requests_total",
			Help: "Total gateway requests by route and response code.",
		}, []string{"route", "code"}),
		activeRequests: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_gateway_active_requests",
			Help: "Gateway requests currently being handled.",
		}),
		requestSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "vkllm_gateway_request_seconds",
			Help: "Gateway request duration by route.",
		}, []string{"route"}),
		authFailuresTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vkllm_gateway_auth_failures_total",
			Help: "Authentication failures by reason.",
		}, []string{"reason"}),
		ttftSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "vkllm_gateway_ttft_seconds",
			Help: "Time to the first upstream token of a streamed response.",
		}),
		outputTokensPerSecond: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "vkllm_gateway_output_tokens_per_second",
			Help: "Output tokens per second for a completed plain or streamed response.",
		}),
		// Future on-demand-mode metrics, registered now so a
		// dashboard panel never shows "no data" after an upgrade.
		pendingRequests: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_gateway_pending_requests",
			Help: "On-demand mode only: requests queued waiting for a cold start.",
		}),
		waitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "vkllm_gateway_wait_seconds",
			Help: "On-demand mode only: time a request waited for a cold start.",
		}),
		coldStartOutcomesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vkllm_gateway_cold_start_outcomes_total",
			Help: "On-demand mode only: cold-start outcomes.",
		}, []string{"outcome"}),
	}
	if reg != nil {
		reg.MustRegister(m.requestsTotal, m.activeRequests, m.requestSeconds, m.authFailuresTotal,
			m.ttftSeconds, m.outputTokensPerSecond, m.pendingRequests, m.waitSeconds, m.coldStartOutcomesTotal)
	}
	return m
}
