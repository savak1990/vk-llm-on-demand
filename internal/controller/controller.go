package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

var (
	ErrGPUUnavailable     = errors.New("controller: gpu unavailable")
	ErrBackendUnavailable = errors.New("controller: backend unavailable")
	ErrDraining           = errors.New("controller: not admitting")
	ErrForceNotEnabled    = errors.New("controller: force not enabled")
)

// Deps are every external dependency the controller calls. Provider,
// Store, Dialer, Clock, Budget and Worker are all interfaces so tests
// use fakes and the loop remains their only caller.
type Deps struct {
	Provider       provider.Provider
	Store          store.Store
	Dialer         tunnel.Dialer
	Clock          clock.Clock
	Budget         Budget
	Worker         Worker
	WorkerSeed     func() ([]byte, error)
	InstallationID string
	ModelAlias     string // e.g. "qwen3.6-35b-a3b"; owned by model.* config, not config.Controller
	Config         config.Controller
	Reg            prometheus.Registerer
	Log            *slog.Logger
}

// Snapshot is a point-in-time copy of the controller's state; Status
// never calls the provider to build one.
type Snapshot struct {
	State      State
	Public     string
	Desired    string
	Generation int64
	Since      time.Time
	InFlight   int

	InstallationID string
	LabelPrefix    string
	Provider       string

	Unavailable string

	GPUName    string
	MachineID  int64
	Hourly     provider.Money
	ProviderID provider.InstanceID

	CreatedAt               time.Time
	ReadyAt                 *time.Time
	LastInferenceFinishedAt *time.Time
	IdleDeadline            *time.Time

	Ready             bool
	Uptime            time.Duration
	UntilIdleShutdown time.Duration

	SpendInstanceUSD float64
	SpendMTDUSD      float64

	Stages           map[string]time.Duration
	LastError        string
	AttemptsThisHour int
	NextCreateAt     time.Time
}

type timerKind int

const (
	timerIdle timerKind = iota
	timerStartup
	timerDrain
	timerDestroyRetry
	timerReconcile
	timerPoll
	timerFlush
	timerMetricsPull
)

// Note: the suspect probe (ReportUpstreamFailure) runs on its own
// goroutine against c.deps.Clock.After directly — it is not a
// table-scheduled timer, so there is no timerSuspect.

// Controller runs the single-instance GPU lifecycle state machine. One
// loop goroutine (started by Run) is the only caller of Provider and
// Dialer; every other method takes mu only for the memory fields it touches.
type Controller struct {
	deps Deps

	procMu sync.Mutex // serializes processEvent between the loop and Start/Stop callers

	runCtx context.Context // set once by Run; effect goroutines use this, never a caller's request ctx

	mu          sync.Mutex
	state       State
	generation  int64
	inFlight    int
	conn        *tunnel.Conn
	failing     bool
	unavailable string
	since       time.Time

	activityPending bool
	activityAt      time.Time
	suspect         bool

	timersMu sync.Mutex
	timers   map[timerKind]time.Time

	events chan Event
	wake   chan struct{}

	stagesMu sync.Mutex
	stages   map[string]time.Duration // completed stage durations, for Snapshot.Stages and the coldstart histogram

	lastErrorMu sync.Mutex
	lastError   string

	balanceMu        sync.Mutex
	lastBalance      provider.Money
	lastBalanceKnown bool // false until a balance read succeeds; Allow then denies as balance_low

	destroyRetryMu sync.Mutex
	destroyRetryN  int // trailing failed-destroy count since Destroying was last entered; backoff = min(5m, 10s*2^(n-1))

	noOfferMu    sync.Mutex
	noOfferUntil time.Time // armed by a no_offer transition; gateReason denies a Start inside it

	reconciling    atomic.Bool
	reconcileMu    sync.Mutex
	orphanObserved map[provider.InstanceID]int // keyed by id, not label: a duplicate shares the active instance's label
	activeLabel    string
	activeObserved int // consecutive lists missing activeLabel
	terminalSeen   int // consecutive lists showing activeLabel in a terminal status

	pollTerminalMu   sync.Mutex
	pollTerminalGen  int64
	pollTerminalSeen int // consecutive startup-poll gets with a terminal status for pollTerminalGen

	inFlightEffects atomic.Int64 // every goroutine this package starts, counted for its whole life; a test harness drives a fake clock off it
}

// New validates the configuration and builds a Controller; it does not
// start the loop (Run does) or touch the provider or the store.
func New(d Deps) (*Controller, error) {
	if d.Config.ProvisioningMode != "explicit" {
		return nil, fmt.Errorf("controller: provisioningMode %q not supported", d.Config.ProvisioningMode)
	}
	var since time.Time
	if d.Clock != nil {
		since = d.Clock.Now()
	}
	return &Controller{
		deps:           d,
		state:          Idle,
		since:          since,
		timers:         make(map[timerKind]time.Time),
		events:         make(chan Event, 64),
		wake:           make(chan struct{}, 1),
		stages:         make(map[string]time.Duration),
		orphanObserved: make(map[provider.InstanceID]int),
	}, nil
}

// postEvent queues an event for the loop; effect dispatchers call this
// from their own goroutine, never the loop goroutine itself, so it must
// never block it — a full 64-slot channel means something is already badly wrong.
func (c *Controller) postEvent(e Event) { c.events <- e }

// spawn runs fn on its own goroutine, counted as in flight for fn's
// whole lifetime, not just a provider/dialer/worker call's own
// duration; every goroutine this package starts goes through it.
func (c *Controller) spawn(fn func()) {
	c.inFlightEffects.Add(1)
	go func() {
		defer c.inFlightEffects.Add(-1)
		fn()
	}()
}
