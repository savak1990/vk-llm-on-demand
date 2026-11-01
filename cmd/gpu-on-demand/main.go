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

// readinessChecks holds every registered readiness probe; it stays empty until checks exist, so readyz never needs restructuring when one is added.
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

	return waitAndShutdown(ctx, errs, api.Shutdown, health.Shutdown)
}

// waitAndShutdown blocks until ctx is done or a listen error arrives, then shuts both down
// concurrently, each on its own 30s deadline started at the same instant.
func waitAndShutdown(ctx context.Context, errs <-chan error, shutdownA, shutdownB func(context.Context) error) error {
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = shutdownA(c)
	}()
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = shutdownB(c)
	}()
	wg.Wait()
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Construction order later specs hang their constructors on, left to right:
	// cfg := config.Load(); o := obs.New(cfg); st := store.New(cfg); bg := budget.New(cfg, st); au := auth.New(cfg)
	// pv := provider.New(cfg); tn := tunnel.New(cfg); ctl := controller.New(pv, st, tn, bg); gw := gateway.New(ctl, st, au); mg := mgmt.New(ctl, bg, au)

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
