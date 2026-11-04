package worker

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Snapshot is one successful read of the worker's llama-server metrics.
type Snapshot struct {
	PromptTokensTotal        float64
	PromptSecondsTotal       float64
	GeneratedTokensTotal     float64
	GenerationSecondsTotal   float64
	PromptTokensPerSecond    float64
	GeneratedTokensPerSecond float64
	RequestsProcessing       float64
	RequestsDeferred         float64
}

// reexportNames lists the eight llama-server metrics worker re-exports,
// in Snapshot field order.
var reexportNames = []string{
	"llamacpp:prompt_tokens_total",
	"llamacpp:prompt_seconds_total",
	"llamacpp:tokens_predicted_total",
	"llamacpp:tokens_predicted_seconds_total",
	"llamacpp:prompt_tokens_seconds",
	"llamacpp:predicted_tokens_seconds",
	"llamacpp:requests_processing",
	"llamacpp:requests_deferred",
}

// Metrics fetches and parses the worker's /metrics.
func (c Client) Metrics(ctx context.Context) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, workerBaseURL+"/metrics", nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return Snapshot{}, fmt.Errorf("worker: /metrics returned status %d", resp.StatusCode)
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return Snapshot{}, fmt.Errorf("worker: parsing /metrics: %w", err)
	}

	value := func(name string) float64 {
		fam, ok := families[name]
		if !ok || len(fam.GetMetric()) == 0 {
			return 0
		}
		m := fam.GetMetric()[0]
		if c := m.GetCounter(); c != nil {
			return c.GetValue()
		}
		return m.GetGauge().GetValue()
	}

	return Snapshot{
		PromptTokensTotal:        value(reexportNames[0]),
		PromptSecondsTotal:       value(reexportNames[1]),
		GeneratedTokensTotal:     value(reexportNames[2]),
		GenerationSecondsTotal:   value(reexportNames[3]),
		PromptTokensPerSecond:    value(reexportNames[4]),
		GeneratedTokensPerSecond: value(reexportNames[5]),
		RequestsProcessing:       value(reexportNames[6]),
		RequestsDeferred:         value(reexportNames[7]),
	}, nil
}

// Exporter serves the last Snapshot under the vkllm_inference_* names.
// Until the first successful Update, only vkllm_inference_up 0 is served.
type Exporter struct {
	mu   sync.Mutex
	up   bool
	snap Snapshot

	promptTokensTotal, promptSecondsTotal           *prometheus.Desc
	generatedTokensTotal, generationSecondsTotal    *prometheus.Desc
	promptTokensPerSecond, generatedTokensPerSecond *prometheus.Desc
	requestsProcessing, requestsDeferred            *prometheus.Desc
	scrapeUp                                        *prometheus.Desc
}

// NewExporter builds an Exporter with no prior scrape recorded.
func NewExporter() *Exporter {
	return &Exporter{
		promptTokensTotal:        prometheus.NewDesc("vkllm_inference_prompt_tokens_total", "re-exported llamacpp:prompt_tokens_total", nil, nil),
		promptSecondsTotal:       prometheus.NewDesc("vkllm_inference_prompt_seconds_total", "re-exported llamacpp:prompt_seconds_total", nil, nil),
		generatedTokensTotal:     prometheus.NewDesc("vkllm_inference_generated_tokens_total", "re-exported llamacpp:tokens_predicted_total", nil, nil),
		generationSecondsTotal:   prometheus.NewDesc("vkllm_inference_generation_seconds_total", "re-exported llamacpp:tokens_predicted_seconds_total", nil, nil),
		promptTokensPerSecond:    prometheus.NewDesc("vkllm_inference_prompt_tokens_per_second", "re-exported llamacpp:prompt_tokens_seconds", nil, nil),
		generatedTokensPerSecond: prometheus.NewDesc("vkllm_inference_generated_tokens_per_second", "re-exported llamacpp:predicted_tokens_seconds", nil, nil),
		requestsProcessing:       prometheus.NewDesc("vkllm_inference_requests_processing", "re-exported llamacpp:requests_processing", nil, nil),
		requestsDeferred:         prometheus.NewDesc("vkllm_inference_requests_deferred", "re-exported llamacpp:requests_deferred", nil, nil),
		scrapeUp:                 prometheus.NewDesc("vkllm_inference_up", "1 if the last scrape of the worker's /metrics succeeded, else 0", nil, nil),
	}
}

// Update records a successful scrape's values; the inference series and
// vkllm_inference_up 1 are served until the next Update or MarkDown.
func (e *Exporter) Update(snap Snapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.snap = snap
	e.up = true
}

// MarkDown records a failed scrape: vkllm_inference_up goes to 0 and the
// inference series stop being served until the next successful Update.
func (e *Exporter) MarkDown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.up = false
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- e.promptTokensTotal
	ch <- e.promptSecondsTotal
	ch <- e.generatedTokensTotal
	ch <- e.generationSecondsTotal
	ch <- e.promptTokensPerSecond
	ch <- e.generatedTokensPerSecond
	ch <- e.requestsProcessing
	ch <- e.requestsDeferred
	ch <- e.scrapeUp
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mu.Lock()
	up, snap := e.up, e.snap
	e.mu.Unlock()

	if up {
		ch <- prometheus.MustNewConstMetric(e.promptTokensTotal, prometheus.CounterValue, snap.PromptTokensTotal)
		ch <- prometheus.MustNewConstMetric(e.promptSecondsTotal, prometheus.CounterValue, snap.PromptSecondsTotal)
		ch <- prometheus.MustNewConstMetric(e.generatedTokensTotal, prometheus.CounterValue, snap.GeneratedTokensTotal)
		ch <- prometheus.MustNewConstMetric(e.generationSecondsTotal, prometheus.CounterValue, snap.GenerationSecondsTotal)
		ch <- prometheus.MustNewConstMetric(e.promptTokensPerSecond, prometheus.GaugeValue, snap.PromptTokensPerSecond)
		ch <- prometheus.MustNewConstMetric(e.generatedTokensPerSecond, prometheus.GaugeValue, snap.GeneratedTokensPerSecond)
		ch <- prometheus.MustNewConstMetric(e.requestsProcessing, prometheus.GaugeValue, snap.RequestsProcessing)
		ch <- prometheus.MustNewConstMetric(e.requestsDeferred, prometheus.GaugeValue, snap.RequestsDeferred)
		ch <- prometheus.MustNewConstMetric(e.scrapeUp, prometheus.GaugeValue, 1)
		return
	}
	ch <- prometheus.MustNewConstMetric(e.scrapeUp, prometheus.GaugeValue, 0)
}
