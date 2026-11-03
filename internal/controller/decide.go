package controller

import (
	"fmt"
	"math"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

// decide is the pure heart of the state machine: given the current
// state, one event, and a snapshot of the facts that matter, it
// returns the next state, an optional CAS mutation, and the effects to
// dispatch after that CAS commits (or immediately, if mutate is nil).
func decide(state State, ev Event, f Facts) (next State, mutate func(*store.Document) error, effects []Effect) {
	switch {
	// T3: a Start while already provisioning or serving, with
	// desired already RUNNING, changes nothing — not even a no-op CAS.
	case isProvisioningOrServing(state) && ev.Kind == EvStart && f.Doc.Desired == store.DesiredRunning:
		return state, nil, nil

	// A backoff wait at Idle is a pacing delay, not a denial: a Tick
	// or Gone must not search again until it elapses. A Start still
	// records the desire right away, with no search of its own, so
	// the next eligible Tick starts the search on its own.
	case state == Idle && (ev.Kind == EvTick || ev.Kind == EvGone) && ev.Reason == "backoff":
		return state, nil, nil
	case state == Idle && ev.Kind == EvStart && ev.Reason == "backoff":
		return Idle, func(d *store.Document) error {
			if d.Instance != nil {
				return errStale
			}
			d.Desired = store.DesiredRunning
			return nil
		}, nil

	// T2: Start denied by the gate, or a stale wanted Tick/Gone newly
	// denied while Idle — write desired=STOPPED (idempotent if already
	// stopped) and deny; the caller maps the denial to its own error.
	case (state == Idle || state == Draining || state == Destroying) && ev.Reason != "" && ev.Reason != "backoff" &&
		(ev.Kind == EvStart || ((ev.Kind == EvTick || ev.Kind == EvGone) && state == Idle && f.Doc.Desired == store.DesiredRunning)):
		return state, func(d *store.Document) error {
			if state == Idle && d.Instance != nil {
				return errStale
			}
			d.Desired = store.DesiredStopped
			return nil
		}, nil

	// T1: Start always supplies desired=RUNNING; Tick/Gone only
	// re-affirm it when it is already set. The CAS is a harmless
	// no-op in the latter case (Transition skips unchanged writes).
	case state == Idle && ev.Reason == "" &&
		(ev.Kind == EvStart || ((ev.Kind == EvTick || ev.Kind == EvGone) && f.Doc.Desired == store.DesiredRunning)):
		return Searching, func(d *store.Document) error {
			if d.Instance != nil {
				return errStale
			}
			if ev.Kind != EvStart && d.Desired != store.DesiredRunning {
				return errStale
			}
			d.Desired = store.DesiredRunning
			return nil
		}, []Effect{effSearch{filter: searchFilter(f)}}

	// T4: Start revives a not-yet-destroyed instance (Creating still
	// desired=STOPPED, or Draining/Destroying); a gate denial is
	// already handled above for the states where that's possible.
	case ((state == Creating && f.Doc.Desired == store.DesiredStopped) || state == Draining || state == Destroying) &&
		ev.Kind == EvStart:
		return state, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != f.Doc.Generation {
				return errStale
			}
			if d.Instance.State != state.String() {
				return errStale
			}
			d.Desired = store.DesiredRunning
			return nil
		}, nil

	// T6: no qualifying offer, or the chosen offer arrived already
	// denied by the gate — give up this round, desired=STOPPED.
	case state == Searching && ((ev.Kind == EvNoOffer && ev.Err == nil) || (ev.Kind == EvOfferChosen && ev.Reason != "")):
		return Idle, func(d *store.Document) error {
			d.Desired = store.DesiredStopped
			return nil
		}, nil

	// T7: a manual Stop during the search, or the search itself
	// erroring, or a chosen offer arriving after Stop already won —
	// back to Idle; only Stop writes a CAS.
	case state == Searching && ev.Kind == EvStop:
		return Idle, func(d *store.Document) error {
			if d.Instance != nil {
				return errStale
			}
			d.Desired = store.DesiredStopped
			return nil
		}, nil
	case state == Searching && ev.Kind == EvNoOffer && ev.Err != nil:
		return Idle, nil, nil
	case state == Searching && ev.Kind == EvOfferChosen && f.Doc.Desired == store.DesiredStopped:
		return Idle, nil, nil

	// T5: an offer was chosen while Searching — commit the op and the
	// label before any provider call, so a crash after this CAS never
	// leaves a create in flight that nothing on record is tracking.
	case state == Searching && ev.Kind == EvOfferChosen && f.Doc.Desired == store.DesiredRunning:
		nextGen := f.Doc.Generation + 1
		label := fmt.Sprintf("vkllm:%s:%d", f.Doc.InstallationID, nextGen)
		return Creating, func(d *store.Document) error {
			if d.Desired != store.DesiredRunning || d.Instance != nil || d.Generation != f.Doc.Generation {
				return errStale
			}
			d.Generation++
			d.Instance = &store.Instance{
				State: Creating.String(), Label: label,
				Offer:     store.Offer{MachineID: ev.Offer.MachineID, GPU: ev.Offer.GPUName, DPH: ev.Offer.Hourly.USD(), InetDown: ev.Offer.InetDownMBps},
				CreatedAt: f.Now,
			}
			d.Ops = append(d.Ops, store.Op{Key: fmt.Sprintf("create-%d", d.Generation), StartedAt: f.Now})
			hour := f.Now.UTC().Format("2006-01-02T15")
			if d.AttemptsThisHour.Hour == hour {
				d.AttemptsThisHour.Count++
			} else {
				d.AttemptsThisHour = store.Attempts{Hour: hour, Count: 1}
			}
			return nil
		}, []Effect{effCreate{offer: ev.Offer, gen: nextGen, label: label}}

	// T8: the provider confirmed creation — adopt the id and open the
	// ledger entry; Destroying instead of Booting if Stop/budget beat
	// the create.
	case state == Creating && ev.Kind == EvCreated && createOpOpen(f.Doc, ev.Gen):
		dest := f.Doc.Desired == store.DesiredStopped
		next := Booting
		if dest {
			next = Destroying
		}
		return next, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if (d.Desired == store.DesiredStopped) != dest {
				return errStale
			}
			op := findOp(d, createOpKey(d.Generation))
			if op == nil || op.Outcome != "" {
				return errStale
			}
			d.Instance.ProviderID = providerIDString(ev.ProviderID)
			if f.Budget != nil {
				f.Budget.OpenEntry(d, f.Now, d.Generation, d.Instance.Offer.DPH)
			}
			if dest {
				d.Instance.State = Destroying.String()
				op.Outcome = "cancelled"
				t := f.Now
				op.FinishedAt = &t
			} else {
				// an outcome still open, not yet terminal: later rows
				// overwrite it once this generation's op finishes.
				d.Instance.State = Booting.String()
				op.Outcome = "created"
			}
			return nil
		}, nil

	// T9: the create itself failed in a non-retryable way — drop the
	// instance and keep desired as-is so the backoff retries.
	case state == Creating && ev.Kind == EvCreateFailed && !ev.Duplicate &&
		(ev.Class == provider.Terminal || ev.Class == provider.Fatal):
		return Idle, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			finishOp(d, createOpKey(d.Generation), "create_error", f.Now)
			d.Instance = nil
			return nil
		}, nil

	// T10: a retryable create error — the saga's own retry resolves
	// it; decide changes nothing.
	case state == Creating && ev.Kind == EvCreateFailed && !ev.Duplicate && ev.Class == provider.Retryable:
		return Creating, nil, nil

	// T11: the provider says our label already exists — destroy every
	// instance under that exact label, using the document's own label
	// rather than a new one, since this attempt never got an id.
	case state == Creating && ev.Kind == EvCreateFailed && ev.Duplicate:
		return Destroying, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if f.Budget != nil {
				f.Budget.OpenEntry(d, f.Now, d.Generation, d.Instance.Offer.DPH)
			}
			d.Instance.State = Destroying.String()
			finishOp(d, createOpKey(d.Generation), "duplicate_label", f.Now)
			return nil
		}, nil

	// T12: the instance is running with an address — dial it, using
	// only this observation's host/port, never the document.
	case state == Booting && ev.Kind == EvProviderObserved && ev.Status == provider.StatusRunning && ev.SSHHost != "":
		return Booting, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if d.Instance.State != Booting.String() {
				return errStale
			}
			d.Instance.SSHHost = ev.SSHHost
			d.Instance.SSHPort = ev.SSHPort
			return nil
		}, []Effect{effDial{inst: dialTarget(f, ev), gen: ev.Gen}}

	// T13: the tunnel is up — pin the host key and start probing.
	case state == Booting && ev.Kind == EvLinkUp:
		return Loading, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if d.Instance.State != Booting.String() {
				return errStale
			}
			d.Instance.State = Loading.String()
			d.Instance.HostKeyFingerprint = ev.HostKey
			return nil
		}, []Effect{effProbe{gen: ev.Gen}}

	// T14: the poster already counted two consecutive non-Ready
	// observations — destroy; only after Ready does this also flip
	// desired to STOPPED.
	case isCreatingThroughDraining(state) && ev.Kind == EvProviderObserved && ev.Status.Terminal():
		before := isCreatingBootingOrLoading(state)
		return Destroying, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			d.Instance.State = Destroying.String()
			if before {
				finishOp(d, createOpKey(d.Generation), "failed_status", f.Now)
			} else {
				d.Desired = store.DesiredStopped
			}
			return nil
		}, nil

	// T15: startup took too long — destroy, denylist the machine for
	// an hour, and keep desired so the backoff retries elsewhere.
	case isCreatingBootingOrLoading(state) && ev.Kind == EvDeadlineExpired:
		snapshotState := ""
		if f.Doc.Instance != nil {
			snapshotState = f.Doc.Instance.State
		}
		return Destroying, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if d.Instance.State != snapshotState {
				return errStale
			}
			if d.Instance.CreatedAt.Add(f.Cfg.StartupTimeout).After(f.Now) {
				return errStale
			}
			d.Instance.State = Destroying.String()
			finishOp(d, createOpKey(d.Generation), "deadline", f.Now)
			d.MachineDenylist = append(d.MachineDenylist, store.DenyEntry{
				MachineID: d.Instance.Offer.MachineID,
				Until:     f.Now.Add(time.Hour),
			})
			return nil
		}, nil

	// T16: Stop/BudgetExhausted once the provider id is known —
	// destroy at once.
	case (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted) &&
		(state == Booting || state == Loading || (state == Creating && f.Doc.Instance != nil && f.Doc.Instance.ProviderID != "")):
		snapshotState := ""
		if f.Doc.Instance != nil {
			snapshotState = f.Doc.Instance.State
		}
		return Destroying, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != f.Doc.Generation {
				return errStale
			}
			if d.Instance.State != snapshotState {
				return errStale
			}
			d.Instance.State = Destroying.String()
			d.Desired = store.DesiredStopped
			finishOp(d, createOpKey(d.Generation), "cancelled", f.Now)
			return nil
		}, nil

	// T17: Stop/BudgetExhausted before the id is known — stay in
	// Creating; there's nothing to destroy yet, so this only records
	// the desire to stop and lets a later event finish the job.
	case (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted) &&
		state == Creating && f.Doc.Instance != nil && f.Doc.Instance.ProviderID == "":
		return Creating, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != f.Doc.Generation {
				return errStale
			}
			if d.Instance.ProviderID != "" {
				return errStale
			}
			d.Desired = store.DesiredStopped
			return nil
		}, nil

	// T20: idle deadline reached while Ready and nothing in flight —
	// drain. The Conn itself closes later, once draining finishes,
	// not here.
	case state == Ready && (ev.Kind == EvIdleTimer || ev.Kind == EvTick) && f.InFlight == 0 && idleRuleFires(f):
		return Draining, func(d *store.Document) error {
			if d.Instance == nil || d.Instance.State != Ready.String() {
				return errStale
			}
			if d.Instance.IdleDeadline == nil || f.Now.Before(*d.Instance.IdleDeadline) {
				return errStale
			}
			d.Desired = store.DesiredStopped
			d.Instance.State = Draining.String()
			return nil
		}, nil

	// T21: past the maximum lifetime — drain regardless of inFlight.
	case (state == Ready || state == Serving) && ev.Kind == EvTick && lifetimeExpired(f):
		return Draining, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != f.Doc.Generation {
				return errStale
			}
			// Serving is memory-only; the document always stores it as Ready.
			if d.Instance.State != Ready.String() {
				return errStale
			}
			if d.Instance.CreatedAt.Add(f.Cfg.MaxLifetime).After(f.Now) {
				return errStale
			}
			d.Desired = store.DesiredStopped
			d.Instance.State = Draining.String()
			return nil
		}, nil

	// T22: a manual Stop or a budget denial while serving — drain.
	case (state == Ready || state == Serving) && (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted):
		return Draining, func(d *store.Document) error {
			if d.Instance == nil || d.Instance.State != Ready.String() {
				return errStale
			}
			d.Desired = store.DesiredStopped
			d.Instance.State = Draining.String()
			return nil
		}, nil

	// T23: the instance vanished after Ready — close the ledger entry
	// and go straight to Idle; nothing left to destroy.
	case (state == Ready || state == Serving) && ev.Kind == EvVanished:
		return Idle, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if f.Budget != nil {
				f.Budget.CloseEntry(d, f.Now, ev.Gen)
			}
			d.Instance = nil
			d.Desired = store.DesiredStopped
			return nil
		}, nil

	// T24: two non-Ready probes, or a host-key mismatch, while
	// Ready/Serving — destroy. (Closing the tunnel is a generic hook
	// for leaving any Conn-holding state, not this row's own job.)
	case (state == Ready || state == Serving) && ev.Kind == EvWorkerFailed:
		return Destroying, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			d.Desired = store.DesiredStopped
			d.Instance.State = Destroying.String()
			if ev.Err != nil {
				d.Instance.LastError = ev.Err.Error()
			}
			return nil
		}, nil

	// T25/T26: draining finished either because the last request
	// ended (checked on the next Tick) or because the drain deadline
	// passed — either way, destroy.
	case state == Draining && (ev.Kind == EvDeadlineExpired || (ev.Kind == EvTick && f.InFlight == 0)):
		return Destroying, func(d *store.Document) error {
			if d.Instance == nil || d.Instance.State != Draining.String() {
				return errStale
			}
			d.Instance.State = Destroying.String()
			return nil
		}, nil

	// T27: the instance vanished before Ready, or while draining —
	// close the ledger entry; desired is left alone (already STOPPED
	// if Draining, kept RUNNING before Ready so the backoff retries).
	case isCreatingLoadingOrDraining(state) && ev.Kind == EvVanished:
		return Idle, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if f.Budget != nil {
				f.Budget.CloseEntry(d, f.Now, ev.Gen)
			}
			finishOp(d, createOpKey(d.Generation), "lost", f.Now)
			d.Instance = nil
			return nil
		}, nil

	// T28/T30: dispatching and retrying the destroy call itself is a
	// generic hook keyed on being in Destroying; decide adds nothing.
	case state == Destroying && ev.Kind == EvTick:
		return Destroying, nil, nil

	// T29: the provider confirms the instance is gone — close the
	// ledger entry and return to Idle; desired is left untouched, so
	// a revival that happened mid-destroy still searches again soon.
	case state == Destroying && ev.Kind == EvGone:
		return Idle, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if f.Budget != nil {
				f.Budget.CloseEntry(d, f.Now, ev.Gen)
			}
			finishOp(d, destroyOpKey(d.Generation), "ok", f.Now)
			d.Instance = nil
			return nil
		}, nil

	// T31: Stop while Idle, Draining or Destroying only needs to make
	// sure desired is STOPPED; everything else is already in motion.
	case (state == Idle || state == Draining || state == Destroying) && ev.Kind == EvStop:
		return state, func(d *store.Document) error {
			if state == Idle && d.Instance != nil {
				return errStale
			}
			d.Desired = store.DesiredStopped
			return nil
		}, nil

	// T32: the worker is healthy — Ready, with the idle deadline armed
	// from this moment (left nil, so the idle rule never fires, when
	// idle shutdown is disabled).
	case state == Loading && ev.Kind == EvHealthOK:
		return Ready, func(d *store.Document) error {
			if d.Instance == nil || d.Generation != ev.Gen {
				return errStale
			}
			if d.Instance.State != Loading.String() {
				return errStale
			}
			d.Instance.State = Ready.String()
			ready := f.Now
			d.Instance.ReadyAt = &ready
			if f.Cfg.IdleShutdownEnabled {
				deadline := ready.Add(f.Cfg.IdleShutdownTimeout)
				d.Instance.IdleDeadline = &deadline
			}
			finishOp(d, createOpKey(d.Generation), "ready", f.Now)
			return nil
		}, nil

	// Any (state, event) pair not listed above leaves the state
	// unchanged — that is itself the intended behavior, not a gap.
	default:
		return state, nil, nil
	}
}

func isProvisioningOrServing(s State) bool {
	switch s {
	case Searching, Creating, Booting, Loading, Ready, Serving:
		return true
	default:
		return false
	}
}

func isCreatingBootingOrLoading(s State) bool {
	switch s {
	case Creating, Booting, Loading:
		return true
	default:
		return false
	}
}

func isCreatingThroughDraining(s State) bool {
	switch s {
	case Creating, Booting, Loading, Ready, Serving, Draining:
		return true
	default:
		return false
	}
}

func isCreatingLoadingOrDraining(s State) bool {
	switch s {
	case Creating, Booting, Loading, Draining:
		return true
	default:
		return false
	}
}

func idleRuleFires(f Facts) bool {
	if f.Doc.Instance == nil || f.Doc.Instance.IdleDeadline == nil {
		return false
	}
	return !f.Now.Before(*f.Doc.Instance.IdleDeadline)
}

func lifetimeExpired(f Facts) bool {
	if f.Doc.Instance == nil || f.Cfg.MaxLifetime <= 0 {
		return false
	}
	return !f.Now.Before(f.Doc.Instance.CreatedAt.Add(f.Cfg.MaxLifetime))
}

func createOpKey(gen int64) string { return fmt.Sprintf("create-%d", gen) }

func destroyOpKey(gen int64) string { return fmt.Sprintf("destroy-%d", gen) }

func createOpOpen(doc store.Document, gen int64) bool {
	for _, op := range doc.Ops {
		if op.Key == createOpKey(gen) {
			return op.Outcome == ""
		}
	}
	return false
}

func findOp(d *store.Document, key string) *store.Op {
	for i := range d.Ops {
		if d.Ops[i].Key == key {
			return &d.Ops[i]
		}
	}
	return nil
}

// finishOp sets an op's outcome unless one already won the race and
// it wasn't just the provisional "created"; "created" itself may
// still be overwritten by whichever terminal outcome comes next.
func finishOp(d *store.Document, key, outcome string, now time.Time) {
	op := findOp(d, key)
	if op == nil || (op.Outcome != "" && op.Outcome != "created") {
		return
	}
	op.Outcome = outcome
	t := now
	op.FinishedAt = &t
}

func providerIDString(id provider.InstanceID) string { return fmt.Sprintf("%d", int64(id)) }

func dialTarget(f Facts, ev Event) tunnel.Instance {
	hostKey := ""
	if f.Doc.Instance != nil {
		hostKey = f.Doc.Instance.HostKeyFingerprint
	}
	return tunnel.Instance{Host: ev.SSHHost, Port: ev.SSHPort, HostKey: hostKey}
}

// searchFilter builds the offer filter from config and the document's
// own unexpired denylist; the actual SearchOffers call is the loop's.
func searchFilter(f Facts) provider.OfferFilter {
	pf := f.Cfg.ProviderFilter
	excl := make([]int64, 0, len(f.Doc.MachineDenylist))
	for _, e := range f.Doc.MachineDenylist {
		if f.Now.Before(e.Until) {
			excl = append(excl, e.MachineID)
		}
	}
	return provider.OfferFilter{
		GPUName:           pf.GPUName,
		NumGPUs:           pf.NumGPUs,
		MinCPUCores:       pf.MinCPUCores,
		MinCPURAMGB:       pf.MinCPURAMGB,
		MinDiskGB:         pf.MinDiskGB,
		MinReliability:    pf.MinReliability,
		MinInetDownMBps:   pf.MinInetDownMBps,
		MinDiskBwMBps:     pf.MinDiskBwMBps,
		MinCUDA:           pf.MinCUDA,
		Geolocation:       pf.Geolocation,
		MaxHourly:         provider.Money(math.Round(f.Cfg.MaxHourlyPriceUSD * 1e6)),
		ExcludeMachineIDs: excl,
	}
}
