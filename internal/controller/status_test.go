package controller

import (
	"testing"
	"time"

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
