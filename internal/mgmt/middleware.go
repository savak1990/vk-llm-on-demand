package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
)

// keyIDSinkKey holds a *string that withAuth fills in on success; a
// pointer survives the chain because withObserve, now outermost, never
// sees the per-layer request copies auth and rate-limit construct.
type keyIDSinkKey struct{}

func withKeyIDSink(ctx context.Context, sink *string) context.Context {
	return context.WithValue(ctx, keyIDSinkKey{}, sink)
}

func keyIDSink(ctx context.Context) *string {
	sink, _ := ctx.Value(keyIDSinkKey{}).(*string)
	return sink
}

// withMiddleware puts observe outermost so every request — including
// one auth, rate-limit or body-limit rejects — still gets one metric
// increment and one log line, not just the ones a handler sees.
func withMiddleware(h http.Handler, d Deps) http.Handler {
	return withObserve(withAuth(d.Keys, withRateLimit(d.Limiter, withBodyLimit(h))), d)
}

func withAuth(keys *auth.Keys, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID, err := keys.Check(r.Header.Get("Authorization"), auth.Management)
		switch {
		case err == nil:
			if sink := keyIDSink(r.Context()); sink != nil {
				*sink = keyID
			}
			next.ServeHTTP(w, r)
		case errors.Is(err, auth.ErrWrongSurface):
			writeError(w, http.StatusForbidden, "permission_denied", "", "")
		default:
			writeError(w, http.StatusUnauthorized, "invalid_token", "", "")
		}
	})
}

func withRateLimit(limiter *auth.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyID := ""
		if sink := keyIDSink(r.Context()); sink != nil {
			keyID = *sink
		}
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

// withObserve wraps the whole chain; op comes from the path alone
// since an inner layer may reject the request before routing runs.
func withObserve(next http.Handler, d Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := d.Clock.Now()
		op := opForPath(r.URL.Path)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		keyID := new(string)
		next.ServeHTTP(rec, r.WithContext(withKeyIDSink(r.Context(), keyID)))
		duration := d.Clock.Now().Sub(start)

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
				"key_id", *keyID,
				"state", snap.Public,
				"generation", snap.Generation,
			)
		}
	})
}

func opForPath(p string) string {
	switch p {
	case "/api/v1/gpu/start":
		return "start"
	case "/api/v1/gpu/status":
		return "status"
	case "/api/v1/gpu/stop":
		return "stop"
	case "/api/v1/gpu/cost":
		return "cost"
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
