package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	"github.com/savak1990/vk-llm-on-demand/internal/budget"
	"github.com/savak1990/vk-llm-on-demand/internal/clock"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/gateway"
	"github.com/savak1990/vk-llm-on-demand/internal/mgmt"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

// wireDeps is everything wire needs; main() supplies real
// provider/store/dialer, wire_test.go supplies fakes.
type wireDeps struct {
	Ctx            context.Context
	Clock          clock.Clock
	Provider       provider.Provider
	Store          store.Store
	Dialer         tunnel.Dialer
	Keys           *auth.Keys
	Seed           *gateway.WorkerSeed    // gateway.Deps needs the *WorkerSeed itself
	WorkerSeedFunc func() ([]byte, error) // controller.Deps needs only the current bytes
	InstallationID string
	ModelAlias     string
	Controller     config.Controller
	Budget         config.Budget
	Gateway        config.Gateway
	Mgmt           config.Mgmt
	Worker         config.Worker
	Reg            prometheus.Registerer // must already carry obs's metrics, e.g. built via obs.NewRegistry
	Log            *slog.Logger
}

// wire builds the controller and mounts the gateway and management
// handlers on one mux. ctl's Run must be started by the caller — wire
// only constructs it, so a test can inspect it before starting the loop.
func wire(d wireDeps) (http.Handler, *controller.Controller, *gateway.Gateway, error) {
	d.Mgmt.BudgetMonthUSD = d.Budget.MonthlyUSD // one source of truth, not a second copy

	if _, err := worker.BuildCreate(worker.CreateParams{
		Image: d.Worker.WorkerImage, DiskGB: d.Controller.ProviderFilter.MinDiskGB, Label: "wire-validate",
		ModelURL: d.Worker.ModelURL, ModelSHA256: d.Worker.ModelSHA256, Alias: d.ModelAlias,
		Ctx: d.Mgmt.ModelContextSize, Parallel: d.Worker.ModelParallel,
		DeadMan: d.Worker.WorkerDeadManTimeout, WorkerKey: "wire-validate",
	}); err != nil {
		return nil, nil, nil, fmt.Errorf("worker config: %w", err)
	}

	exp := worker.NewExporter()
	w := newWorkerAdapter(d.Worker, d.Controller.ProviderFilter.MinDiskGB, d.ModelAlias, d.Mgmt.ModelContextSize, exp)
	controller.RegisterMetrics(d.Reg)
	store.RegisterMetrics(d.Reg)
	d.Reg.MustRegister(exp)
	b := budget.New(budget.Config{MonthlyUSD: d.Budget.MonthlyUSD, WarnFraction: d.Budget.WarnFraction, MaxHourlyPriceUSD: d.Budget.MaxHourlyPriceUSD})

	ctl, err := controller.New(controller.Deps{
		Provider: d.Provider, Store: d.Store, Dialer: d.Dialer, Clock: d.Clock,
		Budget: b, Worker: w, WorkerSeed: d.WorkerSeedFunc,
		InstallationID: d.InstallationID, ModelAlias: d.ModelAlias,
		Config: d.Controller, Reg: d.Reg, Log: d.Log,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	gw, err := gateway.New(gateway.Deps{
		Controller: ctl, Keys: d.Keys, Seed: d.Seed, Clock: d.Clock, Config: d.Gateway,
		InstallationID: d.InstallationID, Reg: d.Reg, Log: d.Log,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	limiter := auth.NewLimiter(d.Mgmt.RateLimitPerKeyRPM, d.Clock)
	mg := mgmt.New(mgmt.Deps{Controller: ctl, Keys: d.Keys, Limiter: limiter, Clock: d.Clock, Config: d.Mgmt, Reg: d.Reg, Log: d.Log})

	apiMux := http.NewServeMux()
	apiMux.Handle("/v1/", gw.Handler())
	apiMux.Handle("/api/v1/gpu/", obs.RequestIDMiddleware(mg))
	return apiMux, ctl, gw, nil
}
