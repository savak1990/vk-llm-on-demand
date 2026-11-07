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
			setKeyID(w, keyID)
			ctx := context.WithValue(r.Context(), ctxKeyKeyID, keyID)
			next.ServeHTTP(w, r.WithContext(ctx))
		case errors.Is(err, auth.ErrMissing):
			g.metrics.authFailuresTotal.WithLabelValues("missing").Inc()
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "no api key provided")
		case errors.Is(err, auth.ErrMalformed):
			g.metrics.authFailuresTotal.WithLabelValues("malformed").Inc()
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "authorization header must be a bearer token")
		case errors.Is(err, auth.ErrWrongSurface):
			g.metrics.authFailuresTotal.WithLabelValues("wrong_surface").Inc()
			writeError(w, http.StatusForbidden, "permission_error", "permission_denied", "management tokens cannot call the inference api")
		default:
			g.metrics.authFailuresTotal.WithLabelValues("unknown_key").Inc()
			writeError(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "the api key is not recognized")
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
		duration := g.deps.Clock.Now().Sub(start)

		metricCode := rec.codeOverride
		if metricCode == "" {
			metricCode = statusLabel(rec.status)
		}
		g.metrics.requestsTotal.WithLabelValues(route, metricCode).Inc()
		if metricCode == "499" {
			g.metrics.cancelledStreamsTotal.Inc()
		}
		g.metrics.requestSeconds.WithLabelValues(route).Observe(duration.Seconds())

		// The log keeps the real status (e.g. "422"); only the metric
		// label above is bucketed, so cardinality stays bounded without
		// losing the exact code an operator reading logs would want.
		logCode := rec.codeOverride
		if logCode == "" {
			logCode = strconv.Itoa(rec.status)
		}
		outcome := rec.outcome
		if outcome == "" {
			outcome = logCode
		}

		g.deps.Log.Info("gateway: request",
			"request_id", r.Context().Value(ctxKeyRequestID),
			"route", route,
			"status_code", logCode,
			"duration_ms", duration.Milliseconds(),
			"key_id", rec.keyID,
			"stream", rec.stream,
			"instance_gen", rec.gen,
			"outcome", outcome,
			"max_tokens_clamped", rec.clamped,
			"max_tokens_requested", rec.maxTokensRequested,
		)
	})
}

// statusLabel buckets any status outside the enumerated label set by
// class, so an unusual upstream code can never grow the series unbounded.
func statusLabel(code int) string {
	switch code {
	case 200, 400, 401, 403, 404, 405, 413, 429, 499, 500, 502, 503, 504:
		return strconv.Itoa(code)
	}
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 400 && code < 500:
		return "4xx"
	default:
		return "5xx"
	}
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
