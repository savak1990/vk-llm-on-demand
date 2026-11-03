package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

func TestStartCoalesces(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	ctx := context.Background()
	go func() {
		for c.step(ctx) {
		}
	}()

	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func() {
			if _, err := c.Start(ctx); err != nil {
				t.Errorf("Start: %v", err)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	deadline := time.Now().Add(2 * time.Second)
	for prov.Calls("search") == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		for c.step(ctx) {
		}
	}
	if got := prov.Calls("search"); got != 1 {
		t.Fatalf("Calls(search) = %d, want 1", got)
	}
}

func TestProcessEventQueuesEffectsOnlyAfterCommit(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.Store = failOnceStore{inner: c.deps.Store, failSave: true}
	ctx := context.Background()
	if _, err := c.Start(ctx); err == nil {
		t.Fatal("Start: want an error when the CAS commit itself fails")
	}
	for c.step(ctx) {
	}
	if got := prov.Calls("search"); got != 0 {
		t.Fatalf("Calls(search) = %d, want 0 — no effect may run if its CAS never committed", got)
	}
}

type failOnceStore struct {
	inner    store.Store
	failSave bool
}

func (f failOnceStore) Load(ctx context.Context) (store.Document, store.Version, error) {
	return f.inner.Load(ctx)
}

func (f failOnceStore) Save(ctx context.Context, d store.Document, v store.Version) (store.Version, error) {
	if f.failSave {
		return "", context.DeadlineExceeded
	}
	return f.inner.Save(ctx, d, v)
}

func TestNextDueAndDueTimerPriorityOrder(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	now := clk.Now()
	c.armTimer(timerPoll, now)
	c.armTimer(timerIdle, now)
	c.armTimer(timerReconcile, now)
	k, ok := c.dueTimer()
	if !ok || k != timerIdle {
		t.Fatalf("dueTimer = %v, %v, want timerIdle first (deadlines before reconcile before poll)", k, ok)
	}
}

func TestNextDueIsEarliestArmed(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	now := clk.Now()
	c.armTimer(timerReconcile, now.Add(time.Minute))
	c.armTimer(timerPoll, now.Add(5*time.Second))
	if got := c.nextDue(); !got.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("nextDue = %v, want %v", got, now.Add(5*time.Second))
	}
}

func TestEnteringDestroyingArmsRetryAtOnce(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.state = Ready
	c.generation = 3
	ctx := context.Background()
	doc, _, _ := c.deps.Store.Load(ctx)
	doc.Generation = 3
	doc.Desired = store.DesiredRunning
	doc.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3"}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	if err := c.processEvent(ctx, Event{Kind: EvWorkerFailed, Gen: 3}); err != nil {
		t.Fatalf("processEvent: %v", err)
	}
	k, ok := c.dueTimer()
	if !ok || k != timerDestroyRetry {
		t.Fatalf("dueTimer after entering Destroying = %v, %v, want timerDestroyRetry due at once", k, ok)
	}
}

func TestLeavingConnHoldingStateClosesConn(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.state = Ready
	c.generation = 3
	closed := make(chan struct{}, 1)
	c.conn = tunnelConnForTest(func() error { closed <- struct{}{}; return nil })
	ctx := context.Background()
	doc, _, _ := c.deps.Store.Load(ctx)
	doc.Generation = 3
	doc.Desired = store.DesiredRunning
	doc.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3"}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	if err := c.processEvent(ctx, Event{Kind: EvWorkerFailed, Gen: 3}); err != nil {
		t.Fatalf("processEvent: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Conn.Close was never called after leaving Ready")
	}
}

func tunnelConnForTest(closer func() error) *tunnel.Conn {
	return tunnel.NewConn(nil, closer, "")
}

func putDoc(t *testing.T, c *Controller, edit func(*store.Document)) {
	t.Helper()
	doc, _, err := c.deps.Store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	edit(&doc)
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)
}

func timerAt(c *Controller, k timerKind) (time.Time, bool) {
	c.timersMu.Lock()
	defer c.timersMu.Unlock()
	at, ok := c.timers[k]
	return at, ok
}

// T5 is the one row that moves the generation; per-generation memory must not survive it.
func TestGenerationChangeResetsInFlightAndSuspect(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	c.deps.Config.StartupTimeout = 15 * time.Minute
	c.state = Searching
	c.inFlight = 5
	c.suspect = true
	putDoc(t, c, func(d *store.Document) { d.Desired = store.DesiredRunning })

	if err := c.processEvent(context.Background(), Event{Kind: EvOfferChosen, Offer: provider.Offer{ID: 11, MachineID: 42, Hourly: 600000}}); err != nil {
		t.Fatalf("processEvent: %v", err)
	}
	if ev, ok := recvEvent(t, c); !ok || ev.Kind != EvCreated {
		t.Fatalf("create effect: event = %+v, ok=%v", ev, ok)
	}
	c.mu.Lock()
	gen, inFlight, suspect, state := c.generation, c.inFlight, c.suspect, c.state
	c.mu.Unlock()
	if gen != 1 || inFlight != 0 || suspect || state != Creating {
		t.Fatalf("after T5: generation=%d inFlight=%d suspect=%v state=%v, want 1, 0, false, Creating", gen, inFlight, suspect, state)
	}
	if at, ok := timerAt(c, timerPoll); !ok || !at.Equal(clk.Now().Add(5*time.Second)) {
		t.Fatalf("timerPoll = %v, %v, want armed 5s after entering Creating", at, ok)
	}
	if at, ok := timerAt(c, timerStartup); !ok || !at.Equal(clk.Now().Add(15*time.Minute)) {
		t.Fatalf("timerStartup = %v, %v, want createdAt+startupTimeout", at, ok)
	}
}

func TestSameGenerationKeepsInFlight(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.state = Ready
	c.generation = 3
	c.inFlight = 2
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3"}
	})
	if err := c.processEvent(context.Background(), Event{Kind: EvStop}); err != nil {
		t.Fatalf("processEvent: %v", err)
	}
	if c.inFlight != 2 || c.state != Draining {
		t.Fatalf("inFlight=%d state=%v, want 2, Draining", c.inFlight, c.state)
	}
}

// Startup and drain both fire EvDeadlineExpired, so a startup timer left
// armed into Draining would end the drain early.
func TestDeadlineTimersFollowStateClass(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	c.deps.Config.ShutdownDrainTimeout = 60 * time.Second
	c.deps.Config.IdleShutdownEnabled = true
	c.state = Loading
	c.generation = 3
	c.armTimer(timerStartup, clk.Now().Add(time.Hour))
	c.armTimer(timerPoll, clk.Now().Add(5*time.Second))
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Loading.String(), Label: "vkllm:iid:3", CreatedAt: clk.Now()}
		d.Ops = append(d.Ops, store.Op{Key: createOpKey(3), StartedAt: clk.Now()})
	})
	ctx := context.Background()

	if err := c.processEvent(ctx, Event{Kind: EvHealthOK, Gen: 3}); err != nil {
		t.Fatalf("HealthOK: %v", err)
	}
	if _, ok := timerAt(c, timerStartup); ok {
		t.Fatal("timerStartup still armed after reaching Ready")
	}
	if at, ok := timerAt(c, timerIdle); !ok || !at.Equal(clk.Now().Add(30*time.Minute)) {
		t.Fatalf("timerIdle = %v, %v, want readyAt+30m", at, ok)
	}
	if _, ok := timerAt(c, timerMetricsPull); !ok {
		t.Fatal("timerMetricsPull not armed on entering Ready")
	}

	c.inFlight = 1
	if err := c.processEvent(ctx, Event{Kind: EvStop}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if at, ok := timerAt(c, timerDrain); !ok || !at.Equal(clk.Now().Add(60*time.Second)) {
		t.Fatalf("timerDrain = %v, %v, want drain start+60s", at, ok)
	}
	if _, ok := timerAt(c, timerIdle); ok {
		t.Fatal("timerIdle still armed after leaving Ready")
	}

	clk.Advance(60 * time.Second)
	if !c.step(ctx) || c.state != Destroying {
		t.Fatalf("after drain deadline: state=%v, want Destroying", c.state)
	}
	if _, ok := timerAt(c, timerDrain); ok {
		t.Fatal("timerDrain still armed after leaving Draining")
	}
}

// A decision that keeps the state must not overwrite a concurrent
// Admit's Ready->Serving flip with the state it decided from.
func TestNoOpDecisionKeepsConcurrentServing(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.state = Ready
	c.generation = 3
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3"}
	})
	c.deps.Store = hookStore{Store: c.deps.Store, onLoad: func() {
		c.mu.Lock()
		c.state = Serving
		c.mu.Unlock()
	}}
	if err := c.processEvent(context.Background(), Event{Kind: EvStart}); err != nil {
		t.Fatalf("processEvent: %v", err)
	}
	if c.state != Serving {
		t.Fatalf("state = %v, want Serving kept", c.state)
	}
}

type hookStore struct {
	store.Store
	onLoad func()
}

func (h hookStore) Load(ctx context.Context) (store.Document, store.Version, error) {
	d, v, err := h.Store.Load(ctx)
	h.onLoad()
	return d, v, err
}

// staleOnceStore hands Transition's first Load a document that already
// left Idle, so the caller's first attempt hits errStale.
type staleOnceStore struct {
	store.Store
	mu    sync.Mutex
	loads int
}

func (s *staleOnceStore) Load(ctx context.Context) (store.Document, store.Version, error) {
	d, v, err := s.Store.Load(ctx)
	s.mu.Lock()
	s.loads++
	n := s.loads
	s.mu.Unlock()
	if n == 2 && err == nil {
		d.Instance = &store.Instance{State: Creating.String()}
	}
	return d, v, err
}

func TestStopRetriesOnStale(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	s := &staleOnceStore{Store: c.deps.Store}
	c.deps.Store = s
	if _, err := c.Stop(context.Background(), false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if s.loads != 4 {
		t.Fatalf("Load calls = %d, want 4 (stale attempt, then a clean retry)", s.loads)
	}
}

func TestStopForceRejectedWhenDisabled(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	if _, err := c.Stop(context.Background(), true); !errors.Is(err, ErrForceNotEnabled) {
		t.Fatalf("Stop(force) err = %v, want ErrForceNotEnabled", err)
	}
}

// A failed read must re-arm the retry; posting Gone would drop the
// instance record without the provider ever confirming absence.
func TestDestroyRetryOnLoadErrorDoesNotPostGone(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	c.state = Destroying
	c.generation = 3
	c.deps.Store.(interface{ FailNext(int, error) }).FailNext(1, errors.New("load failed"))
	c.armTimer(timerDestroyRetry, clk.Now())
	if !c.step(context.Background()) {
		t.Fatal("step: destroy retry not fired")
	}
	if c.state != Destroying {
		t.Fatalf("state = %v, want Destroying", c.state)
	}
	if at, ok := timerAt(c, timerDestroyRetry); !ok || !at.After(clk.Now()) {
		t.Fatalf("timerDestroyRetry = %v, %v, want re-armed with backoff", at, ok)
	}
}

func TestDestroyRetryConfirmsGone(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	c.state = Destroying
	c.generation = 3
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:3", ProviderID: "9"}
	})
	c.armTimer(timerDestroyRetry, clk.Now())
	ctx := context.Background()
	c.step(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for c.state != Idle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		c.step(ctx)
	}
	if c.state != Idle || prov.Calls("destroy") != 1 {
		t.Fatalf("state=%v destroy calls=%d, want Idle, 1", c.state, prov.Calls("destroy"))
	}
}

func runLoop(t *testing.T, c *Controller) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *Controller) stateNow() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// A parked Run must notice a timer armed from a caller goroutine.
func TestRunWakesForStopFromBooting(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.state = Booting
	c.generation = 3
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Booting.String(), Label: "vkllm:iid:3", ProviderID: "9"}
		d.Ops = append(d.Ops, store.Op{Key: createOpKey(3)})
	})
	runLoop(t, c)
	if _, err := c.Stop(context.Background(), false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, "Idle after destroy", func() bool { return c.stateNow() == Idle })
	if got := prov.Calls("destroy"); got != 1 {
		t.Fatalf("Calls(destroy) = %d, want 1", got)
	}
}

// With nothing in flight a drain has nothing to wait for; no clock advance here.
func TestRunStopAtReadyIdleDestroysWithoutDrainWait(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	c.deps.Config.ShutdownDrainTimeout = 60 * time.Second
	c.state = Ready
	c.generation = 3
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9"}
	})
	runLoop(t, c)
	if _, err := c.Stop(context.Background(), false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitFor(t, "Idle after destroy", func() bool { return c.stateNow() == Idle })
	if got := prov.Calls("destroy"); got != 1 {
		t.Fatalf("Calls(destroy) = %d, want 1", got)
	}
}

func TestRequestEndLastOutOfDrainArmsDrainNow(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	c.state = Draining
	c.generation = 3
	c.inFlight = 1
	c.armTimer(timerDrain, clk.Now().Add(time.Minute))
	c.RequestEnd(3)
	if at, ok := timerAt(c, timerDrain); !ok || !at.Equal(clk.Now()) {
		t.Fatalf("timerDrain = %v, %v, want now", at, ok)
	}
}

// The backoff retry is armed from the destroy goroutine, so it must wake a
// parked loop itself; the fake clock is driven by step to stay deterministic.
func TestFailedDestroyRetryWakesLoopAndRetries(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	c.state = Destroying
	c.generation = 3
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	prov.FailDestroy(1)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:3", ProviderID: "9"}
	})
	ctx := context.Background()
	c.armTimer(timerDestroyRetry, clk.Now())
	select {
	case <-c.wake:
	default:
	}
	c.step(ctx)
	waitFor(t, "retry armed", func() bool { _, ok := timerAt(c, timerDestroyRetry); return ok })
	select {
	case <-c.wake:
	default:
		t.Fatal("arming the retry from the destroy goroutine did not wake the loop")
	}
	clk.Advance(20 * time.Second)
	waitFor(t, "Idle after retry", func() bool {
		for c.step(ctx) {
		}
		return c.stateNow() == Idle
	})
	if got := prov.Calls("destroy"); got != 2 {
		t.Fatalf("Calls(destroy) = %d, want 2", got)
	}
}

func TestStartGate(t *testing.T) {
	hour := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15")
	failed := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		edit        func(*store.Document)
		unknownBal  bool
		wantErr     error
		wantDesired string
	}{
		{"attempts cap", func(d *store.Document) { d.AttemptsThisHour = store.Attempts{Hour: hour, Count: 3} }, false, ErrGPUUnavailable, store.DesiredStopped},
		{"balance unknown", func(*store.Document) {}, true, ErrGPUUnavailable, store.DesiredStopped},
		{"budget exhausted", func(d *store.Document) { d.Ledger.TotalUSD = 30 }, false, ErrGPUUnavailable, store.DesiredStopped},
		{"create backoff", func(d *store.Document) {
			d.Ops = append(d.Ops, store.Op{Key: createOpKey(1), FinishedAt: &failed, Outcome: "create_error"})
		}, false, nil, store.DesiredRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, prov, _, _, _ := newTestController(t)
			prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
			c.lastBalanceKnown = !tc.unknownBal
			putDoc(t, c, tc.edit)
			ctx := context.Background()
			if _, err := c.Start(ctx); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Start err = %v, want %v", err, tc.wantErr)
			}
			for c.step(ctx) {
			}
			time.Sleep(10 * time.Millisecond)
			if prov.Calls("search") != 0 || prov.Calls("create") != 0 {
				t.Fatalf("search=%d create=%d, want 0, 0", prov.Calls("search"), prov.Calls("create"))
			}
			doc, _, _ := c.deps.Store.Load(ctx)
			if doc.Desired != tc.wantDesired || c.stateNow() != Idle {
				t.Fatalf("desired=%v state=%v, want %v, Idle", doc.Desired, c.stateNow(), tc.wantDesired)
			}
		})
	}
}

// With no provider id the label is the only handle; the exact-label match
// must not reach a later generation that shares the prefix.
func TestDestroyWithoutIDDestroysEveryExactLabelMatch(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	c.state = Destroying
	c.generation = 3
	prov.AddInstance(provider.Instance{ID: 7, Label: "vkllm:iid:3"})
	prov.AddInstance(provider.Instance{ID: 8, Label: "vkllm:iid:30"})
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:3"}
	})
	c.armTimer(timerDestroyRetry, clk.Now())
	ctx := context.Background()
	c.step(ctx)
	waitFor(t, "Gone", func() bool {
		for c.step(ctx) {
		}
		return c.stateNow() == Idle
	})
	if prov.Calls("list") != 1 || prov.Calls("destroy") != 1 {
		t.Fatalf("list=%d destroy=%d, want 1, 1", prov.Calls("list"), prov.Calls("destroy"))
	}
	if _, err := prov.GetInstance(ctx, 8); err != nil {
		t.Fatalf("instance 8 (gen 30) was destroyed: %v", err)
	}
}

func TestDestroyRecordsOpBeforeProviderCall(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	c.state = Destroying
	c.generation = 3
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	prov.FailDestroy(1)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:3", ProviderID: "9"}
	})
	c.armTimer(timerDestroyRetry, clk.Now())
	c.step(context.Background())
	doc, _, _ := c.deps.Store.Load(context.Background())
	op := findOp(&doc, destroyOpKey(3))
	if op == nil || op.Outcome != "" {
		t.Fatalf("destroy op = %+v, want open destroy-3 written before the call", op)
	}
}

func TestDestroyRefusedByLabelFence(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	c.state = Destroying
	c.generation = 3
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:other:3"})
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:other:3", ProviderID: "9"}
	})
	c.armTimer(timerDestroyRetry, clk.Now())
	c.step(context.Background())
	time.Sleep(10 * time.Millisecond)
	if prov.Calls("destroy") != 0 || c.stateNow() != Destroying {
		t.Fatalf("destroy=%d state=%v, want 0, Destroying", prov.Calls("destroy"), c.stateNow())
	}
	if at, ok := timerAt(c, timerDestroyRetry); !ok || !at.After(clk.Now()) {
		t.Fatalf("timerDestroyRetry = %v, %v, want re-armed", at, ok)
	}
}
