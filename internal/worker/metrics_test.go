package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsReexport(t *testing.T) {
	fixture, err := os.ReadFile("testdata/metrics_ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	c := Client{Transport: redirectTransport(addr), Key: "test-key", Alias: "a"}

	exp := NewExporter()
	snap, err := c.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics() error = %v, want nil", err)
	}
	exp.Update(snap)

	want := `
# HELP vkllm_inference_prompt_tokens_total re-exported llamacpp:prompt_tokens_total
# TYPE vkllm_inference_prompt_tokens_total counter
vkllm_inference_prompt_tokens_total 1234
# HELP vkllm_inference_prompt_seconds_total re-exported llamacpp:prompt_seconds_total
# TYPE vkllm_inference_prompt_seconds_total counter
vkllm_inference_prompt_seconds_total 12.5
# HELP vkllm_inference_generated_tokens_total re-exported llamacpp:tokens_predicted_total
# TYPE vkllm_inference_generated_tokens_total counter
vkllm_inference_generated_tokens_total 5678
# HELP vkllm_inference_generation_seconds_total re-exported llamacpp:tokens_predicted_seconds_total
# TYPE vkllm_inference_generation_seconds_total counter
vkllm_inference_generation_seconds_total 34.2
# HELP vkllm_inference_prompt_tokens_per_second re-exported llamacpp:prompt_tokens_seconds
# TYPE vkllm_inference_prompt_tokens_per_second gauge
vkllm_inference_prompt_tokens_per_second 98.7
# HELP vkllm_inference_generated_tokens_per_second re-exported llamacpp:predicted_tokens_seconds
# TYPE vkllm_inference_generated_tokens_per_second gauge
vkllm_inference_generated_tokens_per_second 65.4
# HELP vkllm_inference_requests_processing re-exported llamacpp:requests_processing
# TYPE vkllm_inference_requests_processing gauge
vkllm_inference_requests_processing 1
# HELP vkllm_inference_requests_deferred re-exported llamacpp:requests_deferred
# TYPE vkllm_inference_requests_deferred gauge
vkllm_inference_requests_deferred 0
# HELP vkllm_inference_up 1 if the last scrape of the worker's /metrics succeeded, else 0
# TYPE vkllm_inference_up gauge
vkllm_inference_up 1
`
	if err := testutil.CollectAndCompare(exp, strings.NewReader(want)); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}

func TestMetricsReexportScrapeFailure(t *testing.T) {
	// A pull loop scrapes successfully once, then the next scrape fails:
	// the exporter must drop the inference series, not keep serving the
	// stale snapshot.
	fixture, err := os.ReadFile("testdata/metrics_ok.txt")
	if err != nil {
		t.Fatal(err)
	}
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	c := Client{Transport: redirectTransport(addr), Key: "test-key", Alias: "a"}
	exp := NewExporter()

	snap, err := c.Metrics(context.Background())
	if err != nil {
		t.Fatalf("Metrics() error = %v, want nil on the first scrape", err)
	}
	exp.Update(snap)

	fail = true
	if _, err := c.Metrics(context.Background()); err == nil {
		t.Fatal("Metrics() error = nil, want an error on a 500 response")
	}
	exp.MarkDown()

	want := `
# HELP vkllm_inference_up 1 if the last scrape of the worker's /metrics succeeded, else 0
# TYPE vkllm_inference_up gauge
vkllm_inference_up 0
`
	if err := testutil.CollectAndCompare(exp, strings.NewReader(want)); err != nil {
		t.Errorf("unexpected metrics: %v", err)
	}
}
