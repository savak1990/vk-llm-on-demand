package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/progress"
)

const longestColdStart = 360 * time.Second

func (g *Gateway) onDemand() bool { return g.deps.Config.ProvisioningMode == "on-demand" }

func keyIDOf(r *http.Request) string {
	id, _ := r.Context().Value(ctxKeyKeyID).(string)
	return id
}

// retryAfter estimates the seconds left in a cold start from when it began.
func retryAfter(s controller.Snapshot, now time.Time) time.Duration {
	began := s.CreatedAt
	if began.IsZero() {
		began = s.Since
	}
	if s.Public == "STOPPED" || began.IsZero() {
		began = now
	}
	return max(longestColdStart-now.Sub(began), 30*time.Second).Round(time.Second)
}

func unavailableMessage(reason string) string {
	switch reason {
	case "budget_exhausted":
		return "The monthly GPU budget is used up."
	case "balance_low":
		return "The GPU provider account balance is too low."
	case "attempts_exhausted":
		return "Too many GPU start attempts this hour; try again later."
	case "no_offer":
		return "No GPU offer matches the price and hardware limits right now; try again in a few minutes."
	default:
		return "The GPU could not start."
	}
}

func (g *Gateway) refuseColdStart(w http.ResponseWriter, r *http.Request) {
	if g.shuttingDown.Load() {
		g.writeShuttingDownOnDemand(w)
		return
	}
	now := g.deps.Clock.Now()
	ra := retryAfter(g.deps.Controller.Status(), now)
	snap, err := g.deps.Controller.Demand(r.Context(), keyIDOf(r), now.Add(ra+g.deps.Config.OnDemand.DemandGrace))
	switch {
	case errors.Is(err, controller.ErrDraining):
		g.writeShuttingDownOnDemand(w)
	case err != nil:
		recorderOf(w).outcome = "gpu_unavailable"
		g.metrics.coldStartOutcomesTotal.WithLabelValues("gpu_unavailable").Inc()
		writeError(w, http.StatusServiceUnavailable, "server_error", "gpu_unavailable", unavailableMessage(snap.Unavailable))
	default:
		recorderOf(w).outcome = "gpu_cold_start"
		g.metrics.coldStartOutcomesTotal.WithLabelValues("gpu_cold_start").Inc()
		w.Header().Set("Retry-After", strconv.Itoa(int(ra.Seconds())))
		writeError(w, http.StatusServiceUnavailable, "server_error", "gpu_cold_start", "The GPU is starting; retry in "+strconv.Itoa(int(ra.Seconds()))+" s.")
	}
}

func (g *Gateway) writeShuttingDownOnDemand(w http.ResponseWriter) {
	recorderOf(w).outcome = "shutting_down"
	g.metrics.coldStartOutcomesTotal.WithLabelValues("shutting_down").Inc()
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusServiceUnavailable, "server_error", "backend_unavailable", "GPU is shutting down; try again in a minute")
}

func (g *Gateway) writeQueueFull(w http.ResponseWriter) {
	recorderOf(w).outcome = "queue_full"
	g.metrics.coldStartOutcomesTotal.WithLabelValues("queue_full").Inc()
	w.Header().Set("Retry-After", "15")
	writeError(w, http.StatusTooManyRequests, "server_error", "queue_full", "Too many requests are waiting for the GPU; retry in 15 s.")
}

const pollEvery = 2 * time.Second

// cachedStatus shares one Status read per poll across every held request.
func (g *Gateway) cachedStatus(now time.Time) controller.Snapshot {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	// Ticker jitter lands some polls just under pollEvery; the slack keeps them from reusing a stale snapshot.
	if now.Sub(g.statusAt) >= pollEvery-pollEvery/4 || g.statusAt.IsZero() {
		g.statusSn, g.statusAt = g.deps.Controller.Status(), now
	}
	return g.statusSn
}

func (g *Gateway) putStatus(now time.Time, s controller.Snapshot) {
	g.statusMu.Lock()
	g.statusSn, g.statusAt = s, now
	g.statusMu.Unlock()
}

func startFailed(s controller.Snapshot) bool {
	switch s.Public {
	case "FAILED", "DELETING", "DRAINING":
		return true
	case "STOPPED":
		return s.Desired != "RUNNING"
	}
	return false
}

// writeHeldTail ends a held stream with a reason line, a finish_reason chunk and [DONE].
func (g *Gateway) writeHeldTail(w http.ResponseWriter, rc *http.ResponseController, code, msg string) {
	setCodeOverride(w, "stream_error")
	_, _ = io.WriteString(w, progressChunk(g.deps.Config.ModelAlias, msg, g.deps.Clock.Now())+errorTail(code, msg))
	_ = rc.Flush()
}

func (g *Gateway) holdAndProxy(w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) {
	cfg := g.deps.Config.OnDemand
	if g.held.Add(1) > int64(cfg.MaxPending) {
		g.held.Add(-1)
		g.writeQueueFull(w)
		return
	}
	g.metrics.pendingRequests.Inc()
	released := false
	release := func() {
		if !released {
			released = true
			g.held.Add(-1)
			g.metrics.pendingRequests.Dec()
		}
	}
	defer release()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	alias := g.deps.Config.ModelAlias
	gone := func() {
		recorderOf(w).outcome = "client_gone"
		setCodeOverride(w, "499")
		g.metrics.coldStartOutcomesTotal.WithLabelValues("client_gone").Inc()
	}
	send := func(s string) bool {
		if _, err := io.WriteString(w, s); err != nil || rc.Flush() != nil {
			gone()
			return false
		}
		return true
	}
	fail := func(outcome, code, msg string) {
		recorderOf(w).outcome = outcome
		g.metrics.coldStartOutcomesTotal.WithLabelValues(outcome).Inc()
		g.writeHeldTail(w, rc, code, msg)
	}

	const restarting = "The service is restarting; send your message again in a minute."
	if g.shuttingDown.Load() {
		fail("shutting_down", "shutting_down", restarting)
		return
	}
	key := keyIDOf(r)
	start := g.deps.Clock.Now()
	snap, err := g.deps.Controller.Demand(r.Context(), key, start.Add(cfg.DemandGrace))
	switch {
	case r.Context().Err() != nil:
		gone()
		return
	case errors.Is(err, controller.ErrDraining):
		fail("shutting_down", "backend_unavailable", "The GPU is shutting down; send your message again in a minute.")
		return
	case errors.Is(err, controller.ErrGPUUnavailable):
		fail("gpu_unavailable", "gpu_unavailable", unavailableMessage(snap.Unavailable))
		return
	case err != nil:
		fail("gpu_unavailable", "gpu_unavailable", "The GPU could not start; send your message again in a minute.")
		return
	}
	g.putStatus(start, snap)

	phrases := cfg.TickerPhrases
	if len(phrases) == 0 {
		phrases = defaultTickerPhrases
	}
	var tr progress.Tracker
	lastLine, lastBeat, lastPhrase := start, start, -1
	tick := g.deps.Clock.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		now := g.deps.Clock.Now()
		if g.shuttingDown.Load() {
			fail("shutting_down", "shutting_down", restarting)
			return
		}
		s := g.cachedStatus(now)
		for _, l := range tr.Lines(progressState(s)) {
			if !send(progressChunk(alias, l, now)) {
				return
			}
			lastLine = now
		}
		switch {
		case s.Ready:
			rt, err := g.deps.Controller.Admit(s.Generation)
			if err != nil {
				fail("gpu_unavailable", "backend_unavailable", "The GPU stopped before it could answer; send your message again.")
				return
			}
			defer g.deps.Controller.RequestEnd(s.Generation)
			g.metrics.waitSeconds.Observe(now.Sub(start).Seconds())
			g.metrics.coldStartOutcomesTotal.WithLabelValues("ready").Inc()
			recorderOf(w).gen = s.Generation
			release()
			g.proxyChat(w, r, s.Generation, rt, body, true, true)
			return
		case startFailed(s):
			fail("gpu_unavailable", "gpu_unavailable", unavailableMessage(s.Unavailable))
			return
		case now.Sub(start) >= cfg.MaxWait:
			// The start continues after the stream ends, so keep it alive long enough for a resend.
			_, _ = g.deps.Controller.Demand(r.Context(), key, now.Add(longestColdStart+cfg.DemandGrace))
			if r.Context().Err() != nil {
				gone()
				return
			}
			fail("gpu_cold_start", "gpu_cold_start", "The GPU is still starting. Send your message again within 5 minutes.")
			return
		}
		if now.Sub(lastBeat) >= cfg.HeartbeatInterval {
			if !send(": keep-alive\n\n") {
				return
			}
			lastBeat = now
			// Any other error is transient; the next heartbeat retries.
			hs, err := g.deps.Controller.Demand(r.Context(), key, now.Add(cfg.DemandGrace))
			switch {
			case r.Context().Err() != nil:
				gone()
				return
			case errors.Is(err, controller.ErrGPUUnavailable):
				fail("gpu_unavailable", "gpu_unavailable", unavailableMessage(hs.Unavailable))
				return
			}
		}
		if now.Sub(lastLine) >= cfg.TickerInterval {
			lastPhrase = nextPhrase(phrases, lastPhrase)
			if !send(progressChunk(alias, phrases[lastPhrase], now)) {
				return
			}
			lastLine = now
		}
		select {
		case <-r.Context().Done():
			gone()
			return
		case <-tick.C:
		}
	}
}
