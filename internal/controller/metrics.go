package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// The remaining 9 rows of the spec's Observability table
// (vkllm_provision_attempts_total, vkllm_gpu_uptime_seconds_total,
// vkllm_gpu_idle_seconds, vkllm_gpu_idle_deadline_seconds,
// vkllm_gpu_last_inference_finished_seconds, vkllm_destroy_attempts_total,
// vkllm_orphans_destroyed_total, vkllm_reconcile_runs_total,
// vkllm_instance_hourly_price_usd) are deferred to a later task; they
// follow one of the 3 shapes below with nothing left to decide.
var (
	// Pattern 1: one-hot gauge over a fixed label enumeration.
	gpuState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vkllm_gpu_state",
	}, []string{"state"})

	// Pattern 2: counter with a bounded label enumeration.
	shutdownsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_shutdowns_total",
	}, []string{"reason"})

	// Pattern 3: histogram with a bounded label enumeration.
	coldstartStageSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vkllm_coldstart_stage_seconds",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 240, 480, 900},
	}, []string{"stage"})
)

// RegisterMetrics registers this package's collectors; main calls it
// once (mirrors internal/store's own RegisterMetrics convention).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(gpuState, shutdownsTotal, coldstartStageSeconds)
}

// setGPUState is the one-hot update: the current public state's gauge
// goes to 1, every other of the 8 public states goes to 0 — call this
// from processEvent whenever c.state actually changes, and once after
// recovery so the gauge isn't empty before the first transition.
func setGPUState(public string) {
	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		if s == public {
			gpuState.WithLabelValues(s).Set(1)
		} else {
			gpuState.WithLabelValues(s).Set(0)
		}
	}
}

// shutdownReason maps a committed transition onto the vkllm_shutdowns_total
// reason label, replaying the exact guards decide.go used (idle checked
// before lifetime, matching its case order) so the two can't be confused;
// it is a pure, side-effect-free read of already-committed facts, called
// only after the CAS that produced next has itself succeeded.
func shutdownReason(state, next State, ev Event, inFlight int, doc store.Document, cfg config.Controller, now time.Time) (string, bool) {
	switch {
	case isCreatingThroughDraining(state) && ev.Kind == EvProviderObserved && ev.Status.Terminal() && next == Destroying:
		return "failure", true
	case isCreatingBootingOrLoading(state) && (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted) && next == Destroying:
		return manualOrBudget(ev), true
	case state == Ready && (ev.Kind == EvIdleTimer || ev.Kind == EvTick) && next == Draining &&
		inFlight == 0 && idleRuleFires(Facts{Now: now, Doc: doc}):
		return "idle", true
	case (state == Ready || state == Serving) && ev.Kind == EvTick && next == Draining &&
		lifetimeExpired(Facts{Now: now, Doc: doc, Cfg: cfg}):
		return "lifetime", true
	case (state == Ready || state == Serving) && (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted) && next == Draining:
		return manualOrBudget(ev), true
	case (state == Ready || state == Serving) && ev.Kind == EvVanished && next == Idle:
		return "failure", true
	case (state == Ready || state == Serving) && ev.Kind == EvWorkerFailed && next == Destroying:
		return "failure", true
	default:
		return "", false
	}
}

func manualOrBudget(ev Event) string {
	if ev.Kind == EvStop {
		return "manual"
	}
	return "budget"
}
