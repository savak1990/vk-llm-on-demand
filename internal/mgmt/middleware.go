package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
)

type keyIDContextKey struct{}

func withKeyID(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, keyIDContextKey{}, keyID)
}

func keyIDFrom(ctx context.Context) string {
	keyID, _ := ctx.Value(keyIDContextKey{}).(string)
	return keyID
}

// withMiddleware orders auth before rate limiting before the body
// limit, so a request is authenticated before its body is read; the
// metrics/logging wrapper sits innermost, next to the mux.
func withMiddleware(h http.Handler, d Deps) http.Handler {
	return withAuth(d.Keys, withRateLimit(d.Limiter, withBodyLimit(withObserve(h, d))))
}

func withAuth(keys *auth.Keys, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID, err := keys.Check(r.Header.Get("Authorization"), auth.Management)
		switch {
		case err == nil:
			next.ServeHTTP(w, r.WithContext(withKeyID(r.Context(), keyID)))
		case errors.Is(err, auth.ErrWrongSurface):
			writeError(w, http.StatusForbidden, "permission_denied", "", "")
		default:
			writeError(w, http.StatusUnauthorized, "invalid_token", "", "")
		}
	})
}

func withRateLimit(limiter *auth.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID := keyIDFrom(r.Context())
		if ok, retryAfter := limiter.Allow(keyID); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", "", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder tees the response so withObserve can parse out the
// error code and reason afterward; the raw body itself is never logged.
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// withObserve records the one metrics/logging line per request, read
// after routing has set the request's matched Pattern.
func withObserve(next http.Handler, d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := d.Clock.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := d.Clock.Now().Sub(start)

		op := opFor(r.Pattern)
		code, reason := errorFields(rec.body.Bytes())
		snap := d.Controller.Status()

		mgmtRequestsTotal.WithLabelValues(op, strconv.Itoa(rec.status)).Inc()
		mgmtRequestSeconds.WithLabelValues(op).Observe(duration.Seconds())

		if d.Log != nil {
			d.Log.Info("mgmt_request",
				"request_id", rec.Header().Get("X-Request-Id"),
				"op", op,
				"status_code", rec.status,
				"code", code,
				"reason", reason,
				"duration_ms", duration.Milliseconds(),
				"key_id", keyIDFrom(r.Context()),
				"state", snap.Public,
				"generation", snap.Generation,
			)
		}
	})
}

func opFor(pattern string) string {
	switch path.Base(pattern) {
	case "start", "status", "stop", "cost":
		return path.Base(pattern)
	default:
		return "other"
	}
}

func errorFields(body []byte) (code, reason string) {
	var doc struct {
		Error struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &doc)
	return doc.Error.Code, doc.Error.Reason
}
