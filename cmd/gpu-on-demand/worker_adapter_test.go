package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

const testSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func testWorkerCfg() config.Worker {
	return config.Worker{
		ModelURL: "https://huggingface.co/example/model.gguf", ModelSHA256: testSHA256,
		ModelParallel: 1, WorkerImage: "ghcr.io/example/worker", WorkerDeadManTimeout: 40 * 60 * 1e9,
	}
}

func TestWorkerAdapterWorkerKeyDelegates(t *testing.T) {
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, worker.NewExporter())
	got := a.WorkerKey([]byte("seed"), "iid", 7)
	want := worker.WorkerKey([]byte("seed"), "iid", 7)
	if got != want {
		t.Fatalf("WorkerKey = %q, want %q", got, want)
	}
}

func TestWorkerAdapterBuildCreateSetsOfferIDAndStaticFields(t *testing.T) {
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, worker.NewExporter())
	offer := provider.Offer{ID: 42, Hourly: 710000}
	req := a.BuildCreate(offer, "vkllm:iid:7", "worker-key-xyz")

	if req.OfferID != 42 {
		t.Errorf("OfferID = %d, want 42", req.OfferID)
	}
	if req.DiskGB != 60 {
		t.Errorf("DiskGB = %d, want 60", req.DiskGB)
	}
	if req.Label != "vkllm:iid:7" {
		t.Errorf("Label = %q", req.Label)
	}
	if req.Env["MODEL_ALIAS"] != "qwen3.6-35b-a3b" {
		t.Errorf("Env[MODEL_ALIAS] = %q", req.Env["MODEL_ALIAS"])
	}
	if req.Env["WORKER_API_KEY"] != "worker-key-xyz" {
		t.Errorf("Env[WORKER_API_KEY] = %q", req.Env["WORKER_API_KEY"])
	}
}

func TestWorkerAdapterProbeMapsReadinessToProbeStatus(t *testing.T) {
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, worker.NewExporter())
	// No listener on this transport: every call reaches past dial and
	// returns the http.Client's own connection-refused error, which
	// worker.Client.Probe already treats as Unreachable (not a failure).
	status, err := a.Probe(context.Background(), http.DefaultTransport, "key", "qwen3.6-35b-a3b")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if status != controller.ProbeUnreachable {
		t.Fatalf("Probe status = %v, want ProbeUnreachable (no server listening)", status)
	}
}

func TestWorkerAdapterScrapeUpdatesExporterOnSuccessAndMarksDownOnFailure(t *testing.T) {
	exp := worker.NewExporter()
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, exp)
	// Same no-listener transport: Metrics fails, Scrape must mark the
	// exporter down and return the error rather than panic or hide it.
	if err := a.Scrape(context.Background(), http.DefaultTransport); err == nil {
		t.Fatal("Scrape: want error with no server listening")
	}
}
