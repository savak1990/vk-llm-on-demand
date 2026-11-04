// Package main implements the GPU on-demand control plane.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/gateway"
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
	//nolint:gosec // ctx is already cancelled; a fresh context is needed for the 30s drain deadline
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = shutdownA(c)
	}()
	//nolint:gosec // ctx is already cancelled; a fresh context is needed for the 30s drain deadline
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

	clk := clock.Real{}

	// gw stays unconstructed until a real *controller.Controller exists to hand it;
	// mgmt wiring onto apiMux is a separate addition not yet made here either.

	authCfg, err := config.LoadAuth()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	if _, err := config.LoadGateway(); err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	keys, err := auth.Load(authCfg.ClientKeysFile, authCfg.ManagementTokensFile, clk, slog.Default())
	if err != nil {
		slog.Error("auth load error", "err", err)
		os.Exit(1)
	}
	go keys.Run(ctx)
	readinessChecks = append(readinessChecks, func() error {
		if !keys.Loaded() {
			return fmt.Errorf("auth keys not loaded")
		}
		return nil
	})

	seed, err := gateway.LoadWorkerSeed(authCfg.WorkerSeedFile, clk)
	if err != nil {
		slog.Error("worker seed load error", "err", err)
		os.Exit(1)
	}
	go seed.Run(ctx)
	readinessChecks = append(readinessChecks, seed.Loaded)

	// gw, and mounting it on apiMux, wait on a real *controller.Controller existing.

	apiMux := http.NewServeMux()
	// apiMux.Handle("/v1/", gw.Handler()) once ctl is constructed here.
	// apiMux.Handle("/api/v1/gpu/", mg) is a separate, not-yet-made addition.
	api := &http.Server{
		Addr:              ":8080",
		Handler:           apiMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Per-request contexts bound how long a stream can run instead
		// of a server-wide write timeout, which would cut a slow but
		// legitimate SSE response off mid-stream.
		WriteTimeout: 0,
	}

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", healthz)
	healthMux.HandleFunc("/readyz", readyz)
	health := &http.Server{Addr: ":9090", Handler: healthMux, ReadHeaderTimeout: 10 * time.Second}

	if err := run(ctx, api, health); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
