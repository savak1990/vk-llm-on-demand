package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
)

type ctxKey int

const requestIDKey ctxKey = iota

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// NewRequestID returns 16 random bytes as 32 lowercase-hex characters.
func NewRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never errors on a supported platform
	return hex.EncodeToString(b)
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// RequestIDMiddleware reuses a valid inbound X-Request-Id, generates
// a fresh one otherwise, and always echoes it on the response — so a
// client can't inject an arbitrary string into logs via this header.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(id) {
			id = NewRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}
