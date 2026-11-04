package controller

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestRegisterMetricsOneHotGPUState(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	setGPUState(PublicState(Ready, false))

	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		want := 0.0
		if s == "READY" {
			want = 1.0
		}
		if got := testutil.ToFloat64(gpuState.WithLabelValues(s)); got != want {
			t.Errorf("gpuState{%q} = %v, want %v", s, got, want)
		}
	}
}

func TestShutdownsTotalIncrementsByReason(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	before := testutil.ToFloat64(shutdownsTotal.WithLabelValues("idle"))
	shutdownsTotal.WithLabelValues("idle").Inc()
	if got := testutil.ToFloat64(shutdownsTotal.WithLabelValues("idle")); got != before+1 {
		t.Fatalf("shutdownsTotal{idle} = %v, want %v", got, before+1)
	}
}

func TestShutdownReasonByTransition(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cfg := config.Controller{MaxLifetime: time.Hour, IdleShutdownEnabled: true}
	readyLongAgo := now.Add(-2 * time.Hour)
	instanceWith := func(idleDeadline *time.Time) store.Document {
		return store.Document{Instance: &store.Instance{State: Ready.String(), ReadyAt: &readyLongAgo, CreatedAt: readyLongAgo, IdleDeadline: idleDeadline}}
	}
	past, future := now.Add(-time.Minute), now.Add(time.Minute)

	cases := []struct {
		name        string
		state, next State
		ev          Event
		inFlight    int
		doc         store.Document
		cfg         config.Controller
		reason      string
		ok          bool
	}{
		{"T14 terminal before ready", Booting, Destroying, Event{Kind: EvProviderObserved, Status: provider.StatusExited}, 0, store.Document{}, config.Controller{}, "failure", true},
		{"T14 terminal while draining", Draining, Destroying, Event{Kind: EvProviderObserved, Status: provider.StatusExited}, 0, store.Document{}, config.Controller{}, "failure", true},
		{"T16 stop while booting", Booting, Destroying, Event{Kind: EvStop}, 0, store.Document{}, config.Controller{}, "manual", true},
		{"T16 budget while creating with id", Creating, Destroying, Event{Kind: EvBudgetExhausted}, 0, store.Document{}, config.Controller{}, "budget", true},
		{"T20 idle timer drains", Ready, Draining, Event{Kind: EvIdleTimer}, 0, instanceWith(&past), config.Controller{IdleShutdownEnabled: true}, "idle", true},
		{"T20/T21 tick, idle due and lifetime due: idle wins", Ready, Draining, Event{Kind: EvTick}, 0, instanceWith(&past), cfg, "idle", true},
		{"T21 tick, idle not due, lifetime due", Ready, Draining, Event{Kind: EvTick}, 0, instanceWith(&future), cfg, "lifetime", true},
		{"T21 lifetime drains regardless of inFlight", Serving, Draining, Event{Kind: EvTick}, 3, instanceWith(&future), cfg, "lifetime", true},
		{"T22 manual drain while serving", Serving, Draining, Event{Kind: EvStop}, 1, store.Document{}, config.Controller{}, "manual", true},
		{"T22 budget drain while ready", Ready, Draining, Event{Kind: EvBudgetExhausted}, 0, store.Document{}, config.Controller{}, "budget", true},
		{"T23 vanished after ready", Serving, Idle, Event{Kind: EvVanished}, 0, store.Document{}, config.Controller{}, "failure", true},
		{"T24 worker failed", Ready, Destroying, Event{Kind: EvWorkerFailed}, 0, store.Document{}, config.Controller{}, "failure", true},
		{"no match leaves state", Ready, Ready, Event{Kind: EvTick}, 0, store.Document{}, config.Controller{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ok := shutdownReason(c.state, c.next, c.ev, c.inFlight, c.doc, c.cfg, now)
			if reason != c.reason || ok != c.ok {
				t.Errorf("shutdownReason(%v->%v, %v) = (%q, %v), want (%q, %v)", c.state, c.next, c.ev.Kind, reason, ok, c.reason, c.ok)
			}
		})
	}
}

func TestProvisionOutcomeByTransition(t *testing.T) {
	cases := []struct {
		name        string
		state, next State
		ev          Event
		outcome     string
		ok          bool
	}{
		{"T6 no offer", Searching, Idle, Event{Kind: EvNoOffer}, "no_offer", true},
		{"T7 search error", Searching, Idle, Event{Kind: EvNoOffer, Err: errStale}, "provider_error", true},
		{"T7 stop during search", Searching, Idle, Event{Kind: EvStop}, "cancelled", true},
		{"T9 create failed", Creating, Idle, Event{Kind: EvCreateFailed}, "provider_error", true},
		{"T14 terminal before ready", Booting, Destroying, Event{Kind: EvProviderObserved, Status: provider.StatusExited}, "failed_status", true},
		{"T15 startup deadline", Loading, Destroying, Event{Kind: EvDeadlineExpired}, "deadline_exceeded", true},
		{"T16 stop while booting", Booting, Destroying, Event{Kind: EvStop}, "cancelled", true},
		{"T8 create lands after stop already flipped desired", Creating, Destroying, Event{Kind: EvCreated}, "cancelled", true},
		{"T27 vanished before ready", Booting, Idle, Event{Kind: EvVanished}, "lost", true},
		{"T32 health ok", Loading, Ready, Event{Kind: EvHealthOK}, "ok", true},
		{"T27 vanished while draining is not a provisioning attempt", Draining, Idle, Event{Kind: EvVanished}, "", false},
		{"no match leaves state", Ready, Ready, Event{Kind: EvTick}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			outcome, ok := provisionOutcome(c.state, c.next, c.ev)
			if outcome != c.outcome || ok != c.ok {
				t.Errorf("provisionOutcome(%v->%v, %v) = (%q, %v), want (%q, %v)", c.state, c.next, c.ev.Kind, outcome, ok, c.outcome, c.ok)
			}
		})
	}
}
