package gateway

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
)

const ctxKeyKeyID ctxKey = iota + 100

func (g *Gateway) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID, err := g.deps.Keys.Check(r.Header.Get("Authorization"), auth.Inference)
		switch {
		case err == nil:
			ctx := context.WithValue(r.Context(), ctxKeyKeyID, keyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		case errors.Is(err, auth.ErrMissing):
			g.metrics.authFailuresTotal.WithLabelValues("missing").Inc()
			writeError(w, http.StatusUnauthorized, "authentication_error", "missing_api_key", "no api key provided")
		case errors.Is(err, auth.ErrMalformed):
			g.metrics.authFailuresTotal.WithLabelValues("malformed").Inc()
			writeError(w, http.StatusUnauthorized, "authentication_error", "malformed_api_key", "authorization header must be a bearer token")
		case errors.Is(err, auth.ErrWrongSurface):
			g.metrics.authFailuresTotal.WithLabelValues("wrong_surface").Inc()
			writeError(w, http.StatusForbidden, "permission_error", "wrong_surface", "management tokens cannot call the inference api")
		default:
			g.metrics.authFailuresTotal.WithLabelValues("unknown_key").Inc()
			writeError(w, http.StatusUnauthorized, "authentication_error", "unknown_key", "the api key is not recognized")
		}
	})
}

// routeLabel keeps the requests_total cardinality fixed regardless of
// how many distinct paths exist, so an attacker probing random paths
// can never create unbounded label series.
func routeLabel(r *http.Request) string {
	switch r.URL.Path {
	case "/v1/chat/completions":
		return "chat_completions"
	case "/v1/models":
		return "models"
	default:
		return "other"
	}
}

// accessLogAndMetrics wraps auth and rate-limiting, not just the mux,
// so a 401/403/429 — which returns before any route handler runs — is
// still counted and logged.
func (g *Gateway) accessLogAndMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := routeLabel(r)
		g.metrics.activeRequests.Inc()
		defer g.metrics.activeRequests.Dec()

		start := g.deps.Clock.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		code := rec.codeOverride
		if code == "" {
			code = statusLabel(rec.status)
		}
		g.metrics.requestsTotal.WithLabelValues(route, code).Inc()
		g.metrics.requestSeconds.WithLabelValues(route).Observe(g.deps.Clock.Now().Sub(start).Seconds())

		g.deps.Log.Info("gateway: request",
			"request_id", r.Context().Value(ctxKeyRequestID),
			"route", route,
			"status_code", code,
			"key_id", r.Context().Value(ctxKeyKeyID),
		)
	})
}

func statusLabel(code int) string {
	return strconv.Itoa(code)
}

func (g *Gateway) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID, _ := r.Context().Value(ctxKeyKeyID).(string)
		ok, retryAfter := g.rl.Allow(keyID)
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
			writeError(w, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}
