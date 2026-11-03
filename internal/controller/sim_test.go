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
	calls   int64
	hostKey string
}

func newFakeDialerForHarness() *fakeDialerForHarness {
	return &fakeDialerForHarness{hostKey: "sha256:fake"}
}

func (d *fakeDialerForHarness) Dial(_ context.Context, _ tunnel.Instance) (*tunnel.Conn, error) {
	atomic.AddInt64(&d.calls, 1)
	return tunnel.NewConn(nil, func() error { return nil }, d.hostKey), nil
}

func (d *fakeDialerForHarness) Calls() int { return int(atomic.LoadInt64(&d.calls)) }

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
