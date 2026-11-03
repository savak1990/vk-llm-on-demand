package controller

import (
	"context"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

func newTestController(t *testing.T) (*Controller, *fakeprovider.Fake, *fakeclock.Clock, *fakeBudget, *fakeWorker) {
	t.Helper()
	clk := fakeclock.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.New(clk)
	budget := newFakeBudget(1.60, 30)
	worker := newFakeWorker()
	c, err := New(Deps{
		Provider: prov, Store: fakestore.New("iid"), Clock: clk, Budget: budget, Worker: worker,
		WorkerSeed:     func() ([]byte, error) { return []byte("seed"), nil },
		InstallationID: "iid", ModelAlias: "qwen3.6-35b-a3b",
		Config: config.Controller{ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80, IdleShutdownTimeout: 30 * time.Minute, ReconcileInterval: 60 * time.Second},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, prov, clk, budget, worker
}

func recvEvent(t *testing.T, c *Controller) (Event, bool) {
	t.Helper()
	select {
	case e := <-c.events:
		return e, true
	case <-time.After(time.Second):
		return Event{}, false
	}
}

func TestDispatchSearchFindsOffer(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	dispatchSearch(context.Background(), c, effSearch{filter: provider.OfferFilter{MaxHourly: 800000}})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvOfferChosen || ev.Offer.ID != 11 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchSearchNoOffer(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	dispatchSearch(context.Background(), c, effSearch{})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Reason != "no_offer" {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchSearchProviderErrorSetsErr(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.FailNext("search", 1, context.DeadlineExceeded)
	dispatchSearch(context.Background(), c, effSearch{})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvNoOffer || ev.Err == nil {
		t.Fatalf("event = %+v, ok=%v, want EvNoOffer with Err set", ev, ok)
	}
}

func TestDispatchCreateSuccess(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	dispatchCreate(context.Background(), c, effCreate{offer: provider.Offer{ID: 11, Hourly: 600000}, gen: 1, label: "vkllm:iid:1"})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreated || ev.Gen != 1 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchCreatePriceOverCapNeverCallsProvider(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	dispatchCreate(context.Background(), c, effCreate{offer: provider.Offer{ID: 11, Hourly: 900000}, gen: 1, label: "vkllm:iid:1"}) // $0.90 > $0.80 cap
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvCreateFailed || ev.Err == nil {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
	if prov.Calls("create") != 0 {
		t.Fatalf("Calls(create) = %d, want 0", prov.Calls("create"))
	}
}

func TestDispatchGetObservesRunningWithSSH(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:1", Status: provider.StatusOther})
	// provider/fake promotes StatusOther -> StatusRunning with a fake ssh host on the first Get.
	dispatchGet(context.Background(), c, effGet{id: 9, gen: 1})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvProviderObserved || ev.Status != provider.StatusRunning || ev.SSHHost == "" {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchGetFailureReadPostsNothing(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.FailNext("get", 1, context.DeadlineExceeded)
	dispatchGet(context.Background(), c, effGet{id: 9, gen: 1})
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event on a failed read (C-9)")
	}
}

func TestDispatchDialPlainFailurePostsNothing(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	// Nothing listens on :1, so this only proves a plain dial failure posts
	// no event; the success path needs a live listener and is covered
	// elsewhere.
	c.deps.Dialer = tunnel.NewDirect("127.0.0.1:1")
	dispatchDial(context.Background(), c, effDial{inst: tunnel.Instance{Host: "127.0.0.1", Port: 1}, gen: 1})
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event on a plain dial failure (retries on next poll)")
	}
}

func TestDispatchProbeReadyPostsHealthOK(t *testing.T) {
	c, _, _, _, worker := newTestController(t)
	c.conn = tunnel.NewConn(nil, func() error { return nil }, "fp")
	worker.SetProbeSequence(ProbeReady)
	dispatchProbe(context.Background(), c, effProbe{gen: 1})
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvHealthOK || ev.Gen != 1 {
		t.Fatalf("event = %+v, ok=%v", ev, ok)
	}
}

func TestDispatchProbeLoadingPostsNothing(t *testing.T) {
	c, _, _, _, worker := newTestController(t)
	c.conn = tunnel.NewConn(nil, func() error { return nil }, "fp")
	worker.SetProbeSequence(ProbeLoading)
	dispatchProbe(context.Background(), c, effProbe{gen: 1})
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event while still Loading")
	}
}

func TestNextCreateAllowedAt(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if got := nextCreateAllowedAt(nil); !got.IsZero() {
		t.Fatalf("no ops: got %v, want zero", got)
	}
	f1 := now
	ops := []store.Op{{Key: "create-1", FinishedAt: &f1, Outcome: "provider_error"}}
	if got := nextCreateAllowedAt(ops); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("1 failed op: got %v, want %v", got, now.Add(time.Minute))
	}
	f2 := now.Add(time.Hour)
	ops = append(ops, store.Op{Key: "create-2", FinishedAt: &f2, Outcome: "provider_error"})
	ops = append(ops, store.Op{Key: "create-3", FinishedAt: &f2, Outcome: "provider_error"})
	if got := nextCreateAllowedAt(ops); !got.Equal(f2.Add(4 * time.Minute)) {
		t.Fatalf("3 failed ops: got %v, want %v", got, f2.Add(4*time.Minute))
	}
	f3 := now.Add(2 * time.Hour)
	ops = append(ops, store.Op{Key: "create-4", FinishedAt: &f3, Outcome: "ready"})
	if got := nextCreateAllowedAt(ops); !got.IsZero() {
		t.Fatalf("ready resets: got %v, want zero", got)
	}
}
