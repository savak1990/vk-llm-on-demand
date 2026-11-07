package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

var (
	gpuState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "vkllm_gpu_state",
	}, []string{"state"})

	shutdownsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_shutdowns_total",
	}, []string{"reason"})

	coldstartStageSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vkllm_coldstart_stage_seconds",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 240, 480, 900},
	}, []string{"stage"})

	provisionAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_provision_attempts_total",
	}, []string{"outcome"})

	gpuUptimeSecondsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vkllm_gpu_uptime_seconds_total",
	})

	gpuIdleSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vkllm_gpu_idle_seconds",
	})

	gpuIdleDeadlineSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vkllm_gpu_idle_deadline_seconds",
	})

	gpuLastInferenceFinishedSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vkllm_gpu_last_inference_finished_seconds",
	})

	destroyAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_destroy_attempts_total",
	}, []string{"outcome"})

	orphansDestroyedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vkllm_orphans_destroyed_total",
	})

	reconcileRunsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vkllm_reconcile_runs_total",
	}, []string{"outcome"})

	instanceHourlyPriceUSD = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vkllm_instance_hourly_price_usd",
	})
)

// RegisterMetrics registers this package's collectors; main calls it once.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(gpuState, shutdownsTotal, coldstartStageSeconds,
		provisionAttemptsTotal, gpuUptimeSecondsTotal, gpuIdleSeconds,
		gpuIdleDeadlineSeconds, gpuLastInferenceFinishedSeconds,
		destroyAttemptsTotal, orphansDestroyedTotal, reconcileRunsTotal,
		instanceHourlyPriceUSD)
	// Every declared stage must appear in /metrics at process start,
	// even though only first_token is ever really observed today.
	for _, stage := range []string{"search", "create", "boot", "link", "download", "load", "ready", "first_token"} {
		coldstartStageSeconds.WithLabelValues(stage)
	}
	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		gpuState.WithLabelValues(s)
	}
	for _, r := range []string{"idle", "manual", "lifetime", "budget", "failure", "orphan"} {
		shutdownsTotal.WithLabelValues(r)
	}
	for _, o := range []string{"ok", "no_offer", "provider_error", "deadline_exceeded", "attempts_exhausted", "budget_exhausted", "failed_status", "lost", "cancelled"} {
		provisionAttemptsTotal.WithLabelValues(o)
	}
	for _, o := range []string{"ok", "retryable", "terminal", "fatal"} {
		destroyAttemptsTotal.WithLabelValues(o)
	}
	for _, o := range []string{"ok", "error"} {
		reconcileRunsTotal.WithLabelValues(o)
	}
}

// publishCostMetrics republishes the budget/cost gauges and adds
// this tick's non-negative spend delta to the monotonic counter —
// called every reconcile tick, not only when something polls Status.
func (c *Controller) publishCostMetrics(doc store.Document) {
	mtd := c.deps.Budget.EstimatedMTD(doc)
	obs.SetBudgetUsedRatio(c.deps.Budget.UsedRatio(doc))

	instanceUSD := 0.0
	if e, ok := openEntry(doc); ok {
		instanceUSD = e.EstUSD
	}
	obs.SetCostInstanceUSD(instanceUSD)

	c.balanceMu.Lock()
	if c.lastBalanceKnown {
		obs.SetProviderBalanceUSD(c.lastBalance.USD())
	}
	c.balanceMu.Unlock()

	c.costMu.Lock()
	delta := mtd - c.lastEstimatedMTD
	if delta < 0 {
		delta = 0 // a monthly rollover drops the raw ledger total; never decrement the exported counter
	}
	c.lastEstimatedMTD = mtd
	c.costMu.Unlock()
	obs.AddCostEstimatedUSD(delta)
}

// setGPUState is the one-hot update over the 8 public states; call on
// every real state change, and once after recovery.
func setGPUState(public string) {
	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		if s == public {
			gpuState.WithLabelValues(s).Set(1)
		} else {
			gpuState.WithLabelValues(s).Set(0)
		}
	}
}

// shutdownReason maps a committed transition onto vkllm_shutdowns_total's
// reason, replaying decide.go's idle/lifetime guards rather than editing it.
func shutdownReason(state, next State, ev Event, inFlight int, doc store.Document, cfg config.Controller, now time.Time) (string, bool) {
	switch {
	case isCreatingThroughDraining(state) && ev.Kind == EvProviderObserved && ev.Status.Terminal() && next == Destroying:
		return "failure", true
	case isCreatingBootingOrLoading(state) && (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted) && next == Destroying:
		return manualOrBudget(ev), true
	case state == Ready && (ev.Kind == EvIdleTimer || ev.Kind == EvTick) && next == Draining &&
		inFlight == 0 && idleRuleFires(Facts{Now: now, Doc: doc, Cfg: cfg}):
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

// provisionOutcome maps a transition onto vkllm_provision_attempts_total's
// outcome, for the rows the spec names one for; T2's edge-triggered gate
// denials need their own allowed/denied memory and are not counted here.
func provisionOutcome(state, next State, ev Event) (string, bool) {
	switch {
	case state == Searching && next == Idle && ev.Kind == EvNoOffer && ev.Err == nil:
		return "no_offer", true
	case state == Searching && next == Idle && ev.Kind == EvOfferChosen && ev.Reason != "":
		return provisionDenialOutcome(ev.Reason)
	case state == Searching && next == Idle && ev.Kind == EvNoOffer && ev.Err != nil:
		return "provider_error", true
	case state == Searching && next == Idle && ev.Kind == EvStop:
		return "cancelled", true
	case state == Creating && next == Idle && ev.Kind == EvCreateFailed:
		return "provider_error", true
	case isCreatingBootingOrLoading(state) && next == Destroying && ev.Kind == EvProviderObserved && ev.Status.Terminal():
		return "failed_status", true
	case isCreatingBootingOrLoading(state) && next == Destroying && ev.Kind == EvDeadlineExpired:
		return "deadline_exceeded", true
	case isCreatingBootingOrLoading(state) && next == Destroying && (ev.Kind == EvStop || ev.Kind == EvBudgetExhausted):
		return "cancelled", true
	case state == Creating && next == Destroying && ev.Kind == EvCreated:
		return "cancelled", true
	case isCreatingBootingOrLoading(state) && next == Idle && ev.Kind == EvVanished:
		return "lost", true
	case state == Loading && next == Ready && ev.Kind == EvHealthOK:
		return "ok", true
	default:
		return "", false
	}
}

// provisionDenialOutcome is the spec's Unavailable-reason-to-outcome
// mapping; the enum is closed, so an unrecognized reason counts nothing.
func provisionDenialOutcome(reason string) (string, bool) {
	switch reason {
	case "no_offer":
		return "no_offer", true
	case "attempts_exhausted":
		return "attempts_exhausted", true
	case "budget_exhausted", "balance_low":
		return "budget_exhausted", true
	default:
		return "", false
	}
}

// recordDocGauges sets the 4 gauges that are pure functions of an
// already-loaded document; every caller passes one in hand, so this
// never triggers a provider or store call of its own.
func recordDocGauges(doc store.Document, state State, inFlight int, now time.Time) {
	idleSeconds, idleDeadline, lastInference, hourly := 0.0, 0.0, 0.0, 0.0
	if doc.Instance != nil {
		hourly = doc.Instance.Offer.DPH
		if doc.Instance.IdleDeadline != nil {
			idleDeadline = float64(doc.Instance.IdleDeadline.Unix())
		}
		if doc.Instance.LastInferenceFinishedAt != nil {
			lastInference = float64(doc.Instance.LastInferenceFinishedAt.Unix())
		}
		if state == Ready && inFlight == 0 {
			base := doc.Instance.ReadyAt
			if doc.Instance.LastInferenceFinishedAt != nil && (base == nil || doc.Instance.LastInferenceFinishedAt.After(*base)) {
				base = doc.Instance.LastInferenceFinishedAt
			}
			if base != nil {
				idleSeconds = now.Sub(*base).Seconds()
			}
		}
	}
	gpuIdleSeconds.Set(idleSeconds)
	gpuIdleDeadlineSeconds.Set(idleDeadline)
	gpuLastInferenceFinishedSeconds.Set(lastInference)
	instanceHourlyPriceUSD.Set(hourly)
}
