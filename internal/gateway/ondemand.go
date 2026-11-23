package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
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

func (g *Gateway) holdAndProxy(w http.ResponseWriter, r *http.Request, _ map[string]json.RawMessage) {
	if g.held.Load() >= int64(g.deps.Config.OnDemand.MaxPending) {
		g.writeQueueFull(w)
		return
	}
	g.refuseColdStart(w, r)
}
