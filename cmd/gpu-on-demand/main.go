package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// readinessChecks holds every registered readiness probe; empty today, CORE-040 appends to it without restructuring readyz.
var readinessChecks []func() error

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func readyz(w http.ResponseWriter, _ *http.Request) {
	for _, check := range readinessChecks {
		if err := check(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// run starts both servers, blocks until ctx is done, then drains each for up to 30s total.
func run(ctx context.Context, api, health *http.Server) error {
	errs := make(chan error, 2)
	go func() {
		if err := api.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()
	go func() {
		if err := health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = api.Shutdown(shutdownCtx) }()
	go func() { defer wg.Done(); _ = health.Shutdown(shutdownCtx) }()
	wg.Wait()
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Construction order later specs hang their constructors on, left to right:
	// cfg := config.Load(); o := obs.New(cfg); st := store.New(cfg); bg := budget.New(cfg, st)
	// au := auth.New(cfg); pv := provider.New(cfg); tn := tunnel.New(cfg); ctl := controller.New(pv, st, tn, bg)
	// gw := gateway.New(ctl, st, au); mg := mgmt.New(ctl, bg, au)

	apiMux := http.NewServeMux()
	// apiMux.Handle("/v1/", gw); apiMux.Handle("/api/v1/gpu/", mg)
	api := &http.Server{Addr: ":8080", Handler: apiMux}

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", healthz)
	healthMux.HandleFunc("/readyz", readyz)
	health := &http.Server{Addr: ":9090", Handler: healthMux}

	if err := run(ctx, api, health); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
