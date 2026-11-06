package controller

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

func TestFakeWorkerKeyAndBuildCreate(t *testing.T) {
	w := newFakeWorker()
	key := w.WorkerKey([]byte("seed"), "iid", 7)
	if key == "" {
		t.Fatal("WorkerKey: empty")
	}
	req := w.BuildCreate(provider.Offer{ID: 11, Hourly: 600000}, "vkllm:iid:7", key)
	if req.OfferID != 11 || req.Label != "vkllm:iid:7" {
		t.Fatalf("BuildCreate: %+v", req)
	}
}

func TestFakeWorkerProbeSequenceSticksOnLastValue(t *testing.T) {
	w := newFakeWorker()
	w.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeReady)
	ctx := context.Background()
	want := []ProbeStatus{ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeReady, ProbeReady, ProbeReady}
	for i, w0 := range want {
		got, err := w.Probe(ctx, nil, "key", "alias")
		if err != nil {
			t.Fatalf("Probe[%d]: %v", i, err)
		}
		if got != w0 {
			t.Fatalf("Probe[%d] = %v, want %v", i, got, w0)
		}
	}
}

func TestFakeWorkerScrapeNoop(t *testing.T) {
	w := newFakeWorker()
	if err := w.Scrape(context.Background(), nil); err != nil {
		t.Fatalf("Scrape: %v", err)
	}
}

type fakeWorker struct {
	mu         sync.Mutex
	probeSeq   []ProbeStatus
	probeIdx   int
	probeCalls int
	keyCalls   int
	buildCalls int
}

func newFakeWorker() *fakeWorker { return &fakeWorker{} }

var _ Worker = (*fakeWorker)(nil)

func (w *fakeWorker) SetProbeSequence(seq ...ProbeStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.probeSeq, w.probeIdx = seq, 0
}

func (w *fakeWorker) WorkerKey(_ []byte, installationID string, gen int64) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keyCalls++
	return fmt.Sprintf("fake-key-%s-%d", installationID, gen)
}

func (w *fakeWorker) BuildCreate(offer provider.Offer, label, workerKey string) provider.CreateRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buildCalls++
	return provider.CreateRequest{OfferID: offer.ID, Label: label, Env: map[string]string{"WORKER_KEY": workerKey}}
}

func (w *fakeWorker) Probe(_ context.Context, _ http.RoundTripper, _, _ string) (ProbeStatus, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.probeCalls++
	if len(w.probeSeq) == 0 {
		return ProbeUnreachable, nil
	}
	s := w.probeSeq[w.probeIdx]
	if w.probeIdx < len(w.probeSeq)-1 {
		w.probeIdx++
	}
	return s, nil
}

func (w *fakeWorker) probes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.probeCalls
}

func (w *fakeWorker) Scrape(context.Context, http.RoundTripper) error { return nil }
