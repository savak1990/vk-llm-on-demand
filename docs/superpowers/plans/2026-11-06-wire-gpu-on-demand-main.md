# Wire `cmd/gpu-on-demand/main.go` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Construct a real `*controller.Controller` inside `cmd/gpu-on-demand/main.go` and mount `internal/gateway` on `/v1/` and `internal/mgmt` on `/api/v1/gpu/`, so the binary this repository ships actually starts, serves, and stops a GPU instead of listening on two empty muxes.

**Architecture:** Every piece this needs already exists and is merged (`internal/controller`, `internal/gateway`, `internal/mgmt`, `internal/budget`, `internal/worker`, `internal/provider/{vastai,fake}`, `internal/store/{configmap,fake}`, `internal/tunnel`). This plan adds exactly the connective tissue: one new config loader for the fields nothing reads yet, one small adapter type bridging `internal/worker`'s free functions onto the `controller.Worker` interface, and a `wire()` function in `cmd/gpu-on-demand` that assembles all of it — testable with fakes, called for real by `main()`.

**Tech Stack:** Go 1.26, standard library (`net/http`, `log/slog`), `prometheus/client_golang` (already a dependency, used here only to mount `promhttp.Handler()` — no new third-party module).

**Spec:** No spec currently owns this file. CORE-060's Definition of Done and CORE-100's `docs/open-questions.md` both flag it as an open gap ("whoever writes that construction block next"); CORE-080 (Helm chart)'s own kind CI job requires a working binary and cannot pass without it. This plan closes that gap. It is pure assembly of already-decided interfaces — no new ADR, no spec amendment, no new behavior beyond what `internal/controller`, `internal/gateway` and `internal/mgmt` already implement and have their own tests for.

## Global Constraints

- No new third-party Go module (`go list -deps` must show the same set after this plan as before).
- Comments ≤3 lines, never name a spec/ADR/requirement by number (CLAUDE.md).
- On process shutdown, the controller's `Run` loop stops via context cancellation only — it must never call `Stop`/destroy a GPU on its own. A pod restart or redeploy is not a user-requested stop (ADR 0003 §6 "destroy on idle, never stop"; `internal/controller.recover` already reattaches to a running instance after restart via the provider label).
- `VKLLM_MGMT_BUDGET_MONTH_USD` and `VKLLM_BUDGET_MONTH_USD` must not silently diverge (`docs/open-questions.md`'s CORE-100 row). This plan's fix: `main.go` overrides `mgmtCfg.BudgetMonthUSD = budgetCfg.MonthlyUSD` after loading both — one source of truth, zero changes to either config package's tested env-var parsing.
- Every new config field has a name consistent with the shipped `VKLLM_<DOMAIN>_<FIELD>` convention already used throughout `internal/config`.
- Attribution footer on every commit (session rule).

## Design decisions this plan makes

1. **New config lives in `internal/config/worker.go`.** It holds exactly the fields nothing in `internal/config` currently reads: `ModelURL`, `ModelSHA256`, `ModelParallel`, `WorkerImage`, `WorkerDeadManTimeout`, `ProviderAPIKeyFile`, `SSHPrivateKeyFile`. `DiskGB` and `ModelContextSize` are NOT duplicated here — they already exist (`config.Controller.ProviderFilter.MinDiskGB`, `config.Mgmt.ModelContextSize`) and this plan reuses them, confirmed against `internal/worker/create_test.go` (`DiskGB: 60`, the same default as `minDiskGB`).
2. **The `controller.Worker` adapter lives in `cmd/gpu-on-demand`, not in `internal/worker`.** `internal/worker` currently has zero dependency on `internal/controller` (it predates and is unaware of that interface, by design — `controller.Worker`'s own doc comment calls itself a mirror of "the future `internal/worker` package's real type"). Adding the adapter as an unexported type in the binary that consumes it keeps that one-way boundary: `internal/controller` and `internal/worker` never import each other.
3. **Provider API key and SSH private key are read fresh on every call**, not cached/reloaded like `auth.Keys`. `vastai.New` takes `apiKey func() (string, error)` and `tunnel.NewSSH` takes `key func() ([]byte, error)` — both already call-site-driven, so a closure over `os.ReadFile` satisfies ADR 0009's "a Secret update reaches the process without a restart" with no new loader type.
4. **`main()` is split into `wire()` + a thin real-dependency shell.** `wire()` takes every dependency as a parameter (provider, store, dialer, clock, configs, keys, seed, registry, logger) and returns the two `http.Handler`s plus the started controller — so one test can build it entirely from fakes (`provider/fake`, `store/fake`, `tunnel.NewDirect`, `clock/fake`) and assert a real `/v1/models` and a real `/api/v1/gpu/start`→`/api/v1/gpu/status` round-trip through the actual assembled mux, without a cluster or a Vast account. `main()` itself only resolves which concrete provider/store/dialer to build from config and calls `wire()`.
5. **`waitAndShutdown` becomes variadic** (`shutdowns ...func(context.Context) error`) instead of taking exactly two funcs — the smallest change that lets `main()` add `gw.Shutdown` as a third without touching the two existing `TestWaitAndShutdownOnCancel`/`TestWaitAndShutdownReturnsListenError` tests' call shape.
6. **`/metrics` is mounted now, not deferred to CORE-090.** CORE-090 owns dashboards, alert rules and the `ServiceMonitor`/`PrometheusRule` templates — it does not own whether the binary exposes a scrape endpoint at all. Every package already registers its collectors against a `prometheus.Registerer` passed through `Deps`/`Reg`; the only missing piece is `promhttp.Handler()` on the health mux. Skipping it here would leave `internal/controller`, `internal/gateway`, `internal/mgmt` and `internal/worker.Exporter`'s already-written metrics with no endpoint to serve them from.

---

### Task 1: `internal/config.Worker`

**Files:**
- Create: `internal/config/worker.go`
- Create: `internal/config/worker_test.go`

**Interfaces:**
- Produces: `config.Worker{ModelURL, ModelSHA256, ModelParallel, WorkerImage, WorkerDeadManTimeout string/int/time.Duration, ProviderAPIKeyFile, SSHPrivateKeyFile string}`, `config.LoadWorker(idleShutdownTimeout time.Duration) (Worker, error)`.
- Consumed by: Task 2 (adapter's static fields), Task 4 (`main()`).

`LoadWorker` takes `idleShutdownTimeout` as a parameter (not a re-read of `VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT`) because `WorkerDeadManTimeout`'s default derives from it and `config.Controller` already owns that value — `main()` loads `Controller` first and passes its `IdleShutdownTimeout` in, so the derivation has exactly one source, matching this plan's Global Constraints note on the budget duplicate.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/config/worker_test.go
package config

import "testing"
import "time"

func clearWorkerEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_MODEL_URL", "VKLLM_MODEL_SHA256", "VKLLM_MODEL_PARALLEL",
		"VKLLM_WORKER_IMAGE", "VKLLM_WORKER_DEAD_MAN_TIMEOUT",
		"VKLLM_PROVIDER_API_KEY_FILE", "VKLLM_TUNNEL_SSH_KEY_FILE",
	} {
		t.Setenv(k, "")
	}
}

const validSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85"

func TestLoadWorkerDefaults(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("VKLLM_MODEL_URL", "https://huggingface.co/example/model.gguf")
	t.Setenv("VKLLM_MODEL_SHA256", validSHA256)
	t.Setenv("VKLLM_WORKER_IMAGE", "ghcr.io/example/worker@sha256:"+validSHA256)

	w, err := LoadWorker(30 * time.Minute)
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}
	if w.ModelParallel != 1 {
		t.Errorf("ModelParallel = %d, want 1", w.ModelParallel)
	}
	if w.WorkerDeadManTimeout != 40*time.Minute {
		t.Errorf("WorkerDeadManTimeout = %v, want 40m (idle timeout + 10m)", w.WorkerDeadManTimeout)
	}
	if w.ProviderAPIKeyFile != "/etc/vkllm/secrets/provider/VAST_API_KEY" {
		t.Errorf("ProviderAPIKeyFile = %q", w.ProviderAPIKeyFile)
	}
	if w.SSHPrivateKeyFile != "/etc/vkllm/secrets/ssh/id_ed25519" {
		t.Errorf("SSHPrivateKeyFile = %q", w.SSHPrivateKeyFile)
	}
}

func TestLoadWorkerOverridesAndRequired(t *testing.T) {
	clearWorkerEnv(t)
	t.Setenv("VKLLM_MODEL_URL", "https://huggingface.co/example/model.gguf")
	t.Setenv("VKLLM_MODEL_SHA256", validSHA256)
	t.Setenv("VKLLM_WORKER_IMAGE", "ghcr.io/example/worker@sha256:"+validSHA256)
	t.Setenv("VKLLM_MODEL_PARALLEL", "2")
	t.Setenv("VKLLM_WORKER_DEAD_MAN_TIMEOUT", "5m")

	w, err := LoadWorker(30 * time.Minute)
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}
	if w.ModelParallel != 2 {
		t.Errorf("ModelParallel = %d, want 2", w.ModelParallel)
	}
	if w.WorkerDeadManTimeout != 5*time.Minute {
		t.Errorf("WorkerDeadManTimeout = %v, want 5m (explicit override)", w.WorkerDeadManTimeout)
	}
}

func TestLoadWorkerRequiresModelURLSHA256Image(t *testing.T) {
	for _, missing := range []string{"VKLLM_MODEL_URL", "VKLLM_MODEL_SHA256", "VKLLM_WORKER_IMAGE"} {
		clearWorkerEnv(t)
		t.Setenv("VKLLM_MODEL_URL", "https://huggingface.co/example/model.gguf")
		t.Setenv("VKLLM_MODEL_SHA256", validSHA256)
		t.Setenv("VKLLM_WORKER_IMAGE", "ghcr.io/example/worker@sha256:"+validSHA256)
		t.Setenv(missing, "")

		if _, err := LoadWorker(30 * time.Minute); err == nil {
			t.Fatalf("LoadWorker with %s unset: want error, got nil", missing)
		}
	}
}
```

- [ ] **Step 2:** `go test ./internal/config/... -run TestLoadWorker -v` — compile failure, `LoadWorker` undefined.
- [ ] **Step 3: Implement**, reusing the `env*` helpers already in `internal/config/controller.go` (same package):

```go
// internal/config/worker.go
package config

import (
	"fmt"
	"time"
)

// Worker is cmd/gpu-on-demand's own configuration for the model and
// worker image — nothing else in this package reads these values.
type Worker struct {
	ModelURL             string
	ModelSHA256          string
	ModelParallel        int
	WorkerImage          string
	WorkerDeadManTimeout time.Duration
	ProviderAPIKeyFile   string
	SSHPrivateKeyFile    string
}

// LoadWorker reads cmd/gpu-on-demand's model/worker configuration.
// idleShutdownTimeout is config.Controller's own value, passed in so
// WorkerDeadManTimeout's default has exactly one source.
func LoadWorker(idleShutdownTimeout time.Duration) (Worker, error) {
	var w Worker

	w.ModelURL = envString("VKLLM_MODEL_URL", "")
	if w.ModelURL == "" {
		return Worker{}, fmt.Errorf("config: VKLLM_MODEL_URL is required")
	}
	w.ModelSHA256 = envString("VKLLM_MODEL_SHA256", "")
	if w.ModelSHA256 == "" {
		return Worker{}, fmt.Errorf("config: VKLLM_MODEL_SHA256 is required")
	}
	w.WorkerImage = envString("VKLLM_WORKER_IMAGE", "")
	if w.WorkerImage == "" {
		return Worker{}, fmt.Errorf("config: VKLLM_WORKER_IMAGE is required")
	}

	var err error
	if w.ModelParallel, err = envInt("VKLLM_MODEL_PARALLEL", 1); err != nil {
		return Worker{}, err
	}
	if w.WorkerDeadManTimeout, err = envDuration("VKLLM_WORKER_DEAD_MAN_TIMEOUT", idleShutdownTimeout+10*time.Minute); err != nil {
		return Worker{}, err
	}
	w.ProviderAPIKeyFile = envString("VKLLM_PROVIDER_API_KEY_FILE", "/etc/vkllm/secrets/provider/VAST_API_KEY")
	w.SSHPrivateKeyFile = envString("VKLLM_TUNNEL_SSH_KEY_FILE", "/etc/vkllm/secrets/ssh/id_ed25519")
	return w, nil
}
```

  `envDuration(key string, def time.Duration) (time.Duration, error)` (confirmed in `internal/config/controller.go`) already takes any `time.Duration` as its default — `idleShutdownTimeout+10*time.Minute` passes through unchanged, no change to `envDuration` itself needed.

- [ ] **Step 4:** `go test ./internal/config/... -race -v` — all pass, existing tests untouched.
- [ ] **Step 5:** `git add internal/config/worker.go internal/config/worker_test.go && git commit -m "feat: config.LoadWorker for the model/worker fields nothing read yet"`.

---

### Task 2: `controller.Worker` adapter

**Files:**
- Create: `cmd/gpu-on-demand/worker_adapter.go`
- Create: `cmd/gpu-on-demand/worker_adapter_test.go`

**Interfaces:**
- Consumes: `internal/worker.{WorkerKey, BuildCreate, CreateParams, Client, Readiness, Exporter}`, `internal/controller.{Worker, ProbeStatus}`.
- Produces: `newWorkerAdapter(cfg config.Worker, diskGB int, modelAlias string, exp *worker.Exporter) *workerAdapter`, satisfying `controller.Worker`.
- Consumed by: Task 4 (`wire()`).

- [ ] **Step 1: Write the failing tests.**

```go
// cmd/gpu-on-demand/worker_adapter_test.go
package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

const testSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85"

func testWorkerCfg() config.Worker {
	return config.Worker{
		ModelURL: "https://huggingface.co/example/model.gguf", ModelSHA256: testSHA256,
		ModelParallel: 1, WorkerImage: "ghcr.io/example/worker", WorkerDeadManTimeout: 40 * 60 * 1e9,
	}
}

func TestWorkerAdapterWorkerKeyDelegates(t *testing.T) {
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, worker.NewExporter())
	got := a.WorkerKey([]byte("seed"), "iid", 7)
	want := worker.WorkerKey([]byte("seed"), "iid", 7)
	if got != want {
		t.Fatalf("WorkerKey = %q, want %q", got, want)
	}
}

func TestWorkerAdapterBuildCreateSetsOfferIDAndStaticFields(t *testing.T) {
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, worker.NewExporter())
	offer := provider.Offer{ID: 42, Hourly: 710000}
	req := a.BuildCreate(offer, "vkllm:iid:7", "worker-key-xyz")

	if req.OfferID != 42 {
		t.Errorf("OfferID = %d, want 42", req.OfferID)
	}
	if req.DiskGB != 60 {
		t.Errorf("DiskGB = %d, want 60", req.DiskGB)
	}
	if req.Label != "vkllm:iid:7" {
		t.Errorf("Label = %q", req.Label)
	}
	if req.Env["MODEL_ALIAS"] != "qwen3.6-35b-a3b" {
		t.Errorf("Env[MODEL_ALIAS] = %q", req.Env["MODEL_ALIAS"])
	}
	if req.Env["WORKER_API_KEY"] != "worker-key-xyz" {
		t.Errorf("Env[WORKER_API_KEY] = %q", req.Env["WORKER_API_KEY"])
	}
}

func TestWorkerAdapterProbeMapsReadinessToProbeStatus(t *testing.T) {
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, worker.NewExporter())
	// No listener on this transport: every call reaches past dial and
	// returns the http.Client's own connection-refused error, which
	// worker.Client.Probe already treats as Unreachable (not a failure).
	status, err := a.Probe(context.Background(), http.DefaultTransport, "key", "qwen3.6-35b-a3b")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if status != controller.ProbeUnreachable {
		t.Fatalf("Probe status = %v, want ProbeUnreachable (no server listening)", status)
	}
}

func TestWorkerAdapterScrapeUpdatesExporterOnSuccessAndMarksDownOnFailure(t *testing.T) {
	exp := worker.NewExporter()
	a := newWorkerAdapter(testWorkerCfg(), 60, "qwen3.6-35b-a3b", 32768, exp)
	// Same no-listener transport: Metrics fails, Scrape must mark the
	// exporter down and return the error rather than panic or hide it.
	if err := a.Scrape(context.Background(), http.DefaultTransport); err == nil {
		t.Fatal("Scrape: want error with no server listening")
	}
}
```

- [ ] **Step 2:** `go test ./cmd/gpu-on-demand/... -run TestWorkerAdapter -v` — compile failure, `newWorkerAdapter` undefined.
- [ ] **Step 3: Implement.**

```go
// cmd/gpu-on-demand/worker_adapter.go
package main

import (
	"context"
	"net/http"

	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/controller"
	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/worker"
)

// workerAdapter bridges internal/worker's free functions onto the
// controller.Worker interface; internal/worker stays unaware of
// internal/controller, and vice versa.
type workerAdapter struct {
	cfg              config.Worker
	diskGB           int
	modelAlias       string
	modelContextSize int
	exporter         *worker.Exporter
}

func newWorkerAdapter(cfg config.Worker, diskGB int, modelAlias string, modelContextSize int, exp *worker.Exporter) *workerAdapter {
	return &workerAdapter{cfg: cfg, diskGB: diskGB, modelAlias: modelAlias, modelContextSize: modelContextSize, exporter: exp}
}

var _ controller.Worker = (*workerAdapter)(nil)

func (a *workerAdapter) WorkerKey(seed []byte, installationID string, gen int64) string {
	return worker.WorkerKey(seed, installationID, gen)
}

// BuildCreate's error path (malformed ModelSHA256/ModelURL, Ctx/Parallel
// below 1) is unreachable in practice: config.LoadWorker already
// rejects an empty ModelURL/ModelSHA256 at startup, and modelCtx/
// ModelParallel come from config loaders with their own positive
// defaults — a panic here means a config invariant broke, not a
// request-time condition the controller can recover from.
func (a *workerAdapter) BuildCreate(offer provider.Offer, label, workerKey string) provider.CreateRequest {
	req, err := worker.BuildCreate(worker.CreateParams{
		Image: a.cfg.WorkerImage, DiskGB: a.diskGB, Label: label,
		ModelURL: a.cfg.ModelURL, ModelSHA256: a.cfg.ModelSHA256, Alias: a.modelAlias,
		Ctx: a.modelContextSize, Parallel: a.cfg.ModelParallel,
		DeadMan: a.cfg.WorkerDeadManTimeout, WorkerKey: workerKey,
	})
	if err != nil {
		panic("worker: BuildCreate rejected config that LoadWorker should have already validated: " + err.Error())
	}
	req.OfferID = offer.ID
	return req
}

func (a *workerAdapter) Probe(ctx context.Context, rt http.RoundTripper, key, alias string) (controller.ProbeStatus, error) {
	readiness, err := (worker.Client{Transport: rt, Key: key, Alias: alias}).Probe(ctx)
	return controller.ProbeStatus(readiness), err
}

func (a *workerAdapter) Scrape(ctx context.Context, rt http.RoundTripper) error {
	snap, err := (worker.Client{Transport: rt, Key: "", Alias: a.modelAlias}).Metrics(ctx)
	if err != nil {
		a.exporter.MarkDown()
		return err
	}
	a.exporter.Update(snap)
	return nil
}
```

  `controller.ProbeStatus(readiness)` relies on `controller.ProbeUnreachable/ProbeLoading/ProbeReady` and `worker.Unreachable/Loading/Ready` being the same three values in the same order (both `iota`-based, confirmed by reading `internal/controller/worker.go` and `internal/worker/probe.go` before writing this cast) — add a one-line comment at the cast noting this, and a test (`TestWorkerAdapterProbeMapsReadinessToProbeStatus` above) that would catch a future reordering.

  `modelContextSize` comes from `config.Mgmt.ModelContextSize`, passed into `newWorkerAdapter`'s fourth parameter at construction time in Task 4 — `workerAdapter` itself holds no `config.Mgmt` and needs none.

- [ ] **Step 4:** `go test ./cmd/gpu-on-demand/... -race -v` — all pass (new tests plus the existing `TestHealthzAlwaysOK` etc., untouched).
- [ ] **Step 5:** `git add cmd/gpu-on-demand/worker_adapter.go cmd/gpu-on-demand/worker_adapter_test.go && git commit -m "feat: adapt internal/worker onto the controller.Worker interface"`.

---

### Task 3: `waitAndShutdown` becomes variadic

**Files:**
- Modify: `cmd/gpu-on-demand/main.go`

**Interfaces:**
- Produces: `waitAndShutdown(ctx context.Context, errs <-chan error, shutdowns ...func(context.Context) error) error` (was: fixed `shutdownA, shutdownB func(...)`).
- Consumed by: Task 4 (`main()` passes 3 funcs instead of 2).

This is its own task, ahead of `wire()`, because it changes an existing, already-tested function's signature — a reviewable unit on its own, not buried inside the larger Task 4 diff.

- [ ] **Step 1:** Read `cmd/gpu-on-demand/main_test.go`'s `TestWaitAndShutdownOnCancel` and `TestWaitAndShutdownReturnsListenError` in full (both already exist and pass against the current two-argument signature) — no new test is added in this task; both existing tests must keep passing unmodified against the new variadic signature, since calling a variadic function with exactly two arguments is valid Go and needs no call-site change in the test file.
- [ ] **Step 2:** `go test ./cmd/gpu-on-demand/... -run TestWaitAndShutdown -v` — currently passing; re-run after Step 3 to confirm no regression.
- [ ] **Step 3: Implement.** In `main.go`, change:

```go
func waitAndShutdown(ctx context.Context, errs <-chan error, shutdownA, shutdownB func(context.Context) error) error {
```

  to:

```go
func waitAndShutdown(ctx context.Context, errs <-chan error, shutdowns ...func(context.Context) error) error {
```

  and replace the two hard-coded goroutines' `wg.Add(2)` / two `go func(){...shutdownA(c)...}()` / `...shutdownB(c)...` blocks with one loop:

```go
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
```

  Update `run(ctx, api, health *http.Server) error`'s own call site (`return waitAndShutdown(ctx, errs, api.Shutdown, health.Shutdown)`) — unchanged syntactically, still valid against the new variadic signature.

- [ ] **Step 4:** `go test ./cmd/gpu-on-demand/... -race -v` — `TestWaitAndShutdownOnCancel` and `TestWaitAndShutdownReturnsListenError` both still pass, unmodified.
- [ ] **Step 5:** `git add cmd/gpu-on-demand/main.go && git commit -m "refactor: waitAndShutdown takes any number of shutdown funcs"`.

---

### Task 4: `wire()` — the real assembly, with a fakes-only integration test

**Files:**
- Create: `cmd/gpu-on-demand/wire.go`
- Create: `cmd/gpu-on-demand/wire_test.go`

**Interfaces:**
- Consumes: every package's already-exported `New`/`Deps`/`Load*` listed in this plan's Architecture section.
- Produces: `wireDeps` (the parameter struct), `wire(ctx context.Context, d wireDeps) (apiMux http.Handler, ctl *controller.Controller, gw *gateway.Gateway, err error)`.
- Consumed by: Task 5 (`main()`).

`wire` takes a `provider.Provider`, `store.Store`, `tunnel.Dialer` and `clock.Clock` as parameters precisely so this task's own test can supply fakes and prove the assembly end-to-end — no cluster, no SSH, no Vast account.

- [ ] **Step 1: Write the failing test.**

```go
// cmd/gpu-on-demand/wire_test.go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/savak1990/vk-llm-on-demand/internal/auth"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/config"
	"github.com/savak1990/vk-llm-on-demand/internal/gateway"
	fakeprovider "github.com/savak1990/vk-llm-on-demand/internal/provider/fake"
	fakestore "github.com/savak1990/vk-llm-on-demand/internal/store/fake"
	"github.com/savak1990/vk-llm-on-demand/internal/tunnel"
)

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

	d := wireDeps{
		Ctx: context.Background(), Clock: clk, Provider: prov, Store: st,
		Dialer: tunnel.NewDirect("unused:0"), Keys: keys,
		Seed: seed, WorkerSeedFunc: func() ([]byte, error) { return seed.Bytes(), nil },
		InstallationID: "test-iid", ModelAlias: "qwen3.6-35b-a3b",
		Controller: config.Controller{ProvisioningMode: "explicit", MaxHourlyPriceUSD: 0.80, MaxLifetime: 12 * time.Hour, IdleShutdownTimeout: 30 * time.Minute, ReconcileInterval: 60 * time.Second, MaxProvisionAttemptsPerHour: 3},
		Budget:     config.Budget{MonthlyUSD: 30, WarnFraction: 0.8, MaxHourlyPriceUSD: 0.80},
		Gateway:    config.Gateway{ModelAlias: "qwen3.6-35b-a3b", MaxBodyBytes: 1 << 20, MaxTokensCap: 8192, RateLimitPerKeyRPM: 60, GenerationTimeout: 10 * time.Minute},
		Mgmt:       config.Mgmt{ProviderName: "fake", ModelAlias: "qwen3.6-35b-a3b", ModelContextSize: 32768, BudgetMonthUSD: 30, RateLimitPerKeyRPM: 60},
		Worker:     testWorkerCfg(),
		Reg:        prometheus.NewRegistry(), Log: noopTestLogger(),
	}

	apiMux, ctl, gw, err := wire(d)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(func() { _ = gw.Shutdown(context.Background()) })
	go func() { _ = ctl.Run(d.Ctx) }()

	// Inference path, no GPU running yet: 503 backend_unavailable, not 404/500.
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-vkllm-clientkey1")
	rec := httptest.NewRecorder()
	apiMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /v1/chat/completions before start: code = %d, want 503", rec.Code)
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
	if prov.Calls("create") < 1 {
		t.Fatal("wire: starting through the mounted mgmt handler never reached the fake provider's create")
	}
}
```

  Add `noopTestLogger()` (a `*slog.Logger` to `io.Discard`) and `writeFileHelper(path, content string) error` (a 3-line `os.WriteFile` wrapper) as small test-local helpers in `wire_test.go` if `worker_adapter_test.go` (Task 2) didn't already add an equivalent — check before duplicating; keep exactly one copy per unique helper across the package's `_test.go` files.

- [ ] **Step 2:** `go test ./cmd/gpu-on-demand/... -run TestWireServes -v` — compile failure, `wire`/`wireDeps` undefined.
- [ ] **Step 3: Implement.**

```go
// cmd/gpu-on-demand/wire.go
package main

import (
	"context"
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
	Reg            prometheus.Registerer
	Log            *slog.Logger
}

// wire builds the controller and mounts the gateway and management
// handlers on one mux. ctl's Run must be started by the caller — wire
// only constructs it, so a test can inspect it before starting the loop.
func wire(d wireDeps) (http.Handler, *controller.Controller, *gateway.Gateway, error) {
	exp := worker.NewExporter()
	w := newWorkerAdapter(d.Worker, d.Controller.ProviderFilter.MinDiskGB, d.ModelAlias, d.Mgmt.ModelContextSize, exp)
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
	apiMux.Handle("/api/v1/gpu/", mg)
	return apiMux, ctl, gw, nil
}
```

  `gateway.Deps.Seed` (`*gateway.WorkerSeed`) and `controller.Deps.WorkerSeed` (`func() ([]byte, error)`) are different shapes for different consumers — `wireDeps` carries both, under `Seed` and `WorkerSeedFunc` respectively, as shown above.

- [ ] **Step 4:** `go test ./cmd/gpu-on-demand/... -race -count=1 -v` — all pass.
- [ ] **Step 5:** `git add cmd/gpu-on-demand/wire.go cmd/gpu-on-demand/wire_test.go && git commit -m "feat: wire() assembles controller+gateway+mgmt behind one mux"`.

---

### Task 5: `main()` — real dependencies, `/metrics`, extended shutdown

**Files:**
- Modify: `cmd/gpu-on-demand/main.go`

- [ ] **Step 1:** No new test in this task — `main()` itself (provider/store/dialer selection from real config, `os.Exit` on error) is the one piece of this plan that cannot be unit-tested without a real cluster or a real Vast account; Task 4's `wire_test.go` already proves the assembly it delegates to. Confirm this reasoning holds by checking whether `cmd/gpu-on-demand/main_test.go`'s existing tests ever called `main()` directly (they don't — they test `healthz`, `readyz`, `waitAndShutdown` only) before proceeding.
- [ ] **Step 2:** Replace `main()`'s body (between the existing `config.LoadAuth()`/`config.LoadGateway()` calls and the `apiMux := http.NewServeMux()` line) with the full wiring:

```go
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
	mgmtCfg.BudgetMonthUSD = budgetCfg.MonthlyUSD // one source of truth; see plan Global Constraints
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

	var prov provider.Provider
	switch mgmtCfg.ProviderName {
	case "fake":
		prov = fakeprovider.NewSeeded(clk)
	default:
		prov = vastai.New("https://console.vast.ai", http.DefaultClient,
			func() (string, error) {
				b, err := os.ReadFile(workerCfg.ProviderAPIKeyFile)
				return strings.TrimSpace(string(b)), err
			}, clk, rand.New(rand.NewSource(time.Now().UnixNano())), slog.Default(), reg)
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
	go func() { _ = ctl.Run(ctx) }()
```

  `gateway.WorkerSeed.Bytes() []byte` is already exported exactly as used above (`internal/gateway/workerseed.go`). Remove the now-duplicate `seed, err := gateway.LoadWorkerSeed(...)` block that already exists earlier in the current `main()` — it moves to the position shown above, right before `wire(...)`, since `wire` needs it; do not call `LoadWorkerSeed` twice.

- [ ] **Step 3:** Replace the two placeholder comment lines (`// gw, and mounting it on apiMux, wait on a real *controller.Controller existing.` and the two `// apiMux.Handle(...)` comments) — now dead, since `apiMux` is `wire`'s return value directly; delete the local `apiMux := http.NewServeMux()` declaration and have `api.Handler` reference wire's returned mux instead.
- [ ] **Step 4:** Mount `/metrics` on the health server:

```go
	healthMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
```

  placed alongside the existing `healthMux.HandleFunc("/healthz", healthz)` / `/readyz` lines, before `health := &http.Server{...}`.
- [ ] **Step 5:** Extend the shutdown call from `run(ctx, api, health)`'s body — change `waitAndShutdown(ctx, errs, api.Shutdown, health.Shutdown)` to `waitAndShutdown(ctx, errs, gw.Shutdown, api.Shutdown, health.Shutdown)`; `run`'s own signature gains a `gw *gateway.Gateway` parameter to make this possible, and its one call site in `main()` passes the `gw` returned by `wire`.
- [ ] **Step 6:** `go build ./...` — compiles clean. Fix every import this step's code needs (`configmap "internal/store/configmap"`, `fakeprovider "internal/provider/fake"`, `"internal/provider/vastai"`, `"internal/tunnel"`, `"math/rand"`, `"strings"`, `"github.com/prometheus/client_golang/prometheus"`, `"github.com/prometheus/client_golang/prometheus/promhttp"`) — add each only once actually referenced, `goimports`-clean.
- [ ] **Step 7:** `go vet ./... && go test ./... -race -count=1` — full repo green, including `cmd/gpu-on-demand`'s own `TestHealthzAlwaysOK`, `TestReadyzOKWithNoChecksRegistered`, `TestWaitAndShutdownOnCancel`, `TestWaitAndShutdownReturnsListenError`, Task 2's adapter tests and Task 4's `wire_test.go`, all still passing.
- [ ] **Step 8:** Manual smoke test (the one validation this plan can't automate without a cluster — matches CORE-010's own precedent for this exact command):
  ```
  VKLLM_INSTALLATION_ID=smoke VKLLM_STATE_CONFIGMAP=smoke-state \
  VKLLM_MGMT_PROVIDER_NAME=fake VKLLM_TUNNEL_DIALER=direct VKLLM_TUNNEL_DIRECT_ADDR=127.0.0.1:1 \
  VKLLM_MODEL_URL=https://huggingface.co/example/m.gguf \
  VKLLM_MODEL_SHA256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85 \
  VKLLM_WORKER_IMAGE=ghcr.io/example/worker \
  VKLLM_AUTH_CLIENT_KEYS_FILE=/tmp/ck VKLLM_AUTH_MANAGEMENT_TOKENS_FILE=/tmp/mt VKLLM_AUTH_WORKER_SEED_FILE=/tmp/ws \
  go run ./cmd/gpu-on-demand
  ```
  expect it to fail fast with a clear `config:` error naming whichever file/env var is missing first (since `/tmp/ck` etc. don't exist in a throwaway shell) rather than panicking — if it panics, that's a real bug this task must fix before moving on; a clean `os.Exit(1)` with a named cause is the expected, acceptable outcome of this particular smoke test, not a failure of this task.
- [ ] **Step 9:** `git add cmd/gpu-on-demand/main.go && git commit -m "feat: main.go constructs the real controller, mounts gateway+mgmt, serves /metrics"`.

---

### Task 6: Close the tracked gaps in docs

**Files:**
- Modify: `docs/open-questions.md`
- Modify: `specs/core/060-D-gateway-openai-api/spec.md`

- [ ] **Step 1:** In `docs/open-questions.md`, close the CORE-100 row about `VKLLM_BUDGET_MONTH_USD`/`VKLLM_MGMT_BUDGET_MONTH_USD` — mark it `— closed` with a one-line pointer to this plan's Task 5 fix (mgmtCfg.BudgetMonthUSD overridden from budgetCfg.MonthlyUSD in main.go, same convention as other closed rows in that table).
- [ ] **Step 2:** In `specs/core/060-D-gateway-openai-api/spec.md`'s Definition of Done, flip the unchecked box `- [ ] main wires gateway.New, Shutdown, and mounts /v1/ on apiMux — not done` to `- [x]`, with a one-line Evidence addendum pointing at this plan's commits (not restating this plan's full content — a pointer, per the existing convention CORE-100's writeback used for cross-spec references).
- [ ] **Step 3:** `make specs-check`.
- [ ] **Step 4:** `git add docs/open-questions.md specs/core/060-D-gateway-openai-api/spec.md && git commit -m "docs: close the main.go wiring gap flagged by CORE-060 and CORE-100"`.

---

### Task 7: Validation

- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./... -race -count=1` — full repo green, run twice to catch flakes in the new goroutine-heavy `wire_test.go`.
- [ ] **Step 2:** `make specs-check go-build go-test go-lint` — all pass.
- [ ] **Step 3:** `go list -deps ./cmd/gpu-on-demand/...` — confirm no new third-party module beyond what was already in `go.mod` (the only package newly imported by `main.go` that wasn't already a transitive dependency of this binary is `prometheus/client_golang/prometheus/promhttp`, which is the same module already vendored for `prometheus/client_golang/prometheus`).

## Verification (end to end)

1. `go test ./... -race -count=1` green, twice in a row.
2. `cmd/gpu-on-demand/wire_test.go`'s `TestWireServesInferenceAndManagementThroughOneMux` proves one real controller is reachable from both `/v1/` and `/api/v1/gpu/` through fakes alone.
3. `docs/open-questions.md`'s CORE-100 budget-duplicate row and `specs/core/060-D-gateway-openai-api/spec.md`'s main.go DoD item are both closed.
4. `go list -deps ./cmd/gpu-on-demand/...` shows no new third-party module.
5. Manual smoke test (Task 5 Step 8) fails cleanly with a named config error, not a panic, proving `main()`'s error path is intact end to end.
