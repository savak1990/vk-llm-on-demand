package controller

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestFakeBudgetAllow(t *testing.T) {
	b := newFakeBudget(1.60, 30)
	doc, _ := store.NewDocument("iid")

	if got := b.Allow(doc, provider.Money(100_000_000), true); got != "" {
		t.Errorf("Allow(known, $100) = %q, want allowed", got)
	}
	if got := b.Allow(doc, provider.Money(1_590_000), true); got != "balance_low" {
		t.Errorf("Allow($1.59) = %q, want balance_low", got)
	}
	if got := b.Allow(doc, 0, false); got != "balance_low" {
		t.Errorf("Allow(unknown) = %q, want balance_low", got)
	}
	doc.Ledger.TotalUSD = 30
	if got := b.Allow(doc, provider.Money(100_000_000), true); got != "budget_exhausted" {
		t.Errorf("Allow(cap reached) = %q, want budget_exhausted", got)
	}
}

func TestFakeBudgetAssertPrice(t *testing.T) {
	b := newFakeBudget(1.60, 30)
	if err := b.AssertPrice(0.60, 0.80); err != nil {
		t.Errorf("AssertPrice(0.60, 0.80): %v", err)
	}
	if err := b.AssertPrice(0.90, 0.80); err == nil {
		t.Error("AssertPrice(0.90, 0.80): want error, got nil")
	}
}

func TestFakeBudgetLedgerLifecycle(t *testing.T) {
	b := newFakeBudget(1.60, 30)
	doc, _ := store.NewDocument("iid")
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	b.Rollover(&doc, t0)
	if doc.Ledger.Month != "2026-11" {
		t.Fatalf("Rollover: Month = %q", doc.Ledger.Month)
	}
	b.OpenEntry(&doc, t0, 7, 0.60)
	if len(doc.Ledger.Entries) != 1 || doc.Ledger.Entries[0].Gen != 7 {
		t.Fatalf("OpenEntry: Entries = %+v", doc.Ledger.Entries)
	}
	b.RefreshOpenEntry(&doc, t0.Add(time.Hour))
	if doc.Ledger.Entries[0].EstUSD != 0.60 {
		t.Fatalf("RefreshOpenEntry after 1h: EstUSD = %v, want 0.60", doc.Ledger.Entries[0].EstUSD)
	}
	b.CloseEntry(&doc, t0.Add(2*time.Hour), 7)
	if doc.Ledger.Entries[0].End == nil || doc.Ledger.TotalUSD != 1.20 {
		t.Fatalf("CloseEntry after 2h: End=%v TotalUSD=%v, want 1.20", doc.Ledger.Entries[0].End, doc.Ledger.TotalUSD)
	}
	if got := b.EstimatedMTD(doc); got != 1.20 {
		t.Fatalf("EstimatedMTD = %v, want 1.20", got)
	}
	if got := b.UsedRatio(doc); got != 1.20/30 {
		t.Fatalf("UsedRatio = %v, want %v", got, 1.20/30)
	}
}

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

type fakeBudget struct {
	floorUSD      float64
	monthlyCapUSD float64
}

func newFakeBudget(floorUSD, monthlyCapUSD float64) *fakeBudget {
	return &fakeBudget{floorUSD: floorUSD, monthlyCapUSD: monthlyCapUSD}
}

var _ Budget = (*fakeBudget)(nil)

func (b *fakeBudget) Allow(doc store.Document, balance provider.Money, known bool) string {
	if !known || balance.USD() < b.floorUSD {
		return "balance_low"
	}
	if doc.Ledger.TotalUSD >= b.monthlyCapUSD {
		return "budget_exhausted"
	}
	return ""
}

func (b *fakeBudget) AssertPrice(hourlyUSD, capUSD float64) error {
	if hourlyUSD > capUSD {
		return fmt.Errorf("controller: offer price %.2f exceeds cap %.2f", hourlyUSD, capUSD)
	}
	return nil
}

func (b *fakeBudget) OpenEntry(doc *store.Document, now time.Time, gen int64, dphUSD float64) {
	doc.Ledger.Entries = append(doc.Ledger.Entries, store.LedgerEntry{Gen: gen, Start: now, DPH: dphUSD})
}

func (b *fakeBudget) findOpenEntry(doc *store.Document, gen int64) *store.LedgerEntry {
	for i := range doc.Ledger.Entries {
		if doc.Ledger.Entries[i].Gen == gen && doc.Ledger.Entries[i].End == nil {
			return &doc.Ledger.Entries[i]
		}
	}
	return nil
}

func (b *fakeBudget) CloseEntry(doc *store.Document, now time.Time, gen int64) {
	e := b.findOpenEntry(doc, gen)
	if e == nil {
		return
	}
	e.EstUSD = now.Sub(e.Start).Hours() * e.DPH
	doc.Ledger.TotalUSD += e.EstUSD
	t := now
	e.End = &t
}

func (b *fakeBudget) Rollover(doc *store.Document, now time.Time) {
	month := now.Format("2006-01")
	if doc.Ledger.Month != month {
		doc.Ledger.Month = month
		doc.Ledger.TotalUSD = 0
		doc.Ledger.Entries = nil
	}
}

func (b *fakeBudget) RefreshOpenEntry(doc *store.Document, now time.Time) {
	for i := range doc.Ledger.Entries {
		if doc.Ledger.Entries[i].End == nil {
			doc.Ledger.Entries[i].EstUSD = now.Sub(doc.Ledger.Entries[i].Start).Hours() * doc.Ledger.Entries[i].DPH
		}
	}
}

func (b *fakeBudget) UsedRatio(doc store.Document) float64 {
	if b.monthlyCapUSD == 0 {
		return 0
	}
	return doc.Ledger.TotalUSD / b.monthlyCapUSD
}

func (b *fakeBudget) EstimatedMTD(doc store.Document) float64 {
	total := doc.Ledger.TotalUSD
	for _, e := range doc.Ledger.Entries {
		if e.End == nil {
			total += e.EstUSD
		}
	}
	return total
}

type fakeWorker struct {
	mu         sync.Mutex
	probeSeq   []ProbeStatus
	probeIdx   int
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
	if len(w.probeSeq) == 0 {
		return ProbeUnreachable, nil
	}
	s := w.probeSeq[w.probeIdx]
	if w.probeIdx < len(w.probeSeq)-1 {
		w.probeIdx++
	}
	return s, nil
}

func (w *fakeWorker) Scrape(context.Context, http.RoundTripper) error { return nil }
