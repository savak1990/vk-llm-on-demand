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
			writeError(w, http.StatusUnauthorized, "authentication_error", "missing_api_key", "no api key provided")
		case errors.Is(err, auth.ErrMalformed):
			writeError(w, http.StatusUnauthorized, "authentication_error", "malformed_api_key", "authorization header must be a bearer token")
		case errors.Is(err, auth.ErrWrongSurface):
			writeError(w, http.StatusForbidden, "permission_error", "wrong_surface", "management tokens cannot call the inference api")
		default:
			writeError(w, http.StatusUnauthorized, "authentication_error", "unknown_key", "the api key is not recognized")
		}
	})
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
