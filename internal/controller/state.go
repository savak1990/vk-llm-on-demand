package controller

import (
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

// State is the controller's internal stage; PublicState maps it to the
// 8 names ADR 0011 §6 exposes externally.
type State int

const (
	Idle State = iota
	Searching
	Creating
	Booting
	Loading
	Ready
	Serving
	Draining
	Destroying
)

func (s State) String() string {
	switch s {
	case Idle:
		return "Idle"
	case Searching:
		return "Searching"
	case Creating:
		return "Creating"
	case Booting:
		return "Booting"
	case Loading:
		return "Loading"
	case Ready:
		return "Ready"
	case Serving:
		return "Serving"
	case Draining:
		return "Draining"
	case Destroying:
		return "Destroying"
	default:
		return "Unknown"
	}
}

// PublicState is the externally reported state and the metric label
// source (Design Decision 6 — never State.String() for the metric).
func PublicState(s State, failing bool) string {
	switch s {
	case Idle:
		return "STOPPED"
	case Searching, Creating:
		return "ALLOCATING"
	case Booting:
		return "STARTING"
	case Loading:
		return "LOADING_MODEL"
	case Ready, Serving:
		return "READY"
	case Draining:
		return "DRAINING"
	case Destroying:
		if failing {
			return "FAILED"
		}
		return "DELETING"
	default:
		return "STOPPED"
	}
}

type EventKind int

const (
	EvStart EventKind = iota
	EvStop
	EvOfferChosen
	EvNoOffer
	EvCreated
	EvCreateFailed
	EvLinkUp
	EvHealthOK
	EvTick
	EvIdleTimer
	EvProviderObserved
	EvVanished
	EvWorkerFailed
	EvDeadlineExpired
	EvBudgetExhausted
	EvGone
)

// Event is the loop's single input type; decide handles every (State,
// Event) pair. Fields irrelevant to a given Kind are left zero.
type Event struct {
	Kind EventKind
	Gen  int64

	Offer      provider.Offer
	Reason     string // "" (gate ok) | "backoff" (pacing, not a denial) | "no_offer" | "budget_exhausted" | "balance_low" | "attempts_exhausted"
	ProviderID provider.InstanceID
	Err        error
	Class      provider.Class
	Duplicate  bool
	HostKey    string
	Status     provider.InstanceStatus
	RawStatus  string
	SSHHost    string
	SSHPort    int
}

// Facts is everything decide reads; it never touches the clock, the
// store, or the provider itself. Doc is a snapshot — decide picks a row
// from it, but the mutate closure it returns re-validates against
// whatever document Transition actually hands it. Cfg and Budget let a
// row read a config duration or call Budget.OpenEntry/CloseEntry
// inside its own CAS.
type Facts struct {
	Now      time.Time
	Doc      store.Document
	InFlight int
	Failing  bool

	Cfg    config.Controller
	Budget Budget
}

// Effect is a closed set of event-derived requests the loop dispatches
// as goroutines (Architecture point 2); decide never calls Provider,
// Dialer or Worker itself.
type Effect interface{ effect() }

// effList and effBalance are deliberately absent: reconcile (Task 7)
// compares a whole instance list against the document, which doesn't
// fit a single-event decide() result, so it runs as its own async
// worker and calls Provider.ListInstances/Balance directly — it only
// feeds back into this Effect pipeline via postEvent on findings that
// affect the active generation (vanish, terminal status, lost).
//
// effDestroy and effCloseConn are also deliberately absent: both are
// generic post-transition hooks keyed on the (state, next) class
// change itself (Task 6's processEvent), not something any individual
// transition row has to remember to return — "entering Destroying
// arms the destroy-retry timer" and "leaving a Conn-holding state
// closes it" apply uniformly to every row that does either, so no row
// needs to ask for them by name.
type effSearch struct{ filter provider.OfferFilter }
type effCreate struct {
	offer provider.Offer
	gen   int64
	label string
}
type effGet struct {
	id  provider.InstanceID
	gen int64
}
type effDial struct {
	inst tunnel.Instance
	gen  int64
}
type effProbe struct{ gen int64 }

func (effSearch) effect() {}
func (effCreate) effect() {}
func (effGet) effect()    {}
func (effDial) effect()   {}
func (effProbe) effect()  {}
