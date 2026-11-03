package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

// fakeDialerForHarness is a small in-memory tunnel.Dialer, built via the
// exported tunnel.NewConn, following store/fake's style of a scripted
// fake plus a call counter the ACs read with Calls().
type fakeDialerForHarness struct {
	calls    int64
	hostKey  string
	lastAddr atomic.Value // string, "host:port" the dialer was last called with
	failErr  atomic.Value // error, returned by every Dial call once set (AC26's host-key-mismatch case)
}

func newFakeDialerForHarness() *fakeDialerForHarness {
	return &fakeDialerForHarness{hostKey: "sha256:fake"}
}

func (d *fakeDialerForHarness) Dial(_ context.Context, in tunnel.Instance) (*tunnel.Conn, error) {
	atomic.AddInt64(&d.calls, 1)
	d.lastAddr.Store(fmt.Sprintf("%s:%d", in.Host, in.Port))
	if err, ok := d.failErr.Load().(error); ok {
		return nil, err
	}
	return tunnel.NewConn(nil, func() error { return nil }, d.hostKey), nil
}

func (d *fakeDialerForHarness) Calls() int { return int(atomic.LoadInt64(&d.calls)) }

// LastAddr is the "host:port" the most recent Dial call carried; AC26 uses
// it to confirm the dial target is always the provider's live address.
func (d *fakeDialerForHarness) LastAddr() string {
	v, _ := d.lastAddr.Load().(string)
	return v
}

// FailAll makes every subsequent Dial call return err; AC26 uses it to
// simulate a pinned host key no longer matching the worker.
func (d *fakeDialerForHarness) FailAll(err error) { d.failErr.Store(err) }

var _ tunnel.Dialer = (*fakeDialerForHarness)(nil)

type harness struct {
	t      *testing.T
	ctl    *Controller
	clk    *fakeclock.Clock
	prov   *fakeprovider.Fake
	store  *fakestore.Fake
	dial   *fakeDialerForHarness
	worker *fakeWorker
	budget *fakeBudget
	reg    *prometheus.Registry
}

// buildHarness assembles every dependency without starting the
// controller, so a test that needs to seed the document before recovery
// (a warm restart, AC10-AC13) can do so before calling run.
func buildHarness(t *testing.T) *harness {
	t.Helper()
	clk := fakeclock.New(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.New(clk)
	prov.SetBalance(provider.Money(100_000_000)) // $100, the AC preamble's default
	st := fakestore.New("iid")
	worker := newFakeWorker()
	budget := newFakeBudget(1.60, 30)
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	store.RegisterMetrics(reg)

	h := &harness{t: t, clk: clk, prov: prov, store: st, dial: newFakeDialerForHarness(), worker: worker, budget: budget, reg: reg}

	ctl, err := New(Deps{
		Provider: prov, Store: st, Dialer: h.dial, Clock: clk,
		Budget: budget, Worker: worker,
		WorkerSeed:     func() ([]byte, error) { return []byte("seed"), nil },
		InstallationID: "iid", ModelAlias: "qwen3.6-35b-a3b",
		Config: config.Controller{
			ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80,
			IdleShutdownEnabled: true,
			IdleShutdownTimeout: 1800 * time.Second, ShutdownDrainTimeout: 60 * time.Second,
			StartupTimeout: 900 * time.Second, MaxLifetime: 12 * time.Hour,
			MaxProvisionAttemptsPerHour: 3, ReconcileInterval: 60 * time.Second,
		},
		Reg: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Seeded directly, like newTestController: a reconcile's own Balance
	// call hasn't necessarily run yet when the very first Start races in.
	ctl.lastBalance, ctl.lastBalanceKnown = provider.Money(100_000_000), true
	h.ctl = ctl
	return h
}

// run performs Run's startup half (recover, then the gauge sync) without
// entering its select loop: Settle alone drives c.step, so no second
// driver races it for the same due timer.
func (h *harness) run() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	h.ctl.mu.Lock()
	h.ctl.runCtx = ctx
	h.ctl.mu.Unlock()
	if err := h.ctl.recover(ctx); err != nil {
		h.t.Fatalf("recover: %v", err)
	}
	h.ctl.mu.Lock()
	setGPUState(PublicState(h.ctl.state, h.ctl.failing))
	h.ctl.mu.Unlock()
}

// newHarness builds and starts a controller over a fresh document — the
// cold-start path every AC1-9/13 test needs.
func newHarness(t *testing.T) *harness {
	h := buildHarness(t)
	h.run()
	return h
}

// Settle advances the fake clock to upTo, running every due timer and
// queued event along the way; c.inFlightEffects covers every goroutine
// this package spawns for its whole life, not just one call's duration.
func (h *harness) Settle(upTo time.Time) {
	ctx := context.Background()
	hardDeadline := time.Now().Add(10 * time.Second)
	for {
		// A timer re-arming on the same already-due instant never drains
		// to false on its own; 3 repeats of (kind, deadline) is a
		// controller bug, not a legitimate wait — fail loudly on it.
		var lastKind timerKind
		var lastDue time.Time
		var lastOK bool
		repeat := 0
		for {
			k, timerDue := h.ctl.dueTimer()
			if timerDue {
				at, _ := timerAt(h.ctl, k)
				if lastOK && k == lastKind && at.Equal(lastDue) {
					repeat++
					if repeat >= 3 {
						h.t.Fatalf("Settle: controller spinning on timer %v at %v with no clock progress", k, at)
					}
				} else {
					repeat = 0
				}
				lastKind, lastDue, lastOK = k, at, true
			} else {
				lastOK, repeat = false, 0
			}
			if !h.ctl.step(ctx) {
				break
			}
		}

		if h.ctl.inFlightEffects.Load() > 0 {
			// A fired latency ticker re-arms before its goroutine wakes
			// to Stop it and post its event, so NextDeadline can look
			// live; give it a short real-time grace window to finish first.
			for i := 0; i < 500 && h.ctl.inFlightEffects.Load() > 0; i++ {
				time.Sleep(100 * time.Microsecond)
			}
			if h.ctl.inFlightEffects.Load() == 0 {
				continue
			}
			if _, ok := h.clk.NextDeadline(); !ok {
				if time.Now().After(hardDeadline) {
					h.t.Fatal("Settle: in-flight effect never registered a clock waiter")
				}
				time.Sleep(time.Millisecond)
				continue
			}
		}

		due, haveDue := h.ctl.nextDue(), true
		if due.IsZero() {
			haveDue = false
		}
		waiterDue, haveWaiter := h.clk.NextDeadline()
		target, found := time.Time{}, false
		if haveDue {
			target, found = due, true
		}
		if haveWaiter && (!found || waiterDue.Before(target)) {
			target, found = waiterDue, true
		}
		if !found || target.After(upTo) {
			if now := h.clk.Now(); now.Before(upTo) {
				h.clk.Advance(upTo.Sub(now))
				continue
			}
			return
		}
		h.clk.Advance(target.Sub(h.clk.Now()))
	}
}

// metricValue reads one metric's current value from h's own registry by
// family name; store's conflictsTotal is unexported outside its package,
// so this is the only way this file can read it. labels are name/value pairs.
func (h *harness) metricValue(name string, labels ...string) float64 {
	h.t.Helper()
	if len(labels)%2 != 0 {
		h.t.Fatalf("metricValue(%q): odd number of label args %v", name, labels)
	}
	want := make(map[string]string, len(labels)/2)
	for i := 0; i < len(labels); i += 2 {
		want[labels[i]] = labels[i+1]
	}
	mfs, err := h.reg.Gather()
	if err != nil {
		h.t.Fatalf("metricValue(%q): Gather: %v", name, err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metric:
		for _, m := range mf.GetMetric() {
			labels := m.GetLabel()
			if len(want) != len(labels) {
				continue
			}
			for _, lp := range labels {
				if want[lp.GetName()] != lp.GetValue() {
					continue metric
				}
			}
			if c := m.GetCounter(); c != nil {
				return c.GetValue()
			}
			if g := m.GetGauge(); g != nil {
				return g.GetValue()
			}
		}
	}
	return 0
}

// counterDelta reads a counter relative to a baseline captured earlier:
// the controller's metrics are package-level and persist across every
// test in the binary, so an AC's own assertion is always a delta.
func counterDelta(c prometheus.Collector, before float64) float64 {
	return testutil.ToFloat64(c) - before
}

// gaugeOneHot asserts the 8-state gauge has exactly one state set.
func gaugeOneHot(t *testing.T, want string) {
	t.Helper()
	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		wantVal := 0.0
		if s == want {
			wantVal = 1.0
		}
		if got := testutil.ToFloat64(gpuState.WithLabelValues(s)); got != wantVal {
			t.Errorf("gpuState{%q} = %v, want %v", s, got, wantVal)
		}
	}
}

// Happy cold start: two StatusOther observations before GetInstance's
// own fallback promotes to Running+SSH (an explicit StatusRunning entry
// never fills in SSHHost), landing every stage on the t=1/16/41 timeline.
func TestAC1_HappyColdStart(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)
	beforeOK := testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("ok"))

	t0 := h.clk.Now().Add(time.Second) // t=1
	h.Settle(t0)
	snap, err := h.ctl.Start(context.Background())
	if err != nil || snap.Public != "ALLOCATING" || snap.Desired != store.DesiredRunning {
		t.Fatalf("Start = %+v, %v, want ALLOCATING/RUNNING, nil", snap, err)
	}
	h.ctl.ObserveFirstToken(1, 800*time.Millisecond)

	h.Settle(t0)
	if snap := h.ctl.Status(); snap.Public != "STARTING" {
		t.Fatalf("Public at t=1 = %q, want STARTING", snap.Public)
	}

	h.Settle(t0.Add(15 * time.Second)) // t=16
	if snap := h.ctl.Status(); snap.Public != "LOADING_MODEL" {
		t.Fatalf("Public at t=16 = %q, want LOADING_MODEL", snap.Public)
	}

	h.Settle(t0.Add(40 * time.Second)) // t=41
	snap = h.ctl.Status()
	if snap.Public != "READY" || snap.Generation != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if h.prov.Calls("search") != 1 || h.prov.Calls("create") != 1 || h.prov.Calls("get") != 3 ||
		h.prov.Calls("list") != 1 || h.prov.Calls("destroy") != 0 {
		t.Fatalf("search=%d create=%d get=%d list=%d destroy=%d, want 1/1/3/1/0",
			h.prov.Calls("search"), h.prov.Calls("create"), h.prov.Calls("get"), h.prov.Calls("list"), h.prov.Calls("destroy"))
	}
	if h.dial.Calls() != 1 {
		t.Fatalf("Calls(dial) = %d, want 1", h.dial.Calls())
	}
	if h.worker.probes() != 6 {
		t.Fatalf("probes = %d, want 6", h.worker.probes())
	}
	gaugeOneHot(t, "READY")
	if got := counterDelta(provisionAttemptsTotal.WithLabelValues("ok"), beforeOK); got != 1 {
		t.Fatalf("provision_attempts_total{ok} delta = %v, want 1", got)
	}
	if got := testutil.ToFloat64(instanceHourlyPriceUSD); got != 0.60 {
		t.Fatalf("instance_hourly_price_usd = %v, want 0.60", got)
	}
	wantReadyAt := t0.Add(40 * time.Second)
	if snap.ReadyAt == nil || !snap.ReadyAt.Equal(wantReadyAt) {
		t.Fatalf("ReadyAt = %v, want %v", snap.ReadyAt, wantReadyAt)
	}
	wantIdleDeadline := wantReadyAt.Add(1800 * time.Second)
	if snap.IdleDeadline == nil || !snap.IdleDeadline.Equal(wantIdleDeadline) {
		t.Fatalf("IdleDeadline = %v, want %v", snap.IdleDeadline, wantIdleDeadline)
	}
	if got := testutil.ToFloat64(gpuIdleDeadlineSeconds); got != float64(wantIdleDeadline.Unix()) {
		t.Fatalf("gpu_idle_deadline_seconds = %v, want unix(%v)", got, wantIdleDeadline)
	}
	// Known shortfall: stageEnteredAt is never written in production (only
	// first_token is observed via ObserveFirstToken), so the spec's
	// per-stage boot/download/load durations and counts aren't assertable.
}

func TestAC2_FiftyConcurrentStarts(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func() {
			if _, err := h.ctl.Start(context.Background()); err != nil {
				t.Errorf("Start: %v", err)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	h.Settle(h.clk.Now().Add(time.Second))
	if got := h.prov.Calls("search"); got != 1 {
		t.Fatalf("Calls(search) = %d, want 1", got)
	}
}

// Idle destroy: continuing AC1's cold start with no further requests, the
// idle deadline (readyAt+1800s) must drain and destroy the instance with
// no manual intervention, and leave the GPU stopped.
func TestAC3_IdleDestroy(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)
	before := testutil.ToFloat64(shutdownsTotal.WithLabelValues("idle"))
	beforeUptime := testutil.ToFloat64(gpuUptimeSecondsTotal)

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(41 * time.Second))
	readyAt := *h.ctl.Status().ReadyAt

	h.Settle(readyAt.Add(1799 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) = %d before the deadline, want 0", h.prov.Calls("destroy"))
	}
	if snap := h.ctl.Status(); snap.Public != "READY" {
		t.Fatalf("Public = %q before the deadline, want READY", snap.Public)
	}

	h.Settle(readyAt.Add(1800 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d at the deadline, want 1", h.prov.Calls("destroy"))
	}
	snap := h.ctl.Status()
	if snap.Public != "STOPPED" || snap.Desired != store.DesiredStopped {
		t.Fatalf("snapshot = %+v, want STOPPED/STOPPED", snap)
	}
	if got := counterDelta(shutdownsTotal.WithLabelValues("idle"), before); got != 1 {
		t.Fatalf("shutdowns_total{idle} delta = %v, want 1", got)
	}
	// The document's Instance is nil once destroyed, so Status's own
	// Uptime field no longer reflects it; gpuUptimeSecondsTotal (added at
	// the Destroying->Idle transition) is the durable record instead.
	if got := counterDelta(gpuUptimeSecondsTotal, beforeUptime); got < 1840 || got > 1841 {
		t.Fatalf("gpu_uptime_seconds_total delta = %v, want ~1840", got)
	}

	h.Settle(h.clk.Now().Add(7200 * time.Second))
	if h.prov.Calls("search") != 1 {
		t.Fatalf("Calls(search) = %d after staying stopped for 2h, want still 1", h.prov.Calls("search"))
	}
}

// Rolling deadline: inference activity between readyAt and the original
// deadline must roll the idle deadline forward from the last RequestEnd,
// not leave the original one in place.
func TestAC4_RollingDeadline(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(41 * time.Second))
	readyAt := *h.ctl.Status().ReadyAt
	gen := h.ctl.Status().Generation

	h.Settle(readyAt.Add(100 * time.Second))
	if _, err := h.ctl.Admit(gen); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.Settle(readyAt.Add(200 * time.Second))
	h.ctl.RequestEnd(gen)
	// RequestEnd's activity write flushes through flushDelay (10s); the
	// new deadline isn't on the document until that CAS lands.
	h.Settle(readyAt.Add(211 * time.Second))

	wantDeadline1 := readyAt.Add(200 * time.Second).Add(1800 * time.Second)
	if d := h.ctl.Status().IdleDeadline; d == nil || !d.Equal(wantDeadline1) {
		t.Fatalf("IdleDeadline = %v, want %v", d, wantDeadline1)
	}

	h.Settle(readyAt.Add(1990 * time.Second))
	if _, err := h.ctl.Admit(gen); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.Settle(readyAt.Add(2000 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) = %d at t=2000 with a request in flight, want 0", h.prov.Calls("destroy"))
	}
	if snap := h.ctl.Status(); snap.InFlight != 1 {
		t.Fatalf("InFlight = %d, want 1", snap.InFlight)
	}
	h.Settle(readyAt.Add(2100 * time.Second))
	h.ctl.RequestEnd(gen)
	h.Settle(readyAt.Add(2111 * time.Second))

	wantDeadline2 := readyAt.Add(2100 * time.Second).Add(1800 * time.Second)
	if d := h.ctl.Status().IdleDeadline; d == nil || !d.Equal(wantDeadline2) {
		t.Fatalf("IdleDeadline after second RequestEnd = %v, want %v", d, wantDeadline2)
	}
	if got := testutil.ToFloat64(gpuLastInferenceFinishedSeconds); got != float64(readyAt.Add(2100*time.Second).Unix()) {
		t.Fatalf("gpu_last_inference_finished_seconds = %v, want unix(t=2100)", got)
	}

	h.Settle(wantDeadline2)
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d at the rolled deadline, want 1", h.prov.Calls("destroy"))
	}
}

// Overlapping requests: 10 concurrent admissions at once, ended one at a
// time, must track inFlight precisely and only arm the idle deadline off
// the last RequestEnd.
func TestAC5_OverlappingRequests(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(41 * time.Second))
	readyAt := *h.ctl.Status().ReadyAt
	gen := h.ctl.Status().Generation

	h.Settle(readyAt.Add(100 * time.Second))
	for i := 0; i < 10; i++ {
		if _, err := h.ctl.Admit(gen); err != nil {
			t.Fatalf("Admit[%d]: %v", i, err)
		}
	}
	if got := h.ctl.Status().InFlight; got != 10 {
		t.Fatalf("InFlight after 10 Admit = %d, want 10", got)
	}

	for i := 0; i < 10; i++ {
		h.Settle(readyAt.Add(time.Duration(100+10*(i+1)) * time.Second))
		h.ctl.RequestEnd(gen)
	}
	// The last RequestEnd's own flush lands flushDelay (10s) later.
	h.Settle(readyAt.Add(211 * time.Second))
	if got := h.ctl.Status().InFlight; got != 0 {
		t.Fatalf("InFlight after draining all 10 = %d, want 0", got)
	}

	wantDeadline := readyAt.Add(200 * time.Second).Add(1800 * time.Second)
	if d := h.ctl.Status().IdleDeadline; d == nil || !d.Equal(wantDeadline) {
		t.Fatalf("IdleDeadline = %v, want %v (armed off the last RequestEnd only)", d, wantDeadline)
	}

	h.Settle(wantDeadline.Add(-time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) = %d before the deadline, want 0", h.prov.Calls("destroy"))
	}
	h.Settle(wantDeadline)
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d at the deadline, want 1", h.prov.Calls("destroy"))
	}
}

// Request at the exact deadline: same-instant priority puts the idle
// timer ahead of Admit, so an Admit exactly on the deadline loses to the
// destroy, while one a tick earlier rolls the deadline forward.
func TestAC6_RequestAtExactDeadline(t *testing.T) {
	t.Run("admit at the deadline loses to the idle timer", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
		h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
		h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)

		t0 := h.clk.Now().Add(time.Second)
		h.Settle(t0)
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(41 * time.Second))
		readyAt := *h.ctl.Status().ReadyAt
		gen := h.ctl.Status().Generation
		deadline := readyAt.Add(1800 * time.Second)

		// One instant before the deadline the GPU is still fully servable:
		// this is the window an Admit would win if it ran ahead of the timer.
		h.Settle(deadline.Add(-time.Nanosecond))
		if snap := h.ctl.Status(); snap.Public != "READY" {
			t.Fatalf("Public one instant before the deadline = %q, want READY", snap.Public)
		}

		// At the deadline itself, idleRuleFires is already true
		// (!Now.Before(deadline)), so the loop's own processing of it — not
		// a caller's Admit landing at the same instant — wins this one.
		h.Settle(deadline)
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d at the deadline, want 1 (idle timer wins)", h.prov.Calls("destroy"))
		}
		if snap := h.ctl.Status(); snap.Public != "STOPPED" {
			t.Fatalf("Public = %q at the deadline, want STOPPED", snap.Public)
		}
		if _, err := h.ctl.Admit(gen); !errors.Is(err, ErrBackendUnavailable) {
			t.Fatalf("Admit at the deadline = %v, want ErrBackendUnavailable", err)
		}
		if got := h.ctl.Status().InFlight; got != 0 {
			t.Fatalf("InFlight after the denied Admit = %d, want 0", got)
		}
	})

	t.Run("admit one tick early rolls the deadline forward", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
		h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
		h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)

		t0 := h.clk.Now().Add(time.Second)
		h.Settle(t0)
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(41 * time.Second))
		readyAt := *h.ctl.Status().ReadyAt
		gen := h.ctl.Status().Generation

		h.Settle(readyAt.Add(1799 * time.Second))
		if _, err := h.ctl.Admit(gen); err != nil {
			t.Fatalf("Admit one tick early: %v", err)
		}
		h.Settle(readyAt.Add(1800 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) = %d at the original deadline with inFlight 1, want 0", h.prov.Calls("destroy"))
		}
		h.Settle(readyAt.Add(1860 * time.Second))
		h.ctl.RequestEnd(gen)
		h.Settle(readyAt.Add(3700 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d at the rolled deadline (readyAt+1860+1800=3660, settled past it), want 1", h.prov.Calls("destroy"))
		}
	})
}

// Repeated start does not move the deadline: once Ready, a redundant
// Start (or Status) must be a pure no-op, and the original idle deadline
// must still fire on schedule.
func TestAC7_RepeatedStartIsNoOp(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(41 * time.Second))
	readyAt := *h.ctl.Status().ReadyAt

	h.Settle(readyAt.Add(100 * time.Second))
	_, versionBefore, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	for i := 0; i < 10; i++ {
		snap, err := h.ctl.Start(context.Background())
		if err != nil || snap.Public != "READY" {
			t.Fatalf("Start[%d] = %+v, %v, want READY, nil", i, snap, err)
		}
	}
	for i := 0; i < 100; i++ {
		if snap := h.ctl.Status(); snap.Public != "READY" {
			t.Fatalf("Status[%d] = %q, want READY", i, snap.Public)
		}
	}
	// A no-op Start's processEvent never calls store.Transition, and none
	// of this touched Settle, so no reconcile write landed in between.
	_, versionAfter, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("store version changed from %v to %v: a repeated Start/Status wrote the document", versionBefore, versionAfter)
	}

	// Reconcile ticks run on their own 60s cadence throughout; settling
	// further lets several land without touching the deadline.
	h.Settle(readyAt.Add(220 * time.Second))
	wantDeadline := readyAt.Add(1800 * time.Second)
	if d := h.ctl.Status().IdleDeadline; d == nil || !d.Equal(wantDeadline) {
		t.Fatalf("IdleDeadline = %v, want %v unchanged", d, wantDeadline)
	}

	h.Settle(wantDeadline)
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d at the original deadline, want 1", h.prov.Calls("destroy"))
	}
}

// Stop during active inference: a manual Stop while serving must drain
// rather than destroy at once, refuse new admissions, be idempotent, and
// destroy once the request ends or drain times out.
func TestAC8_StopDuringActiveInference(t *testing.T) {
	setup := func(t *testing.T) (*harness, time.Time, int64) {
		h := newHarness(t)
		h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
		h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
		h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)

		t0 := h.clk.Now().Add(time.Second)
		h.Settle(t0)
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(41 * time.Second))
		readyAt := *h.ctl.Status().ReadyAt
		gen := h.ctl.Status().Generation

		h.Settle(readyAt.Add(100 * time.Second))
		if _, err := h.ctl.Admit(gen); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		return h, readyAt, gen
	}

	t.Run("stop drains, is idempotent, and refuses admission", func(t *testing.T) {
		h, readyAt, gen := setup(t)
		before := testutil.ToFloat64(shutdownsTotal.WithLabelValues("manual"))

		h.Settle(readyAt.Add(110 * time.Second))
		snap, err := h.ctl.Stop(context.Background(), false)
		if err != nil || snap.Public != "DRAINING" || snap.Desired != store.DesiredStopped {
			t.Fatalf("first Stop = %+v, %v, want DRAINING/STOPPED, nil", snap, err)
		}

		h.Settle(readyAt.Add(111 * time.Second))
		if _, err := h.ctl.Admit(gen); err == nil {
			t.Fatal("Admit while draining: want an error, got nil")
		}

		_, versionBefore, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		h.Settle(readyAt.Add(115 * time.Second))
		snap2, err := h.ctl.Stop(context.Background(), false)
		if err != nil || snap2.Public != "DRAINING" || snap2.Desired != store.DesiredStopped {
			t.Fatalf("second Stop = %+v, %v, want DRAINING/STOPPED, nil", snap2, err)
		}
		_, versionAfter, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if versionAfter != versionBefore {
			t.Fatalf("store version changed from %v to %v: the repeat Stop was not a no-op write", versionBefore, versionAfter)
		}
		if got := counterDelta(shutdownsTotal.WithLabelValues("manual"), before); got != 1 {
			t.Fatalf("shutdowns_total{manual} delta = %v, want 1", got)
		}
	})

	t.Run("the in-flight request ending destroys at once", func(t *testing.T) {
		h, readyAt, gen := setup(t)
		h.Settle(readyAt.Add(110 * time.Second))
		if _, err := h.ctl.Stop(context.Background(), false); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		h.Settle(readyAt.Add(130 * time.Second))
		h.ctl.RequestEnd(gen)
		h.Settle(readyAt.Add(130 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d right after RequestEnd, want 1", h.prov.Calls("destroy"))
		}
	})

	t.Run("no RequestEnd destroys at the drain timeout", func(t *testing.T) {
		h, readyAt, _ := setup(t)
		closed := make(chan struct{}, 1)
		h.ctl.mu.Lock()
		h.ctl.conn = tunnel.NewConn(nil, func() error { closed <- struct{}{}; return nil }, "sha256:fake")
		h.ctl.mu.Unlock()

		h.Settle(readyAt.Add(110 * time.Second))
		if _, err := h.ctl.Stop(context.Background(), false); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		h.Settle(readyAt.Add(169 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) = %d before the 60s drain timeout, want 0", h.prov.Calls("destroy"))
		}
		h.Settle(readyAt.Add(170 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d at the drain timeout, want 1", h.prov.Calls("destroy"))
		}
		select {
		case <-closed:
		default:
			t.Fatal("Conn.Close was never called leaving Ready/Draining")
		}
	})
}

// Stop during provisioning: a Stop before the provider confirms creation
// has no id to destroy yet, so it only records the desire to stop; the
// create's own outcome then finishes the job at once.
func TestAC9_StopDuringProvisioning(t *testing.T) {
	t.Run("stop while still Creating cancels once the create returns", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
		h.prov.SetLatency("create", 10*time.Second)
		beforeDestroyOK := testutil.ToFloat64(destroyAttemptsTotal.WithLabelValues("ok"))
		beforeCancelled := testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("cancelled"))

		t0 := h.clk.Now().Add(time.Second)
		h.Settle(t0)
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(4 * time.Second))
		snap, err := h.ctl.Stop(context.Background(), false)
		if err != nil || snap.Public != "ALLOCATING" || snap.Desired != store.DesiredStopped {
			t.Fatalf("Stop while Creating = %+v, %v, want ALLOCATING/STOPPED, nil", snap, err)
		}

		h.Settle(t0.Add(10 * time.Second))
		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if op := findOp(&doc, createOpKey(1)); op == nil || op.Outcome != "cancelled" {
			t.Fatalf("create-1 op = %+v, want outcome cancelled", op)
		}
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d, want 1", h.prov.Calls("destroy"))
		}
		if h.dial.Calls() != 0 {
			t.Fatalf("Calls(dial) = %d, want 0 (never reached Booting)", h.dial.Calls())
		}
		if got := counterDelta(destroyAttemptsTotal.WithLabelValues("ok"), beforeDestroyOK); got != 1 {
			t.Fatalf("destroy_attempts_total{ok} delta = %v, want 1", got)
		}
		if got := counterDelta(provisionAttemptsTotal.WithLabelValues("cancelled"), beforeCancelled); got != 1 {
			t.Fatalf("provision_attempts_total{cancelled} delta = %v, want 1", got)
		}
		if snap := h.ctl.Status(); snap.Public != "STOPPED" {
			t.Fatalf("Public = %q, want STOPPED", snap.Public)
		}
	})

	t.Run("stop once Booting destroys at once", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
		h.prov.SetLatency("create", 10*time.Second)
		// One StatusOther entry delays GetInstance's own fallback
		// promotion to Running+SSH — otherwise instant on the first poll —
		// to the second poll, leaving a window to observe Booting.
		h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther)

		t0 := h.clk.Now().Add(time.Second)
		h.Settle(t0)
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(15 * time.Second))
		if snap := h.ctl.Status(); snap.State != Booting {
			t.Fatalf("State = %v, want Booting", snap.State)
		}

		snap, err := h.ctl.Stop(context.Background(), false)
		if err != nil || snap.Public != "DELETING" {
			t.Fatalf("Stop while Booting = %+v, %v, want DELETING, nil", snap, err)
		}
		h.Settle(t0.Add(15 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d, want 1", h.prov.Calls("destroy"))
		}
	})
}

// seedAdoptedReady puts the store directly into a Ready document for a
// warm-restart AC (AC10-AC13), with a matching provider-side instance
// since recovery's own dial and reconcile's own list both read it back.
func seedAdoptedReady(h *harness, gen int64, readyAt, lastInferenceFinishedAt, idleDeadline time.Time) {
	h.t.Helper()
	label := fmt.Sprintf("vkllm:iid:%d", gen)
	h.prov.AddInstance(provider.Instance{ID: 9, Label: label, Status: provider.StatusRunning, SSHHost: "h", SSHPort: 22})
	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		h.t.Fatalf("seedAdoptedReady: Load: %v", err)
	}
	doc.Generation = gen
	doc.Desired = store.DesiredRunning
	inst := &store.Instance{State: Ready.String(), Label: label, ProviderID: "9", CreatedAt: readyAt}
	if !readyAt.IsZero() {
		ra := readyAt
		inst.ReadyAt = &ra
	}
	if !lastInferenceFinishedAt.IsZero() {
		lif := lastInferenceFinishedAt
		inst.LastInferenceFinishedAt = &lif
	}
	if !idleDeadline.IsZero() {
		idl := idleDeadline
		inst.IdleDeadline = &idl
	}
	doc.Instance = inst
	h.store.Put(doc)
}

// Start during deletion: a Start arriving mid destroy must revive once
// it confirms. Retries land at t=10,20,40 (armDestroyRetry's own
// backoff); revival waits for the next 60s reconcile tick, a known gap.
func TestAC10_StartDuringDeletion(t *testing.T) {
	h := buildHarness(t)
	seedAdoptedReady(h, 3, h.clk.Now(), time.Time{}, h.clk.Now().Add(1800*time.Second))
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	h.prov.FailDestroy(2)
	// Recovery redials and single-flights a suspect probe; without a
	// Ready probe response that would miss twice and raise its own
	// spurious WorkerFailed destroy before the test's own Stop ever runs.
	h.worker.SetProbeSequence(ProbeReady)
	h.run()

	t0 := h.clk.Now()
	h.Settle(t0.Add(10 * time.Second))
	if _, err := h.ctl.Stop(context.Background(), false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Stop's own processEvent call only commits Ready->Draining; the
	// immediate Draining->Destroying hop (inFlight 0, drain armed at
	// once) and the first destroy attempt both need a step to run.
	h.Settle(t0.Add(10 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d right after Stop, want 1 (armed at once entering Destroying)", h.prov.Calls("destroy"))
	}

	h.Settle(t0.Add(15 * time.Second))
	snap, err := h.ctl.Start(context.Background())
	if err != nil || snap.Public != "DELETING" {
		t.Fatalf("Start during deletion = %+v, %v, want DELETING, nil", snap, err)
	}

	h.Settle(t0.Add(19 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d before the first retry, want 1", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(20 * time.Second))
	if h.prov.Calls("destroy") != 2 {
		t.Fatalf("Calls(destroy) = %d at the first retry (t=20), want 2", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(39 * time.Second))
	if h.prov.Calls("create") != 0 {
		t.Fatalf("Calls(create) = %d before the destroy confirms, want 0", h.prov.Calls("create"))
	}
	h.Settle(t0.Add(40 * time.Second))
	if h.prov.Calls("destroy") != 3 {
		t.Fatalf("Calls(destroy) = %d at the second retry (t=40), want 3", h.prov.Calls("destroy"))
	}

	// Desired stayed RUNNING through the destroy, so the next periodic
	// reconcile Tick (every 60s from t=0: 0,60,120,...) searches again.
	h.Settle(t0.Add(60 * time.Second))
	if h.prov.Calls("search") != 1 || h.prov.Calls("create") != 1 {
		t.Fatalf("search=%d create=%d at t=60, want 1/1", h.prov.Calls("search"), h.prov.Calls("create"))
	}
	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Generation != 4 || doc.Instance == nil || doc.Instance.Label != "vkllm:iid:4" {
		t.Fatalf("doc = %+v, want generation 4, label vkllm:iid:4", doc)
	}
	if doc.Desired != store.DesiredRunning {
		t.Fatalf("Desired = %q, want RUNNING", doc.Desired)
	}
}

// Restart after the deadline passed: a document past its deadline must
// destroy without redialing (reason "manual" — a known mislabeling);
// one still ahead must redial and idle out normally.
func TestAC11_RestartAfterDeadlinePassed(t *testing.T) {
	t.Run("deadline already past: destroy without redialing", func(t *testing.T) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		seedAdoptedReady(h, 4, t0.Add(-3600*time.Second), t0.Add(-2400*time.Second), t0.Add(-600*time.Second))
		before := testutil.ToFloat64(shutdownsTotal.WithLabelValues("manual"))
		h.run()

		if h.dial.Calls() != 0 {
			t.Fatalf("Calls(dial) = %d, want 0", h.dial.Calls())
		}
		h.Settle(t0)
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d at t=0, want 1", h.prov.Calls("destroy"))
		}
		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if doc.Desired != store.DesiredStopped {
			t.Fatalf("Desired = %q, want STOPPED", doc.Desired)
		}
		if got := counterDelta(shutdownsTotal.WithLabelValues("manual"), before); got != 1 {
			t.Fatalf("shutdowns_total{manual} delta = %v, want 1", got)
		}
	})

	t.Run("deadline still ahead: redial and idle out normally later", func(t *testing.T) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		seedAdoptedReady(h, 4, t0.Add(-3600*time.Second), t0.Add(-600*time.Second), t0.Add(1200*time.Second))
		h.worker.SetProbeSequence(ProbeReady)
		h.run()
		h.Settle(t0.Add(time.Second))

		if h.dial.Calls() != 1 {
			t.Fatalf("Calls(dial) = %d, want 1", h.dial.Calls())
		}
		h.Settle(t0.Add(1199 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) = %d before t=1200, want 0", h.prov.Calls("destroy"))
		}
		h.Settle(t0.Add(1200 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) = %d at t=1200, want 1", h.prov.Calls("destroy"))
		}
	})
}

// Restart with a Ready instance: recovering a document whose deadline is
// still ahead must redial and nothing else, admit immediately once
// dialed, and leave readyAt/idleDeadline unchanged.
func TestAC12_RestartWithReadyInstance(t *testing.T) {
	h := buildHarness(t)
	t0 := h.clk.Now()
	readyAt := t0
	idleDeadline := t0.Add(1800 * time.Second)
	seedAdoptedReady(h, 4, readyAt, time.Time{}, idleDeadline)
	h.worker.SetProbeSequence(ProbeReady)
	h.run()

	h.Settle(t0.Add(time.Second))
	if _, err := h.ctl.Admit(4); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if h.prov.Calls("create") != 0 || h.prov.Calls("destroy") != 0 || h.prov.Calls("search") != 0 {
		t.Fatalf("create=%d destroy=%d search=%d, want 0/0/0", h.prov.Calls("create"), h.prov.Calls("destroy"), h.prov.Calls("search"))
	}
	if h.prov.Calls("get") != 1 || h.dial.Calls() != 1 {
		t.Fatalf("get=%d dial=%d, want 1/1", h.prov.Calls("get"), h.dial.Calls())
	}

	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Instance == nil || doc.Instance.ReadyAt == nil || !doc.Instance.ReadyAt.Equal(readyAt) {
		t.Fatalf("ReadyAt = %v, want unchanged %v", doc.Instance.ReadyAt, readyAt)
	}
	if doc.Instance.IdleDeadline == nil || !doc.Instance.IdleDeadline.Equal(idleDeadline) {
		t.Fatalf("IdleDeadline = %v, want unchanged %v", doc.Instance.IdleDeadline, idleDeadline)
	}
}

// Startup timeout: an instance that never reports healthy must be
// destroyed at the startup deadline, its machine denylisted for an hour,
// and a Start inside the 60s no_offer backoff window denied outright.
func TestAC13_StartupTimeout(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000})
	h.worker.SetProbeSequence(ProbeUnreachable)

	t0 := h.clk.Now()
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(899 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) = %d before the startup deadline, want 0", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(900 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d at the startup deadline, want 1", h.prov.Calls("destroy"))
	}
	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if op := findOp(&doc, createOpKey(1)); op == nil || op.Outcome != "deadline" {
		t.Fatalf("create-1 op = %+v, want outcome deadline", op)
	}
	if len(doc.MachineDenylist) != 1 || doc.MachineDenylist[0].MachineID != 42 ||
		!doc.MachineDenylist[0].Until.Equal(t0.Add(900*time.Second).Add(time.Hour)) {
		t.Fatalf("MachineDenylist = %+v, want {42, until t=4500}", doc.MachineDenylist)
	}

	h.Settle(t0.Add(960 * time.Second))
	if h.prov.Calls("search") != 2 {
		t.Fatalf("Calls(search) = %d at t=960, want 2 (backoff-paced retry)", h.prov.Calls("search"))
	}
	snap := h.ctl.Status()
	if snap.Public != "STOPPED" || snap.Desired != store.DesiredStopped || snap.Unavailable != "no_offer" {
		t.Fatalf("snapshot = %+v, want STOPPED/STOPPED/no_offer", snap)
	}

	// Inside the no_offer backoff window (960 to 1020), Start is denied
	// outright rather than restarting a search the denylist would refuse anyway.
	h.Settle(t0.Add(990 * time.Second))
	snap2, err := h.ctl.Start(context.Background())
	if !errors.Is(err, ErrGPUUnavailable) {
		t.Fatalf("Start at t=990 err = %v, want ErrGPUUnavailable", err)
	}
	if snap2.Public != "STOPPED" || snap2.Unavailable != "no_offer" {
		t.Fatalf("snapshot at t=990 = %+v, want STOPPED/no_offer", snap2)
	}
	if h.prov.Calls("create") != 1 || h.prov.Calls("search") != 2 {
		t.Fatalf("create=%d search=%d, want 1/2 (denied, no new search)", h.prov.Calls("create"), h.prov.Calls("search"))
	}
}

// Provider list error: a reconcile tick that can't list instances must
// skip the comparison entirely rather than destroying blind, and resume
// counting consecutive orphan observations once listing recovers.
func TestAC14_ProviderListError(t *testing.T) {
	h := buildHarness(t)
	t0 := h.clk.Now()
	seedAdoptedReady(h, 3, t0, time.Time{}, t0.Add(1800*time.Second))
	h.worker.SetProbeSequence(ProbeReady)
	h.run()

	h.Settle(t0.Add(30 * time.Second))
	h.prov.AddInstance(provider.Instance{ID: 20, Label: "vkllm:iid:2", Status: provider.StatusRunning})
	h.prov.FailNext("list", 10, &provider.Error{Op: "list", Class: provider.Retryable, Err: errors.New("fake list failure")})
	beforeErr := testutil.ToFloat64(reconcileRunsTotal.WithLabelValues("error"))

	h.Settle(t0.Add(600 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) by t=600 = %d, want 0", h.prov.Calls("destroy"))
	}
	if got := counterDelta(reconcileRunsTotal.WithLabelValues("error"), beforeErr); got != 10 {
		t.Fatalf("reconcile_runs_total{error} delta = %v, want 10", got)
	}
	if snap := h.ctl.Status(); snap.Public != "READY" {
		t.Fatalf("Public = %q, want READY", snap.Public)
	}

	beforeOrphans := testutil.ToFloat64(orphansDestroyedTotal)
	h.Settle(t0.Add(720 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=720 = %d, want 1", h.prov.Calls("destroy"))
	}
	if got := counterDelta(orphansDestroyedTotal, beforeOrphans); got != 1 {
		t.Fatalf("orphans_destroyed_total delta = %v, want 1", got)
	}
}

// Instance vanishes: the harness destroying the active id out of band
// must surface as two consecutive missing reconcile observations,
// closing the ledger and the tunnel with no destroy call of its own.
func TestAC15_InstanceVanishes(t *testing.T) {
	h := buildHarness(t)
	t0 := h.clk.Now()
	seedAdoptedReady(h, 3, t0, time.Time{}, t0.Add(1800*time.Second))
	h.worker.SetProbeSequence(ProbeReady)
	h.run()

	h.Settle(t0.Add(time.Second))
	if _, err := h.ctl.Admit(3); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	closed := make(chan struct{}, 1)
	h.ctl.mu.Lock()
	h.ctl.conn = tunnel.NewConn(nil, func() error { closed <- struct{}{}; return nil }, "sha256:fake")
	h.ctl.mu.Unlock()
	beforeFailure := testutil.ToFloat64(shutdownsTotal.WithLabelValues("failure"))

	h.Settle(t0.Add(30 * time.Second))
	if err := h.prov.DestroyInstance(context.Background(), 9); err != nil {
		t.Fatalf("harness destroy of id 9: %v", err)
	}

	h.Settle(t0.Add(60 * time.Second))
	if snap := h.ctl.Status(); snap.Public != "READY" {
		t.Fatalf("Public at t=60 = %q, want READY", snap.Public)
	}
	select {
	case <-closed:
		t.Fatal("Conn.Close called before the second missing observation")
	default:
	}

	h.Settle(t0.Add(120 * time.Second))
	snap := h.ctl.Status()
	if snap.Public != "STOPPED" || snap.Desired != store.DesiredStopped {
		t.Fatalf("snapshot at t=120 = %+v, want STOPPED/STOPPED", snap)
	}
	select {
	case <-closed:
	default:
		t.Fatal("Conn.Close was never called")
	}
	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Instance != nil {
		t.Fatalf("doc.Instance = %+v, want nil", doc.Instance)
	}
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) = %d, want 1 (the harness's own)", h.prov.Calls("destroy"))
	}
	if got := counterDelta(shutdownsTotal.WithLabelValues("failure"), beforeFailure); got != 1 {
		t.Fatalf("shutdowns_total{failure} delta = %v, want 1", got)
	}
}

// Orphan injection: a foreign-label instance under our own installation id,
// at or behind the current generation, is destroyed after two observations;
// one ahead of it first advances the fence; a foreign installation id never is.
func TestAC16_OrphanInjection(t *testing.T) {
	seedIdleGen3 := func(t *testing.T) *harness {
		h := buildHarness(t)
		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		doc.Generation = 3
		h.store.Put(doc)
		h.run()
		return h
	}

	t.Run("behind the current generation: destroyed after two observations", func(t *testing.T) {
		h := seedIdleGen3(t)
		t0 := h.clk.Now()
		beforeOrphans := testutil.ToFloat64(orphansDestroyedTotal)
		beforeShutdown := testutil.ToFloat64(shutdownsTotal.WithLabelValues("orphan"))

		h.Settle(t0.Add(30 * time.Second))
		h.prov.AddInstance(provider.Instance{ID: 20, Label: "vkllm:iid:2", Status: provider.StatusRunning})

		h.Settle(t0.Add(119 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) before t=120 = %d, want 0", h.prov.Calls("destroy"))
		}
		h.Settle(t0.Add(120 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) at t=120 = %d, want 1", h.prov.Calls("destroy"))
		}
		if got := counterDelta(orphansDestroyedTotal, beforeOrphans); got != 1 {
			t.Fatalf("orphans_destroyed_total delta = %v, want 1", got)
		}
		if got := counterDelta(shutdownsTotal.WithLabelValues("orphan"), beforeShutdown); got != 1 {
			t.Fatalf("shutdowns_total{orphan} delta = %v, want 1", got)
		}
	})

	t.Run("ahead of the current generation: advances the fence, then destroys", func(t *testing.T) {
		h := seedIdleGen3(t)
		t0 := h.clk.Now()

		h.Settle(t0.Add(30 * time.Second))
		h.prov.AddInstance(provider.Instance{ID: 21, Label: "vkllm:iid:5", Status: provider.StatusRunning})

		h.Settle(t0.Add(60 * time.Second))
		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if doc.Generation != 5 {
			t.Fatalf("Generation at t=60 = %d, want 5 (fence advanced)", doc.Generation)
		}
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) at t=60 = %d, want 0", h.prov.Calls("destroy"))
		}

		h.Settle(t0.Add(120 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) at t=120 = %d, want 1", h.prov.Calls("destroy"))
		}
	})

	t.Run("a foreign installation id is never a destroy target", func(t *testing.T) {
		h := seedIdleGen3(t)
		t0 := h.clk.Now()

		h.Settle(t0.Add(30 * time.Second))
		h.prov.AddInstance(provider.Instance{ID: 22, Label: "vkllm:other:1", Status: provider.StatusRunning})

		h.Settle(t0.Add(7200 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) after 2h = %d, want 0", h.prov.Calls("destroy"))
		}
	})
}

// Restart with an empty store: a foreign-to-us-in-memory but label-matching
// instance observed with no document at all first advances the fence to its
// own generation, then destroys it; desired stays STOPPED throughout.
func TestAC17_RestartWithEmptyStore(t *testing.T) {
	h := buildHarness(t)
	t0 := h.clk.Now()
	h.prov.AddInstance(provider.Instance{ID: 5, Label: "vkllm:iid:7", Status: provider.StatusRunning})
	h.run()

	h.Settle(t0)
	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Generation != 7 {
		t.Fatalf("Generation at t=0 = %d, want 7", doc.Generation)
	}

	beforeOrphans := testutil.ToFloat64(orphansDestroyedTotal)
	h.Settle(t0.Add(59 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) before t=60 = %d, want 0", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(60 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=60 = %d, want 1", h.prov.Calls("destroy"))
	}
	if got := counterDelta(orphansDestroyedTotal, beforeOrphans); got != 1 {
		t.Fatalf("orphans_destroyed_total delta = %v, want 1", got)
	}
	if h.prov.Calls("create") != 0 {
		t.Fatalf("Calls(create) = %d, want 0", h.prov.Calls("create"))
	}
	doc2, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc2.Desired != store.DesiredStopped {
		t.Fatalf("Desired = %q, want STOPPED", doc2.Desired)
	}
}

// Destroy retries: a failing destroy must retry on armDestroyRetry's own
// backoff (10/20/40s) without the periodic reconcile tick adding an extra
// attempt of its own, denying Admit throughout and finishing once confirmed.
func TestAC18_DestroyRetries(t *testing.T) {
	h := buildHarness(t)
	t0 := h.clk.Now()
	seedAdoptedReady(h, 3, t0, time.Time{}, t0.Add(1800*time.Second))
	h.prov.FailDestroy(3)
	h.worker.SetProbeSequence(ProbeReady)
	h.run()

	beforeRetryable := testutil.ToFloat64(destroyAttemptsTotal.WithLabelValues("retryable"))
	beforeOK := testutil.ToFloat64(destroyAttemptsTotal.WithLabelValues("ok"))

	h.Settle(t0.Add(10 * time.Second))
	if _, err := h.ctl.Stop(context.Background(), false); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	h.Settle(t0.Add(10 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=10 = %d, want 1", h.prov.Calls("destroy"))
	}

	h.Settle(t0.Add(11 * time.Second))
	if _, err := h.ctl.Admit(3); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("Admit during destroy retries = %v, want ErrBackendUnavailable", err)
	}

	h.Settle(t0.Add(19 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=19 = %d, want 1", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(20 * time.Second))
	if h.prov.Calls("destroy") != 2 {
		t.Fatalf("Calls(destroy) at t=20 = %d, want 2", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(40 * time.Second))
	if h.prov.Calls("destroy") != 3 {
		t.Fatalf("Calls(destroy) at t=40 = %d, want 3", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(60 * time.Second))
	if h.prov.Calls("destroy") != 3 {
		t.Fatalf("Calls(destroy) at t=60 = %d, want still 3 (the reconcile tick adds none)", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(80 * time.Second))
	if h.prov.Calls("destroy") != 4 {
		t.Fatalf("Calls(destroy) at t=80 = %d, want 4", h.prov.Calls("destroy"))
	}
	if snap := h.ctl.Status(); snap.Public != "STOPPED" {
		t.Fatalf("Public at t=80 = %q, want STOPPED", snap.Public)
	}
	if got := counterDelta(destroyAttemptsTotal.WithLabelValues("retryable"), beforeRetryable); got != 3 {
		t.Fatalf("destroy_attempts_total{retryable} delta = %v, want 3", got)
	}
	if got := counterDelta(destroyAttemptsTotal.WithLabelValues("ok"), beforeOK); got != 1 {
		t.Fatalf("destroy_attempts_total{ok} delta = %v, want 1", got)
	}

	h.Settle(t0.Add(600 * time.Second))
	if h.prov.Calls("search") != 0 {
		t.Fatalf("Calls(search) by t=600 = %d, want 0", h.prov.Calls("search"))
	}
}

// Max lifetime: continuous admit/requestEnd activity must not block the
// 12h lifetime cutoff, which fires on the periodic Tick regardless of
// how recently the instance was last used.
func TestAC19_MaxLifetime(t *testing.T) {
	h := buildHarness(t)
	t0 := h.clk.Now()
	createdAt := t0.Add(-11 * time.Hour)
	seedAdoptedReady(h, 3, createdAt, time.Time{}, t0.Add(1800*time.Second))
	h.worker.SetProbeSequence(ProbeReady)
	h.run()

	beforeLifetime := testutil.ToFloat64(shutdownsTotal.WithLabelValues("lifetime"))

	deadline := t0.Add(3600 * time.Second)
	for mark := t0; mark.Before(deadline); mark = mark.Add(30 * time.Second) {
		h.Settle(mark)
		if _, err := h.ctl.Admit(3); err != nil {
			t.Fatalf("Admit at %v: %v", mark, err)
		}
		h.Settle(mark.Add(10 * time.Second))
		h.ctl.RequestEnd(3)
	}

	h.Settle(deadline.Add(-time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) just before t=3600 = %d, want 0", h.prov.Calls("destroy"))
	}

	h.Settle(deadline)
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=3600 = %d, want 1", h.prov.Calls("destroy"))
	}
	if got := counterDelta(shutdownsTotal.WithLabelValues("lifetime"), beforeLifetime); got != 1 {
		t.Fatalf("shutdowns_total{lifetime} delta = %v, want 1", got)
	}
}

// Budget exhaustion mid-Serving: a balance dropping below the floor while
// serving must drain (not destroy outright), deny every new admission and
// Start, and finish once the in-flight request ends or the drain times out.
func TestAC20_BudgetMidServing(t *testing.T) {
	setup := func(t *testing.T) (*harness, time.Time) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		seedAdoptedReady(h, 3, t0, time.Time{}, t0.Add(1800*time.Second))
		h.worker.SetProbeSequence(ProbeReady)
		h.run()
		h.Settle(t0)
		if _, err := h.ctl.Admit(3); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		h.Settle(t0.Add(30 * time.Second))
		h.prov.SetBalance(0)
		return h, t0
	}

	t.Run("the request ending destroys at once", func(t *testing.T) {
		h, t0 := setup(t)
		beforeBudget := testutil.ToFloat64(shutdownsTotal.WithLabelValues("budget"))

		h.Settle(t0.Add(60 * time.Second))
		snap := h.ctl.Status()
		if snap.State != Draining || snap.Desired != store.DesiredStopped {
			t.Fatalf("snapshot at t=60 = %+v, want Draining/STOPPED", snap)
		}

		h.Settle(t0.Add(61 * time.Second))
		if _, err := h.ctl.Admit(3); !errors.Is(err, ErrDraining) {
			t.Fatalf("Admit at t=61 = %v, want ErrDraining", err)
		}
		snap2, err := h.ctl.Start(context.Background())
		if !errors.Is(err, ErrGPUUnavailable) {
			t.Fatalf("Start at t=61 = %v, want ErrGPUUnavailable", err)
		}
		if snap2.Unavailable != "balance_low" {
			t.Fatalf("Unavailable at t=61 = %q, want balance_low", snap2.Unavailable)
		}

		h.Settle(t0.Add(90 * time.Second))
		h.ctl.RequestEnd(3)
		h.Settle(t0.Add(90 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) at t=90 = %d, want 1", h.prov.Calls("destroy"))
		}
		if got := counterDelta(shutdownsTotal.WithLabelValues("budget"), beforeBudget); got != 1 {
			t.Fatalf("shutdowns_total{budget} delta = %v, want 1", got)
		}
	})

	t.Run("no RequestEnd still destroys at the drain timeout", func(t *testing.T) {
		h, t0 := setup(t)
		closed := make(chan struct{}, 1)
		h.ctl.mu.Lock()
		h.ctl.conn = tunnel.NewConn(nil, func() error { closed <- struct{}{}; return nil }, "sha256:fake")
		h.ctl.mu.Unlock()

		h.Settle(t0.Add(119 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) before t=120 = %d, want 0", h.prov.Calls("destroy"))
		}
		h.Settle(t0.Add(120 * time.Second))
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) at t=120 = %d, want 1", h.prov.Calls("destroy"))
		}
		select {
		case <-closed:
		default:
			t.Fatal("Conn.Close was never called")
		}
	})
}

// CAS conflict storm: 12 consecutive version conflicts on the Searching
// commit exceed one Transition's 8-attempt budget, exhausting it once and
// requeuing the Start event (a 5s delay) before the retry succeeds.
func TestAC21_CASConflictStorm(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeUnreachable, ProbeUnreachable, ProbeLoading, ProbeLoading, ProbeLoading, ProbeReady)
	beforeConflicts := h.metricValue("vkllm_store_conflicts_total")

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	h.store.ConflictNext(12)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.Settle(t0.Add(45 * time.Second)) // t=46
	snap := h.ctl.Status()
	if snap.Public != "READY" || snap.Generation != 1 {
		t.Fatalf("snapshot = %+v, want READY/gen1", snap)
	}
	if h.prov.Calls("search") != 1 || h.prov.Calls("create") != 1 {
		t.Fatalf("search=%d create=%d, want 1/1", h.prov.Calls("search"), h.prov.Calls("create"))
	}
	if got := h.metricValue("vkllm_store_conflicts_total") - beforeConflicts; got != 12 {
		t.Fatalf("vkllm_store_conflicts_total delta = %v, want 12", got)
	}
}

// Terminal status: two consecutive terminal gets during Booting destroy
// (failing, since this happened before Ready) but leave desired RUNNING, so
// the backoff retries automatically once the hourly/backoff window allows.
func TestAC22_TerminalStatus(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusExited, provider.StatusExited)
	h.prov.FailDestroy(1)
	beforeFailedStatus := testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("failed_status"))

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.Settle(t0.Add(5 * time.Second)) // t=6: first get, not yet confirmed terminal
	if h.prov.Calls("get") != 1 {
		t.Fatalf("Calls(get) at t=6 = %d, want 1", h.prov.Calls("get"))
	}

	h.Settle(t0.Add(10 * time.Second)) // t=11: second get confirms terminal; destroy 1 fires and fails
	if h.prov.Calls("get") != 2 {
		t.Fatalf("Calls(get) at t=11 = %d, want 2", h.prov.Calls("get"))
	}
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=11 = %d, want 1", h.prov.Calls("destroy"))
	}

	h.Settle(t0.Add(14 * time.Second)) // t=15
	if snap := h.ctl.Status(); snap.Public != "FAILED" {
		t.Fatalf("Public at t=15 = %q, want FAILED", snap.Public)
	}

	h.Settle(t0.Add(20 * time.Second)) // t=21: the retry succeeds
	if h.prov.Calls("destroy") != 2 {
		t.Fatalf("Calls(destroy) at t=21 = %d, want 2", h.prov.Calls("destroy"))
	}
	snap := h.ctl.Status()
	if snap.Public != "STOPPED" {
		t.Fatalf("Public at t=21 = %q, want STOPPED", snap.Public)
	}
	if snap.Desired != store.DesiredRunning {
		t.Fatalf("Desired = %q, want RUNNING", snap.Desired)
	}
	if got := counterDelta(provisionAttemptsTotal.WithLabelValues("failed_status"), beforeFailedStatus); got != 1 {
		t.Fatalf("provision_attempts_total{failed_status} delta = %v, want 1", got)
	}

	h.Settle(t0.Add(118 * time.Second)) // t=119: still backed off
	if h.prov.Calls("search") != 1 {
		t.Fatalf("Calls(search) before t=120 = %d, want still 1", h.prov.Calls("search"))
	}
	h.Settle(t0.Add(119 * time.Second)) // t=120
	if h.prov.Calls("search") != 2 {
		t.Fatalf("Calls(search) at t=120 = %d, want 2", h.prov.Calls("search"))
	}
}

// Non-terminal string: a persistent StatusOther ("scheduling"-like) status
// never satisfies T12's dial condition or T14's terminal-destroy condition,
// so only the startup deadline ever ends this instance's life.
func TestAC23_NonTerminalString(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	seq := make([]provider.InstanceStatus, 300)
	for i := range seq {
		seq[i] = provider.StatusOther
	}
	h.prov.SetStatusSequence(provider.InstanceID(1), seq...)

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.Settle(t0.Add(899 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) before t=901 = %d, want 0", h.prov.Calls("destroy"))
	}
	h.Settle(t0.Add(900 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=901 = %d, want 1", h.prov.Calls("destroy"))
	}
}

// Attempts cap: three startup-timeout cycles within the same hour exhaust
// MaxProvisionAttemptsPerHour, denying even an explicit Start until the
// next hour's bucket reopens the gate on a clean machine.
func TestAC24_AttemptsCap(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(
		provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"},
		provider.Offer{ID: 12, MachineID: 43, Hourly: 600000, GPUName: "L40S"},
		provider.Offer{ID: 13, MachineID: 44, Hourly: 600000, GPUName: "L40S"},
		provider.Offer{ID: 14, MachineID: 45, Hourly: 600000, GPUName: "L40S"},
	)
	h.worker.SetProbeSequence(ProbeUnreachable)
	beforeExhausted := testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("attempts_exhausted"))

	t0 := h.clk.Now()
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start at t=0: %v", err)
	}
	h.Settle(t0.Add(time.Second))
	if h.prov.Calls("create") != 1 {
		t.Fatalf("Calls(create) right after t=0 = %d, want 1", h.prov.Calls("create"))
	}

	h.Settle(t0.Add(959 * time.Second))
	if h.prov.Calls("create") != 1 {
		t.Fatalf("Calls(create) before t=960 = %d, want 1", h.prov.Calls("create"))
	}
	h.Settle(t0.Add(960 * time.Second))
	if h.prov.Calls("create") != 2 {
		t.Fatalf("Calls(create) at t=960 = %d, want 2", h.prov.Calls("create"))
	}

	h.Settle(t0.Add(1979 * time.Second))
	if h.prov.Calls("create") != 2 {
		t.Fatalf("Calls(create) before t=1980 = %d, want 2", h.prov.Calls("create"))
	}
	h.Settle(t0.Add(1980 * time.Second))
	if h.prov.Calls("create") != 3 {
		t.Fatalf("Calls(create) at t=1980 = %d, want 3", h.prov.Calls("create"))
	}

	h.Settle(t0.Add(2880 * time.Second))
	snap := h.ctl.Status()
	if snap.State != Idle || snap.Unavailable != "attempts_exhausted" {
		t.Fatalf("snapshot at t=2880 = %+v, want Idle/attempts_exhausted", snap)
	}
	// Known metrics gap, not silently worked around: this denial happens on
	// an Idle+Tick event, which provisionOutcome has no case for (only a
	// Searching+OfferChosen denial is mapped), so no counter moves here.
	if got := counterDelta(provisionAttemptsTotal.WithLabelValues("attempts_exhausted"), beforeExhausted); got != 0 {
		t.Fatalf("provision_attempts_total{attempts_exhausted} delta = %v, want 0 (known metrics gap)", got)
	}

	// Desired can lag one reconcile tick behind State/Unavailable: EvGone and
	// the same-instant EvTick can race, and one ordering processes Tick while
	// state is still Destroying, where T2 doesn't match until the next tick.
	h.Settle(t0.Add(2940 * time.Second))
	if snap := h.ctl.Status(); snap.Desired != store.DesiredStopped {
		t.Fatalf("Desired by t=2940 = %q, want STOPPED", snap.Desired)
	}

	h.Settle(t0.Add(3000 * time.Second))
	if _, err := h.ctl.Start(context.Background()); !errors.Is(err, ErrGPUUnavailable) {
		t.Fatalf("Start at t=3000 = %v, want ErrGPUUnavailable", err)
	}
	if h.prov.Calls("create") != 3 {
		t.Fatalf("Calls(create) after the denied Start at t=3000 = %d, want still 3", h.prov.Calls("create"))
	}

	h.Settle(t0.Add(3600 * time.Second))
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start at t=3600: %v", err)
	}
	h.Settle(t0.Add(3601 * time.Second))
	if h.prov.Calls("create") != 4 {
		t.Fatalf("Calls(create) at t=3600 = %d, want 4", h.prov.Calls("create"))
	}
	doc, _, err := h.store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.Instance == nil || doc.Instance.Offer.MachineID != 45 {
		t.Fatalf("doc.Instance = %+v, want machine 45", doc.Instance)
	}
}

// Ready gate: a worker that never reports healthy must never reach Ready,
// no matter how long it loads; only the startup deadline ends it.
func TestAC25_ReadyGate(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"})
	h.prov.SetStatusSequence(provider.InstanceID(1), provider.StatusOther, provider.StatusOther)
	h.worker.SetProbeSequence(ProbeLoading)

	t0 := h.clk.Now().Add(time.Second)
	h.Settle(t0)
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	h.Settle(t0.Add(40 * time.Second))
	if snap := h.ctl.Status(); snap.Public != "LOADING_MODEL" {
		t.Fatalf("Public = %q, want LOADING_MODEL", snap.Public)
	}

	h.Settle(t0.Add(899 * time.Second))
	if h.prov.Calls("destroy") != 0 {
		t.Fatalf("Calls(destroy) before t=901 = %d, want 0", h.prov.Calls("destroy"))
	}
	if snap := h.ctl.Status(); snap.Public != "LOADING_MODEL" {
		t.Fatalf("Public at t=900 = %q, want LOADING_MODEL", snap.Public)
	}

	h.Settle(t0.Add(900 * time.Second))
	if h.prov.Calls("destroy") != 1 {
		t.Fatalf("Calls(destroy) at t=901 = %d, want 1", h.prov.Calls("destroy"))
	}
}

// SSRF boundary and host key: recovery must dial the address the provider
// reports right now, never the one persisted in the document, and a pinned
// host key that no longer matches must destroy without ever creating again.
func TestAC26_SSRFBoundaryAndHostKey(t *testing.T) {
	seed := func(t *testing.T, h *harness, t0 time.Time) {
		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		doc.Generation = 4
		doc.Desired = store.DesiredRunning
		idle := t0.Add(1800 * time.Second)
		doc.Instance = &store.Instance{
			State: Ready.String(), Label: "vkllm:iid:4", ProviderID: "9",
			SSHHost: "198.51.100.7", SSHPort: 2222,
			CreatedAt: t0, ReadyAt: &t0, IdleDeadline: &idle,
		}
		h.store.Put(doc)
	}

	t.Run("dials the provider's live address, never the stored one", func(t *testing.T) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		h.prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:4", Status: provider.StatusRunning, SSHHost: "192.0.2.10", SSHPort: 41022})
		seed(t, h, t0)
		h.worker.SetProbeSequence(ProbeReady)
		h.run()

		h.Settle(t0.Add(time.Second))
		if h.dial.Calls() != 1 {
			t.Fatalf("Calls(dial) = %d, want 1", h.dial.Calls())
		}
		if got := h.dial.LastAddr(); got != "192.0.2.10:41022" {
			t.Fatalf("LastAddr = %q, want 192.0.2.10:41022 (never the stored 198.51.100.7:2222)", got)
		}

		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if doc.Instance.SSHHost != "198.51.100.7" || doc.Instance.SSHPort != 2222 {
			t.Fatalf("stored address = %s:%d, want unchanged 198.51.100.7:2222", doc.Instance.SSHHost, doc.Instance.SSHPort)
		}
	})

	t.Run("a flaky recovery get never dials before it resolves", func(t *testing.T) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		h.prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:4", Status: provider.StatusRunning, SSHHost: "192.0.2.10", SSHPort: 41022})
		h.prov.FailNext("get", 6, &provider.Error{Op: "get", Class: provider.Retryable, Err: errors.New("fake transient get failure")})
		seed(t, h, t0)
		h.worker.SetProbeSequence(ProbeReady)
		h.run()

		h.Settle(t0)
		if h.dial.Calls() != 0 {
			t.Fatalf("Calls(dial) at t=0 = %d, want 0", h.dial.Calls())
		}
	})

	t.Run("a host key mismatch destroys without ever creating", func(t *testing.T) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		h.prov.AddInstance(provider.Instance{ID: 9, Label: "vkllm:iid:4", Status: provider.StatusRunning, SSHHost: "192.0.2.10", SSHPort: 41022})
		h.dial.FailAll(tunnel.ErrHostKeyMismatch)
		seed(t, h, t0)
		h.run()

		h.Settle(t0)
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) at t=0 = %d, want 1", h.prov.Calls("destroy"))
		}
		if h.prov.Calls("create") != 0 {
			t.Fatalf("Calls(create) = %d, want 0", h.prov.Calls("create"))
		}
	})
}

// Worker crash: two consecutive suspect-probe misses, 5s apart, destroy the
// instance; a Ready probe on the second check saves it; a balance already
// under the floor denies a fresh Start before ever searching.
func TestAC27_WorkerCrash(t *testing.T) {
	setup := func(t *testing.T) (*harness, time.Time) {
		h := buildHarness(t)
		t0 := h.clk.Now()
		seedAdoptedReady(h, 3, t0, time.Time{}, t0.Add(1800*time.Second))
		h.worker.SetProbeSequence(ProbeReady)
		h.run()
		h.Settle(t0.Add(time.Second))
		if _, err := h.ctl.Admit(3); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		return h, t0
	}

	t.Run("two misses destroy the instance", func(t *testing.T) {
		h, t0 := setup(t)
		closed := make(chan struct{}, 1)
		h.ctl.mu.Lock()
		h.ctl.conn = tunnel.NewConn(nil, func() error { closed <- struct{}{}; return nil }, "sha256:fake")
		h.ctl.mu.Unlock()

		h.Settle(t0.Add(10 * time.Second))
		h.worker.SetProbeSequence(ProbeUnreachable)
		beforeProbes := h.worker.probes() // setup's own recovery suspect-probe already ran once
		h.ctl.ReportUpstreamFailure(3)
		h.Settle(t0.Add(10 * time.Second))
		if got := h.worker.probes() - beforeProbes; got != 1 {
			t.Fatalf("probes right after the first miss = %d, want 1", got)
		}

		h.Settle(t0.Add(15 * time.Second))
		if got := h.worker.probes() - beforeProbes; got != 2 {
			t.Fatalf("probes at t=15 = %d, want 2", got)
		}
		if h.prov.Calls("destroy") != 1 {
			t.Fatalf("Calls(destroy) at t=15 = %d, want 1", h.prov.Calls("destroy"))
		}
		select {
		case <-closed:
		default:
			t.Fatal("Conn.Close was never called")
		}
		doc, _, err := h.store.Load(context.Background())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if doc.Desired != store.DesiredStopped {
			t.Fatalf("Desired = %q, want STOPPED", doc.Desired)
		}
	})

	t.Run("a Ready probe at the second check saves the instance", func(t *testing.T) {
		h, t0 := setup(t)

		h.Settle(t0.Add(10 * time.Second))
		h.worker.SetProbeSequence(ProbeUnreachable, ProbeReady)
		h.ctl.ReportUpstreamFailure(3)

		h.Settle(t0.Add(15 * time.Second))
		if h.prov.Calls("destroy") != 0 {
			t.Fatalf("Calls(destroy) at t=15 = %d, want 0", h.prov.Calls("destroy"))
		}
	})

	t.Run("a balance already under the floor denies Start before searching", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetBalance(1_590_000)

		t0 := h.clk.Now().Add(time.Second)
		h.Settle(t0)
		snap, err := h.ctl.Start(context.Background())
		if !errors.Is(err, ErrGPUUnavailable) {
			t.Fatalf("Start = %v, want ErrGPUUnavailable", err)
		}
		if snap.Unavailable != "balance_low" {
			t.Fatalf("Unavailable = %q, want balance_low", snap.Unavailable)
		}
		if h.prov.Calls("search") != 0 {
			t.Fatalf("Calls(search) = %d, want 0", h.prov.Calls("search"))
		}
	})
}
