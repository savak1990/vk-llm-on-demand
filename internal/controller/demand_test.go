package controller

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func newOnDemandController(t *testing.T) (*Controller, *fakeprovider.Fake) {
	t.Helper()
	c, prov, _, _, _ := newTestController(t)
	c.deps.Config.ProvisioningMode = "on-demand"
	c.deps.Config.DemandGrace = time.Minute
	return c, prov
}

func TestDemandExplicitModeRefuses(t *testing.T) {
	c, _, _, _, _ := newTestController(t)
	if _, err := c.Demand(context.Background(), "k1", time.Now()); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("err = %v, want ErrBackendUnavailable", err)
	}
}

func TestDemandStartsOnceAndRecordsOrigin(t *testing.T) {
	c, prov := newOnDemandController(t)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	ctx := context.Background()
	until := c.deps.Clock.Now().Add(time.Minute)
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() { _, err := c.Demand(ctx, "k1", until); done <- err }()
	}
	for i := 0; i < 10; i++ {
		if err := <-done; err != nil {
			t.Fatalf("Demand: %v", err)
		}
	}
	waitFor(t, "one search", func() bool {
		for c.step(ctx) {
		}
		return prov.Calls("search") == 1
	})
	doc, _, _ := c.deps.Store.Load(ctx)
	if doc.StartOrigin != originDemand {
		t.Fatalf("StartOrigin = %q, want demand", doc.StartOrigin)
	}
	if at, ok := timerAt(c, timerDemand); !ok || !at.Equal(until) {
		t.Fatalf("timerDemand = %v,%v, want %v", at, ok, until)
	}
}

func TestDemandDeniedByAttemptsCap(t *testing.T) {
	c, prov := newOnDemandController(t)
	putDoc(t, c, func(d *store.Document) {
		d.AttemptsThisHour = store.Attempts{Hour: c.deps.Clock.Now().UTC().Format("2006-01-02T15"), Count: 3}
	})
	snap, err := c.Demand(context.Background(), "k1", c.deps.Clock.Now().Add(time.Minute))
	if !errors.Is(err, ErrGPUUnavailable) || snap.Unavailable != "attempts_exhausted" {
		t.Fatalf("err %v unavailable %q", err, snap.Unavailable)
	}
	if prov.Calls("search") != 0 {
		t.Fatal("a denied Demand must not search")
	}
}

func bootingDoc(origin string) func(*store.Document) {
	return func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.StartOrigin = origin
		d.Instance = &store.Instance{State: Booting.String(), Label: "vkllm:iid:3", ProviderID: "9"}
		d.Ops = append(d.Ops, store.Op{Key: createOpKey(3)})
	}
}

func TestExpiredDemandCancelsDemandStart(t *testing.T) {
	c, prov := newOnDemandController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	c.state, c.generation = Booting, 3
	putDoc(t, c, bootingDoc(originDemand))
	c.cancelUnattendedStart(context.Background())
	if c.stateNow() != Destroying {
		t.Fatalf("state = %v, want Destroying", c.stateNow())
	}
}

func TestExpiredDemandKeepsExplicitStart(t *testing.T) {
	c, prov := newOnDemandController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	c.state, c.generation = Booting, 3
	putDoc(t, c, bootingDoc(originExplicit))
	c.cancelUnattendedStart(context.Background())
	if c.stateNow() != Booting {
		t.Fatalf("state = %v, want Booting", c.stateNow())
	}
}

func TestExplicitStartAdoptsDemandStart(t *testing.T) {
	c, _ := newOnDemandController(t)
	c.state, c.generation = Booting, 3
	putDoc(t, c, bootingDoc(originDemand))
	if _, err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	doc, _, _ := c.deps.Store.Load(context.Background())
	if doc.StartOrigin != originExplicit {
		t.Fatalf("StartOrigin = %q, want explicit", doc.StartOrigin)
	}
	c.cancelUnattendedStart(context.Background())
	if c.stateNow() != Booting {
		t.Fatalf("state = %v, want Booting", c.stateNow())
	}
}

func TestDemandNeverMovesIdleDeadline(t *testing.T) {
	c, _ := newOnDemandController(t)
	dl := c.deps.Clock.Now().Add(10 * time.Minute)
	c.state, c.generation = Ready, 3
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredRunning
		d.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3", ProviderID: "9", IdleDeadline: &dl}
	})
	if _, err := c.Demand(context.Background(), "k1", c.deps.Clock.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Demand: %v", err)
	}
	doc, _, _ := c.deps.Store.Load(context.Background())
	if !doc.Instance.IdleDeadline.Equal(dl) {
		t.Fatalf("IdleDeadline moved to %v", doc.Instance.IdleDeadline)
	}
}

func TestDemandWhileDrainingRefuses(t *testing.T) {
	c, _ := newOnDemandController(t)
	c.state = Draining
	if _, err := c.Demand(context.Background(), "k1", c.deps.Clock.Now()); !errors.Is(err, ErrDraining) {
		t.Fatalf("err = %v, want ErrDraining", err)
	}
}

func TestRestartRearmsDemandDeadline(t *testing.T) {
	c, prov := newOnDemandController(t)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	putDoc(t, c, bootingDoc(originDemand))
	c.rearmDemandAfterRecovery(context.Background())
	at, ok := timerAt(c, timerDemand)
	if !ok || !at.Equal(c.deps.Clock.Now().Add(time.Minute)) {
		t.Fatalf("timerDemand = %v,%v, want now+grace", at, ok)
	}
}

func creatingStoppedDoc(origin string) func(*store.Document) {
	return func(d *store.Document) {
		d.Generation = 3
		d.Desired = store.DesiredStopped
		d.StartOrigin = origin
		d.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:3"}
		d.Ops = append(d.Ops, store.Op{Key: createOpKey(3)})
	}
}

func TestExplicitStartRevivingDemandStartIsKept(t *testing.T) {
	c, _ := newOnDemandController(t)
	c.state, c.generation = Creating, 3
	putDoc(t, c, creatingStoppedDoc(originDemand))
	if _, err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	c.cancelUnattendedStart(context.Background())
	doc, _, _ := c.deps.Store.Load(context.Background())
	if doc.StartOrigin != originExplicit || doc.Desired != store.DesiredRunning {
		t.Fatalf("origin %q desired %q, want explicit RUNNING", doc.StartOrigin, doc.Desired)
	}
}

func TestDemandRevivingExplicitStartIsCancellable(t *testing.T) {
	c, _ := newOnDemandController(t)
	c.state, c.generation = Creating, 3
	putDoc(t, c, creatingStoppedDoc(originExplicit))
	if _, err := c.Demand(context.Background(), "k1", c.deps.Clock.Now()); err != nil {
		t.Fatalf("Demand: %v", err)
	}
	c.cancelUnattendedStart(context.Background())
	doc, _, _ := c.deps.Store.Load(context.Background())
	if doc.StartOrigin != originDemand || doc.Desired != store.DesiredStopped {
		t.Fatalf("origin %q desired %q, want demand STOPPED", doc.StartOrigin, doc.Desired)
	}
}

func TestExpiredDemandWhileDestroyingRetriesLater(t *testing.T) {
	c, _ := newOnDemandController(t)
	c.state, c.generation = Destroying, 3
	putDoc(t, c, func(d *store.Document) {
		bootingDoc(originDemand)(d)
		d.Instance.State = Destroying.String()
	})
	c.cancelUnattendedStart(context.Background())
	at, ok := timerAt(c, timerDemand)
	if !ok || !at.Equal(c.deps.Clock.Now().Add(requeueDelay)) {
		t.Fatalf("timerDemand = %v,%v, want now+requeueDelay", at, ok)
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines returns the logged lines whose message is exactly msg.
func (b *lockedBuffer) lines(msg string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, l := range strings.Split(b.buf.String(), "\n") {
		if strings.Contains(l, "msg="+msg+" ") {
			out = append(out, l)
		}
	}
	return out
}

func captureLog(c *Controller) *lockedBuffer {
	b := &lockedBuffer{}
	c.deps.Log = slog.New(slog.NewTextHandler(b, nil))
	return b
}

func TestDemandStartLogsOnceWithKey(t *testing.T) {
	c, prov := newOnDemandController(t)
	logs := captureLog(c)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	until := c.deps.Clock.Now().Add(time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := c.Demand(context.Background(), "k1", until); err != nil {
			t.Fatalf("Demand: %v", err)
		}
	}
	got := logs.lines("gpu_start")
	if len(got) != 1 || !strings.Contains(got[0], "origin=demand") || !strings.Contains(got[0], "key_id=k1") {
		t.Fatalf("gpu_start lines = %q", got)
	}
}

func TestExplicitStartLogsExplicitOrigin(t *testing.T) {
	c, prov, _, _, _ := newTestController(t)
	logs := captureLog(c)
	prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	if _, err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := logs.lines("gpu_start")
	if len(got) != 1 || !strings.Contains(got[0], "origin=explicit") {
		t.Fatalf("gpu_start lines = %q", got)
	}
}

func TestBackoffDeferredDemandStartLogsKeyOnTick(t *testing.T) {
	c, _ := newOnDemandController(t)
	logs := captureLog(c)
	now := c.deps.Clock.Now()
	putDoc(t, c, func(d *store.Document) {
		d.Generation = 1
		d.Ops = append(d.Ops, store.Op{Key: createOpKey(1), StartedAt: now, Outcome: "failed", FinishedAt: &now})
	})
	if _, err := c.Demand(context.Background(), "k1", now.Add(time.Hour)); err != nil {
		t.Fatalf("Demand: %v", err)
	}
	if c.stateNow() != Idle || len(logs.lines("gpu_start")) != 0 {
		t.Fatalf("state %v, lines %q: want a deferred start", c.stateNow(), logs.lines("gpu_start"))
	}
	c.deps.Clock.(interface{ Advance(time.Duration) }).Advance(61 * time.Second)
	if err := c.processEvent(context.Background(), Event{Kind: EvTick}); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got := logs.lines("gpu_start")
	if c.stateNow() != Searching || len(got) != 1 || !strings.Contains(got[0], "origin=demand") || !strings.Contains(got[0], "key_id=k1") {
		t.Fatalf("state %v, gpu_start lines = %q", c.stateNow(), got)
	}
}

func TestExpiredDemandLogsCancelOnce(t *testing.T) {
	c, prov := newOnDemandController(t)
	logs := captureLog(c)
	prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:3"})
	c.state, c.generation = Booting, 3
	putDoc(t, c, bootingDoc(originDemand))
	fs := c.deps.Store.(interface{ ConflictNext(int) })
	fs.ConflictNext(1000)
	c.cancelUnattendedStart(context.Background())
	if got := logs.lines("gpu_start_cancelled"); len(got) != 0 {
		t.Fatalf("a stop that never committed logged %q", got)
	}
	fs.ConflictNext(0)
	c.cancelUnattendedStart(context.Background())
	c.cancelUnattendedStart(context.Background())
	if got := logs.lines("gpu_start_cancelled"); len(got) != 1 || c.stateNow() != Destroying {
		t.Fatalf("gpu_start_cancelled lines = %q", got)
	}
}

func TestDemandStopSkippedByOriginGuardLogsNothing(t *testing.T) {
	c, _ := newOnDemandController(t)
	logs := captureLog(c)
	c.state, c.generation = Booting, 3
	putDoc(t, c, bootingDoc(originExplicit))
	if err := c.processEvent(context.Background(), Event{Kind: EvStop, Origin: originDemand}); err != nil {
		t.Fatalf("processEvent: %v", err)
	}
	if got := logs.lines("gpu_start_cancelled"); len(got) != 0 || c.stateNow() != Booting {
		t.Fatalf("state %v, gpu_start_cancelled lines = %q", c.stateNow(), got)
	}
}
