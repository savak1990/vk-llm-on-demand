package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func offersOnMachines(n int) []provider.Offer {
	out := make([]provider.Offer, n)
	for i := range out {
		out[i] = provider.Offer{ID: int64(21 + i), MachineID: int64(101 + i), Hourly: provider.Money(400000 + 50000*i), GPUName: "L40S"}
	}
	return out
}

func TestRefusedOffersTryNextAtOnce(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(offersOnMachines(3)...)
	h.prov.RefuseCreate(21, 22)
	h.worker.SetProbeSequence(ProbeReady)
	refused, perr := outcomeCounts()

	t0 := h.clk.Now()
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(time.Second))
	if r, p := outcomeCounts(); r-refused != 2 || p != perr {
		t.Fatalf("outcome deltas refused=%v provider_error=%v, want 2 and 0 so refusals never page", r-refused, p-perr)
	}
	if n := h.prov.Calls("create"); n != 3 {
		t.Fatalf("creates after 1s = %d, want 3 with no backoff between them", n)
	}
	h.Settle(t0.Add(50 * time.Second))
	doc, _, _ := h.store.Load(context.Background())
	if doc.Instance == nil || doc.Instance.ReadyAt == nil || doc.Instance.Offer.MachineID != 103 {
		t.Fatalf("instance %+v, want READY on machine 103", doc.Instance)
	}
	if len(doc.MachineDenylist) != 2 {
		t.Fatalf("denylist %+v, want machines 101 and 102", doc.MachineDenylist)
	}
	for i, e := range doc.MachineDenylist {
		if e.MachineID != int64(101+i) || e.Until.Sub(t0) < time.Hour || e.Until.Sub(t0) > time.Hour+time.Second {
			t.Fatalf("denylist[%d] = %+v, want machine %d until now+1h", i, e, 101+i)
		}
	}
	if doc.AttemptsThisHour.Count != 1 || doc.RefusedInRow != 0 {
		t.Fatalf("AttemptsThisHour = %+v run %d, want only the real create counted and the run reset", doc.AttemptsThisHour, doc.RefusedInRow)
	}
}

func outcomeCounts() (refused, providerError float64) {
	return testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("refused")),
		testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("provider_error"))
}

// A run cut short by no_offer, or paused by an ambiguous failure's backoff,
// gets a fresh budget of 5 refusals from the next Start.
func TestRefusedRunResetsOnStart(t *testing.T) {
	t.Run("after no_offer", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetOffers(offersOnMachines(3)...)
		h.prov.RefuseCreate(21, 22, 23)
		t0 := h.clk.Now()
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(time.Second))
		if n, s := h.prov.Calls("create"), h.ctl.Status(); n != 3 || s.Unavailable != "no_offer" {
			t.Fatalf("creates %d snapshot %+v, want 3 then no_offer", n, s)
		}
		h.prov.SetOffers(offersOnMachines(9)[3:]...)
		h.prov.RefuseCreate(24, 25, 26, 27, 28)
		h.Settle(t0.Add(90 * time.Second))
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("second Start: %v", err)
		}
		h.Settle(t0.Add(5 * time.Minute))
		if n, s := h.prov.Calls("create"), h.ctl.Status(); n != 8 || s.Unavailable != "offers_refused" {
			t.Fatalf("creates %d snapshot %+v, want 5 more refusals then offers_refused", n, s)
		}
	})
	t.Run("during backoff", func(t *testing.T) {
		h := newHarness(t)
		h.prov.SetOffers(offersOnMachines(9)...)
		h.prov.RefuseCreate(21, 22)
		h.prov.FailNext("create", 1, &provider.Error{Op: "create", Class: provider.Terminal, Status: 200, Err: errors.New("create rejected")})
		t0 := h.clk.Now()
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		h.Settle(t0.Add(time.Second))
		if n, s := h.prov.Calls("create"), h.ctl.Status(); n != 3 || s.Desired != store.DesiredRunning {
			t.Fatalf("creates %d snapshot %+v, want 2 refusals, an ambiguous failure, desired kept", n, s)
		}
		h.prov.RefuseCreate(23, 24, 25, 26, 27)
		if _, err := h.ctl.Start(context.Background()); err != nil {
			t.Fatalf("Start in backoff: %v", err)
		}
		h.Settle(t0.Add(5 * time.Minute))
		if n, s := h.prov.Calls("create"), h.ctl.Status(); n != 8 || s.Unavailable != "offers_refused" {
			t.Fatalf("creates %d snapshot %+v, want 5 more refusals then offers_refused", n, s)
		}
	})
}

func TestRefusedOffersStopAfterFive(t *testing.T) {
	h := newHarness(t)
	h.prov.SetOffers(offersOnMachines(6)...)
	h.prov.RefuseCreate(21, 22, 23, 24, 25)
	h.worker.SetProbeSequence(ProbeReady)
	refused, perr := outcomeCounts()
	ended := testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("offers_refused"))

	t0 := h.clk.Now()
	if _, err := h.ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.Settle(t0.Add(5 * time.Minute))
	if n := h.prov.Calls("create"); n != 5 {
		t.Fatalf("creates = %d, want 5 and the 6th offer untried", n)
	}
	r, p := outcomeCounts()
	if e := testutil.ToFloat64(provisionAttemptsTotal.WithLabelValues("offers_refused")) - ended; r-refused != 4 || p != perr || e != 1 {
		t.Fatalf("outcome deltas refused=%v provider_error=%v offers_refused=%v, want 4, 0, 1", r-refused, p-perr, e)
	}
	snap := h.ctl.Status()
	if snap.State != Idle || snap.Desired != store.DesiredStopped || snap.Unavailable != "offers_refused" {
		t.Fatalf("snapshot %+v, want Idle, STOPPED, offers_refused", snap)
	}
	doc, _, _ := h.store.Load(context.Background())
	if len(doc.MachineDenylist) != 5 || doc.AttemptsThisHour.Count != 0 || doc.RefusedInRow != 0 {
		t.Fatalf("denylist %d attempts %+v run %d, want 5, 0, 0", len(doc.MachineDenylist), doc.AttemptsThisHour, doc.RefusedInRow)
	}
}

// The volume retry runs first; only when the offer itself is refused does
// the controller move to the next machine.
func TestRefusedOfferWithSeedVolumeMovesOn(t *testing.T) {
	h := newCacheHarness(t)
	h.prov.SetOffers(
		provider.Offer{ID: 21, MachineID: 101, Hourly: 400000, GPUName: "L40S", InetDownMbps: 2400},
		provider.Offer{ID: 22, MachineID: 102, Hourly: 450000, GPUName: "L40S", InetDownMbps: 2400},
	)
	h.prov.SetVolumeOffers(provider.VolumeOffer{ID: 900, MachineID: 101, StoragePerGBMonth: provider.FromUSD(0.30), DiskSpaceGB: 500})
	h.prov.RefuseCreate(21)

	doc := h.startReady(h.clk.Now().Add(time.Second))
	if n := h.prov.Calls("create"); n != 3 || doc.Instance.Offer.MachineID != 102 {
		t.Fatalf("creates %d machine %d, want the volume retry then machine 102", n, doc.Instance.Offer.MachineID)
	}
	if len(doc.MachineDenylist) != 1 || doc.MachineDenylist[0].MachineID != 101 {
		t.Fatalf("denylist %+v, want machine 101", doc.MachineDenylist)
	}
	if v := doc.Cache.Volumes[0]; v.MachineID != 101 || v.DeleteReason != "seed_failed" {
		t.Fatalf("volume %+v, want the refused seed released as seed_failed", v)
	}
}

func TestRefusedOpsDoNotBackOff(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ops := []store.Op{
		{Key: "create-1", Outcome: "ready", FinishedAt: &at},
		{Key: "create-2", Outcome: "refused", FinishedAt: &at},
		{Key: "create-3", Outcome: "refused", FinishedAt: &at},
	}
	if got := nextCreateAllowedAt(ops); !got.IsZero() {
		t.Fatalf("after refusals only: %v, want no backoff", got)
	}
	ops = append(ops, store.Op{Key: "create-4", Outcome: "create_error", FinishedAt: &at})
	if got := nextCreateAllowedAt(ops); !got.Equal(at.Add(time.Minute)) {
		t.Fatalf("after one real failure: %v, want the 1-minute backoff", got)
	}
}

func TestCreateFailureOnlyRefusalMovesOn(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 30, 0, time.UTC)
	for _, c := range []struct {
		name    string
		class   provider.Class
		err     error
		next    State
		refused bool
		stopped bool
	}{
		{"400 refused", provider.Terminal, &provider.Error{Op: "create", Class: provider.Terminal, Status: 400}, Searching, true, false},
		{"404 refused", provider.Terminal, &provider.Error{Op: "create", Class: provider.Terminal, Status: 404}, Searching, true, false},
		{"400 refused after a stop", provider.Terminal, &provider.Error{Op: "create", Class: provider.Terminal, Status: 400}, Idle, true, true},
		{"401", provider.Fatal, &provider.Error{Op: "create", Class: provider.Fatal, Status: 401}, Idle, false, false},
		{"403", provider.Fatal, &provider.Error{Op: "create", Class: provider.Fatal, Status: 403}, Idle, false, false},
		{"2xx without an id", provider.Terminal, &provider.Error{Op: "create", Class: provider.Terminal, Status: 200}, Idle, false, false},
		{"503", provider.Retryable, &provider.Error{Op: "create", Class: provider.Retryable, Status: 503}, Creating, false, false},
		{"429", provider.Retryable, &provider.Error{Op: "create", Class: provider.Retryable, Status: 429}, Creating, false, false},
		{"404 from the pre-create list", provider.Terminal, &provider.Error{Op: "list", Class: provider.Terminal, Status: 404}, Idle, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc, _ := store.NewDocument("iid")
			doc.Generation = 1
			doc.Desired = store.DesiredRunning
			if c.stopped {
				doc.Desired = store.DesiredStopped
			}
			doc.Instance = &store.Instance{State: Creating.String(), Label: "vkllm:iid:1", Offer: store.Offer{MachineID: 7}}
			doc.Ops = []store.Op{{Key: "create-1", StartedAt: now}}
			doc.AttemptsThisHour = store.Attempts{Hour: "2026-11-01T00", Count: 1}
			next, mutate, effects := decide(Creating, Event{Kind: EvCreateFailed, Gen: 1, Class: c.class, Err: c.err}, Facts{Now: now, Doc: doc})
			if next != c.next {
				t.Fatalf("next = %v, want %v", next, c.next)
			}
			got := doc
			if mutate != nil {
				if err := mutate(&got); err != nil {
					t.Fatalf("mutate: %v", err)
				}
			}
			refused := c.refused
			if c.next == Searching {
				s, ok := effects[0].(effSearch)
				if len(effects) != 1 || !ok || len(s.filter.ExcludeMachineIDs) != 1 || s.filter.ExcludeMachineIDs[0] != 7 {
					t.Fatalf("effects %+v, want one search excluding machine 7", effects)
				}
			} else if len(effects) != 0 {
				t.Fatalf("effects %+v, want none", effects)
			}
			if (len(got.MachineDenylist) == 1) != refused || (got.AttemptsThisHour.Count == 0) != refused {
				t.Fatalf("denylist %+v attempts %+v, want denylisted and uncharged only when refused", got.MachineDenylist, got.AttemptsThisHour)
			}
		})
	}
}
