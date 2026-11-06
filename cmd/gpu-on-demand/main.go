// Package main implements the GPU on-demand control plane.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/gateway"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/provider/vastai"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/store/configmap"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
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
func run(ctx context.Context, api, health *http.Server, gw *gateway.Gateway) error {
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

	return waitAndShutdown(ctx, errs, gw.Shutdown, api.Shutdown, health.Shutdown)
}

// waitAndShutdown blocks until ctx is done or a listen error arrives, then shuts all down
// concurrently, each on its own 30s deadline started at the same instant.
func waitAndShutdown(ctx context.Context, errs <-chan error, shutdowns ...func(context.Context) error) error {
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	var wg sync.WaitGroup
	wg.Add(len(shutdowns))
	for _, shutdown := range shutdowns {
		shutdown := shutdown
		//nolint:gosec // ctx is already cancelled; a fresh context is needed for the 30s drain deadline
		go func() {
			defer wg.Done()
			c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = shutdown(c)
		}()
	}
	wg.Wait()
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	clk := clock.Real{}

	authCfg, err := config.LoadAuth()
	if err != nil {
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

	topCfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	controllerCfg, err := config.LoadController()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	budgetCfg, err := config.LoadBudget()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	gatewayCfg, err := config.LoadGateway()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	mgmtCfg, err := config.LoadMgmt()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}
	workerCfg, err := config.LoadWorker(controllerCfg.IdleShutdownTimeout)
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	reg := prometheus.NewRegistry()

	st, err := configmap.InCluster(topCfg.StateConfigMapName, topCfg.InstallationID)
	if err != nil {
		slog.Error("store config error", "err", err)
		os.Exit(1)
	}
	storeClient, err := configmap.New(st)
	if err != nil {
		slog.Error("store init error", "err", err)
		os.Exit(1)
	}
	readinessChecks = append(readinessChecks, store.Ready(storeClient))

	var prov provider.Provider
	switch mgmtCfg.ProviderName {
	case "fake":
		prov = fakeprovider.NewSeeded(clk)
	case "vastai":
		//nolint:gosec // backoff jitter, not a credential
		rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
		prov = vastai.New("https://console.vast.ai", http.DefaultClient,
			func() (string, error) {
				b, err := os.ReadFile(workerCfg.ProviderAPIKeyFile)
				return strings.TrimSpace(string(b)), err
			}, clk, rnd, slog.Default(), reg)
	default:
		slog.Error("config error", "err", fmt.Errorf("unrecognized VKLLM_PROVIDER_NAME %q", mgmtCfg.ProviderName))
		os.Exit(1)
	}

	var dialer tunnel.Dialer
	switch controllerCfg.TunnelDialer {
	case "direct":
		dialer = tunnel.NewDirect(controllerCfg.TunnelDirectAddr)
	default:
		dialer = tunnel.NewSSH(func() ([]byte, error) {
			return os.ReadFile(workerCfg.SSHPrivateKeyFile)
		})
	}

	seed, err := gateway.LoadWorkerSeed(authCfg.WorkerSeedFile, clk)
	if err != nil {
		slog.Error("worker seed load error", "err", err)
		os.Exit(1)
	}
	go seed.Run(ctx)
	readinessChecks = append(readinessChecks, seed.Loaded)

	apiMux, ctl, gw, err := wire(wireDeps{
		Ctx: ctx, Clock: clk, Provider: prov, Store: storeClient, Dialer: dialer,
		Keys: keys, Seed: seed, WorkerSeedFunc: func() ([]byte, error) { return seed.Bytes(), nil },
		InstallationID: topCfg.InstallationID, ModelAlias: gatewayCfg.ModelAlias,
		Controller: controllerCfg, Budget: budgetCfg, Gateway: gatewayCfg, Mgmt: mgmtCfg, Worker: workerCfg,
		Reg: reg, Log: slog.Default(),
	})
	if err != nil {
		slog.Error("wire error", "err", err)
		os.Exit(1)
	}
	go func() {
		if err := ctl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("controller run exited", "err", err)
		}
	}()

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
	healthMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	health := &http.Server{Addr: ":9090", Handler: healthMux, ReadHeaderTimeout: 10 * time.Second}

	if err := run(ctx, api, health, gw); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
