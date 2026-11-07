package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/gateway"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

func noopTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func writeFileHelper(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func writeTestKeyFile(t *testing.T, name string, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/" + name
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := writeFileHelper(path, content); err != nil {
		t.Fatalf("writeFileHelper: %v", err)
	}
	return path
}

func TestWireServesInferenceAndManagementThroughOneMux(t *testing.T) {
	clk := fakeclock.New(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.NewSeeded(clk)
	st := fakestore.New("test-iid")

	clientKeysPath := writeTestKeyFile(t, "client-keys", "sk-vkllm-clientkey1")
	mgmtTokensPath := writeTestKeyFile(t, "management-tokens", "sk-vkllm-mgmttoken1")
	keys, err := auth.Load(clientKeysPath, mgmtTokensPath, clk, noopTestLogger())
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}

	seed, err := gateway.LoadWorkerSeed(writeTestKeyFile(t, "worker-seed", "seed-bytes"), clk)
	if err != nil {
		t.Fatalf("gateway.LoadWorkerSeed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d := wireDeps{
		Ctx: ctx, Clock: clk, Provider: prov, Store: st,
		Dialer: tunnel.NewDirect("unused:0"), Keys: keys,
		Seed: seed, WorkerSeedFunc: func() ([]byte, error) { return seed.Bytes(), nil },
		InstallationID: "test-iid", ModelAlias: "qwen3.6-35b-a3b",
		Controller: config.Controller{ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80, MaxLifetime: 12 * time.Hour, IdleShutdownTimeout: 30 * time.Minute, ReconcileInterval: 60 * time.Second, MaxProvisionAttemptsPerHour: 3},
		Budget:     config.Budget{MonthlyUSD: 30, WarnFraction: 0.8, MaxHourlyPriceUSD: 0.80},
		Gateway:    config.Gateway{ModelAlias: "qwen3.6-35b-a3b", MaxBodyBytes: 1 << 20, MaxTokensCap: 8192, RateLimitPerKeyRPM: 60, GenerationTimeout: 10 * time.Minute},
		Mgmt:       config.Mgmt{ProviderName: "fake", ModelAlias: "qwen3.6-35b-a3b", ModelContextSize: 32768, BudgetMonthUSD: 30, RateLimitPerKeyRPM: 60},
		Worker:     testWorkerCfg(),
		Reg:        obs.NewRegistry(), Log: noopTestLogger(),
	}

	apiMux, ctl, gw, err := wire(d)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	go func() { _ = ctl.Run(d.Ctx) }()

	// Inference path, no GPU running yet: 503 backend_unavailable, not 404/500.
	// A valid JSON body naming the configured model is required so the
	// handler reaches the Admit() check instead of 400-ing on bad JSON first.
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"qwen3.6-35b-a3b","messages":[]}`))
	req.Header.Set("Authorization", "Bearer sk-vkllm-clientkey1")
	rec := httptest.NewRecorder()
	apiMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/chat/completions before start: code = %d, want 503", rec.Code)
	}

	// Run's startup reconcile reads the provider balance on its own
	// goroutine (controller.go's lastBalanceKnown); without waiting for
	// it, Allow races an unknown balance and denies start as balance_low.
	balanceDeadline := time.Now().Add(2 * time.Second)
	for ctl.Status().ProviderBalanceUSD == nil && time.Now().Before(balanceDeadline) {
		time.Sleep(10 * time.Millisecond)
	}

	// Management path: start, then status reports something other than
	// STOPPED — proves mgmt.New is really driving the same controller.
	req = httptest.NewRequest("POST", "/api/v1/gpu/start", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec = httptest.NewRecorder()
	apiMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/gpu/start: code = %d, want 200 or 202", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/gpu/status", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec = httptest.NewRecorder()
	apiMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/gpu/status: code = %d, want 200", rec.Code)
	}
	var status struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if status.State == "STOPPED" {
		t.Fatalf("status state = %q, want something other than STOPPED after start", status.State)
	}
	// Start's effects (search, then create) run on the controller's own
	// spawned goroutines, not inline in the mgmt handler, so give them a
	// short real-time window before failing.
	deadline := time.Now().Add(2 * time.Second)
	for prov.Calls("create") < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if prov.Calls("create") < 1 {
		t.Fatal("wire: starting through the mounted mgmt handler never reached the fake provider's create")
	}
}

// TestWireBudgetOverridesMgmtBudgetMonthUSD proves wire() makes
// Budget.MonthlyUSD the one source of truth for the cost document, not
// whatever Mgmt.BudgetMonthUSD happened to carry in.
func TestWireBudgetOverridesMgmtBudgetMonthUSD(t *testing.T) {
	clk := fakeclock.New(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.NewSeeded(clk)
	st := fakestore.New("test-iid")

	clientKeysPath := writeTestKeyFile(t, "client-keys", "sk-vkllm-clientkey1")
	mgmtTokensPath := writeTestKeyFile(t, "management-tokens", "sk-vkllm-mgmttoken1")
	keys, err := auth.Load(clientKeysPath, mgmtTokensPath, clk, noopTestLogger())
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	seed, err := gateway.LoadWorkerSeed(writeTestKeyFile(t, "worker-seed", "seed-bytes"), clk)
	if err != nil {
		t.Fatalf("gateway.LoadWorkerSeed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	d := wireDeps{
		Ctx: ctx, Clock: clk, Provider: prov, Store: st,
		Dialer: tunnel.NewDirect("unused:0"), Keys: keys,
		Seed: seed, WorkerSeedFunc: func() ([]byte, error) { return seed.Bytes(), nil },
		InstallationID: "test-iid", ModelAlias: "qwen3.6-35b-a3b",
		Controller: config.Controller{ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80, MaxLifetime: 12 * time.Hour, IdleShutdownTimeout: 30 * time.Minute, ReconcileInterval: 60 * time.Second, MaxProvisionAttemptsPerHour: 3},
		Budget:     config.Budget{MonthlyUSD: 42, WarnFraction: 0.8, MaxHourlyPriceUSD: 0.80},
		Gateway:    config.Gateway{ModelAlias: "qwen3.6-35b-a3b", MaxBodyBytes: 1 << 20, MaxTokensCap: 8192, RateLimitPerKeyRPM: 60, GenerationTimeout: 10 * time.Minute},
		Mgmt:       config.Mgmt{ProviderName: "fake", ModelAlias: "qwen3.6-35b-a3b", ModelContextSize: 32768, BudgetMonthUSD: 99, RateLimitPerKeyRPM: 60},
		Worker:     testWorkerCfg(),
		Reg:        obs.NewRegistry(), Log: noopTestLogger(),
	}

	apiMux, ctl, gw, err := wire(d)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	go func() { _ = ctl.Run(d.Ctx) }()

	req := httptest.NewRequest("GET", "/api/v1/gpu/cost", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-mgmttoken1")
	rec := httptest.NewRecorder()
	apiMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/gpu/cost: code = %d, want 200", rec.Code)
	}

	var cost struct {
		BudgetMonthUSD float64 `json:"budgetMonthUSD"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&cost); err != nil {
		t.Fatalf("decode cost response: %v", err)
	}
	if cost.BudgetMonthUSD != 42 {
		t.Fatalf("budgetMonthUSD = %v, want 42 (Budget.MonthlyUSD, not Mgmt.BudgetMonthUSD=99)", cost.BudgetMonthUSD)
	}
}

// TestWireMetricsCatalogComplete proves the registry wire() actually
// populates — not a hand-built scratch registry — carries every metric
// obs.Catalog() promises, minus the vastai-only ones the fake provider
// never touches and the worker-scrape-gated ones no scrape ever
// succeeds for here, and nothing else.
func TestWireMetricsCatalogComplete(t *testing.T) {
	clk := fakeclock.New(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC))
	prov := fakeprovider.NewSeeded(clk)
	st := fakestore.New("test-iid")

	clientKeysPath := writeTestKeyFile(t, "client-keys", "sk-vkllm-clientkey1")
	mgmtTokensPath := writeTestKeyFile(t, "management-tokens", "sk-vkllm-mgmttoken1")
	keys, err := auth.Load(clientKeysPath, mgmtTokensPath, clk, noopTestLogger())
	if err != nil {
		t.Fatalf("auth.Load: %v", err)
	}
	seed, err := gateway.LoadWorkerSeed(writeTestKeyFile(t, "worker-seed", "seed-bytes"), clk)
	if err != nil {
		t.Fatalf("gateway.LoadWorkerSeed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg := obs.NewRegistry()
	d := wireDeps{
		Ctx: ctx, Clock: clk, Provider: prov, Store: st,
		Dialer: tunnel.NewDirect("unused:0"), Keys: keys,
		Seed: seed, WorkerSeedFunc: func() ([]byte, error) { return seed.Bytes(), nil },
		InstallationID: "test-iid", ModelAlias: "qwen3.6-35b-a3b",
		Controller: config.Controller{ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80, MaxLifetime: 12 * time.Hour, IdleShutdownTimeout: 30 * time.Minute, ReconcileInterval: 60 * time.Second, MaxProvisionAttemptsPerHour: 3},
		Budget:     config.Budget{MonthlyUSD: 30, WarnFraction: 0.8, MaxHourlyPriceUSD: 0.80},
		Gateway:    config.Gateway{ModelAlias: "qwen3.6-35b-a3b", MaxBodyBytes: 1 << 20, MaxTokensCap: 8192, RateLimitPerKeyRPM: 60, GenerationTimeout: 10 * time.Minute},
		Mgmt:       config.Mgmt{ProviderName: "fake", ModelAlias: "qwen3.6-35b-a3b", ModelContextSize: 32768, BudgetMonthUSD: 30, RateLimitPerKeyRPM: 60},
		Worker:     testWorkerCfg(),
		Reg:        reg, Log: noopTestLogger(),
	}
	_, ctl, gw, err := wire(d)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	go func() { _ = ctl.Run(ctx) }()

	// The 8 llamacpp re-exports the worker Exporter withholds until a
	// real scrape succeeds; wire() here dials an address nothing
	// listens on, so none of these can ever appear.
	workerScrapeGated := []string{
		"vkllm_inference_prompt_tokens_total", "vkllm_inference_prompt_seconds_total",
		"vkllm_inference_generated_tokens_total", "vkllm_inference_generation_seconds_total",
		"vkllm_inference_prompt_tokens_per_second", "vkllm_inference_generated_tokens_per_second",
		"vkllm_inference_requests_processing", "vkllm_inference_requests_deferred",
	}

	wantNames := map[string]bool{}
	for _, s := range obs.Catalog() {
		wantNames[s.Name] = true
	}
	for _, n := range obs.VastaiOnlyMetrics() {
		delete(wantNames, n) // config.Mgmt.ProviderName="fake" above never registers these 3
	}
	for _, n := range workerScrapeGated {
		delete(wantNames, n)
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	gotNames := map[string]bool{}
	for _, f := range families {
		gotNames[f.GetName()] = true
	}
	for name := range wantNames {
		if !gotNames[name] {
			t.Errorf("missing metric family %s", name)
		}
	}
	for name := range gotNames {
		if !wantNames[name] {
			t.Errorf("unexpected metric family %s (not in obs.Catalog(), or a vastai-only/worker-scrape-gated one leaked)", name)
		}
	}

	if err := obs.AuditCardinality(reg, obs.DeniedMetricLabels); err != nil {
		t.Fatalf("AuditCardinality: %v", err)
	}
}
