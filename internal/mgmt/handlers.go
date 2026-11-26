package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

func (s *server) routes() {
	s.mux.HandleFunc("/api/v1/gpu/start", s.method(http.MethodPost, s.handleStart))
	s.mux.HandleFunc("/api/v1/gpu/status", s.method(http.MethodGet, s.handleStatus))
	s.mux.HandleFunc("/api/v1/gpu/stop", s.method(http.MethodPost, s.handleStop))
	s.mux.HandleFunc("/api/v1/gpu/cost", s.method(http.MethodGet, s.handleCost))
	s.mux.HandleFunc("/api/v1/gpu/budget", s.method(http.MethodPost, s.handleBudget))
	s.mux.HandleFunc("/api/v1/gpu/rentals", s.method(http.MethodGet, s.handleRentals))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "", "")
	})
}

func (s *server) method(want string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != want {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "")
			return
		}
		h(w, r)
	}
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	if !decodeStrict(w, r, &struct{}{}) {
		return
	}
	snap, err := s.start.do(r.Context(), s.d.Controller.Start)
	s.respondOp(w, "start", snap, err)
}

type stopRequest struct {
	Force bool `json:"force"`
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	var req stopRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	if req.Force && !s.d.Config.ShutdownAllowForce {
		writeError(w, http.StatusBadRequest, "force_not_enabled", "", "")
		return
	}
	snap, err := s.stop.do(r.Context(), func(ctx context.Context) (controller.Snapshot, error) {
		return s.d.Controller.Stop(ctx, req.Force)
	})
	s.respondOp(w, "stop", snap, err)
}

func (s *server) respondOp(w http.ResponseWriter, op string, snap controller.Snapshot, err error) {
	switch {
	case err == nil:
		doc := buildStatusDocument(snap, s.d.Config, s.d.Clock.Now())
		doc.Result = resultFor(op, snap)
		code := http.StatusAccepted
		if doc.Result == "already_running" || doc.Result == "already_stopped" {
			code = http.StatusOK
		}
		writeJSON(w, code, doc)
	case errors.Is(err, controller.ErrGPUUnavailable):
		message := ""
		if snap.Unavailable == "budget_exhausted" {
			message = fmt.Sprintf("monthly budget of $%.2f reached ($%.2f spent); raise it with vk-llm budget set",
				snap.BudgetMonthUSD, snap.SpendMTDUSD)
		}
		writeError(w, http.StatusServiceUnavailable, "gpu_unavailable", message, snap.Unavailable)
	case errors.Is(err, controller.ErrForceNotEnabled):
		writeError(w, http.StatusBadRequest, "force_not_enabled", "", "")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "", "")
	}
}

func (s *server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	snap := s.d.Controller.Status()
	writeJSON(w, http.StatusOK, buildStatusDocument(snap, s.d.Config, s.d.Clock.Now()))
}

func (s *server) handleCost(w http.ResponseWriter, _ *http.Request) {
	snap := s.d.Controller.Status()
	writeJSON(w, http.StatusOK, buildCostDocument(snap, s.d.Config, s.d.Clock.Now()))
}

func (s *server) handleRentals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildRentalsDocument(s.d.Controller.Status()))
}

type budgetRequest struct {
	MonthlyUSD float64 `json:"monthlyUSD"`
}

func (s *server) handleBudget(w http.ResponseWriter, r *http.Request) {
	var req budgetRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	before := s.d.Controller.Status()
	snap, err := s.d.Controller.SetBudgetMonthUSD(r.Context(), req.MonthlyUSD)
	switch {
	case err == nil:
		keyID := ""
		if sink := keyIDSink(r.Context()); sink != nil {
			keyID = *sink
		}
		if s.d.Log != nil {
			s.d.Log.Info("mgmt: budget changed", "key_id", keyID, "oldUSD", before.BudgetMonthUSD, "newUSD", snap.BudgetMonthUSD)
		}
		writeJSON(w, http.StatusOK, buildStatusDocument(snap, s.d.Config, s.d.Clock.Now()))
	case errors.Is(err, controller.ErrBudgetInvalid):
		writeError(w, http.StatusBadRequest, "budget_invalid", "monthlyUSD must be a positive number", "")
	case errors.Is(err, controller.ErrBudgetAboveCeiling):
		writeError(w, http.StatusBadRequest, "budget_above_ceiling",
			fmt.Sprintf("monthlyUSD must not exceed the configured ceiling of $%.2f", snap.BudgetCeilingUSD), "")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "", "")
	}
}

// decodeStrict rejects unknown fields and the wrong JSON type for
// Force (e.g. "force":"yes"); an empty body is valid (all fields default).
func decodeStrict(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if r.ContentLength == 0 {
		return true
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "", "")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "", "")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, errCode, message, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(errorBody(errCode, message, reason))
}
