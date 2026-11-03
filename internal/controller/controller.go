package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

// Note: the suspect probe (ReportUpstreamFailure, Task 5) runs on its
// own goroutine against c.deps.Clock.After directly — it is not a
// table-scheduled timer, so there is no timerSuspect.

// Controller runs the single-instance GPU lifecycle state machine. One
// loop goroutine (started by Run, Task 6) is the only caller of
// Provider and Dialer; every other method takes mu only for the memory
// fields it touches.
type Controller struct {
	deps Deps

	runCtx context.Context // set once by Run (Task 6); effect goroutines use this, never a caller's request ctx

	mu         sync.Mutex
	state      State
	generation int64
	label      string
	inFlight   int
	admitting  bool
	conn       *tunnel.Conn
	failing    bool
	since      time.Time

	activityPending bool
	activityAt      time.Time
	suspect         bool

	timersMu sync.Mutex
	timers   map[timerKind]time.Time

	events chan Event
	wake   chan struct{}

	stagesMu       sync.Mutex
	stages         map[string]time.Duration // completed stage durations, for Snapshot.Stages and the coldstart histogram (Task 6, Task 9)
	stageEnteredAt map[string]time.Time     // open stage-boundary markers the loop stamps on each state transition (Task 6)

	lastErrorMu sync.Mutex
	lastError   string

	destroyRetryMu sync.Mutex
	destroyRetryN  int // trailing failed-destroy count since Destroying was last entered; backoff = min(5m, 10s*2^n)
}

// New validates the configuration and builds a Controller; it does not
// start the loop (Run does, Task 6) or touch the provider or the store.
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
		stageEnteredAt: make(map[string]time.Time),
	}, nil
}

// postEvent queues an event for the loop; effect dispatchers (Task 4,
// Task 7) call this from their own goroutine, never the loop goroutine
// itself, so it must never block it. The channel is large enough
// (64) that a blocking send here would mean something is already
// badly wrong, which is the signal a full channel should give.
func (c *Controller) postEvent(e Event) { c.events <- e }
