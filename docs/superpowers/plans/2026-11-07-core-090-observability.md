# CORE-090: Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `internal/obs` (health/ready/metrics server, redacting JSON logger, shared request-id middleware, cardinality auditor, the 5 metrics no other package owns), wire it into `cmd/gpu-on-demand`, close the real gaps the exploration below found in already-shipped packages, and ship a `PrometheusRule` + Grafana dashboard `ConfigMap` in CORE-080's chart.

**Architecture:** `internal/obs` is a new, dependency-free leaf package (stdlib + `prometheus/client_golang` only) that every other package either feeds (via its setters) or is fed by (the shared logger/request-id middleware). `cmd/gpu-on-demand/main.go` and `wire.go` are the only call sites that assemble it.

**Tech Stack:** `log/slog`, `prometheus/client_golang`, Prometheus `promtool` (new pinned CI binary), Helm.

**Spec:** [specs/core/090-D-observability/spec.md](../../../specs/core/090-D-observability/spec.md)

## Why this plan looks different from the spec's own effort estimate

Spec 090 was written 2026-11-01, before any of CORE-050/055/060/070/100 existed, and estimates "one session (4-6h)" assuming `internal/obs` would need to invent most of the metric catalog. A full parallel exploration of the real, shipped code (branch `worktree-core-090-observability`, forked from `main` at commit `e019143`) found the opposite: **38 of the catalog's 43 metrics already exist, correctly named, labeled, and registered into one shared `*prometheus.Registry`** (`cmd/gpu-on-demand/main.go:148`, threaded through `wire.go` into every package). `internal/obs` itself is a green field (`internal/obs/doc.go` is its only file, 2 lines). The real remaining work is: building `internal/obs` itself, a handful of small, real, additive touches to already-shipped packages, and the chart templates CORE-080 deliberately deferred here.

Ten concrete findings from that exploration, each resolved by a ruling below or a task:

1. `/healthz`, `/readyz`, `/metrics` are hand-rolled directly in `main.go` today (`main.go:34-48,226-230`) — exactly the logic `internal/obs.NewServer` absorbs.
2. No redacting `slog.Handler` exists; today's "redaction" is just never passing secret fields to `slog` at the call site. `internal/obs.RedactingHandler` is genuinely new code, not a refactor.
3. A request-id middleware already exists, but it's `internal/gateway`-local (`gateway.go:135-156`) and generates **16** hex characters, not the spec's required **32** (`^[0-9a-f]{32}$`, R7) — a real, pre-existing non-compliance. `internal/mgmt` has no request-id middleware of its own at all; its own doc comment (`mgmt.go:48`) says the caller is supposed to wrap it, but `wire.go` never does.
4. `internal/controller`, `internal/provider/vastai`, `internal/store`, `internal/gateway`, `internal/mgmt`, `internal/worker` all already register their own catalog metrics for real, with real call sites — confirmed by grepping every one.
5. One real instrumentation gap: `vkllm_coldstart_stage_seconds` has all 8 `stage` values in its enum but only `first_token` is ever `.Observe()`'d (`admission.go:108`); the other 7 have no call site anywhere.
6. One real missing metric: `vkllm_gateway_cancelled_streams_total` does not exist in code at all — confirmed by grepping the whole `internal/gateway` tree. Spec 090's own Open Questions row flags this and names CORE-060 as the closer, but CORE-060 already shipped `DONE` without it.
7. `internal/provider/vastai`'s 3 metrics only register as a side effect of `vastai.New(...)` (`main.go:169-173`), which only runs when `VKLLM_PROVIDER_NAME=vastai`. `internal/provider/fake` registers nothing. Every test and the only CI integration point (`chart-kind`'s kind job) use the fake provider — so **"43 families" is only ever true against a real Vast.ai deployment; every environment this repo can actually test shows 40.**
8. `internal/budget.Checker` (not `Budget` — the real exported type name) already has `UsedRatio`/`EstimatedMTD` (`checker.go`) and `Rollover` (`ledger.go:73-85`, resets `doc.Ledger.TotalUSD` to 0 at a UTC month boundary, dropping any pre-rollover partial cost). `controller.Snapshot` already carries `SpendInstanceUSD`/`SpendMTDUSD`/`ProviderBalanceUSD` (added ahead of this spec). Nothing currently calls an `obs` setter with any of these, because `obs` doesn't exist yet — but the natural call site is `recordDocGauges`'s own call point inside `processEvent` (`loop.go:165`), which already runs on every periodic `EvTick`, not `Status()` (an HTTP-triggered, on-demand read that would leave the budget-high alert silent between polls).
9. CORE-080's chart has no `PrometheusRule` or dashboard `ConfigMap` template (confirmed: `templates/` has no `prometheusrule*`/`dashboard*`/`grafana*` file), and no `promtool`/`yq` binary is installed anywhere in this repo's CI today.
10. `cmd/gpu-on-demand/main_test.go` tests the package-level `healthz`/`readyz` functions directly by name — those functions move into `obs`, so those two tests move with them (replaced by `obs`'s own `TestServerEndpoints`, not duplicated).

## Global Constraints

- Comments ≤3 lines, never name a spec/ADR/requirement by number (CLAUDE.md).
- Nothing sensitive (secrets, root domains, fqdns) in any file; use `<root-domain>`/`lab.example.com` placeholders where a shape must be shown (not needed in this plan — nothing here touches a domain).
- `make specs-check go-build go-test go-lint helm-lint helm-template` all clean before any PR; never claim a check passes without running it in this session.
- Full commit-SHA image tags, never `latest` (unaffected by this plan — no image changes).
- Attribution footer on every commit (session rule).
- `internal/obs` has zero internal dependencies on any other `internal/*` package — only `prometheus/client_golang`, `log/slog`, and stdlib. This is load-bearing: every other package depends on `obs`, never the reverse.
- A Prometheus `Counter.Add` panics on a negative argument; every monotonic-counter setter in this plan clamps its own delta to `>= 0` before calling it.

## Design decisions this plan makes

1. **AC6's "43 families" is amended to be provider-conditional**, per finding 7 above: 40 families in every test and CI environment this repo has (fake provider), 43 only against a real Vast.ai deployment. `obs.VastaiOnlyMetrics()` names the 3 excluded series so a test can compute the right expected set without hardcoding two separate literal lists.
2. **R9's full-cross-product pre-init requirement is scoped to exactly what AC1 tests**: `vkllm_coldstart_stage_seconds`'s 8 stages (not yet done — Task 6 fixes it) and `vkllm_gpu_state`'s 8 states (already done, via the existing one-hot `setGPUState`). Pre-initializing every other labeled metric's full combination set (e.g. `vkllm_gateway_requests_total`'s `route`×`code`) is real but untested-by-this-spec work; it is not in scope here and is recorded as an explicit residual, not silently dropped.
3. **`internal/obs`'s registration API follows this codebase's own existing per-package convention** (`RegisterMetrics(reg prometheus.Registerer)`, matching `controller.RegisterMetrics`/`store.RegisterMetrics`) rather than the spec's original literal `NewRegistry() *prometheus.Registry`-only sketch (written before that convention existed in any shipped code). `NewRegistry()` still exists as `main.go`'s one-line convenience wrapper around `RegisterMetrics`.
4. **The 5 obs-owned metrics are rebuilt fresh on every `RegisterMetrics` call** (an `atomic.Pointer` swap, not package-level singleton `var`s) specifically so `obs`'s own tests — and any other package's tests that call it — get a clean slate every time, the same test-isolation problem `internal/mgmt`'s existing package-level metric vars have but untested.
5. **Request-id consolidation takes the smaller diff per package**, matching each package's own existing design intent: `internal/gateway` keeps its `requestIDMiddleware` call site (`Handler()`'s chain, first) and swaps only its body to delegate to `obs`; `internal/mgmt`'s own doc comment already says its caller is supposed to wrap it with request-id middleware, so `wire.go` is the one line that starts actually doing that.
6. **The PrometheusRule's alert rules and the `promtool test rules` fixture live as raw files** (`deploy/helm/gpu-on-demand/files/alerts.yaml`, `alerts_test.yaml`) with a bare top-level `groups:` key — exactly `promtool`'s own native rule-file format — so CI runs `promtool check/test rules` directly against the source file, with no Helm render and no `yq` envelope-stripping step. The chart template pulls the same file verbatim via `.Files.Get` for the rendered `PrometheusRule`.
7. **The cost-metric publish call lives in `internal/controller`, at `loop.go:165`'s existing `recordDocGauges` call site**, not in `Status()` — so the budget-high alert and the dashboard's spend panel stay fresh on every reconcile tick regardless of whether anything ever polls the management API (finding 8 above).

---

### Task 1: Spec amendment — rulings, AC6 fix, DRAFT→READY

**Files:**
- Modify: `specs/core/090-D-observability/spec.md`
- Modify: `docs/open-questions.md`

- [ ] **Step 1:** In spec 090's AC6, replace the text:
  > On a kind cluster with the fake provider, `curl :9090/metrics` returns HTTP 200 and `grep -c '^# TYPE vkllm_'` on the body (one `# TYPE` line per family regardless of `_bucket`/`_sum`/`_count` suffixes) equals `43`. (R1, R6)

  with:
  > On a kind cluster with the fake provider, `curl :9090/metrics` returns HTTP 200 and `grep -c '^# TYPE vkllm_'` on the body equals `40`: `internal/provider/vastai`'s 3 series (`vkllm_provider_requests_total`, `vkllm_provider_request_seconds`, `vkllm_provider_rate_limited_total`) only register as a side effect of constructing the real Vast.ai client, which the fake provider path never does. A deployment with `config.provider.name=vastai` shows all 43. (R1, R6)

- [ ] **Step 2:** In the Metric catalog's intro paragraph, replace "43 metric families total." with: "43 metric families total; 40 of them register unconditionally, the 3 `vkllm_provider_*` series only when the real Vast.ai provider is wired (see AC6)."

- [ ] **Step 3:** Add a new row to the Open Questions table:
  | Question | Why it matters | Settled by | Default |
  |---|---|---|---|
  | R9's "every label combination initialized to zero at process start" is only tested by AC1 for 2 of the catalog's labeled metrics (`vkllm_coldstart_stage_seconds`, `vkllm_gpu_state`) | Pre-initializing every other labeled metric's full combination set (e.g. `vkllm_gateway_requests_total`'s `route`×`code`) is real, untested-by-this-spec work | Implementation time, CORE-090 | Scoped to AC1's 2 metrics for this spec; a future spec or CI assertion can widen it |

- [ ] **Step 4:** Replace the `vkllm_coldstart_stage_seconds` catalog row's trailing description (after "Duration of each cold-start stage (architecture §4)") — no text change needed there, but add one sentence to the Metric catalog's intro, after the existing pre-init sentence: "`vkllm_coldstart_stage_seconds` is pre-initialized to zero for all 8 stages at registration time (R9); only `first_token` is observed with a real duration today, the other 7 stages' instrumentation is out of this spec's scope (see Open questions)." Add a matching row to the Open Questions table naming this as a residual.

- [ ] **Step 5:** Close the existing Open Questions row about `vkllm_gateway_cancelled_streams_total` (currently "Settled by: CORE-060, before either spec leaves DRAFT") by replacing its "Settled by" / "Default" cells with: "**closed** (CORE-090): CORE-060 shipped `DONE` without it, so this spec adds the counter itself, in `internal/gateway`, incrementing at the same point `vkllm_gateway_requests_total`'s `code` label is computed as `\"499\"` — a one-line addition to the single place that already decides the metric's code label, not a new call site per disconnect path."

- [ ] **Step 6:** Add one new paragraph after the "Request id propagation" section's existing text: "`internal/gateway`'s existing request-id generator predates this spec and produces 16 hex characters, not this section's required 32 (`^[0-9a-f]{32}$}`); CORE-090 fixes this by having `internal/gateway`'s existing middleware call site delegate to `obs.NewRequestID`/`obs.RequestIDMiddleware` internally, and has `internal/mgmt`'s already-documented 'the caller wraps it' design actually wired up for the first time, in `wire.go`."

- [ ] **Step 7:** Replace the AC8 text:
  > `yq '.spec' prometheusrule.yaml | promtool check rules /dev/stdin` exits 0 and its output reports `4 rules found`. A fifth bogus rule added to a scratch copy changes that count, proving the test is not vacuous. (R10, NFR-10.6)

  with:
  > `promtool check rules deploy/helm/gpu-on-demand/files/alerts.yaml` exits 0 and its output reports `4 rules found`. The file's top-level `groups:` key is `promtool`'s own native rule-file format, so no Helm render or `yq` envelope-stripping step is needed; the chart's `PrometheusRule` template pulls this same file verbatim via `.Files.Get`. A fifth bogus rule added to a scratch copy changes that count, proving the test is not vacuous. (R10, NFR-10.6)

  And replace both AC10 and AC11's `promtool test rules` references from "a rules-test file" to "`deploy/helm/gpu-on-demand/files/alerts_test.yaml`" by name.

- [ ] **Step 8:** Flip `status: "DRAFT"` to `status: "READY"` in the front matter. Folder stays `090-P-...` (both `DRAFT` and `READY` map to `P` per `specs/README.md`'s letter table).

- [ ] **Step 9:** `make specs-check`.

- [ ] **Step 10:** `git add specs/core/090-D-observability/spec.md docs/open-questions.md && git commit -m "docs: CORE-090 spec amendment — provider-conditional AC6, pre-init scope, cancelled_streams_total and request-id rulings"`.

---

### Task 2: `internal/obs` — registry, owned metrics, catalog

**Files:**
- Create: `internal/obs/metrics.go`
- Create: `internal/obs/metrics_test.go`
- Create: `internal/obs/catalog.go`
- Create: `internal/obs/catalog_test.go`

**Interfaces:**
- Produces: `obs.RegisterMetrics(reg prometheus.Registerer)`, `obs.NewRegistry() *prometheus.Registry`, `obs.SetBudgetUsedRatio(v float64)`, `obs.SetCostInstanceUSD(v float64)`, `obs.SetProviderBalanceUSD(v float64)`, `obs.IncInferenceScrapeErrors()`, `obs.AddCostEstimatedUSD(delta float64)`, `obs.Catalog() []obs.Spec`, `obs.VastaiOnlyMetrics() []string`.
- Consumed by: every later task in this plan.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/obs/metrics_test.go
package obs

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestObsOwnedMetricsPreInitialized(t *testing.T) {
	reg := NewRegistry()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := map[string]bool{
		"vkllm_budget_used_ratio": true, "vkllm_cost_estimated_usd_total": true,
		"vkllm_cost_instance_usd": true, "vkllm_provider_balance_usd": true,
		"vkllm_inference_scrape_errors_total": true,
	}
	got := map[string]bool{}
	for _, f := range families {
		if !want[f.GetName()] {
			continue
		}
		got[f.GetName()] = true
		m := f.GetMetric()[0]
		if g := m.GetGauge(); g != nil && g.GetValue() != 0 {
			t.Errorf("%s = %v, want 0", f.GetName(), g.GetValue())
		}
		if c := m.GetCounter(); c != nil && c.GetValue() != 0 {
			t.Errorf("%s = %v, want 0", f.GetName(), c.GetValue())
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing metric family %s", name)
		}
	}
}

func TestSettersUpdateValuesAndNegativeDeltaIsIgnored(t *testing.T) {
	reg := NewRegistry()
	SetBudgetUsedRatio(0.42)
	SetCostInstanceUSD(1.5)
	SetProviderBalanceUSD(12.5)
	IncInferenceScrapeErrors()
	AddCostEstimatedUSD(3.0)
	AddCostEstimatedUSD(-1.0) // must not panic, must not apply

	families, _ := reg.Gather()
	values := map[string]float64{}
	for _, f := range families {
		m := f.GetMetric()[0]
		switch {
		case m.GetGauge() != nil:
			values[f.GetName()] = m.GetGauge().GetValue()
		case m.GetCounter() != nil:
			values[f.GetName()] = m.GetCounter().GetValue()
		}
	}
	if values["vkllm_budget_used_ratio"] != 0.42 {
		t.Errorf("budget_used_ratio = %v, want 0.42", values["vkllm_budget_used_ratio"])
	}
	if values["vkllm_cost_instance_usd"] != 1.5 {
		t.Errorf("cost_instance_usd = %v, want 1.5", values["vkllm_cost_instance_usd"])
	}
	if values["vkllm_provider_balance_usd"] != 12.5 {
		t.Errorf("provider_balance_usd = %v, want 12.5", values["vkllm_provider_balance_usd"])
	}
	if values["vkllm_inference_scrape_errors_total"] != 1 {
		t.Errorf("inference_scrape_errors_total = %v, want 1", values["vkllm_inference_scrape_errors_total"])
	}
	if values["vkllm_cost_estimated_usd_total"] != 3.0 {
		t.Errorf("cost_estimated_usd_total = %v, want 3.0 (negative delta must be ignored)", values["vkllm_cost_estimated_usd_total"])
	}
	_ = testutil.ToFloat64 // kept available for a future, more targeted assertion
}

func TestRegisterMetricsIsTestIsolated(t *testing.T) {
	regA := NewRegistry()
	SetBudgetUsedRatio(0.9)
	_ = regA

	regB := NewRegistry() // a second RegisterMetrics call must start from zero
	families, _ := regB.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_budget_used_ratio" {
			if v := f.GetMetric()[0].GetGauge().GetValue(); v != 0 {
				t.Fatalf("budget_used_ratio on a fresh registry = %v, want 0 (state leaked across RegisterMetrics calls)", v)
			}
		}
	}
}
```

```go
// internal/obs/catalog_test.go
package obs

import "testing"

func TestCatalogHas43SortedUniqueNames(t *testing.T) {
	c := Catalog()
	if len(c) != 43 {
		t.Fatalf("len(Catalog()) = %d, want 43", len(c))
	}
	seen := map[string]bool{}
	for i, s := range c {
		if seen[s.Name] {
			t.Fatalf("duplicate name %s", s.Name)
		}
		seen[s.Name] = true
		if i > 0 && c[i-1].Name >= s.Name {
			t.Fatalf("Catalog() not sorted: %s before %s", c[i-1].Name, s.Name)
		}
	}
}

func TestVastaiOnlyMetricsAreInCatalog(t *testing.T) {
	names := map[string]bool{}
	for _, s := range Catalog() {
		names[s.Name] = true
	}
	for _, n := range VastaiOnlyMetrics() {
		if !names[n] {
			t.Fatalf("%s from VastaiOnlyMetrics is not in Catalog()", n)
		}
	}
	if len(VastaiOnlyMetrics()) != 3 {
		t.Fatalf("len(VastaiOnlyMetrics()) = %d, want 3", len(VastaiOnlyMetrics()))
	}
}
```

- [ ] **Step 2:** `go test ./internal/obs/... -v` — compile failure, nothing defined.

- [ ] **Step 3: Implement `metrics.go`.**

```go
// internal/obs/metrics.go
package obs

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

type ownedMetrics struct {
	budgetUsedRatio            prometheus.Gauge
	costEstimatedUSDTotal      prometheus.Counter
	costInstanceUSD            prometheus.Gauge
	providerBalanceUSD         prometheus.Gauge
	inferenceScrapeErrorsTotal prometheus.Counter
}

var current atomic.Pointer[ownedMetrics]

// RegisterMetrics registers the 5 metrics obs owns directly into reg,
// so the catalog is complete independent of which other packages are
// wired. Safe to call more than once (e.g. once per test): it builds
// fresh collectors each time rather than reusing package-level
// singletons, so no state leaks across calls.
func RegisterMetrics(reg prometheus.Registerer) {
	m := &ownedMetrics{
		budgetUsedRatio: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_budget_used_ratio",
			Help: "Month-to-date estimated spend divided by the configured monthly budget.",
		}),
		costEstimatedUSDTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_cost_estimated_usd_total",
			Help: "Monotonic estimated spend; never decremented within this process's lifetime.",
		}),
		costInstanceUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_cost_instance_usd",
			Help: "Estimated cost of the current instance so far; 0 when no instance exists.",
		}),
		providerBalanceUSD: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vkllm_provider_balance_usd",
			Help: "Last observed provider account balance.",
		}),
		inferenceScrapeErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_inference_scrape_errors_total",
			Help: "Failed GET /metrics pulls over the tunnel.",
		}),
	}
	reg.MustRegister(m.budgetUsedRatio, m.costEstimatedUSDTotal, m.costInstanceUSD,
		m.providerBalanceUSD, m.inferenceScrapeErrorsTotal)
	current.Store(m)
}

func SetBudgetUsedRatio(v float64)    { current.Load().budgetUsedRatio.Set(v) }
func SetCostInstanceUSD(v float64)    { current.Load().costInstanceUSD.Set(v) }
func SetProviderBalanceUSD(v float64) { current.Load().providerBalanceUSD.Set(v) }
func IncInferenceScrapeErrors()       { current.Load().inferenceScrapeErrorsTotal.Inc() }

// AddCostEstimatedUSD adds delta to the monotonic spend counter. A
// Prometheus counter panics on a negative Add, so a caller computing a
// delta across a value that can drop (e.g. a monthly rollover) must
// clamp to >=0 itself; this still guards the floor defensively.
func AddCostEstimatedUSD(delta float64) {
	if delta > 0 {
		current.Load().costEstimatedUSDTotal.Add(delta)
	}
}

// NewRegistry builds a fresh registry with obs's own metrics already
// registered. cmd/gpu-on-demand/main.go's sole constructor for the
// process-wide registry.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	return reg
}
```

- [ ] **Step 4:** `go test ./internal/obs/... -run 'TestObsOwned|TestSetters|TestRegisterMetricsIsTestIsolated' -race -v` — all pass.

- [ ] **Step 5: Implement `catalog.go`.**

```go
// internal/obs/catalog.go
package obs

import "sort"

// Spec is one entry in the metric catalog; Catalog's slice is the
// single source of truth this spec document and the golden-list test
// both read from.
type Spec struct {
	Name string
	Type string // counter, gauge, histogram
}

func Catalog() []Spec {
	c := []Spec{
		{"vkllm_gateway_requests_total", "counter"},
		{"vkllm_gateway_active_requests", "gauge"},
		{"vkllm_gateway_pending_requests", "gauge"},
		{"vkllm_gateway_wait_seconds", "histogram"},
		{"vkllm_gateway_request_seconds", "histogram"},
		{"vkllm_gateway_ttft_seconds", "histogram"},
		{"vkllm_gateway_output_tokens_per_second", "histogram"},
		{"vkllm_gateway_cold_start_outcomes_total", "counter"},
		{"vkllm_gateway_auth_failures_total", "counter"},
		{"vkllm_gateway_cancelled_streams_total", "counter"},
		{"vkllm_mgmt_requests_total", "counter"},
		{"vkllm_mgmt_request_seconds", "histogram"},
		{"vkllm_gpu_state", "gauge"},
		{"vkllm_provision_attempts_total", "counter"},
		{"vkllm_coldstart_stage_seconds", "histogram"},
		{"vkllm_gpu_uptime_seconds_total", "counter"},
		{"vkllm_gpu_idle_seconds", "gauge"},
		{"vkllm_gpu_idle_deadline_seconds", "gauge"},
		{"vkllm_gpu_last_inference_finished_seconds", "gauge"},
		{"vkllm_shutdowns_total", "counter"},
		{"vkllm_destroy_attempts_total", "counter"},
		{"vkllm_orphans_destroyed_total", "counter"},
		{"vkllm_reconcile_runs_total", "counter"},
		{"vkllm_instance_hourly_price_usd", "gauge"},
		{"vkllm_provider_requests_total", "counter"},
		{"vkllm_provider_request_seconds", "histogram"},
		{"vkllm_provider_rate_limited_total", "counter"},
		{"vkllm_store_conflicts_total", "counter"},
		{"vkllm_store_ops_total", "counter"},
		{"vkllm_inference_prompt_tokens_total", "counter"},
		{"vkllm_inference_prompt_seconds_total", "counter"},
		{"vkllm_inference_generated_tokens_total", "counter"},
		{"vkllm_inference_generation_seconds_total", "counter"},
		{"vkllm_inference_prompt_tokens_per_second", "gauge"},
		{"vkllm_inference_generated_tokens_per_second", "gauge"},
		{"vkllm_inference_requests_processing", "gauge"},
		{"vkllm_inference_requests_deferred", "gauge"},
		{"vkllm_inference_up", "gauge"},
		{"vkllm_inference_scrape_errors_total", "counter"},
		{"vkllm_budget_used_ratio", "gauge"},
		{"vkllm_cost_estimated_usd_total", "counter"},
		{"vkllm_cost_instance_usd", "gauge"},
		{"vkllm_provider_balance_usd", "gauge"},
	}
	sort.Slice(c, func(i, j int) bool { return c[i].Name < c[j].Name })
	return c
}

// VastaiOnlyMetrics names the 3 catalog entries that only register
// when the real Vast.ai provider is wired (internal/provider/vastai's
// metrics are constructed as a side effect of vastai.New, not of
// obs.RegisterMetrics) — the fake provider never produces them, so a
// test or a kind run using it sees Catalog() minus these 3.
func VastaiOnlyMetrics() []string {
	return []string{
		"vkllm_provider_requests_total",
		"vkllm_provider_request_seconds",
		"vkllm_provider_rate_limited_total",
	}
}
```

- [ ] **Step 6:** `go test ./internal/obs/... -race -v` — all pass. `go vet ./internal/obs/...`.

- [ ] **Step 7:** `git add internal/obs/metrics.go internal/obs/metrics_test.go internal/obs/catalog.go internal/obs/catalog_test.go && git commit -m "feat: obs-owned metrics and the 43-name catalog"`.

---

### Task 3: `internal/obs` — NewServer (`/healthz`, `/readyz`, `/metrics`)

**Files:**
- Create: `internal/obs/server.go`
- Create: `internal/obs/server_test.go`

**Interfaces:**
- Produces: `obs.Config{Addr string}`, `obs.NewServer(reg *prometheus.Registry, checks []func() error, cfg Config) *http.Server`.
- Consumed by: Task 9 (`cmd/gpu-on-demand/main.go`).

- [ ] **Step 1: Write the failing test.**

```go
// internal/obs/server_test.go
package obs

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestServerEndpoints(t *testing.T) {
	reg := NewRegistry()
	failing := false
	checks := []func() error{func() error {
		if failing {
			return errors.New("not ready yet")
		}
		return nil
	}}
	srv := NewServer(reg, checks, Config{Addr: ":0"})

	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 {
		t.Fatalf("/healthz = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("/readyz (no failures) = %d, want 200", rec.Code)
	}

	failing = true
	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 || rec.Body.String() != "not ready yet\n" {
		t.Fatalf("/readyz (failing) = %d %q, want 503 \"not ready yet\\n\"", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
}
```

- [ ] **Step 2:** `go test ./internal/obs/... -run TestServerEndpoints -v` — compile failure.

- [ ] **Step 3: Implement.**

```go
// internal/obs/server.go
package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config holds the obs HTTP server's own settings.
type Config struct {
	Addr string // ":9090"
}

// NewServer builds the health/ready/metrics server: /healthz is
// always 200, /readyz is 503 on the first failing check, /metrics
// serves reg via promhttp.
func NewServer(reg *prometheus.Registry, checks []func() error, cfg Config) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		for _, check := range checks {
			if err := check(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	return &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
}
```

- [ ] **Step 4:** `go test ./internal/obs/... -race -v` — all pass.

- [ ] **Step 5:** `git add internal/obs/server.go internal/obs/server_test.go && git commit -m "feat: obs.NewServer for healthz/readyz/metrics"`.

---

### Task 4: `internal/obs` — redacting JSON logger

**Files:**
- Create: `internal/obs/logger.go`
- Create: `internal/obs/logger_test.go`

**Interfaces:**
- Produces: `obs.DeniedLogFields []string`, `obs.NewLogger(w io.Writer, level slog.Level) *slog.Logger`, `obs.RedactingHandler(next slog.Handler) slog.Handler`.
- Consumed by: Task 9 (`cmd/gpu-on-demand/main.go`'s `slog.SetDefault`).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/obs/logger_test.go
package obs

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRedactionInlineAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.Info("request", "authorization", "Bearer sk-vkllm-secret", "route", "chat_completions")
	out := buf.String()
	if strings.Contains(out, "sk-vkllm-secret") {
		t.Fatalf("log line leaked a secret: %s", out)
	}
	if !strings.Contains(out, `"route":"chat_completions"`) {
		t.Fatalf("log line dropped a non-denied field: %s", out)
	}
	if !strings.Contains(out, `"ts":`) {
		t.Fatalf("log line missing the \"ts\" key: %s", out)
	}
}

func TestRedactionWithAttrsChain(t *testing.T) {
	for _, field := range []string{"authorization", "prompt", "messages", "content"} {
		var buf bytes.Buffer
		logger := NewLogger(&buf, slog.LevelInfo)
		logger.With(field, "super secret payload").Info("x")
		if strings.Contains(buf.String(), "super secret payload") {
			t.Fatalf("Logger.With(%q) leaked its value: %s", field, buf.String())
		}
	}
}

func TestRedactionInsideGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.Info("x", slog.Group("req", "authorization", "Bearer secretvalue"))
	if strings.Contains(buf.String(), "secretvalue") {
		t.Fatalf("a denied field inside a group leaked: %s", buf.String())
	}
}

func TestNonDeniedFieldsSurviveWithAttrsChain(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)
	logger.With("key_id", "abcd1234").Info("x")
	if !strings.Contains(buf.String(), `"key_id":"abcd1234"`) {
		t.Fatalf("a non-denied field via Logger.With was lost: %s", buf.String())
	}
}
```

- [ ] **Step 2:** `go test ./internal/obs/... -run TestRedaction -v` — compile failure.

- [ ] **Step 3: Implement.** The handler filters attrs in `WithAttrs` *before* delegating to the inner `slog.JSONHandler`'s own `WithAttrs` — `JSONHandler.WithAttrs` pre-formats its attrs into internal state immediately, so filtering after delegating would already have baked the secret in.

```go
// internal/obs/logger.go
package obs

import (
	"context"
	"io"
	"log/slog"
)

var DeniedLogFields = []string{"authorization", "prompt", "messages", "content"}

const redactedValue = "[REDACTED]"

// NewLogger builds the process-wide JSON logger: "ts" instead of
// slog's default "time" key, wrapped by RedactingHandler.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				a.Key = "ts"
			}
			return a
		},
	})
	return slog.New(RedactingHandler(h))
}

type redactingHandler struct {
	next slog.Handler
}

// RedactingHandler strips DeniedLogFields whether set via Handle or
// via Logger.With — a request-scoped logger attaches fields through
// With, so filtering only Handle's own attrs would miss them.
func RedactingHandler(next slog.Handler) slog.Handler {
	return &redactingHandler{next: next}
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redactAttr(a)
	}
	return &redactingHandler{next: h.next.WithAttrs(redacted)}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{next: h.next.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	for _, denied := range DeniedLogFields {
		if a.Key == denied {
			return slog.String(a.Key, redactedValue)
		}
	}
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		redacted := make([]slog.Attr, len(group))
		for i, ga := range group {
			redacted[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(redacted...)}
	}
	return a
}
```

- [ ] **Step 4:** `go test ./internal/obs/... -race -v` — all pass.

- [ ] **Step 5:** `git add internal/obs/logger.go internal/obs/logger_test.go && git commit -m "feat: obs redacting JSON logger"`.

---

### Task 5: `internal/obs` — request-id middleware and cardinality audit

**Files:**
- Create: `internal/obs/requestid.go`
- Create: `internal/obs/requestid_test.go`
- Create: `internal/obs/audit.go`
- Create: `internal/obs/audit_test.go`

**Interfaces:**
- Produces: `obs.NewRequestID() string`, `obs.WithRequestID(ctx, id) context.Context`, `obs.RequestIDFromContext(ctx) string`, `obs.RequestIDMiddleware(next http.Handler) http.Handler`, `obs.DeniedMetricLabels []string`, `obs.AuditCardinality(reg *prometheus.Registry, denylist []string) error`.
- Consumed by: Task 8 (`internal/gateway`, `internal/mgmt`, `wire.go`), Task 9 (`wire_test.go`).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/obs/requestid_test.go
package obs

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestRequestIDRoundTrip(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	var seen string
	h := RequestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	got := rec.Header().Get("X-Request-Id")
	if !hex32.MatchString(got) {
		t.Fatalf("no inbound header: got %q, want 32 lowercase hex", got)
	}
	if seen != got {
		t.Fatalf("context id %q != response header %q", seen, got)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", "not-valid")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") == "not-valid" {
		t.Fatal("malformed inbound id was echoed back instead of replaced")
	}

	valid := NewRequestID()
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", valid)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") != valid {
		t.Fatalf("valid inbound id was replaced: got %q, want %q", rec.Header().Get("X-Request-Id"), valid)
	}
}
```

```go
// internal/obs/audit_test.go
package obs

import (
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAuditCatchesDenylistedLabel(t *testing.T) {
	reg := prometheus.NewRegistry()
	bad := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_bad_metric"}, []string{"instance_id"})
	reg.MustRegister(bad)
	bad.WithLabelValues("i-123").Inc()

	err := AuditCardinality(reg, DeniedMetricLabels)
	if err == nil || !regexp.MustCompile("instance_id").MatchString(err.Error()) {
		t.Fatalf("AuditCardinality = %v, want an error naming instance_id", err)
	}

	clean := prometheus.NewRegistry()
	cleanCounter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_clean_metric"}, []string{"route"})
	clean.MustRegister(cleanCounter)
	cleanCounter.WithLabelValues("chat_completions").Inc()
	if err := AuditCardinality(clean, DeniedMetricLabels); err != nil {
		t.Fatalf("AuditCardinality on a clean registry = %v, want nil", err)
	}
}
```

- [ ] **Step 2:** `go test ./internal/obs/... -run 'TestRequestID|TestAudit' -v` — compile failure.

- [ ] **Step 3: Implement.**

```go
// internal/obs/requestid.go
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
```

```go
// internal/obs/audit.go
package obs

import (
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

var DeniedMetricLabels = []string{"request_id", "key", "instance_id", "ip"}

// AuditCardinality gathers reg and returns one error naming every
// family×label whose name matches denylist case-insensitively, so a
// label added later without review still gets caught.
func AuditCardinality(reg *prometheus.Registry, denylist []string) error {
	families, err := reg.Gather()
	if err != nil {
		return fmt.Errorf("obs: gather: %w", err)
	}
	seen := map[string]bool{}
	var msgs []string
	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				for _, denied := range denylist {
					if !strings.EqualFold(l.GetName(), denied) {
						continue
					}
					msg := fmt.Sprintf("%s carries denylisted label %q", f.GetName(), l.GetName())
					if !seen[msg] {
						seen[msg] = true
						msgs = append(msgs, msg)
					}
				}
			}
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("obs: cardinality audit failed:\n%s", strings.Join(msgs, "\n"))
}
```

- [ ] **Step 4:** `go test ./internal/obs/... -race -v` — all pass. `go vet ./internal/obs/...`.

- [ ] **Step 5:** `git add internal/obs/requestid.go internal/obs/requestid_test.go internal/obs/audit.go internal/obs/audit_test.go && git commit -m "feat: obs request-id middleware and cardinality audit"`.

---

### Task 6: `internal/controller` — coldstart pre-init, cost-metric publishing, scrape-error wiring

**Files:**
- Modify: `internal/controller/controller.go` (2 new fields on `Controller`)
- Modify: `internal/controller/metrics.go` (pre-init loop + new `publishCostMetrics` method)
- Modify: `internal/controller/loop.go` (2 one-line call sites)
- Create: `internal/controller/cost_metrics_test.go`

This is a merged-package touch to already-shipped, already-reviewed code — kept additive-only (zero existing lines removed) and as small as the three real findings allow, same category as CORE-055's own `Snapshot.ProviderBalanceUSD` addition.

- [ ] **Step 1: Write the failing tests.**

```go
// internal/controller/cost_metrics_test.go
package controller

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/savak1990/vk-llm-on-demand/internal/budget"
	"github.com/savak1990/vk-llm-on-demand/internal/obs"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func gatherValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			m := f.GetMetric()[0]
			switch {
			case m.GetCounter() != nil:
				return m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("metric %s not found in Gather()", name)
	return 0
}

func TestColdstartStagePreInitialized(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_coldstart_stage_seconds" {
			if len(f.GetMetric()) != 8 {
				t.Fatalf("vkllm_coldstart_stage_seconds has %d label combos, want 8", len(f.GetMetric()))
			}
			return
		}
	}
	t.Fatal("vkllm_coldstart_stage_seconds not registered")
}

func TestPublishCostMetricsAccumulatesAndClampsOnRollover(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs.RegisterMetrics(reg)
	c := &Controller{deps: Deps{Budget: budget.New(budget.Config{MonthlyUSD: 30})}}

	var doc store.Document
	doc.Ledger.TotalUSD = 5.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 5.0 {
		t.Fatalf("after first publish = %v, want 5.0", v)
	}
	if v := gatherValue(t, reg, "vkllm_budget_used_ratio"); v != 5.0/30 {
		t.Fatalf("used ratio = %v, want %v", v, 5.0/30)
	}

	doc.Ledger.TotalUSD = 8.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 8.0 {
		t.Fatalf("after second publish = %v, want 8.0 (5 + delta 3)", v)
	}

	// A monthly rollover drops the raw ledger total; the exported
	// counter must never decrease.
	doc.Ledger.TotalUSD = 1.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 8.0 {
		t.Fatalf("after rollover drop = %v, want unchanged 8.0", v)
	}

	// Growth resumes from the new, lower baseline.
	doc.Ledger.TotalUSD = 4.0
	c.publishCostMetrics(doc)
	if v := gatherValue(t, reg, "vkllm_cost_estimated_usd_total"); v != 11.0 {
		t.Fatalf("after post-rollover growth = %v, want 11.0 (8 + delta 3)", v)
	}
}

func TestPublishCostMetricsNoOpenEntryIsZeroInstanceSpend(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs.RegisterMetrics(reg)
	c := &Controller{deps: Deps{Budget: budget.New(budget.Config{MonthlyUSD: 30})}}

	c.publishCostMetrics(store.Document{})
	if v := gatherValue(t, reg, "vkllm_cost_instance_usd"); v != 0 {
		t.Fatalf("instance spend with no open entry = %v, want 0", v)
	}
}
```

- [ ] **Step 2:** `go test ./internal/controller/... -run 'TestColdstart|TestPublishCostMetrics' -v` — compile failure (`publishCostMetrics`, `costMu`, `lastEstimatedMTD` don't exist).

- [ ] **Step 3: Add 2 fields to `Controller` in `controller.go`**, next to the existing `balanceMu`/`lastBalance`/`lastBalanceKnown` block:

```go
	costMu           sync.Mutex
	lastEstimatedMTD float64 // last EstimatedMTD seen by publishCostMetrics, for AddCostEstimatedUSD's delta
```

- [ ] **Step 4: Implement `publishCostMetrics` and the coldstart pre-init in `metrics.go`.** Add the `"github.com/savak1990/vk-llm-on-demand/internal/obs"` import, extend `RegisterMetrics`, and add the new method:

```go
// RegisterMetrics registers this package's collectors; main calls it once.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(gpuState, shutdownsTotal, coldstartStageSeconds,
		provisionAttemptsTotal, gpuUptimeSecondsTotal, gpuIdleSeconds,
		gpuIdleDeadlineSeconds, gpuLastInferenceFinishedSeconds,
		destroyAttemptsTotal, orphansDestroyedTotal, reconcileRunsTotal,
		instanceHourlyPriceUSD)
	// R9: every declared stage must appear in /metrics at process
	// start, even though only first_token is ever really observed today.
	for _, stage := range []string{"search", "create", "boot", "link", "download", "load", "ready", "first_token"} {
		coldstartStageSeconds.WithLabelValues(stage)
	}
}

// publishCostMetrics republishes the budget/cost gauges and adds
// this tick's non-negative spend delta to the monotonic counter —
// called every reconcile tick, not only when something polls Status.
func (c *Controller) publishCostMetrics(doc store.Document) {
	mtd := c.deps.Budget.EstimatedMTD(doc)
	obs.SetBudgetUsedRatio(c.deps.Budget.UsedRatio(doc))

	instanceUSD := 0.0
	if e, ok := openEntry(doc); ok {
		instanceUSD = e.EstUSD
	}
	obs.SetCostInstanceUSD(instanceUSD)

	c.balanceMu.Lock()
	if c.lastBalanceKnown {
		obs.SetProviderBalanceUSD(c.lastBalance.USD())
	}
	c.balanceMu.Unlock()

	c.costMu.Lock()
	delta := mtd - c.lastEstimatedMTD
	if delta < 0 {
		delta = 0 // a monthly rollover drops the raw ledger total; never decrement the exported counter
	}
	c.lastEstimatedMTD = mtd
	c.costMu.Unlock()
	obs.AddCostEstimatedUSD(delta)
}
```

- [ ] **Step 5:** `go test ./internal/controller/... -run 'TestColdstart|TestPublishCostMetrics' -race -v` — passes.

- [ ] **Step 6: Wire the 2 call sites in `loop.go`.** Add the import. Right after the existing `recordDocGauges(doc, next, c.inFlight, now)` line (inside `processEvent`, still under `c.mu` held — `publishCostMetrics` takes its own locks internally, matching `recordDocGauges`'s own pattern of being safe to call while `c.mu` is held):

```go
	recordDocGauges(doc, next, c.inFlight, now)
	c.publishCostMetrics(doc)
```

  And in `dispatchScrape`, add the increment next to the existing warning log:

```go
	c.spawn(func() {
		if err := c.deps.Worker.Scrape(ctx, conn.RoundTripper()); err != nil {
			obs.IncInferenceScrapeErrors()
			if c.deps.Log != nil {
				c.deps.Log.Warn("controller: metrics scrape failed", "err", err)
			}
		}
	})
```

  **Ruling, not a gap:** this one-line wiring is covered by Task 2's `TestSettersUpdateValuesAndNegativeDeltaIsIgnored` (proves `IncInferenceScrapeErrors` itself is correct) plus task review by inspection; a full `dispatchScrape`-level regression test would need a real `tunnel.Conn` and a fake `Worker` wired into a live `Controller`, which is disproportionate plumbing for a one-line, zero-branching addition. AC9's end-to-end confirmation is the existing CORE-070 AC11 fixture, as spec 090's own step 7 already says.

- [ ] **Step 7:** `go build ./internal/controller/...` then `go test ./internal/controller/... -race -count=1` — full package green (confirms nothing else broke).

- [ ] **Step 8:** `git add internal/controller/controller.go internal/controller/metrics.go internal/controller/loop.go internal/controller/cost_metrics_test.go && git commit -m "feat: coldstart pre-init, cost-metric publishing, scrape-error metric"`.

---

### Task 7: `internal/gateway` — `vkllm_gateway_cancelled_streams_total`

**Files:**
- Modify: `internal/gateway/metrics.go`
- Modify: `internal/gateway/middleware.go`
- Create: `internal/gateway/cancelled_streams_test.go`

Closes spec 090's own open question for real: CORE-060 shipped `DONE` without this metric; it is added here, at the one place that already decides `code="499"`, not at every disconnect call site individually.

- [ ] **Step 1: Write the failing test.**

```go
// internal/gateway/cancelled_streams_test.go
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	fakeclock "github.com/savak1990/vk-llm-on-demand/internal/clock/fake"
)

func TestCancelledStreamsTotalIncrementsOn499(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newGatewayMetrics(reg)
	g := &Gateway{deps: Deps{Clock: fakeclock.New(time.Unix(0, 0)), Log: noopTestLogger()}, metrics: m}

	h := g.accessLogAndMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCodeOverride(w, "499")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))

	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_gateway_cancelled_streams_total" {
			if v := f.GetMetric()[0].GetCounter().GetValue(); v != 1 {
				t.Fatalf("cancelled_streams_total = %v, want 1", v)
			}
			return
		}
	}
	t.Fatal("vkllm_gateway_cancelled_streams_total not registered")
}

func TestCancelledStreamsTotalNotIncrementedOnOtherCodes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newGatewayMetrics(reg)
	g := &Gateway{deps: Deps{Clock: fakeclock.New(time.Unix(0, 0)), Log: noopTestLogger()}, metrics: m}

	h := g.accessLogAndMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models", nil))

	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "vkllm_gateway_cancelled_streams_total" {
			if v := f.GetMetric()[0].GetCounter().GetValue(); v != 0 {
				t.Fatalf("cancelled_streams_total = %v, want 0 on a 200", v)
			}
			return
		}
	}
	t.Fatal("vkllm_gateway_cancelled_streams_total not registered")
}
```

  (If `noopTestLogger` is not visible from this package's own test files, add the ~3-line `*slog.Logger`-to-`io.Discard` helper locally in this new test file — check `internal/gateway`'s existing `*_test.go` files for one first, per the ladder: reuse before writing.)

- [ ] **Step 2:** `go test ./internal/gateway/... -run TestCancelledStreams -v` — compile failure (field doesn't exist).

- [ ] **Step 3: Add the field in `metrics.go`.**

```go
type gatewayMetrics struct {
	requestsTotal          *prometheus.CounterVec
	activeRequests         prometheus.Gauge
	requestSeconds         *prometheus.HistogramVec
	authFailuresTotal      *prometheus.CounterVec
	ttftSeconds            prometheus.Histogram
	outputTokensPerSecond  prometheus.Histogram
	pendingRequests        prometheus.Gauge
	waitSeconds            prometheus.Histogram
	coldStartOutcomesTotal *prometheus.CounterVec
	cancelledStreamsTotal  prometheus.Counter
}
```

  In `newGatewayMetrics`, add the construction (after `coldStartOutcomesTotal`'s block) and add `m.cancelledStreamsTotal` to the `reg.MustRegister(...)` call:

```go
		cancelledStreamsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vkllm_gateway_cancelled_streams_total",
			Help: "Streamed requests ended by a client disconnect before the upstream finished.",
		}),
```

- [ ] **Step 4: Increment it in `middleware.go`'s `accessLogAndMetrics`**, right after the existing `g.metrics.requestsTotal.WithLabelValues(route, metricCode).Inc()` line:

```go
		g.metrics.requestsTotal.WithLabelValues(route, metricCode).Inc()
		if metricCode == "499" {
			g.metrics.cancelledStreamsTotal.Inc()
		}
```

- [ ] **Step 5:** `go test ./internal/gateway/... -race -count=1` — full package green.

- [ ] **Step 6:** `git add internal/gateway/metrics.go internal/gateway/middleware.go internal/gateway/cancelled_streams_test.go && git commit -m "feat: vkllm_gateway_cancelled_streams_total"`.

---

### Task 8: Request-id consolidation (`internal/gateway`, `internal/mgmt`, `wire.go`)

**Files:**
- Modify: `internal/gateway/gateway.go`
- Modify: `internal/gateway/middleware.go`
- Modify: `internal/mgmt/middleware.go`
- Modify: `cmd/gpu-on-demand/wire.go`

Fixes the real, pre-existing non-compliance found in exploration: gateway's own request id was 16 hex characters, not the spec's required 32; mgmt never got one at all.

- [ ] **Step 1: `internal/gateway/gateway.go`** — delete `type ctxKey int`, `const ctxKeyRequestID ctxKey = iota`, and `func newRequestID() string` entirely. Replace `requestIDMiddleware`'s body to delegate to `obs`, keeping the same call site (still first in `Handler()`'s chain):

```go
func (g *Gateway) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Theoretically races Shutdown's Wait() if a new request's Add(1)
		// lands exactly when the counter is momentarily zero; in practice
		// the listener that feeds this handler is already closed by then.
		g.activeWG.Add(1)
		defer g.activeWG.Done()
		obs.RequestIDMiddleware(next).ServeHTTP(w, r)
	})
}
```

  Add the `"github.com/savak1990/vk-llm-on-demand/internal/obs"` import. Remove the now-unused `"crypto/rand"` and `"encoding/hex"` imports if nothing else in the file uses them (verify with `go build`, which errors on an unused import).

- [ ] **Step 2: `internal/gateway/middleware.go`** — in `accessLogAndMetrics`'s log call, replace `"request_id", r.Context().Value(ctxKeyRequestID),` with `"request_id", obs.RequestIDFromContext(r.Context()),`. Add the `obs` import.

- [ ] **Step 3: `internal/mgmt/middleware.go`** — in `withObserve`'s log call, replace `"request_id", rec.Header().Get("X-Request-Id"),` with `"request_id", obs.RequestIDFromContext(r.Context()),`. Add the `obs` import.

- [ ] **Step 4: `cmd/gpu-on-demand/wire.go`** — mgmt's own `New` doc comment already says its caller wraps it with request-id middleware; make that true for the first time. Add the `obs` import and change:

```go
	apiMux := http.NewServeMux()
	apiMux.Handle("/v1/", gw.Handler())
	apiMux.Handle("/api/v1/gpu/", mg)
```

  to:

```go
	apiMux := http.NewServeMux()
	apiMux.Handle("/v1/", gw.Handler())
	apiMux.Handle("/api/v1/gpu/", obs.RequestIDMiddleware(mg))
```

- [ ] **Step 5:** `go build ./... && go test ./internal/gateway/... ./internal/mgmt/... ./cmd/gpu-on-demand/... -race -count=1` — everything still green (exploration confirmed no existing test asserts the old 16-char length or references `ctxKeyRequestID` from outside `gateway.go`, so nothing else needs touching).

- [ ] **Step 6:** `git add internal/gateway/gateway.go internal/gateway/middleware.go internal/mgmt/middleware.go cmd/gpu-on-demand/wire.go && git commit -m "fix: request-id consolidation — gateway's id was 16 hex not 32, mgmt had none"`.

---

### Task 9: Wire `cmd/gpu-on-demand` to `obs`

**Files:**
- Modify: `cmd/gpu-on-demand/main.go`
- Modify: `cmd/gpu-on-demand/main_test.go`
- Modify: `cmd/gpu-on-demand/wire.go`
- Modify: `cmd/gpu-on-demand/wire_test.go`

- [ ] **Step 1: `main.go`.** Make `slog.SetDefault(obs.NewLogger(...))` the first statement in `main()`, before any other `slog.Default()` call (several exist later in the file and would otherwise keep the old text handler):

```go
func main() {
	slog.SetDefault(obs.NewLogger(os.Stderr, slog.LevelInfo))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	...
```

  Delete the package-level `var readinessChecks []func() error` and the `func healthz(...)`/`func readyz(...)` functions entirely — add a function-local `var readinessChecks []func() error` right after the `SetDefault` line instead (every existing `readinessChecks = append(readinessChecks, ...)` call site is untouched; Go's scoping handles a local variable of the same name identically). Replace `reg := prometheus.NewRegistry()` with `reg := obs.NewRegistry()`. Replace the final block:

```go
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", healthz)
	healthMux.HandleFunc("/readyz", readyz)
	healthMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	health := &http.Server{Addr: ":9090", Handler: healthMux, ReadHeaderTimeout: 10 * time.Second}
```

  with:

```go
	health := obs.NewServer(reg, readinessChecks, obs.Config{Addr: ":9090"})
```

  Add the `"github.com/savak1990/vk-llm-on-demand/internal/obs"` import. Remove the now-unused `"github.com/prometheus/client_golang/prometheus/promhttp"` import, and `"github.com/prometheus/client_golang/prometheus"` too if nothing else in the file references it directly after this change (verify with `go build`).

- [ ] **Step 2: `main_test.go`.** Delete `TestHealthzAlwaysOK` and `TestReadyzOKWithNoChecksRegistered` — the functions they call no longer exist; the same behavior is covered by `internal/obs`'s own `TestServerEndpoints` (Task 3). Leave every other test in the file untouched.

- [ ] **Step 3: `wire.go`.** Add `obs.RegisterMetrics(d.Reg)` alongside the existing `controller.RegisterMetrics(d.Reg)`/`store.RegisterMetrics(d.Reg)` lines, so any caller building its own scratch registry (like `wire_test.go`, which never goes through `main.go`'s `obs.NewRegistry()`) still gets all 5 obs-owned metrics:

```go
	controller.RegisterMetrics(d.Reg)
	store.RegisterMetrics(d.Reg)
	obs.RegisterMetrics(d.Reg)
```

  Add the `"github.com/savak1990/vk-llm-on-demand/internal/obs"` import.

- [ ] **Step 4: Write the failing golden-catalog test in `wire_test.go`.** This is the real, non-tautological version of the spec's `TestCatalogGolden`: it runs against the actual registry `wire()` populates with a fake provider, not a hand-built fake-collector scratch registry.

```go
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

	reg := prometheus.NewRegistry()
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

	wantNames := map[string]bool{}
	for _, s := range obs.Catalog() {
		wantNames[s.Name] = true
	}
	for _, n := range obs.VastaiOnlyMetrics() {
		delete(wantNames, n) // config.Mgmt.ProviderName="fake" above never registers these 3
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
			t.Errorf("unexpected metric family %s (not in obs.Catalog(), or a vastai-only one leaked with the fake provider)", name)
		}
	}

	if err := obs.AuditCardinality(reg, obs.DeniedMetricLabels); err != nil {
		t.Fatalf("AuditCardinality: %v", err)
	}
}
```

  Add the `"github.com/savak1990/vk-llm-on-demand/internal/obs"` import to `wire_test.go`.

- [ ] **Step 2 (repeated — run it):** `go test ./cmd/gpu-on-demand/... -run TestWireMetricsCatalogComplete -v` — compile failure, then make it pass against Steps 1 and 3's real wiring.

- [ ] **Step 5:** `go build ./... && go test ./cmd/gpu-on-demand/... -race -count=1 -v` — all pass, including the pre-existing `TestWireServesInferenceAndManagementThroughOneMux`.

- [ ] **Step 6:** `go test ./... -count=1` — the whole repo, clean.

- [ ] **Step 7:** `git add cmd/gpu-on-demand/main.go cmd/gpu-on-demand/main_test.go cmd/gpu-on-demand/wire.go cmd/gpu-on-demand/wire_test.go && git commit -m "feat: wire cmd/gpu-on-demand to obs; real golden-catalog test"`.

---

### Task 9b: Pre-initialize 12 Vec metrics (closes the R9 residual for real)

**Added mid-plan.** Task 9's own implementation work proved the Task 1 ruling that scoped R9's pre-init requirement down to just `vkllm_coldstart_stage_seconds`/`vkllm_gpu_state` was too narrow: a `*Vec` collector emits no family at all until something calls `.WithLabelValues(...)` on it, and `wire()`'s own test never drives enough real traffic or waits long enough to touch 12 of the catalog's Vec-typed metrics. Rather than leave this as a residual, this task pre-initializes all 12 — following the exact pattern `coldstartStageSeconds`'s pre-init loop already uses — closing R9 in full for every metric whose label set is a small, closed enum (every catalog metric except the 3 vastai-only and 8 worker-scrape-gated ones, which are correctly excluded on their own separate grounds).

**Files:**
- Modify: `internal/controller/metrics.go`
- Modify: `internal/store/metrics.go`
- Modify: `internal/mgmt/metrics.go`
- Modify: `internal/gateway/metrics.go`

This is a merged-package touch to 4 already-shipped packages — additive only.

- [ ] **Step 1: `internal/controller/metrics.go`** — in `RegisterMetrics`, right after the existing coldstart pre-init loop, add:

```go
	for _, s := range []string{"STOPPED", "ALLOCATING", "STARTING", "LOADING_MODEL", "READY", "DRAINING", "DELETING", "FAILED"} {
		gpuState.WithLabelValues(s)
	}
	for _, r := range []string{"idle", "manual", "lifetime", "budget", "failure", "orphan"} {
		shutdownsTotal.WithLabelValues(r)
	}
	for _, o := range []string{"ok", "no_offer", "provider_error", "deadline_exceeded", "attempts_exhausted", "budget_exhausted", "failed_status", "lost", "cancelled"} {
		provisionAttemptsTotal.WithLabelValues(o)
	}
	for _, o := range []string{"ok", "retryable", "terminal", "fatal"} {
		destroyAttemptsTotal.WithLabelValues(o)
	}
	for _, o := range []string{"ok", "error"} {
		reconcileRunsTotal.WithLabelValues(o)
	}
```

- [ ] **Step 2: `internal/store/metrics.go`** — in `RegisterMetrics`, add (op×outcome, every combination the catalog declares):

```go
	for _, op := range []string{"load", "save", "transition"} {
		for _, outcome := range []string{"ok", "conflict", "exhausted", "too_large", "corrupt", "error", "reset", "installation", "missing"} {
			storeOpsTotal.WithLabelValues(op, outcome)
		}
	}
```

- [ ] **Step 3: `internal/mgmt/metrics.go`** — in `registerMetrics`, add:

```go
	for _, op := range []string{"start", "status", "stop", "cost", "other"} {
		mgmtRequestSeconds.WithLabelValues(op)
		for _, code := range []string{"200", "202", "400", "401", "403", "404", "405", "413", "429", "500", "503"} {
			mgmtRequestsTotal.WithLabelValues(op, code)
		}
	}
```

- [ ] **Step 4: `internal/gateway/metrics.go`** — in `newGatewayMetrics`, after constructing `m` and before `return m`, add:

```go
	for _, route := range []string{"chat_completions", "models", "other"} {
		m.requestSeconds.WithLabelValues(route)
		for _, code := range []string{"200", "400", "401", "403", "404", "405", "413", "429", "499", "500", "502", "503", "504", "stream_error"} {
			m.requestsTotal.WithLabelValues(route, code)
		}
	}
	for _, reason := range []string{"missing", "malformed", "unknown_key", "wrong_surface"} {
		m.authFailuresTotal.WithLabelValues(reason)
	}
	for _, outcome := range []string{"ready", "gpu_cold_start", "queue_full", "gpu_unavailable", "client_gone", "shutting_down"} {
		m.coldStartOutcomesTotal.WithLabelValues(outcome)
	}
```

- [ ] **Step 5:** `go build ./... && go test ./internal/controller/... ./internal/store/... ./internal/mgmt/... ./internal/gateway/... -race -count=1` — every touched package's full suite green; pre-touching a label with no `.Inc()`/`.Observe()` must not change any existing test's assertions about counts (a pre-touched zero-value child is indistinguishable from an absent one to anything that was checking a specific label's value, only `Gather()`'s family-presence changes).

- [ ] **Step 6:** `git add internal/controller/metrics.go internal/store/metrics.go internal/mgmt/metrics.go internal/gateway/metrics.go && git commit -m "feat: pre-initialize the remaining 12 Vec metrics at registration time"`.

---

### Task 10: Chart — `PrometheusRule`, Grafana dashboard `ConfigMap`, CI

**Files:**
- Create: `deploy/helm/gpu-on-demand/files/alerts.yaml`
- Create: `deploy/helm/gpu-on-demand/files/alerts_test.yaml`
- Create: `deploy/helm/gpu-on-demand/files/dashboard.json`
- Create: `deploy/helm/gpu-on-demand/templates/prometheusrule.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/dashboard.yaml`
- Modify: `deploy/helm/gpu-on-demand/values.yaml`
- Modify: `deploy/helm/gpu-on-demand/values.schema.json`
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yml`

- [ ] **Step 1: `files/alerts.yaml`** — the spec's 4 alerts, as `promtool`'s own native rule-file format (bare top-level `groups:`, no CRD envelope):

```yaml
groups:
  - name: vkllm
    rules:
      - alert: VkllmOrphanDestroyed
        expr: increase(vkllm_orphans_destroyed_total[1h]) > 0
        labels: {severity: warning}
        annotations: {summary: "An untracked GPU instance was destroyed"}
      - alert: VkllmProvisioningFailing
        expr: |
          increase(vkllm_provision_attempts_total{outcome="attempts_exhausted"}[1h]) >= 1
          or
          sum(increase(vkllm_provision_attempts_total{outcome=~"provider_error|deadline_exceeded|failed_status"}[1h])) >= 3
        labels: {severity: critical}
        annotations: {summary: "GPU provisioning is failing or exhausted its hourly attempt budget"}
      - alert: VkllmDestroyStuck
        expr: vkllm_gpu_state{state="DELETING"} == 1
        for: 10m
        labels: {severity: critical}
        annotations: {summary: "An instance has been deleting for over 10 minutes"}
      - alert: VkllmBudgetHigh
        expr: vkllm_budget_used_ratio >= 0.8
        labels: {severity: warning}
        annotations: {summary: "Month-to-date spend has reached 80% of budget"}
```

- [ ] **Step 2: `files/alerts_test.yaml`** — `promtool test rules`' own format, covering AC10/AC11:

```yaml
rule_files:
  - alerts.yaml
evaluation_interval: 1m
tests:
  - interval: 1m
    input_series:
      - series: 'vkllm_budget_used_ratio'
        values: '0.79 0.8'
    alert_rule_test:
      - eval_time: 0m
        alertname: VkllmBudgetHigh
        exp_alerts: []
      - eval_time: 1m
        alertname: VkllmBudgetHigh
        exp_alerts:
          - exp_labels:
              severity: warning
            exp_annotations:
              summary: "Month-to-date spend has reached 80% of budget"
  - interval: 1m
    input_series:
      - series: 'vkllm_gpu_state{state="DELETING"}'
        values: '1+0x15'
    alert_rule_test:
      - eval_time: 9m
        alertname: VkllmDestroyStuck
        exp_alerts: []
      - eval_time: 11m
        alertname: VkllmDestroyStuck
        exp_alerts:
          - exp_labels:
              state: DELETING
              severity: critical
            exp_annotations:
              summary: "An instance has been deleting for over 10 minutes"
```

- [ ] **Step 3:** Download Prometheus (which bundles `promtool`) locally — pick the latest stable release at implementation time, mirroring the exact pinned-binary pattern `kubeconform` already uses in `ci.yml:127-145` (`curl` the release tarball, `sha256sum -c` against a hardcoded checksum computed from the real downloaded file, never a guessed one):

```bash
PROM_VERSION=3.1.0  # verify against https://github.com/prometheus/prometheus/releases at implementation time
curl -sSLo prometheus.tar.gz "https://github.com/prometheus/prometheus/releases/download/v${PROM_VERSION}/prometheus-${PROM_VERSION}.linux-amd64.tar.gz"
sha256sum prometheus.tar.gz   # record this real value as PROMETHEUS_SHA256 in ci.yml, below — never hand-type a guessed digest
tar xzf prometheus.tar.gz
./prometheus-${PROM_VERSION}.linux-amd64/promtool check rules deploy/helm/gpu-on-demand/files/alerts.yaml
cd deploy/helm/gpu-on-demand/files && /path/to/promtool test rules alerts_test.yaml
```

  Both must report success locally (`check rules`: "4 rules found"; `test rules`: no `FAILED` lines) before continuing — this is the real verification step, not a formality.

- [ ] **Step 4: `files/dashboard.json`** — the spec's 9 panels, minimal valid Grafana dashboard JSON (schema version/panel ids are mechanical per spec's own Open Questions default):

```json
{
  "title": "vk-llm-on-demand",
  "schemaVersion": 39,
  "panels": [
    {"id": 1, "title": "GPU state timeline", "type": "state-timeline", "targets": [{"expr": "vkllm_gpu_state"}]},
    {"id": 2, "title": "Cold-start stages", "type": "bargauge", "targets": [{"expr": "histogram_quantile(0.95, sum by (le, stage) (rate(vkllm_coldstart_stage_seconds_bucket[5m])))"}]},
    {"id": 3, "title": "Time to first token", "type": "timeseries", "targets": [{"expr": "histogram_quantile(0.95, rate(vkllm_gateway_ttft_seconds_bucket[5m]))"}]},
    {"id": 4, "title": "Tokens per second", "type": "timeseries", "targets": [{"expr": "vkllm_inference_generated_tokens_per_second"}]},
    {"id": 5, "title": "Spend MTD vs budget", "type": "stat", "targets": [{"expr": "vkllm_budget_used_ratio"}]},
    {"id": 6, "title": "Provisioning outcomes", "type": "barchart", "targets": [{"expr": "sum by (outcome) (increase(vkllm_provision_attempts_total[1h]))"}]},
    {"id": 7, "title": "Orphan events", "type": "timeseries", "targets": [{"expr": "increase(vkllm_orphans_destroyed_total[1h])"}]},
    {"id": 8, "title": "Idle deadline countdown", "type": "stat", "targets": [{"expr": "vkllm_gpu_idle_deadline_seconds - time()"}]},
    {"id": 9, "title": "Shutdowns by reason", "type": "barchart", "targets": [{"expr": "sum by (reason) (increase(vkllm_shutdowns_total[1h]))"}]}
  ]
}
```

- [ ] **Step 5:** `jq -e '(.panels | length) == 9 and (.panels | all(.targets[0].expr | test("vkllm_")))' deploy/helm/gpu-on-demand/files/dashboard.json` exits 0.

- [ ] **Step 6: `templates/prometheusrule.yaml`**, following `templates/servicemonitor.yaml`'s own `enabled`/label pattern exactly:

```yaml
{{- if .Values.prometheusRule.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: {{ include "svc.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
spec:
{{ .Files.Get "files/alerts.yaml" | indent 2 }}
{{- end }}
```

- [ ] **Step 7: `templates/dashboard.yaml`:**

```yaml
{{- if .Values.dashboard.enabled }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "svc.fullname" . }}-dashboard-observability
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
    grafana_dashboard: "1"
data:
  dashboard.json: |
{{ .Files.Get "files/dashboard.json" | indent 4 }}
{{- end }}
```

- [ ] **Step 8: `values.yaml`** — add, near the existing `serviceMonitor:` block:

```yaml
prometheusRule:
  enabled: false
dashboard:
  enabled: false
```

- [ ] **Step 9: `values.schema.json`** — add `prometheusRule`/`dashboard` as objects each with a boolean `enabled` property, matching `serviceMonitor`'s existing schema node shape exactly (same file, same pattern — copy its node, rename it twice).

- [ ] **Step 10: `Makefile`** — add the two new flags to the existing `HELM_TEST_SET` variable, so `helm-lint`/`helm-template` always exercise both new templates:

```makefile
HELM_TEST_SET = --set image.tag=test \
	--set existingSecrets.provider=p --set existingSecrets.apiKeys=k --set existingSecrets.ssh=s \
	--set config.model.url=https://example.com/m.gguf \
	--set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 \
	--set config.worker.image=example/worker \
	--set prometheusRule.enabled=true --set dashboard.enabled=true
```

- [ ] **Step 11:** `make helm-lint helm-template` both pass.

- [ ] **Step 12: `.github/workflows/ci.yml`** — add a new pinned-binary install step to the `chart-kind` job, right after its existing `azure/setup-helm` step, using the real checksum from Step 3:

```yaml
      - name: Install promtool
        env:
          PROMETHEUS_VERSION: "3.1.0"  # match Step 3's real verified version
          PROMETHEUS_SHA256: "<the real sha256sum from Step 3, never a guessed value>"
        run: |
          set -euo pipefail
          curl -sSLo prometheus.tar.gz \
            "https://github.com/prometheus/prometheus/releases/download/v${PROMETHEUS_VERSION}/prometheus-${PROMETHEUS_VERSION}.linux-amd64.tar.gz"
          echo "${PROMETHEUS_SHA256}  prometheus.tar.gz" | sha256sum -c -
          tar xzf prometheus.tar.gz
          sudo mv "prometheus-${PROMETHEUS_VERSION}.linux-amd64/promtool" /usr/local/bin/promtool

      - name: promtool check/test rules
        run: |
          set -euo pipefail
          promtool check rules deploy/helm/gpu-on-demand/files/alerts.yaml
          cd deploy/helm/gpu-on-demand/files && promtool test rules alerts_test.yaml
```

- [ ] **Step 13:** In the same job's `helm install` and `helm upgrade` steps, add `--set prometheusRule.enabled=true --set dashboard.enabled=true` to each command's existing flag list (both already span multiple `--set` lines; append these as one more line to each, changing nothing else).

- [ ] **Step 14:** Add a new step right after "Inference API reachable" — not after "Confirm initial state is STOPPED" — because the 8 `vkllm_inference_*` families only appear once the worker Exporter has been `Update`d by a real successful scrape, which only happens after a real inference call has gone through. Running this check any earlier would see 32 families, not 40, for a reason that has nothing to do with the chart. Same port-forward pattern as the existing steps, a second port on `:9090`, asserting the provider-conditional family count this plan's AC6 ruling set (Task 1, Step 1): 40 families, since this job's `config.provider.name=fake`.

```yaml
      - name: Metrics endpoint reachable, catalog complete for the fake provider
        run: |
          set -euo pipefail
          kubectl port-forward svc/gpu-on-demand 19090:9090 >/dev/null 2>&1 &
          sleep 2
          count=$(curl -sf http://localhost:19090/metrics | grep -c '^# TYPE vkllm_')
          test "$count" = "40"
          kubectl get configmap -l grafana_dashboard=1 | grep -q dashboard-observability
          kubectl get prometheusrule gpu-on-demand
```

- [ ] **Step 15:** `git add deploy/helm/gpu-on-demand/files deploy/helm/gpu-on-demand/templates/prometheusrule.yaml deploy/helm/gpu-on-demand/templates/dashboard.yaml deploy/helm/gpu-on-demand/values.yaml deploy/helm/gpu-on-demand/values.schema.json Makefile .github/workflows/ci.yml && git commit -m "feat: PrometheusRule, Grafana dashboard ConfigMap, promtool CI check"`.

---

### Task 11: Write-back

**Files (for final DONE state):**
- Rename the spec folder's status letter from P to D (DONE status); folder becomes `specs/core/090-D-observability`
- Modify: `specs/README.md` to point at the renamed spec
- Modify: any spec whose cross-reference still points at `090-P-...`

**Steps (perform after Task 10 implementation sign-off):**

- [ ] **Step 1:** Fill spec 090's Evidence section: the real `go test ./... -race -count=1` runtime, the real `curl :9090/metrics` family count from the kind job (40), the rendered `files/alerts.yaml`'s `promtool check rules` output, `files/dashboard.json`'s `jq` check result, and an explicit note of the 2 residuals recorded in Task 1 (R9's pre-init scope, the 7 un-instrumented coldstart stages) so they're disclosed, not silently dropped.
- [ ] **Step 2:** Check every box in spec 090's Definition of done against this plan's real AC1-AC11 results.
- [ ] **Step 3:** Rename folder (folder becomes `090-D-observability` when status flips to "DONE") and update front-matter `status: "DONE"`.
- [ ] **Step 4:** `grep -rn '090-P-observability'` across the repo; fix every stale cross-reference (at minimum `specs/README.md`).
- [ ] **Step 5:** `grep -rEn '([0-9]{1,3}\.){3}[0-9]{1,3}' specs/core/090-D-observability/spec.md` — leak check (standing rule); this spec touches no real IPs.
- [ ] **Step 6:** `make specs-check`.
- [ ] **Step 7:** `git add -A && git commit -m "docs: CORE-090 observability DONE"`.

---

### Task 12: Validation and PR

- [ ] **Step 1:** `make specs-check go-build go-test go-lint helm-lint helm-template` — all pass.
- [ ] **Step 2:** `go test ./... -race -count=1` — full repo, run twice, no flakes.
- [ ] **Step 3:** `go list -deps ./internal/obs/...` — confirm `internal/obs` imports no other `internal/*` package (Global Constraints) and no new third-party module beyond `prometheus/client_golang` (already present).
- [ ] **Step 4:** Push the branch, open the PR against `main` (via the `finishing-a-development-branch` skill's menu), body covering: the spec's effort-estimate correction (38/43 metrics already existed), every real bug/gap closed (coldstart pre-init, `cancelled_streams_total`, request-id 16-vs-32-char fix, mgmt's missing request-id wrap, the cost-metric publish site), the provider-conditional AC6 ruling, and the deliberately parked residual (full R9 cross-product pre-init beyond the 2 metrics AC1 tests), ending with the required attribution lines.

---

## Verification (end to end)

1. `go test ./... -race -count=1` green, twice.
2. `internal/obs` has zero `internal/*` imports (`go list -deps`).
3. `curl :9090/metrics` on a kind run with the fake provider shows exactly 40 `# TYPE vkllm_` lines; `obs.AuditCardinality` against the real wired registry (Task 9's `wire_test.go`) returns nil.
4. `promtool check rules deploy/helm/gpu-on-demand/files/alerts.yaml` reports "4 rules found"; `promtool test rules alerts_test.yaml` has no `FAILED` line.
5. `jq` schema check on `files/dashboard.json` passes for 9 panels, fails for 8.
6. After Task 11 write-back: folder renamed to `090-D-*` (DONE status), old path gone, `make specs-check` passes.
