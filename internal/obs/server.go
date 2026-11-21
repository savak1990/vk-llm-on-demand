package obs

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds the obs HTTP server's own settings.
type Config struct {
	Addr string // ":9090"
	Log  *slog.Logger
}

// NewServer builds the health/ready/metrics server: /healthz is
// always 200, /readyz is 503 on the first failing check (cause logged,
// never echoed to the caller), /metrics serves reg via promhttp.
func NewServer(reg *prometheus.Registry, checks []func() error, cfg Config) *http.Server {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		for _, check := range checks {
			if err := check(); err != nil {
				log.Warn("readiness_check_failed", "err", err)
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
}
