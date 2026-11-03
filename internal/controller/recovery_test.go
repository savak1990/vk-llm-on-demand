package controller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

func TestRecoveryEmptyStoreStartsIdle(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	if err := c.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if c.state != Idle {
		t.Fatalf("state = %v, want Idle", c.state)
	}
	if k, ok := c.dueTimer(); !ok || k != timerReconcile {
		t.Fatalf("dueTimer = %v, %v, want timerReconcile armed at once", k, ok)
	}
}

func TestRecoveryResumesAtBootingForCreatingWithID(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	c.deps.Config.StartupTimeout = 15 * time.Minute
	doc, _ := store.NewDocument("iid")
	doc.Generation = 4
	doc.Desired = store.DesiredRunning
	doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:4", ProviderID: "9", CreatedAt: clk.Now()}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	if err := c.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if c.state != Booting {
		t.Fatalf("state = %v, want Booting", c.state)
	}
	if _, ok := c.timers[timerStartup]; !ok {
		t.Fatal("timerStartup must be armed from the persisted createdAt")
	}
	if got, _, _ := c.deps.Store.Load(context.Background()); got.Instance.State != Booting.String() {
		t.Fatalf("stored state = %q, want Booting so T12/T13 can commit", got.Instance.State)
	}
}

func TestRecoveryCreatingWithoutIDStaysCreating(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	doc, _ := store.NewDocument("iid")
	doc.Generation = 4
	doc.Desired = store.DesiredRunning
	doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:4", CreatedAt: clk.Now()}
	doc.Ops = []store.Op{{Key: createOpKey(4), StartedAt: clk.Now()}}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	if err := c.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if c.state != Creating {
		t.Fatalf("state = %v, want Creating (T8 adopts only from Creating)", c.state)
	}
}

func TestRecoveryDrainsAtOnceWhenDeadlineAlreadyPassed(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	doc, _ := store.NewDocument("iid")
	doc.Generation = 3
	doc.Desired = store.DesiredRunning
	past := clk.Now().Add(-time.Second)
	readyAt := clk.Now().Add(-time.Hour)
	doc.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ReadyAt: &readyAt, IdleDeadline: &past}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	if err := c.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if c.state != Destroying {
		t.Fatalf("state = %v, want Destroying (deadline already passed, no dial)", c.state)
	}
	if k, ok := c.dueTimer(); !ok || k != timerDestroyRetry {
		t.Fatalf("dueTimer = %v, %v, want timerDestroyRetry", k, ok)
	}
	if got, _, _ := c.deps.Store.Load(context.Background()); got.Desired != store.DesiredStopped {
		t.Fatalf("desired = %q, want STOPPED so the destroy is not followed by a new search", got.Desired)
	}
}

func TestRecoveryStaysReadyWhenDeadlineNotPassed(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	doc, _ := store.NewDocument("iid")
	doc.Generation = 3
	doc.Desired = store.DesiredRunning
	readyAt := clk.Now()
	future := clk.Now().Add(time.Hour)
	doc.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ReadyAt: &readyAt, IdleDeadline: &future}
	c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

	if err := c.recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if c.state != Ready {
		t.Fatalf("state = %v, want Ready", c.state)
	}
	if k, due := c.timers[timerIdle]; !due || !k.Equal(future) {
		t.Fatalf("timerIdle = %v, %v, want armed at %v", k, due, future)
	}
}

func TestRecoveryDrainingAndDestroyingResumeAtDestroying(t *testing.T) {
	for _, s := range []State{Draining, Destroying} {
		c, _, _, _, _ := newTestController(t)
		doc, _ := store.NewDocument("iid")
		doc.Generation = 2
		doc.Instance = &store.Instance{State: s.String(), Label: "vkllm:iid:2"}
		c.deps.Store.(interface{ Put(store.Document) }).Put(doc)

		if err := c.recover(context.Background()); err != nil {
			t.Fatalf("recover(%v): %v", s, err)
		}
		if c.state != Destroying {
			t.Fatalf("recover(%v): state = %v, want Destroying", s, c.state)
		}
	}
}

func TestRecoveryInstallationMismatchStopsRecovery(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.deps.Store = mismatchedInstallationStore{c.deps.Store}
	if err := c.recover(context.Background()); !errors.Is(err, store.ErrInstallation) {
		t.Fatalf("recover: want ErrInstallation, got %v", err)
	}
}

type mismatchedInstallationStore struct{ store.Store }

func (mismatchedInstallationStore) Load(context.Context) (store.Document, store.Version, error) {
	return store.Document{}, "", store.ErrInstallation
}

// A failed Load is retried every 5s until it succeeds; nothing is mapped before that.
func TestRecoveryRetriesLoadEvery5s(t *testing.T) {
	c, _, clk, _, _ := newTestController(t)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 2
		d.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:2"}
	})
	c.deps.Store.(interface{ FailNext(int, error) }).FailNext(1, store.ErrStoreMissing)

	done := make(chan error, 1)
	go func() { done <- c.recover(context.Background()) }()
	clk.BlockUntil(1)
	if s := c.stateNow(); s != Idle {
		t.Fatalf("state before a successful Load = %v, want Idle", s)
	}
	clk.Advance(5 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("recover: %v", err)
	}
	if s := c.stateNow(); s != Destroying {
		t.Fatalf("state = %v, want Destroying", s)
	}
}

func TestRecoveryStoreMissingStopsOnCancel(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	c.deps.Store = missingStore{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.recover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("recover: want context.Canceled, got %v", err)
	}
	if _, ok := c.dueTimer(); ok {
		t.Fatal("no timer may be armed before a Load succeeds")
	}
}

type missingStore struct{}

func (missingStore) Load(context.Context) (store.Document, store.Version, error) {
	return store.Document{}, "", store.ErrStoreMissing
}
func (missingStore) Save(context.Context, store.Document, store.Version) (store.Version, error) {
	return "", store.ErrStoreMissing
}

type recordDialer struct {
	mu  sync.Mutex
	got []tunnel.Instance
	err error
}

func (d *recordDialer) Dial(_ context.Context, in tunnel.Instance) (*tunnel.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, in)
	if d.err != nil {
		return nil, d.err
	}
	return tunnel.NewConn(nil, func() error { return nil }, in.HostKey), nil
}

func (d *recordDialer) dials() []tunnel.Instance {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]tunnel.Instance(nil), d.got...)
}

// The real loop must destroy the GPU and then stay stopped: a recovered idle
// deadline is a stop, not a cue to provision again.
func TestRunRecoveryPastDeadlineDestroysAndStaysStopped(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	dialer := &recordDialer{}
	c.deps.Dialer = dialer
	prov.SetBalance(100_000_000)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning, SSHHost: "h", SSHPort: 22})
	past := clk.Now().Add(-time.Second)
	readyAt := clk.Now().Add(-time.Hour)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9", HostKeyFingerprint: "fp", ReadyAt: &readyAt, IdleDeadline: &past}
		d.Ops = []store.Op{{Key: createOpKey(3), Outcome: "ready"}}
	})

	runLoop(t, c)
	waitFor(t, "instance destroyed", func() bool { return c.stateNow() == Idle && loadDoc(t, c).Instance == nil })
	if _, err := prov.GetInstance(context.Background(), 9); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("GetInstance(9) = %v, want ErrNotFound", err)
	}
	for i := 0; i < 3; i++ {
		clk.Advance(c.deps.Config.ReconcileInterval)
		time.Sleep(5 * time.Millisecond)
	}
	if got := loadDoc(t, c).Desired; got != store.DesiredStopped {
		t.Fatalf("desired = %q, want STOPPED", got)
	}
	if n := prov.Calls("search"); n != 0 {
		t.Fatalf("Calls(search) = %d, want 0 after an idle-expired recovery", n)
	}
	if d := dialer.dials(); len(d) != 0 {
		t.Fatalf("dials = %v, want none for an expired instance", d)
	}
}

// A Ready instance is re-adopted through the real loop: dialed at the pinned
// host key, probed, and left Ready with no write.
func TestRunRecoveryReadoptsReadyWithPinnedHostKey(t *testing.T) {
	c, prov, clk, _, worker := newTestController(t)
	dialer := &recordDialer{}
	c.deps.Dialer = dialer
	worker.SetProbeSequence(ProbeReady)
	prov.SetBalance(100_000_000)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning, SSHHost: "h", SSHPort: 2222})
	readyAt := clk.Now()
	future := clk.Now().Add(30 * time.Minute)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9", HostKeyFingerprint: "fp", ReadyAt: &readyAt, IdleDeadline: &future}
	})
	before := loadDoc(t, c)

	runLoop(t, c)
	waitFor(t, "conn installed", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.conn != nil && !c.suspect
	})
	d := dialer.dials()
	if len(d) != 1 || d[0] != (tunnel.Instance{Host: "h", Port: 2222, HostKey: "fp"}) {
		t.Fatalf("dials = %+v, want one to h:2222 pinned at fp", d)
	}
	if s := c.stateNow(); s != Ready {
		t.Fatalf("state = %v, want Ready", s)
	}
	if after := loadDoc(t, c); after.Instance == nil || after.Instance.State != Ready.String() || !after.Instance.IdleDeadline.Equal(*before.Instance.IdleDeadline) {
		t.Fatalf("stored instance = %+v, want unchanged Ready", after.Instance)
	}
	if n := prov.Calls("destroy"); n != 0 {
		t.Fatalf("Calls(destroy) = %d, want 0", n)
	}
}

// A restart mid-model-load resumes at Booting and must progress to Ready
// through the normal saga, not stall until the startup deadline.
func TestRunRecoveryLoadingResumesToReady(t *testing.T) {
	c, prov, clk, _, worker := newTestController(t)
	c.deps.Config.StartupTimeout = 15 * time.Minute
	dialer := &recordDialer{}
	c.deps.Dialer = dialer
	worker.SetProbeSequence(ProbeReady)
	prov.SetBalance(100_000_000)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning, SSHHost: "h", SSHPort: 22})
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Loading.String(), Label: "vkllm:iid:3", ProviderID: "9", HostKeyFingerprint: "fp", CreatedAt: clk.Now()}
		d.Ops = []store.Op{{Key: createOpKey(3), Outcome: "created"}}
	})

	runLoop(t, c)
	for i := 0; i < 100 && c.stateNow() != Ready; i++ {
		clk.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if s := c.stateNow(); s != Ready {
		t.Fatalf("state = %v, want Ready", s)
	}
	if got := loadDoc(t, c).Instance.State; got != Ready.String() {
		t.Fatalf("stored state = %q, want Ready", got)
	}
	if d := dialer.dials(); len(d) == 0 || d[0].HostKey != "fp" {
		t.Fatalf("dials = %+v, want the pinned host key", d)
	}
}

// failNthLoad fails exactly the nth Load and passes every other call through.
type failNthLoad struct {
	store.Store
	mu      sync.Mutex
	n, fail int
}

func (s *failNthLoad) Load(ctx context.Context) (store.Document, store.Version, error) {
	s.mu.Lock()
	s.n++
	hit := s.n == s.fail
	s.mu.Unlock()
	if hit {
		return store.Document{}, "", errors.New("transient load failure")
	}
	return s.Store.Load(ctx)
}

// No Ready row ever drains a desired=STOPPED instance, so a stop-write lost
// during recovery must rerun recovery rather than strand the GPU.
func TestRunRecoveryStoppedReadyRetriesFailedStop(t *testing.T) {
	c, prov, clk, _, _ := newTestController(t)
	c.deps.Config.IdleShutdownEnabled = false
	prov.SetBalance(100_000_000)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning, SSHHost: "h", SSHPort: 22})
	readyAt := clk.Now()
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredStopped
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9", ReadyAt: &readyAt}
	})
	c.deps.Store = &failNthLoad{Store: c.deps.Store, fail: 2} // recoverOnce's Load passes, the stop's own Load fails

	runLoop(t, c)
	clk.BlockUntil(1)
	clk.Advance(recoverRetry)
	waitFor(t, "instance destroyed", func() bool { return c.stateNow() == Idle && loadDoc(t, c).Instance == nil })
	if _, err := prov.GetInstance(context.Background(), 9); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("GetInstance(9) = %v, want ErrNotFound", err)
	}
	if d := c.deps.Dialer.(*recordDialer).dials(); len(d) != 0 {
		t.Fatalf("dials = %v, want none for a stopped instance", d)
	}
}

func runRecoveredReady(t *testing.T, seq ...ProbeStatus) (*Controller, *fakeprovider.Fake, *fakeclock.Clock, *fakeWorker) {
	t.Helper()
	c, prov, clk, _, worker := newTestController(t)
	worker.SetProbeSequence(seq...)
	prov.SetBalance(100_000_000)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3", Status: provider.StatusRunning, SSHHost: "h", SSHPort: 22})
	readyAt := clk.Now()
	future := clk.Now().Add(30 * time.Minute)
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9", HostKeyFingerprint: "fp", ReadyAt: &readyAt, IdleDeadline: &future}
	})
	runLoop(t, c)
	waitFor(t, "first recovery probe", func() bool { return worker.probes() >= 1 })
	return c, prov, clk, worker
}

func TestRunRecoveryOneProbeMissKeepsReady(t *testing.T) {
	c, prov, clk, worker := runRecoveredReady(t, ProbeUnreachable, ProbeReady)
	for i := 0; i < 20 && worker.probes() < 2; i++ {
		clk.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	if n := prov.Calls("destroy"); n != 0 {
		t.Fatalf("Calls(destroy) = %d, want 0 after a single miss", n)
	}
	if s := c.stateNow(); s != Ready {
		t.Fatalf("state = %v, want Ready after a single miss", s)
	}
	if n := worker.probes(); n < 2 {
		t.Fatalf("probes = %d, want the 5s follow-up probe", n)
	}
}

func TestRunRecoveryTwoProbeMissesDestroy(t *testing.T) {
	c, prov, clk, _ := runRecoveredReady(t, ProbeUnreachable, ProbeUnreachable)
	for i := 0; i < 20 && prov.Calls("destroy") == 0; i++ {
		clk.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	waitFor(t, "instance destroyed", func() bool { return c.stateNow() == Idle && loadDoc(t, c).Instance == nil })
	if _, err := prov.GetInstance(context.Background(), 9); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("GetInstance(9) = %v, want ErrNotFound", err)
	}
}
