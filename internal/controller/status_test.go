package controller

import (
	"math"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestStatusReflectsMemoryAndDocument(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	doc, _ := store.NewDocument("iid")
	doc.Generation = 3
	doc.Desired = store.DesiredRunning
	readyAt := clk.Now().Add(-10 * time.Minute)
	deadline := readyAt.Add(30 * time.Minute)
	doc.Instance = &store.Instance{
		State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9",
		Offer:     store.Offer{GPU: "L40S", MachineID: 42, DPH: 0.60},
		CreatedAt: readyAt, ReadyAt: &readyAt, IdleDeadline: &deadline,
	}
	doc.Ledger.Entries = []store.LedgerEntry{{Gen: 3, DPH: 0.60, EstUSD: 0.10}}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)
	c.mu.Lock()
	c.state, c.generation = Ready, 3
	c.mu.Unlock()

	snap := c.Status()
	if snap.State != Ready || snap.Public != "READY" || snap.Generation != 3 {
		t.Fatalf("snap = %+v", snap)
	}
	if snap.GPUName != "L40S" || snap.MachineID != 42 {
		t.Fatalf("offer fields: %+v", snap)
	}
	if !snap.Ready || snap.IdleDeadline == nil || !snap.IdleDeadline.Equal(deadline) {
		t.Fatalf("ready/deadline: %+v", snap)
	}
	if snap.SpendInstanceUSD != 0.10 {
		t.Fatalf("SpendInstanceUSD = %v, want 0.10", snap.SpendInstanceUSD)
	}
}

func TestStatusNeverCallsProvider(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	_ = c.Status()
	if prov.Calls("get")+prov.Calls("list")+prov.Calls("search")+prov.Calls("create")+prov.Calls("destroy") != 0 {
		t.Fatal("Status must never call the provider")
	}
}

func TestSplitSpendDividesTimeCostByPriceRatio(t *testing.T) {
	e := store.LedgerEntry{DPH: 0.6, FixedUSD: 1.14, EstUSD: 1.14 + 0.3}
	gpu, storage, dl := splitSpend(e, &store.OfferPrices{GPUUSDPerHour: 0.56})
	if math.Abs(gpu-0.28) > 1e-9 || math.Abs(storage-0.02) > 1e-9 || dl != 1.14 {
		t.Fatalf("split = %v %v %v, want 0.28 0.02 1.14", gpu, storage, dl)
	}
	gpu, storage, _ = splitSpend(e, nil)
	if math.Abs(gpu-0.3) > 1e-9 || storage != 0 {
		t.Fatalf("legacy split = %v %v, want all time cost on GPU", gpu, storage)
	}
}

func TestStatusProviderBalanceUnknownUntilFirstRead(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.balanceMu.Lock()
	c.lastBalanceKnown = false
	c.balanceMu.Unlock()
	snap := c.Status()
	if snap.ProviderBalanceUSD != nil {
		t.Fatalf("ProviderBalanceUSD = %v, want nil before any successful balance read", snap.ProviderBalanceUSD)
	}
	c.balanceMu.Lock()
	c.lastBalance, c.lastBalanceKnown = provider.Money(12_500_000), true
	c.balanceMu.Unlock()
	snap = c.Status()
	if snap.ProviderBalanceUSD == nil || *snap.ProviderBalanceUSD != 12.5 {
		t.Fatalf("ProviderBalanceUSD = %v, want 12.5", snap.ProviderBalanceUSD)
	}
}
