package controller

import (
	"errors"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestTransitionTable(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	t.Run("T3_start_while_provisioning_is_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, effects := decide(Booting, Event{Kind: EvStart}, Facts{Now: now, Doc: doc})
		if next != Booting || mutate != nil || effects != nil {
			t.Fatalf("T3: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T1_start_from_idle_writes_running_and_searches", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		cfg := config.Controller{MaxHourlyPriceUSD: 0.80}
		next, mutate, effects := decide(Idle, Event{Kind: EvStart}, Facts{Now: now, Doc: doc, Cfg: cfg})
		if next != Searching || mutate == nil || len(effects) != 1 {
			t.Fatalf("T1: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		if _, ok := effects[0].(effSearch); !ok {
			t.Fatalf("T1 effect = %T, want effSearch", effects[0])
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T1 mutate: %v", err)
		}
		if got.Desired != store.DesiredRunning || got.Instance != nil {
			t.Fatalf("T1 mutate result: desired=%q instance=%+v", got.Desired, got.Instance)
		}
	})

	t.Run("T1_tick_reaffirms_already_wanted", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, effects := decide(Idle, Event{Kind: EvTick}, Facts{Now: now, Doc: doc})
		if next != Searching || mutate == nil || len(effects) != 1 {
			t.Fatalf("T1 tick: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T1 tick mutate: %v", err)
		}
		if got.Desired != store.DesiredRunning {
			t.Fatalf("T1 tick mutate result: desired=%q", got.Desired)
		}
	})

	t.Run("T1_tick_without_wanted_is_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		next, mutate, effects := decide(Idle, Event{Kind: EvTick}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate != nil || effects != nil {
			t.Fatalf("T1 tick not-wanted: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T1_guard_restale_if_stop_won_the_race", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		_, mutate, _ := decide(Idle, Event{Kind: EvTick}, Facts{Now: now, Doc: doc})
		live := doc
		live.Desired = store.DesiredStopped // a concurrent T31 Stop committed first
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T1 mutate against a Stop that won the race: want errStale, got %v", err)
		}
		if live.Desired != store.DesiredStopped {
			t.Fatalf("T1 must not undo the Stop: desired=%q", live.Desired)
		}
	})

	t.Run("T2_start_denied_at_idle", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		next, mutate, effects := decide(Idle, Event{Kind: EvStart, Reason: "budget_exhausted"}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T2: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		got.Desired = store.DesiredRunning
		if err := mutate(&got); err != nil {
			t.Fatalf("T2 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped {
			t.Fatalf("T2 mutate result: desired=%q", got.Desired)
		}
	})

	t.Run("T2_start_denied_while_destroying", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 2
		doc.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:2"}
		next, mutate, _ := decide(Destroying, Event{Kind: EvStart, Reason: "balance_low"}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate == nil {
			t.Fatalf("T2 destroying: next=%v mutate=%v", next, mutate != nil)
		}
	})

	t.Run("backoff_start_at_idle_is_true_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		next, mutate, effects := decide(Idle, Event{Kind: EvStart, Reason: "backoff"}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate != nil || effects != nil {
			t.Fatalf("backoff start: next=%v mutate=%v effects=%v, want true no-op", next, mutate != nil, effects)
		}
	})

	t.Run("backoff_tick_at_idle_is_true_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, effects := decide(Idle, Event{Kind: EvTick, Reason: "backoff"}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate != nil || effects != nil {
			t.Fatalf("backoff tick: next=%v mutate=%v effects=%v, want true no-op", next, mutate != nil, effects)
		}
	})

	t.Run("backoff_never_triggers_t2_denial_cas", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 2
		doc.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:2"}
		next, mutate, effects := decide(Destroying, Event{Kind: EvStart, Reason: "backoff"}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate != nil || effects != nil {
			t.Fatalf("backoff at destroying: next=%v mutate=%v effects=%v, want true no-op, not T2's CAS", next, mutate != nil, effects)
		}
	})

	t.Run("T4_start_revives_creating_with_desired_stopped", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1"}
		next, mutate, effects := decide(Creating, Event{Kind: EvStart}, Facts{Now: now, Doc: doc})
		if next != Creating || mutate == nil || effects != nil {
			t.Fatalf("T4: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T4 mutate: %v", err)
		}
		if got.Desired != store.DesiredRunning {
			t.Fatalf("T4 mutate result: desired=%q", got.Desired)
		}
	})

	t.Run("T4_start_revives_draining", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Draining.String(), Label: "vkllm:iid:1"}
		next, mutate, _ := decide(Draining, Event{Kind: EvStart}, Facts{Now: now, Doc: doc})
		if next != Draining || mutate == nil {
			t.Fatalf("T4 draining: next=%v mutate=%v", next, mutate != nil)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T4 draining mutate: %v", err)
		}
		if got.Desired != store.DesiredRunning {
			t.Fatalf("T4 draining mutate result: desired=%q", got.Desired)
		}
	})

	t.Run("T4_guard_restale_if_generation_moved", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:1"}
		_, mutate, _ := decide(Destroying, Event{Kind: EvStart}, Facts{Now: now, Doc: doc})
		live := doc
		live.Generation = 2
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T4 guard: want errStale, got %v", err)
		}
	})

	t.Run("T4_guard_restale_if_stage_moved_on", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Draining.String(), Label: "vkllm:iid:1"}
		_, mutate, _ := decide(Draining, Event{Kind: EvStart}, Facts{Now: now, Doc: doc})
		live := doc
		live.Instance = &store.Instance{State: Destroying.String(), Label: "vkllm:iid:1"} // T25/T26 already advanced it
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T4 mutate against a stage that already moved on: want errStale, got %v", err)
		}
	})

	t.Run("T6_no_qualifying_offer", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, effects := decide(Searching, Event{Kind: EvNoOffer}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T6: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T6 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped {
			t.Fatalf("T6 mutate result: desired=%q", got.Desired)
		}
	})

	t.Run("T6_offer_chosen_but_gate_denied", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, _ := decide(Searching, Event{Kind: EvOfferChosen, Reason: "attempts_exhausted"}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil {
			t.Fatalf("T6 denied: next=%v mutate=%v", next, mutate != nil)
		}
	})

	t.Run("T7_stop_during_search", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, effects := decide(Searching, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T7 stop: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T7 stop mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped {
			t.Fatalf("T7 stop mutate result: desired=%q", got.Desired)
		}
	})

	t.Run("T7_search_error", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		next, mutate, effects := decide(Searching, Event{Kind: EvNoOffer, Err: errors.New("provider down")}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate != nil || effects != nil {
			t.Fatalf("T7 search error: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T7_offer_chosen_after_stop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredStopped
		next, mutate, effects := decide(Searching, Event{Kind: EvOfferChosen}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate != nil || effects != nil {
			t.Fatalf("T7 stale offer: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T5_offer_chosen_commits_before_create", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		offer := provider.Offer{ID: 11, MachineID: 42, Hourly: 600000, GPUName: "L40S"}
		next, mutate, effects := decide(Searching, Event{Kind: EvOfferChosen, Offer: offer}, Facts{Now: now, Doc: doc})
		if next != Creating || mutate == nil || len(effects) != 1 {
			t.Fatalf("T5: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		if _, ok := effects[0].(effCreate); !ok {
			t.Fatalf("T5 effect = %T, want effCreate", effects[0])
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T5 mutate: %v", err)
		}
		if got.Generation != 1 || got.Instance == nil || got.Instance.Label != "vkllm:iid:1" {
			t.Fatalf("T5 mutate result: gen=%d instance=%+v", got.Generation, got.Instance)
		}
		if len(got.Ops) != 1 || got.Ops[0].Key != "create-1" {
			t.Fatalf("T5 ops = %+v", got.Ops)
		}
	})

	t.Run("T5_guard_restale_if_instance_already_exists", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		_, mutate, _ := decide(Searching, Event{Kind: EvOfferChosen, Offer: provider.Offer{ID: 11}}, Facts{Now: now, Doc: doc})
		live := doc
		live.Instance = &store.Instance{} // another writer already created one
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T5 mutate against existing instance: want errStale, got %v", err)
		}
	})

	t.Run("T5_guard_restale_if_generation_moved", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		_, mutate, _ := decide(Searching, Event{Kind: EvOfferChosen, Offer: provider.Offer{ID: 11}}, Facts{Now: now, Doc: doc})
		live := doc
		live.Generation = 5
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T5 mutate against moved generation: want errStale, got %v", err)
		}
	})

	t.Run("T8_created_goes_to_booting", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1", Offer: store.Offer{DPH: 0.6}, CreatedAt: now}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now}}
		b := newFakeBudget(1, 30)
		next, mutate, effects := decide(Creating, Event{Kind: EvCreated, Gen: 1, ProviderID: provider.InstanceID(99)}, Facts{Now: now, Doc: doc, Budget: b})
		if next != Booting || mutate == nil || effects != nil {
			t.Fatalf("T8: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T8 mutate: %v", err)
		}
		if got.Instance.ProviderID != "99" || got.Instance.State != Booting.String() {
			t.Fatalf("T8 mutate result: %+v", got.Instance)
		}
		if got.Ops[0].Outcome != "created" || got.Ops[0].FinishedAt != nil {
			t.Fatalf("T8 op result: %+v, want created/not finished yet", got.Ops[0])
		}
		if len(got.Ledger.Entries) != 1 || got.Ledger.Entries[0].Gen != 1 {
			t.Fatalf("T8: OpenEntry not called, entries = %+v", got.Ledger.Entries)
		}
	})

	t.Run("T8_created_while_desired_stopped_goes_to_destroying", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredStopped
		doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1", CreatedAt: now}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now}}
		next, mutate, _ := decide(Creating, Event{Kind: EvCreated, Gen: 1, ProviderID: provider.InstanceID(5)}, Facts{Now: now, Doc: doc, Budget: newFakeBudget(1, 30)})
		if next != Destroying || mutate == nil {
			t.Fatalf("T8 cancelled: next=%v mutate=%v", next, mutate != nil)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T8 cancelled mutate: %v", err)
		}
		if got.Ops[0].Outcome != "cancelled" || got.Instance.State != Destroying.String() {
			t.Fatalf("T8 cancelled result: instance=%+v op=%+v", got.Instance, got.Ops[0])
		}
	})

	t.Run("T8_guard_noop_if_op_already_closed", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1"}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		next, mutate, effects := decide(Creating, Event{Kind: EvCreated, Gen: 1}, Facts{Now: now, Doc: doc})
		if next != Creating || mutate != nil || effects != nil {
			t.Fatalf("T8 already closed: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T8_guard_restale_if_stop_won_the_race", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning // snapshot: decide picks Booting
		doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1", CreatedAt: now}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now}}
		next, mutate, _ := decide(Creating, Event{Kind: EvCreated, Gen: 1, ProviderID: provider.InstanceID(9)}, Facts{Now: now, Doc: doc, Budget: newFakeBudget(1, 30)})
		if next != Booting {
			t.Fatalf("T8: next=%v, want Booting", next)
		}
		live := doc
		live.Desired = store.DesiredStopped // T17's Stop committed before this CAS ran
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T8 mutate against a Stop that won the race: want errStale, got %v", err)
		}
	})

	t.Run("T9_create_failed_terminal", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1"}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now}}
		next, mutate, effects := decide(Creating, Event{Kind: EvCreateFailed, Gen: 1, Class: provider.Terminal}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T9: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T9 mutate: %v", err)
		}
		if got.Instance != nil || got.Ops[0].Outcome != "create_error" || got.Desired != store.DesiredRunning {
			t.Fatalf("T9 mutate result: instance=%+v op=%+v desired=%q", got.Instance, got.Ops[0], got.Desired)
		}
	})

	t.Run("T10_create_failed_retryable_is_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Creating.String()}
		next, mutate, effects := decide(Creating, Event{Kind: EvCreateFailed, Class: provider.Retryable}, Facts{Now: now, Doc: doc})
		if next != Creating || mutate != nil || effects != nil {
			t.Fatalf("T10: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T11_create_failed_duplicate_label", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Creating.String()}
		next, mutate, effects := decide(Creating, Event{Kind: EvCreateFailed, Duplicate: true}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate != nil || effects != nil {
			t.Fatalf("T11: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T12_provider_observed_running_dials", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Booting.String(), HostKeyFingerprint: ""}
		next, mutate, effects := decide(Booting, Event{Kind: EvProviderObserved, Gen: 1, Status: provider.StatusRunning, SSHHost: "203.0.113.5", SSHPort: 41022}, Facts{Now: now, Doc: doc})
		if next != Booting || mutate == nil || len(effects) != 1 {
			t.Fatalf("T12: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		dial, ok := effects[0].(effDial)
		if !ok || dial.inst.Host != "203.0.113.5" || dial.inst.Port != 41022 {
			t.Fatalf("T12 effect = %+v", effects[0])
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T12 mutate: %v", err)
		}
		if got.Instance.SSHHost != "203.0.113.5" || got.Instance.SSHPort != 41022 {
			t.Fatalf("T12 mutate result: %+v", got.Instance)
		}
	})

	t.Run("T13_link_up_starts_loading", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Booting.String()}
		next, mutate, effects := decide(Booting, Event{Kind: EvLinkUp, Gen: 1, HostKey: "abcd"}, Facts{Now: now, Doc: doc})
		if next != Loading || mutate == nil || len(effects) != 1 {
			t.Fatalf("T13: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		if _, ok := effects[0].(effProbe); !ok {
			t.Fatalf("T13 effect = %T, want effProbe", effects[0])
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T13 mutate: %v", err)
		}
		if got.Instance.State != Loading.String() || got.Instance.HostKeyFingerprint != "abcd" {
			t.Fatalf("T13 mutate result: %+v", got.Instance)
		}
	})

	t.Run("T14_terminal_status_before_ready_destroys", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Loading.String()}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		next, mutate, effects := decide(Loading, Event{Kind: EvProviderObserved, Gen: 1, Status: provider.StatusExited}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate == nil || effects != nil {
			t.Fatalf("T14 before ready: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T14 before ready mutate: %v", err)
		}
		if got.Ops[0].Outcome != "failed_status" || got.Desired != store.DesiredRunning {
			t.Fatalf("T14 before ready result: op=%+v desired=%q", got.Ops[0], got.Desired)
		}
	})

	t.Run("T14_terminal_status_after_ready_stops_desired", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Ready.String()}
		next, mutate, _ := decide(Ready, Event{Kind: EvProviderObserved, Gen: 1, Status: provider.StatusOffline}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate == nil {
			t.Fatalf("T14 after ready: next=%v mutate=%v", next, mutate != nil)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T14 after ready mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped {
			t.Fatalf("T14 after ready result: desired=%q", got.Desired)
		}
	})

	t.Run("T15_startup_timeout_destroys_and_denylists", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Booting.String(), Offer: store.Offer{MachineID: 42}, CreatedAt: now.Add(-16 * time.Minute)}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		cfg := config.Controller{StartupTimeout: 15 * time.Minute}
		next, mutate, effects := decide(Booting, Event{Kind: EvDeadlineExpired, Gen: 1}, Facts{Now: now, Doc: doc, Cfg: cfg})
		if next != Destroying || mutate == nil || effects != nil {
			t.Fatalf("T15: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T15 mutate: %v", err)
		}
		if got.Ops[0].Outcome != "deadline" || len(got.MachineDenylist) != 1 || got.MachineDenylist[0].MachineID != 42 {
			t.Fatalf("T15 result: op=%+v denylist=%+v", got.Ops[0], got.MachineDenylist)
		}
		if !got.MachineDenylist[0].Until.Equal(now.Add(time.Hour)) {
			t.Fatalf("T15 denylist until = %v, want %v", got.MachineDenylist[0].Until, now.Add(time.Hour))
		}
	})

	t.Run("T15_guard_restale_if_not_actually_expired", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Booting.String(), Offer: store.Offer{MachineID: 42}, CreatedAt: now.Add(-16 * time.Minute)}
		cfg := config.Controller{StartupTimeout: 15 * time.Minute}
		_, mutate, _ := decide(Booting, Event{Kind: EvDeadlineExpired, Gen: 1}, Facts{Now: now, Doc: doc, Cfg: cfg})
		live := doc
		restarted := now.Add(-time.Minute)
		live.Instance = &store.Instance{State: Booting.String(), Offer: store.Offer{MachineID: 42}, CreatedAt: restarted} // a redial reset the clock
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T15 mutate against a timer that no longer applies: want errStale, got %v", err)
		}
	})

	t.Run("T16_stop_with_id_known_destroys", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Booting.String(), ProviderID: "9"}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		next, mutate, effects := decide(Booting, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate == nil || effects != nil {
			t.Fatalf("T16: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T16 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped || got.Instance.State != Destroying.String() || got.Ops[0].Outcome != "cancelled" {
			t.Fatalf("T16 result: instance=%+v op=%+v desired=%q", got.Instance, got.Ops[0], got.Desired)
		}
	})

	t.Run("T16_budget_exhausted_creating_with_id", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Creating.String(), ProviderID: "9"}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		next, mutate, _ := decide(Creating, Event{Kind: EvBudgetExhausted}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate == nil {
			t.Fatalf("T16 budget: next=%v mutate=%v", next, mutate != nil)
		}
	})

	t.Run("T17_stop_before_id_known_stays_creating", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Creating.String(), ProviderID: ""}
		next, mutate, effects := decide(Creating, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		if next != Creating || mutate == nil || effects != nil {
			t.Fatalf("T17: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T17 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped || got.Instance.State != Creating.String() {
			t.Fatalf("T17 result: instance=%+v desired=%q", got.Instance, got.Desired)
		}
	})

	t.Run("T17_guard_restale_if_id_arrived_meanwhile", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Creating.String(), ProviderID: ""}
		_, mutate, _ := decide(Creating, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		live := doc
		live.Instance = &store.Instance{State: Booting.String(), ProviderID: "9"} // T8 adopted an id first
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T17 mutate against an id that arrived meanwhile: want errStale, got %v", err)
		}
	})

	t.Run("T18_T19_admit_and_requestend_are_not_decide_rows", func(t *testing.T) {
		t.Skip("Admit/RequestEnd act directly under the admission mutex (R16); decide has no EventKind for them")
	})

	t.Run("T20_idle_deadline_reached_drains", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		past := now.Add(-time.Second)
		doc.Instance = &store.Instance{State: Ready.String(), IdleDeadline: &past}
		next, mutate, effects := decide(Ready, Event{Kind: EvIdleTimer}, Facts{Now: now, Doc: doc, InFlight: 0})
		if next != Draining || mutate == nil || effects != nil {
			t.Fatalf("T20: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T20 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped || got.Instance.State != Draining.String() {
			t.Fatalf("T20 mutate result: desired=%q state=%q", got.Desired, got.Instance.State)
		}
	})

	t.Run("T20_guard_restale_if_deadline_moved", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		past := now.Add(-time.Second)
		doc.Instance = &store.Instance{State: Ready.String(), IdleDeadline: &past}
		_, mutate, _ := decide(Ready, Event{Kind: EvIdleTimer}, Facts{Now: now, Doc: doc, InFlight: 0})
		live := doc
		future := now.Add(time.Hour)
		live.Instance.IdleDeadline = &future // deadline moved by a RequestEnd between decide and the CAS
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T20 mutate against moved deadline: want errStale, got %v", err)
		}
	})

	t.Run("T20_guard_restale_if_stage_moved_on", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Desired = store.DesiredRunning
		past := now.Add(-time.Second)
		doc.Instance = &store.Instance{State: Ready.String(), IdleDeadline: &past}
		_, mutate, _ := decide(Ready, Event{Kind: EvIdleTimer}, Facts{Now: now, Doc: doc, InFlight: 0})
		live := doc
		live.Instance = &store.Instance{State: Destroying.String(), IdleDeadline: &past} // T24 already destroyed it
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T20 mutate against a stage that already moved on: want errStale, got %v", err)
		}
	})

	t.Run("T21_max_lifetime_drains_regardless_of_inflight", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Ready.String(), CreatedAt: now.Add(-13 * time.Hour)}
		cfg := config.Controller{MaxLifetime: 12 * time.Hour}
		next, mutate, effects := decide(Serving, Event{Kind: EvTick}, Facts{Now: now, Doc: doc, InFlight: 3, Cfg: cfg})
		if next != Draining || mutate == nil || effects != nil {
			t.Fatalf("T21: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T21 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped || got.Instance.State != Draining.String() {
			t.Fatalf("T21 result: desired=%q state=%q", got.Desired, got.Instance.State)
		}
	})

	t.Run("T21_guard_not_fired_before_lifetime", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Ready.String(), CreatedAt: now.Add(-1 * time.Hour)}
		cfg := config.Controller{MaxLifetime: 12 * time.Hour}
		next, mutate, effects := decide(Ready, Event{Kind: EvTick}, Facts{Now: now, Doc: doc, Cfg: cfg})
		if next != Ready || mutate != nil || effects != nil {
			t.Fatalf("T21 not yet: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T21_guard_restale_if_stage_moved_on", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Serving.String(), CreatedAt: now.Add(-13 * time.Hour)}
		cfg := config.Controller{MaxLifetime: 12 * time.Hour}
		_, mutate, _ := decide(Serving, Event{Kind: EvTick}, Facts{Now: now, Doc: doc, Cfg: cfg})
		live := doc
		live.Instance = &store.Instance{State: Draining.String(), CreatedAt: now.Add(-13 * time.Hour)} // T20 already drained it
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T21 mutate against a stage that already moved on: want errStale, got %v", err)
		}
	})

	t.Run("T22_stop_while_serving_drains", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Ready.String()}
		next, mutate, effects := decide(Serving, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		if next != Draining || mutate == nil || effects != nil {
			t.Fatalf("T22: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T22 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped || got.Instance.State != Draining.String() {
			t.Fatalf("T22 result: desired=%q state=%q", got.Desired, got.Instance.State)
		}
	})

	t.Run("T22_guard_restale_if_stage_moved_on", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Ready.String()}
		_, mutate, _ := decide(Serving, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		live := doc
		live.Instance = &store.Instance{State: Destroying.String()} // a concurrent T24 already committed
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T22 mutate against a stage that already moved on: want errStale, got %v", err)
		}
	})

	t.Run("T23_vanished_after_ready_returns_to_idle", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 3
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Ready.String()}
		b := newFakeBudget(1, 30)
		next, mutate, effects := decide(Ready, Event{Kind: EvVanished, Gen: 3}, Facts{Now: now, Doc: doc, Budget: b})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T23: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T23 mutate: %v", err)
		}
		if got.Instance != nil || got.Desired != store.DesiredStopped {
			t.Fatalf("T23 result: instance=%+v desired=%q", got.Instance, got.Desired)
		}
	})

	t.Run("T24_worker_failed_destroys", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 3
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Ready.String(), Label: "vkllm:iid:3"}
		next, mutate, effects := decide(Ready, Event{Kind: EvWorkerFailed, Gen: 3, Err: errors.New("probe failed")}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate == nil || effects != nil {
			t.Fatalf("T24: next=%v mutate=%v effects=%v, want Destroying/non-nil/nil (closing the Conn is Task 6's generic hook)", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T24 mutate: %v", err)
		}
		if got.Desired != store.DesiredStopped || got.Instance.State != Destroying.String() || got.Instance.LastError == "" {
			t.Fatalf("T24 mutate result: %+v", got.Instance)
		}
	})

	t.Run("T25_drain_finished_once_inflight_zero", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Draining.String()}
		next, mutate, effects := decide(Draining, Event{Kind: EvTick}, Facts{Now: now, Doc: doc, InFlight: 0})
		if next != Destroying || mutate == nil || effects != nil {
			t.Fatalf("T25: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T25_guard_not_fired_with_inflight", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Draining.String()}
		next, mutate, effects := decide(Draining, Event{Kind: EvTick}, Facts{Now: now, Doc: doc, InFlight: 2})
		if next != Draining || mutate != nil || effects != nil {
			t.Fatalf("T25 guard: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T26_drain_deadline_expired", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Draining.String()}
		next, mutate, effects := decide(Draining, Event{Kind: EvDeadlineExpired}, Facts{Now: now, Doc: doc, InFlight: 4})
		if next != Destroying || mutate == nil || effects != nil {
			t.Fatalf("T26: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T25_T26_guard_restale_if_stage_moved_on", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Draining.String()}
		_, mutate, _ := decide(Draining, Event{Kind: EvDeadlineExpired}, Facts{Now: now, Doc: doc})
		live := doc
		live.Instance = &store.Instance{State: Destroying.String()} // T28's retry timer already moved it on
		if err := mutate(&live); !errors.Is(err, errStale) {
			t.Fatalf("T25/T26 mutate against a stage that already moved on: want errStale, got %v", err)
		}
	})

	t.Run("T27_vanished_before_ready_keeps_desired", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredRunning
		doc.Instance = &store.Instance{State: Loading.String()}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		next, mutate, effects := decide(Loading, Event{Kind: EvVanished, Gen: 1}, Facts{Now: now, Doc: doc, Budget: newFakeBudget(1, 30)})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T27: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T27 mutate: %v", err)
		}
		if got.Instance != nil || got.Ops[0].Outcome != "lost" || got.Desired != store.DesiredRunning {
			t.Fatalf("T27 result: instance=%+v op=%+v desired=%q", got.Instance, got.Ops[0], got.Desired)
		}
	})

	t.Run("T27_vanished_while_draining", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Desired = store.DesiredStopped
		doc.Instance = &store.Instance{State: Draining.String()}
		next, mutate, _ := decide(Draining, Event{Kind: EvVanished, Gen: 1}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil {
			t.Fatalf("T27 draining: next=%v mutate=%v", next, mutate != nil)
		}
	})

	t.Run("T28_T30_destroying_tick_is_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Destroying.String()}
		next, mutate, effects := decide(Destroying, Event{Kind: EvTick}, Facts{Now: now, Doc: doc})
		if next != Destroying || mutate != nil || effects != nil {
			t.Fatalf("T28/T30: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T29_gone_returns_to_idle", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 4
		doc.Desired = store.DesiredRunning // T4 revived it mid-destroy
		doc.Instance = &store.Instance{State: Destroying.String()}
		doc.Ops = []store.Op{{Key: "destroy-4", StartedAt: now}}
		b := newFakeBudget(1, 30)
		next, mutate, effects := decide(Destroying, Event{Kind: EvGone, Gen: 4}, Facts{Now: now, Doc: doc, Budget: b})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T29: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T29 mutate: %v", err)
		}
		if got.Instance != nil || got.Desired != store.DesiredRunning {
			t.Fatalf("T29 result: instance=%+v desired=%q", got.Instance, got.Desired)
		}
		if got.Ops[0].Outcome != "ok" || got.Ops[0].FinishedAt == nil {
			t.Fatalf("T29 destroy op result: %+v", got.Ops[0])
		}
	})

	t.Run("T31_stop_at_idle_keeps_desired_stopped", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		next, mutate, effects := decide(Idle, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate == nil || effects != nil {
			t.Fatalf("T31: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
	})

	t.Run("T31_stop_while_draining", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Instance = &store.Instance{State: Draining.String()}
		next, mutate, _ := decide(Draining, Event{Kind: EvStop}, Facts{Now: now, Doc: doc})
		if next != Draining || mutate == nil {
			t.Fatalf("T31 draining: next=%v mutate=%v", next, mutate != nil)
		}
	})

	t.Run("T32_health_ok_arms_idle_deadline", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Loading.String()}
		doc.Ops = []store.Op{{Key: "create-1", StartedAt: now, Outcome: "created"}}
		cfg := config.Controller{IdleShutdownEnabled: true, IdleShutdownTimeout: 30 * time.Minute}
		next, mutate, effects := decide(Loading, Event{Kind: EvHealthOK, Gen: 1}, Facts{Now: now, Doc: doc, Cfg: cfg})
		if next != Ready || mutate == nil || effects != nil {
			t.Fatalf("T32: next=%v mutate=%v effects=%v", next, mutate != nil, effects)
		}
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T32 mutate: %v", err)
		}
		if got.Instance.State != Ready.String() || got.Instance.ReadyAt == nil || got.Instance.IdleDeadline == nil {
			t.Fatalf("T32 result: %+v", got.Instance)
		}
		if !got.Instance.IdleDeadline.Equal(now.Add(30 * time.Minute)) {
			t.Fatalf("T32 idleDeadline = %v, want %v", got.Instance.IdleDeadline, now.Add(30*time.Minute))
		}
		if got.Ops[0].Outcome != "ready" {
			t.Fatalf("T32 op = %+v", got.Ops[0])
		}
	})

	t.Run("T32_idle_shutdown_disabled_leaves_deadline_nil", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		doc.Generation = 1
		doc.Instance = &store.Instance{State: Loading.String()}
		cfg := config.Controller{IdleShutdownEnabled: false}
		_, mutate, _ := decide(Loading, Event{Kind: EvHealthOK, Gen: 1}, Facts{Now: now, Doc: doc, Cfg: cfg})
		got := doc
		if err := mutate(&got); err != nil {
			t.Fatalf("T32 disabled mutate: %v", err)
		}
		if got.Instance.IdleDeadline != nil {
			t.Fatalf("T32 disabled: idleDeadline = %v, want nil", got.Instance.IdleDeadline)
		}
	})

	t.Run("unlisted_pair_is_noop", func(t *testing.T) {
		doc, _ := store.NewDocument("iid")
		next, mutate, effects := decide(Idle, Event{Kind: EvHealthOK}, Facts{Now: now, Doc: doc})
		if next != Idle || mutate != nil || effects != nil {
			t.Fatalf("unlisted pair: next=%v mutate=%v effects=%v, want Idle/nil/nil", next, mutate != nil, effects)
		}
	})
}
