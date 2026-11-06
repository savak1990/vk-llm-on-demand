package main

import (
	"context"
	"net/http"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

// workerAdapter bridges internal/worker's free functions onto the
// controller.Worker interface; internal/worker stays unaware of
// internal/controller, and vice versa.
type workerAdapter struct {
	cfg              config.Worker
	diskGB           int
	modelAlias       string
	modelContextSize int
	exporter         *worker.Exporter
}

func newWorkerAdapter(cfg config.Worker, diskGB int, modelAlias string, modelContextSize int, exp *worker.Exporter) *workerAdapter {
	return &workerAdapter{cfg: cfg, diskGB: diskGB, modelAlias: modelAlias, modelContextSize: modelContextSize, exporter: exp}
}

var _ controller.Worker = (*workerAdapter)(nil)

func (a *workerAdapter) WorkerKey(seed []byte, installationID string, gen int64) string {
	return worker.WorkerKey(seed, installationID, gen)
}

// BuildCreate's error path is unreachable: wire() already validates
// these same fields once at startup via worker.BuildCreate directly.
// A panic here means that check and this call disagree.
func (a *workerAdapter) BuildCreate(offer provider.Offer, label, workerKey string) provider.CreateRequest {
	req, err := worker.BuildCreate(worker.CreateParams{
		Image: a.cfg.WorkerImage, DiskGB: a.diskGB, Label: label,
		ModelURL: a.cfg.ModelURL, ModelSHA256: a.cfg.ModelSHA256, Alias: a.modelAlias,
		Ctx: a.modelContextSize, Parallel: a.cfg.ModelParallel,
		DeadMan: a.cfg.WorkerDeadManTimeout, WorkerKey: workerKey,
	})
	if err != nil {
		panic("worker: BuildCreate rejected config that LoadWorker should have already validated: " + err.Error())
	}
	req.OfferID = offer.ID
	return req
}

func (a *workerAdapter) Probe(ctx context.Context, rt http.RoundTripper, key, alias string) (controller.ProbeStatus, error) {
	readiness, err := (worker.Client{Transport: rt, Key: key, Alias: alias}).Probe(ctx)
	// Readiness and ProbeStatus both use iota: Unreachable/ProbeUnreachable(0), Loading/ProbeLoading(1), Ready/ProbeReady(2).
	return controller.ProbeStatus(readiness), err
}

func (a *workerAdapter) Scrape(ctx context.Context, rt http.RoundTripper) error {
	snap, err := (worker.Client{Transport: rt, Key: "", Alias: a.modelAlias}).Metrics(ctx)
	if err != nil {
		a.exporter.MarkDown()
		return err
	}
	a.exporter.Update(snap)
	return nil
}
