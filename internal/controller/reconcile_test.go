package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestReconcileOrphanDestroyedAfterTwoObservations(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:2", Status: provider.StatusRunning})
	doc, _, _ := c.deps.Store.Load(context.Background())
	doc.Generation = 5
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	c.reconcileCompareInstances(context.Background(), doc, mustList(t, prov))
	if prov.Calls("destroy") != 0 {
		t.Fatalf("destroy on first observation: Calls(destroy) = %d, want 0", prov.Calls("destroy"))
	}
	c.reconcileCompareInstances(context.Background(), doc, mustList(t, prov))
	deadline := time.Now().Add(time.Second)
	for prov.Calls("destroy") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prov.Calls("destroy") != 1 {
		t.Fatalf("destroy after two observations: Calls(destroy) = %d, want 1", prov.Calls("destroy"))
	}
}

func TestReconcileForeignLabelNeverDestroyed(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:other:1", Status: provider.StatusRunning})
	doc, _, _ := c.deps.Store.Load(context.Background())
	doc.Generation = 5
	for i := 0; i < 3; i++ {
		c.reconcileCompareInstances(context.Background(), doc, mustList(t, prov))
	}
	time.Sleep(50 * time.Millisecond)
	if prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) = %d, want 0 for a foreign-installation label", prov.Calls("destroy"))
	}
}

func TestReconcileVanishAfterTwoObservations(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	doc, _, _ := c.deps.Store.Load(context.Background())
	doc.Generation = 3
	doc.Instance = &store.Instance{Label: "vkllm:iid:3", State: Ready.String()}

	c.reconcileCompareInstances(context.Background(), doc, nil) // absent, 1st observation
	if _, ok := recvEvent(t, c); ok {
		t.Fatal("want no event after the first absent observation")
	}
	c.reconcileCompareInstances(context.Background(), doc, nil) // absent, 2nd observation
	ev, ok := recvEvent(t, c)
	if !ok || ev.Kind != EvVanished || ev.Gen != 3 {
		t.Fatalf("event = %+v, ok=%v, want EvVanished gen=3", ev, ok)
	}
}

func TestReconcilePrunesExpiredDenylistAndKeepsLive(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	now := c.deps.Clock.Now()
	doc, _, _ := c.deps.Store.Load(context.Background())
	doc.MachineDenylist = []store.DenyEntry{
		{MachineID: 1, Until: now.Add(-time.Hour)},
		{MachineID: 2, Until: now.Add(time.Hour)},
	}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	c.reconcilePruneAndBudget(context.Background(), provider.Money(100_000_000), true)
	got, _, _ := c.deps.Store.Load(context.Background())
	if len(got.MachineDenylist) != 1 || got.MachineDenylist[0].MachineID != 2 {
		t.Fatalf("MachineDenylist after prune = %+v, want only machine 2", got.MachineDenylist)
	}
}

// MaxInstanceSpendUSD has its own cap, independent of the monthly one:
// an open ledger entry already at or past it must raise
// EvBudgetExhausted even though the monthly total is nowhere near its cap.
func TestReconcileMaxInstanceSpendExceededPostsBudgetExhausted(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.deps.Config.MaxInstanceSpendUSD = 1.0
	now := c.deps.Clock.Now()
	doc, _, _ := c.deps.Store.Load(context.Background())
	doc.Generation = 3
	doc.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3"}
	doc.Ledger.Month = now.Format("2006-01")
	doc.Ledger.Entries = []store.LedgerEntry{{Gen: 3, Start: now.Add(-2 * time.Hour), DPH: 0.60}}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	c.reconcilePruneAndBudget(context.Background(), provider.Money(100_000_000), true)

	if ev, ok := recvEvent(t, c); !ok || ev.Kind != EvTick {
		t.Fatalf("first event = %+v, ok=%v, want EvTick", ev, ok)
	}
	if ev, ok := recvEvent(t, c); !ok || ev.Kind != EvBudgetExhausted || ev.Reason != "budget_exhausted" {
		t.Fatalf("second event = %+v, ok=%v, want EvBudgetExhausted{budget_exhausted}", ev, ok)
	}
}

func mustList(t *testing.T, prov *fakeprovider.Fake) []provider.Instance {
	t.Helper()
	instances, err := prov.ListInstances(context.Background(), "vkllm:iid:")
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	return instances
}

// queuedEvents drains what is already queued; compare posts synchronously, so nothing is still in flight.
func queuedEvents(c *Controller) []Event {
	var out []Event
	for {
		select {
		case e := <-c.events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func loadDoc(t *testing.T, c *Controller) store.Document {
	t.Helper()
	doc, _, err := c.deps.Store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return doc
}

func TestReconcileTable(t *testing.T) {
	ctx := context.Background()
	ready := func(gen int64, id string) func(*store.Document) {
		return func(d *store.Document) {
			d.Generation = gen
			d.Desired = store.DesiredRunning
			d.Instance = &store.Instance{Label: "vkllm:iid:" + itoa(gen), ProviderID: id, State: Ready.String()}
		}
	}
	cases := []struct {
		name        string
		edit        func(*store.Document)
		instances   []provider.Instance
		passes      int
		wantDestroy int
		wantGen     int64
		wantEvents  []Event
	}{
		{
			name:      "orphan_first_observation_only_counts",
			edit:      func(d *store.Document) { d.Generation = 3 },
			instances: []provider.Instance{{ID: 7, Label: "vkllm:iid:2", Status: provider.StatusRunning}},
			passes:    1, wantDestroy: 0, wantGen: 3,
		},
		{
			name:      "orphan_destroyed_on_second_observation",
			edit:      func(d *store.Document) { d.Generation = 3 },
			instances: []provider.Instance{{ID: 7, Label: "vkllm:iid:2", Status: provider.StatusRunning}},
			passes:    2, wantDestroy: 1, wantGen: 3,
		},
		{
			name: "duplicate_label_with_other_id_is_orphan",
			edit: ready(3, "9"),
			instances: []provider.Instance{
				{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning},
				{ID: 10, Label: "vkllm:iid:3", Status: provider.StatusRunning},
			},
			passes: 2, wantDestroy: 1, wantGen: 3,
		},
		{
			name:      "foreign_installation_never_touched",
			edit:      func(d *store.Document) { d.Generation = 3 },
			instances: []provider.Instance{{ID: 7, Label: "vkllm:other:1", Status: provider.StatusRunning}},
			passes:    3, wantDestroy: 0, wantGen: 3,
		},
		{
			name:      "unparsable_label_never_touched",
			edit:      func(d *store.Document) { d.Generation = 3 },
			instances: []provider.Instance{{ID: 7, Label: "vkllm:iid:x", Status: provider.StatusRunning}},
			passes:    3, wantDestroy: 0, wantGen: 3,
		},
		{
			name:      "fence_advance_counts_as_first_observation",
			edit:      func(d *store.Document) { d.Generation = 3 },
			instances: []provider.Instance{{ID: 7, Label: "vkllm:iid:5", Status: provider.StatusRunning}},
			passes:    1, wantDestroy: 0, wantGen: 5,
		},
		{
			name:      "fence_advance_then_destroy_on_second_tick",
			edit:      func(d *store.Document) { d.Generation = 3 },
			instances: []provider.Instance{{ID: 7, Label: "vkllm:iid:5", Status: provider.StatusRunning}},
			passes:    2, wantDestroy: 1, wantGen: 5,
		},
		{
			name:      "empty_store_fence_advance_then_destroy",
			edit:      func(*store.Document) {},
			instances: []provider.Instance{{ID: 5, Label: "vkllm:iid:7", Status: provider.StatusRunning}},
			passes:    2, wantDestroy: 1, wantGen: 7,
		},
		{
			name: "fence_advance_deferred_while_instance_active",
			edit: ready(3, "9"),
			instances: []provider.Instance{
				{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning},
				{ID: 7, Label: "vkllm:iid:5", Status: provider.StatusRunning},
			},
			passes: 3, wantDestroy: 0, wantGen: 3,
		},
		{
			name: "pending_op_adopts_lowest_id",
			edit: func(d *store.Document) {
				d.Generation = 4
				d.Desired = store.DesiredRunning
				d.Instance = &store.Instance{Label: "vkllm:iid:4", State: Creating.String()}
				d.Ops = []store.Op{{Key: "create-4"}}
			},
			instances: []provider.Instance{
				{ID: 12, Label: "vkllm:iid:4", Status: provider.StatusOther},
				{ID: 11, Label: "vkllm:iid:4", Status: provider.StatusOther},
			},
			passes: 2, wantDestroy: 0, wantGen: 4,
			wantEvents: []Event{{Kind: EvCreated, Gen: 4, ProviderID: 11}, {Kind: EvCreated, Gen: 4, ProviderID: 11}},
		},
		{
			name:      "terminal_status_first_observation_only_counts",
			edit:      ready(3, "9"),
			instances: []provider.Instance{{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusExited, RawStatus: "exited"}},
			passes:    1, wantDestroy: 0, wantGen: 3,
		},
		{
			name:      "terminal_status_forwarded_on_second_observation",
			edit:      ready(3, "9"),
			instances: []provider.Instance{{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusExited, RawStatus: "exited"}},
			passes:    2, wantDestroy: 0, wantGen: 3,
			wantEvents: []Event{{Kind: EvProviderObserved, Gen: 3, Status: provider.StatusExited}},
		},
		{
			name:      "running_active_posts_nothing",
			edit:      ready(3, "9"),
			instances: []provider.Instance{{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning}},
			passes:    3, wantDestroy: 0, wantGen: 3,
		},
		{
			name:   "absent_active_vanishes_on_second_observation",
			edit:   ready(3, "9"),
			passes: 2, wantDestroy: 0, wantGen: 3,
			wantEvents: []Event{{Kind: EvVanished, Gen: 3}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, prov, _, _, _ := newTestController(t)
			for _, inst := range tc.instances {
				prov.AddInstance(inst)
			}
			putDoc(t, c, tc.edit)
			for i := 0; i < tc.passes; i++ {
				c.reconcileCompareInstances(ctx, loadDoc(t, c), mustList(t, prov))
			}
			if tc.wantDestroy > 0 {
				waitFor(t, "orphan destroy", func() bool { return prov.Calls("destroy") >= tc.wantDestroy })
			} else {
				time.Sleep(20 * time.Millisecond)
			}
			if got := prov.Calls("destroy"); got != tc.wantDestroy {
				t.Fatalf("Calls(destroy) = %d, want %d", got, tc.wantDestroy)
			}
			if got := loadDoc(t, c).Generation; got != tc.wantGen {
				t.Fatalf("stored generation = %d, want %d", got, tc.wantGen)
			}
			got := queuedEvents(c)
			if len(got) != len(tc.wantEvents) {
				t.Fatalf("events = %+v, want %+v", got, tc.wantEvents)
			}
			for i := range got {
				if got[i] != tc.wantEvents[i] {
					t.Fatalf("event[%d] = %+v, want %+v", i, got[i], tc.wantEvents[i])
				}
			}
		})
	}
}

func itoa(n int64) string { return providerIDString(provider.InstanceID(n)) }

// One absence for gen 3 and one for gen 4 are not two observations of the same instance.
func TestReconcileVanishCounterResetsOnLabelChange(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	doc := loadDoc(t, c)
	doc.Generation = 3
	doc.Instance = &store.Instance{Label: "vkllm:iid:3", ProviderID: "9", State: Ready.String()}
	c.reconcileCompareInstances(context.Background(), doc, nil)
	doc.Generation = 4
	doc.Instance = &store.Instance{Label: "vkllm:iid:4", ProviderID: "10", State: Booting.String()}
	c.reconcileCompareInstances(context.Background(), doc, nil)
	if got := queuedEvents(c); len(got) != 0 {
		t.Fatalf("events = %+v, want none", got)
	}
}

// Drives the real decide path: a Vanished stamped with the wrong Gen would be dropped as stale and leave Ready.
func TestReconcileVanishTakesReadyToIdleThroughLoop(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	prov.SetBalance(100_000_000)
	c.state, c.generation = Ready, 3
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{Label: "vkllm:iid:3", ProviderID: "9", State: Ready.String()}
	})
	runLoop(t, c)

	c.armTimer(timerReconcile, clk.Now())
	waitFor(t, "first absent observation", func() bool {
		c.reconcileMu.Lock()
		defer c.reconcileMu.Unlock()
		return c.activeObserved == 1 && !c.reconciling.Load()
	})
	if s := c.stateNow(); s != Ready {
		t.Fatalf("state after one absent observation = %v, want Ready", s)
	}
	clk.Advance(c.deps.Config.ReconcileInterval)
	// The nudge covers a loop that parked on After only after the Advance.
	waitFor(t, "Idle after vanish", func() bool { c.signalWake(); return c.stateNow() == Idle })

	doc := loadDoc(t, c)
	if doc.Instance != nil || doc.Desired != store.DesiredStopped {
		t.Fatalf("stored instance = %+v desired = %q, want nil, STOPPED", doc.Instance, doc.Desired)
	}
	if got := prov.Calls("destroy"); got != 0 {
		t.Fatalf("Calls(destroy) = %d, want 0 for a vanished instance", got)
	}
}

// Saga step 4's zero-match outcome: two empty lists while Creating with no id record the create as lost.
func TestReconcileCreateLostThroughDecide(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetBalance(100_000_000)
	c.state, c.generation = Creating, 4
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 4
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{Label: "vkllm:iid:4", State: Creating.String()}
		d.Ops = []store.Op{{Key: "create-4"}}
	})
	ctx := context.Background()
	c.reconcileOnce(ctx)
	c.reconcileOnce(ctx)
	for c.step(ctx) {
	}
	if s := c.stateNow(); s != Idle {
		t.Fatalf("state = %v, want Idle", s)
	}
	doc := loadDoc(t, c)
	if doc.Instance != nil || len(doc.Ops) != 1 || doc.Ops[0].Outcome != "lost" {
		t.Fatalf("instance = %+v ops = %+v, want nil and create-4 lost", doc.Instance, doc.Ops)
	}
}

// Start's budget gate reads the balance only reconcile writes; a failed read keeps the last good value.
func TestReconcileCachesBalanceForStartGate(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.lastBalance, c.lastBalanceKnown = 0, false
	ctx := context.Background()

	if _, err := c.Start(ctx); !errors.Is(err, ErrGPUUnavailable) {
		t.Fatalf("Start before any balance read: err = %v, want ErrGPUUnavailable", err)
	}
	prov.SetBalance(100_000_000)
	c.reconcileOnce(ctx)
	prov.SetBalance(1)
	prov.FailNext("balance", 1, errors.New("balance down"))
	c.reconcileOnce(ctx)

	c.balanceMu.Lock()
	balance, known := c.lastBalance, c.lastBalanceKnown
	c.balanceMu.Unlock()
	if !known || balance != 100_000_000 {
		t.Fatalf("cached balance = %v known=%v, want 100_000_000 true", balance, known)
	}
	for _, e := range queuedEvents(c) {
		if e.Kind == EvBudgetExhausted {
			t.Fatalf("budget event %+v after a failed balance read with a good cached value", e)
		}
	}
	if _, err := c.Start(ctx); err != nil {
		t.Fatalf("Start after reconcile cached the balance: %v", err)
	}
}

// A list outage blocks destroy decisions only; the Tick still carries the time rules.
func TestReconcileListErrorStillTicks(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetBalance(100_000_000)
	putDoc(t, c, func(d *store.Document) { d.Generation = 3 })
	prov.FailNext("list", 1, errors.New("list down"))
	c.reconcileOnce(context.Background())
	got := queuedEvents(c)
	if len(got) != 1 || got[0].Kind != EvTick || got[0].Gen != 3 {
		t.Fatalf("events = %+v, want one EvTick gen=3", got)
	}
}

func TestReconcileBudgetExhaustedCarriesGen(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetBalance(100_000_000)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning})
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{Label: "vkllm:iid:3", ProviderID: "9", State: Ready.String()}
		d.Ledger = store.Ledger{Month: "2026-11", TotalUSD: 30}
	})
	c.reconcileOnce(context.Background())
	got := queuedEvents(c)
	if len(got) != 2 || got[1] != (Event{Kind: EvBudgetExhausted, Gen: 3, Reason: "budget_exhausted"}) {
		t.Fatalf("events = %+v, want Tick then BudgetExhausted gen=3", got)
	}
}

func drainSteps(c *Controller) {
	for c.step(context.Background()) {
	}
}

// One transient terminal status must not destroy a serving GPU; only a second consecutive one does.
func TestReconcileTransientTerminalStatusDoesNotDestroy(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetBalance(100_000_000)
	c.state, c.generation = Ready, 3
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{Label: "vkllm:iid:3", ProviderID: "9", State: Ready.String()}
	})
	ctx := context.Background()
	observe := func(s provider.InstanceStatus) {
		prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: s})
		c.reconcileOnce(ctx)
		drainSteps(c)
	}
	for _, s := range []provider.InstanceStatus{provider.StatusOffline, provider.StatusRunning, provider.StatusOffline} {
		observe(s)
		if st := c.stateNow(); st != Ready {
			t.Fatalf("state after %v = %v, want Ready", s, st)
		}
	}
	if d := loadDoc(t, c); d.Desired != store.DesiredRunning {
		t.Fatalf("desired = %q, want RUNNING after non-consecutive terminal observations", d.Desired)
	}
	observe(provider.StatusOffline)
	if st := c.stateNow(); st != Destroying && st != Idle {
		t.Fatalf("state after two consecutive offline = %v, want Destroying", st)
	}
	waitFor(t, "destroy", func() bool { return prov.Calls("destroy") >= 1 })
}

// The startup poll has the same rule: a single exited get during Booting is not a destroy decision.
func TestPollTransientTerminalStatusDoesNotDestroy(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.state, c.generation = Booting, 3
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	prov.SetStatusSequence(9, provider.StatusExited, provider.StatusRunning, provider.StatusExited)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{Label: "vkllm:iid:3", ProviderID: "9", State: Booting.String()}
		d.Ops = []store.Op{{Key: "create-3", Outcome: "created"}}
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		dispatchGet(ctx, c, effGet{id: 9, gen: 3})
		drainSteps(c)
		if st := c.stateNow(); st != Booting {
			t.Fatalf("state after get %d = %v, want Booting", i+1, st)
		}
	}
	if got := prov.Calls("destroy"); got != 0 {
		t.Fatalf("Calls(destroy) = %d, want 0", got)
	}
	dispatchGet(ctx, c, effGet{id: 9, gen: 3})
	drainSteps(c)
	if st := c.stateNow(); st != Destroying && st != Idle {
		t.Fatalf("state after two consecutive exited = %v, want Destroying", st)
	}
	waitFor(t, "destroy", func() bool { return prov.Calls("destroy") >= 1 })
}

// A balance never read since start must not drain a live instance; Start's own gate still denies.
func TestReconcileUnknownBalanceDoesNotDrainReady(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.lastBalance, c.lastBalanceKnown = 0, false
	prov.FailNext("balance", 1, errors.New("balance down"))
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning})
	c.state, c.generation = Ready, 3
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{Label: "vkllm:iid:3", ProviderID: "9", State: Ready.String()}
	})
	c.reconcileOnce(context.Background())
	drainSteps(c)
	if st := c.stateNow(); st != Ready {
		t.Fatalf("state = %v, want Ready", st)
	}
	if d := loadDoc(t, c); d.Desired != store.DesiredRunning {
		t.Fatalf("desired = %q, want RUNNING", d.Desired)
	}
}

// A count taken while an id was ahead of the fence must not carry over once that id has been active.
func TestReconcileOrphanCountClearedWhileActive(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.AddInstance(provider.Instance{ID: 7, Label: "vkllm:iid:5", Status: provider.StatusRunning})
	ctx := context.Background()
	putDoc(t, c, func(d *store.Document) { d.Generation = 3 })
	c.reconcileCompareInstances(ctx, loadDoc(t, c), mustList(t, prov)) // ahead: counted, fence to 5
	putDoc(t, c, func(d *store.Document) {
		d.Instance = &store.Instance{Label: "vkllm:iid:5", ProviderID: "7", State: Ready.String()}
	})
	c.reconcileCompareInstances(ctx, loadDoc(t, c), mustList(t, prov)) // active
	putDoc(t, c, func(d *store.Document) { d.Instance = nil })
	c.reconcileCompareInstances(ctx, loadDoc(t, c), mustList(t, prov)) // first true orphan observation
	time.Sleep(20 * time.Millisecond)
	if got := prov.Calls("destroy"); got != 0 {
		t.Fatalf("Calls(destroy) = %d after one orphan observation, want 0", got)
	}
	c.reconcileCompareInstances(ctx, loadDoc(t, c), mustList(t, prov))
	waitFor(t, "orphan destroy", func() bool { return prov.Calls("destroy") == 1 })
}
