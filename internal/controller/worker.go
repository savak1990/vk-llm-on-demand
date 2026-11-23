package controller

import (
	"context"
	"net/http"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

// ProbeStatus is the worker health-probe result that the future
// internal/worker package's real type mirrors.
type ProbeStatus int

const (
	ProbeUnreachable ProbeStatus = iota
	ProbeLoading
	ProbeReady
)

// Worker is the subset of the future internal/worker package this package
// calls: building the create request, deriving the per-instance key,
// probing readiness, the metrics-pull tick, and the download-progress read.
type Worker interface {
	WorkerKey(workerSeed []byte, installationID string, gen int64) string
	BuildCreate(offer provider.Offer, diskGB int, label, workerKey string) provider.CreateRequest
	Probe(ctx context.Context, rt http.RoundTripper, key, alias string) (ProbeStatus, error)
	Scrape(ctx context.Context, rt http.RoundTripper) error
	Progress(ctx context.Context, exec func(context.Context, string) ([]byte, error)) (DownloadProgress, error)
}

// DownloadProgress mirrors worker.Progress so this package does not import internal/worker.
type DownloadProgress struct {
	Bytes  int64
	Stages []string
}
