package gateway

import "net/http"

func (g *Gateway) bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, g.deps.Config.MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
