// Package gateway serves the OpenAI-compatible inference API under /v1.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
)

// Controller is the subset of *controller.Controller this package calls;
// Start, Stop and Demand are deliberately absent so the gateway cannot
// provision, stop, or register on-demand demand by construction.
type Controller interface {
	Admit(gen int64) (http.RoundTripper, error)
	RequestEnd(gen int64)
	ObserveFirstToken(gen int64, d time.Duration)
	ReportUpstreamFailure(gen int64)
	Status() controller.Snapshot
}

// Deps are every external dependency Gateway calls.
type Deps struct {
	Controller     Controller
	Keys           *auth.Keys
	Seed           *WorkerSeed
	Clock          clock.Clock
	Config         config.Gateway
	InstallationID string
	Reg            prometheus.Registerer
	Log            *slog.Logger
}

type Gateway struct {
	deps      Deps
	mux       *http.ServeMux
	startedAt time.Time
	rl        *auth.Limiter

	// shuttingDown will gate admission once draining logic exists;
	// declared now so that logic can be added without touching this struct.
	shuttingDown atomic.Bool
}

func New(d Deps) (*Gateway, error) {
	if d.Config.ModelAlias == "" {
		return nil, fmt.Errorf("gateway: model alias required")
	}
	if d.Controller == nil {
		return nil, fmt.Errorf("gateway: controller required")
	}
	if d.Keys == nil {
		return nil, fmt.Errorf("gateway: keys required")
	}
	if d.Seed == nil {
		return nil, fmt.Errorf("gateway: worker seed required")
	}
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}

	g := &Gateway{deps: d, startedAt: d.Clock.Now()}
	g.mux = http.NewServeMux()
	g.mux.HandleFunc("GET /v1/models", g.handleModels)
	g.mux.HandleFunc("POST /v1/chat/completions", g.handleChatCompletions)
	g.rl = auth.NewLimiter(d.Config.RateLimitPerKeyRPM, d.Clock)
	return g, nil
}

// Handler returns the full /v1 handler with all middleware chained.
func (g *Gateway) Handler() http.Handler {
	return chain(g.mux,
		g.requestIDMiddleware,
		g.recoverMiddleware,
		g.bodyLimitMiddleware,
		g.authMiddleware,
		g.rateLimitMiddleware,
		g.loggingMiddleware,
	)
}

func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey int

const ctxKeyRequestID ctxKey = iota

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (g *Gateway) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := newRequestID()
		w.Header().Set("X-Request-Id", rid)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, rid)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (g *Gateway) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				g.deps.Log.Error("gateway: panic", "panic", true, "request_id", r.Context().Value(ctxKeyRequestID))
				// A panic after headers are already sent cannot be turned
				// into a fresh status line; the only safe move left is to
				// stop, not write a 500.
				if sr, ok := w.(*statusRecorder); ok && sr.wroteHeader {
					return
				}
				writeError(w, http.StatusInternalServerError, "server_error", "internal_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps the response writer so outer middleware can read
// the real status after a handler returns. codeOverride lets a handler
// record a metric label a plain numeric status can't express.
type statusRecorder struct {
	http.ResponseWriter
	status       int
	wroteHeader  bool
	codeOverride string
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying flusher
// through this wrapper, required for SSE flushing to keep working.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// setCodeOverride is a no-op unless w is a *statusRecorder, which is
// fine for a test that calls a handler directly, outside the middleware
// chain.
func setCodeOverride(w http.ResponseWriter, code string) {
	if sr, ok := w.(*statusRecorder); ok {
		sr.codeOverride = code
	}
}

func (g *Gateway) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
