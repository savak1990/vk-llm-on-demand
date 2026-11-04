// Package gateway serves the OpenAI-compatible inference API under /v1.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
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

	// shuttingDown lets classify() label an in-flight failure correctly;
	// it does not itself block new admission, so Shutdown's caller must
	// stop the listener first, the way http.Server.Shutdown does.
	shuttingDown atomic.Bool

	activeWG  sync.WaitGroup
	activeMu  sync.Mutex
	activeFns map[int]context.CancelFunc
	nextID    int

	metrics *gatewayMetrics
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

	g := &Gateway{deps: d, startedAt: d.Clock.Now(), activeFns: make(map[int]context.CancelFunc)}
	g.mux = http.NewServeMux()
	g.mux.HandleFunc("GET /v1/models", g.handleModels)
	g.mux.HandleFunc("POST /v1/chat/completions", g.handleChatCompletions)
	g.rl = auth.NewLimiter(d.Config.RateLimitPerKeyRPM, d.Clock)
	g.metrics = newGatewayMetrics(d.Reg)
	return g, nil
}

// Handler returns the full /v1 handler with all middleware chained.
func (g *Gateway) Handler() http.Handler {
	return chain(g.mux,
		g.requestIDMiddleware,
		g.accessLogAndMetrics,
		g.recoverMiddleware,
		g.bodyLimitMiddleware,
		g.authMiddleware,
		g.rateLimitMiddleware,
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
		g.activeWG.Add(1)
		defer g.activeWG.Done()
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
	keyID        string
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

// setKeyID stashes the authenticated key id on the *statusRecorder so
// accessLogAndMetrics, sitting above authMiddleware in the chain, can
// log it; authMiddleware's own context value never reaches that layer.
func setKeyID(w http.ResponseWriter, keyID string) {
	if sr, ok := w.(*statusRecorder); ok {
		sr.keyID = keyID
	}
}

// shutdownCutoffDelay returns how long to wait before cancelling active
// requests: ctx's deadline minus a 5s margin for handlers to write
// their own shutting_down response, floored at 0.
func shutdownCutoffDelay(now, deadline time.Time) time.Duration {
	d := deadline.Sub(now) - 5*time.Second
	if d < 0 {
		return 0
	}
	return d
}

// Shutdown gives in-flight requests a cutoff window to respond on their
// own, then cancels whatever remains and waits for every handler to
// return; it never touches the instance, only HTTP serving stops.
func (g *Gateway) Shutdown(ctx context.Context) error {
	g.shuttingDown.Store(true)
	if deadline, ok := ctx.Deadline(); ok {
		if delay := shutdownCutoffDelay(g.deps.Clock.Now(), deadline); delay > 0 {
			select {
			case <-g.deps.Clock.After(delay):
			case <-ctx.Done():
			}
		}
	}
	g.cancelActive()

	done := make(chan struct{})
	go func() { g.activeWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

func (g *Gateway) registerActive(cancel context.CancelFunc) (id int) {
	g.activeMu.Lock()
	defer g.activeMu.Unlock()
	id = g.nextID
	g.nextID++
	g.activeFns[id] = cancel
	return id
}

func (g *Gateway) unregisterActive(id int) {
	g.activeMu.Lock()
	defer g.activeMu.Unlock()
	delete(g.activeFns, id)
}

func (g *Gateway) cancelActive() {
	g.activeMu.Lock()
	defer g.activeMu.Unlock()
	for _, cancel := range g.activeFns {
		cancel()
	}
}
