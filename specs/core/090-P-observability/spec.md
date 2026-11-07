---
id: "CORE-090"
status: "READY"
updated: "2026-11-01"
depends_on: ["CORE-050", "CORE-060"]
adrs: ["0003", "0006", "0007", "0009", "0010", "0011", "0012"]
---

# 090 — Observability

**Status note:** DRAFT, written 2026-11-01 before any code exists; updated
2026-11-01 for explicit provisioning (ADR 0010), the rolling inactivity
timeout (ADR 0011) and the management API (ADR 0012). Specs 050, 055 and
060 are being updated in the same wave from their briefs; this spec is the
single metric catalog and reconciles every name the five other specs (020,
040, 050, 055, 060) and spec 070 already use or propose.

**Complexity:** M
**Risk:** medium — this is the one place that must stay consistent as six
other packages each add metrics and log fields independently
**Estimated effort:** one session (4–6 h)
**Recommended model:** standard
**Depends on:** [CORE-050](../050-D-gpu-lifecycle-controller/spec.md),
[CORE-060](../060-D-gateway-openai-api/spec.md)

## Outcome

`internal/obs` serves `/healthz`, `/readyz` and `/metrics` on `:9090`, owns
the process-wide `*prometheus.Registry`, the `slog` JSON logger with
redaction, and request-id propagation. Every metric any package in this
repository emits appears, under one name, in the catalog below. A Grafana
dashboard ConfigMap and a `PrometheusRule` with four alerts ship with the
chart. `curl :9090/metrics` on a kind cluster shows exactly the 43
catalogued metric families and no label outside the bounded enumerations.

## Scope

- `internal/obs`: registry constructor, HTTP server for the three internal
  endpoints, `slog` JSON handler with `ts` as the time key, the redacting
  handler, request-id middleware, the cardinality auditor, and the five
  metrics that no other package yet owns (`vkllm_budget_used_ratio`,
  `vkllm_cost_estimated_usd_total`, `vkllm_cost_instance_usd`,
  `vkllm_provider_balance_usd`, `vkllm_inference_scrape_errors_total`)
  with setter functions for 050/070/100 to call.
- The metric catalog (this document is the source of truth; `internal/obs`
  exposes it as `Catalog() []Spec` for the golden-list test).
- Two templates under CORE-080's chart: the dashboard ConfigMap (panel
  list and query table below; the exact file path and chart templating are
  CORE-080's, this spec hands it the JSON and the label) and the
  `PrometheusRule` (four alerts, below).
- Tests: catalog golden list, label-denylist audit, redaction (including
  `Logger.With`), request-id round trip, the `vkllm_inference_*` fixture
  delta beyond CORE-070's AC11, dashboard JSON schema check, `promtool
  check rules`.

Excludes: the `ServiceMonitor` object (rendered by CORE-080, referenced
only, §"ServiceMonitor" below); the business logic that decides *when* a
metric changes (that lives in 020/040/050/055/060/070/100, each cited per
row in the catalog); Loki query definitions (platform default, out of scope).

## Design and contracts

### Package `internal/obs`

```go
package obs

func NewRegistry() *prometheus.Registry

type Config struct {
    Addr string // ":9090"
}

func NewServer(reg *prometheus.Registry, checks []func() error, cfg Config) *http.Server
// GET /healthz -> 200 "ok" always once the process is running.
// GET /readyz  -> 200 "ok" if every check in checks returns nil, else 503
//                 with the first error's message. CORE-040 appends
//                 store.Ready, CORE-060 appends KeysLoaded. Replaces
//                 CORE-010's main.go handlers with identical semantics.
// GET /metrics -> promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

func NewLogger(w io.Writer, level slog.Level) *slog.Logger
// slog.NewJSONHandler with ReplaceAttr renaming the default "time" key to
// "ts"; wrapped by RedactingHandler.

func RedactingHandler(next slog.Handler) slog.Handler
// Implements Handle AND WithAttrs/WithGroup: a logger built with
// l.With("authorization", v) must redact at Handle time, not only at the
// call site, because With is how a request-scoped logger gets built.

var DeniedLogFields = []string{"authorization", "prompt", "messages", "content"}

func AuditCardinality(reg *prometheus.Registry, denylist []string) error
// Gathers reg; returns one error per family×label listing the family and
// label name if any label matches the denylist (case-insensitive).

var DeniedMetricLabels = []string{"request_id", "key", "instance_id", "ip"}

func NewRequestID() string                                     // 16 random bytes, lowercase hex, 32 chars
func WithRequestID(ctx context.Context, id string) context.Context
func RequestIDFromContext(ctx context.Context) string
func RequestIDMiddleware(next http.Handler) http.Handler
// Reads X-Request-Id; if absent or not ^[0-9a-f]{32}$, generates one.
// Always writes X-Request-Id on the response, success or error.

// Owned here because no other package exists yet to own them (see
// "Metrics owned directly by obs" below).
func SetBudgetUsedRatio(v float64)
func AddCostEstimatedUSD(delta float64)  // monotonic; CORE-100 R6
func SetCostInstanceUSD(v float64)       // 0 when no instance exists
func SetProviderBalanceUSD(v float64)    // last observed Vast.ai balance (research §1.5)
func IncInferenceScrapeErrors()
```

No new interface: `internal/obs` has one implementation of everything, so
nothing here is abstracted beyond what `prometheus/client_golang` and
`log/slog` already provide (C-26).

### Metrics owned directly by `obs`

CORE-100 (cost controls) and the CORE-050 metrics-pull loop do not exist
yet in this wave. So that the catalog is complete and the golden-list test is
satisfiable at CORE-090's own `DONE`, `internal/obs` registers these five
series itself, pre-initialized to a zero value, and exposes a setter;
CORE-100 and CORE-050's pull loop call the setter instead of registering
their own collector for the same name. `IncInferenceScrapeErrors` is called
by the same code that calls `worker.Client.Metrics` (CORE-050's pull loop)
on a non-nil error. `vkllm_cost_estimated_usd_total` is monotonic
(CORE-100 R6): only `AddCostEstimatedUSD` increments it, never a `Set`.

### Metric catalog

Prefix `vkllm_`. Every label is a bounded enumeration (C-34); allowed
values are listed in full. A counter with no labels and zero increments
still appears in `/metrics` once registered; a `*Vec` metric only appears
for label combinations it has been given a value for, which is why R9
requires pre-initializing every declared combination at startup.

| Name | Type | Labels (allowed values) | Help | Source |
|---|---|---|---|---|
| `vkllm_gateway_requests_total` | counter | `route`∈{chat_completions,models,other}; `code`∈{200,400,401,403,404,405,413,429,499,500,502,503,504,stream_error} | Gateway HTTP requests by route and status code | 060 |
| `vkllm_gateway_active_requests` | gauge | none | Requests currently proxied to the worker | 060 |
| `vkllm_gateway_pending_requests` | gauge | none | On-demand only (ADR 0010 §4); registered, always 0 in explicit mode. Requests queued waiting for the GPU (ADR 0007 §2) | 060 |
| `vkllm_gateway_wait_seconds` | histogram | none | On-demand only; no observations in explicit mode. Time a request spent in the pending queue | 060 |
| `vkllm_gateway_request_seconds` | histogram | `route` (same set) | End-to-end gateway handling time, excluding upstream generation | 060 |
| `vkllm_gateway_ttft_seconds` | histogram | none | Time to first streamed token once proxying started | 060 |
| `vkllm_gateway_output_tokens_per_second` | histogram | none | Output decode rate observed per completed request | 060 |
| `vkllm_gateway_cold_start_outcomes_total` | counter | `outcome`∈{ready,gpu_cold_start,queue_full,gpu_unavailable,client_gone,shutting_down} | On-demand only; always 0 in explicit mode. How a cold-start wait resolved | 060 |
| `vkllm_gateway_auth_failures_total` | counter | `reason`∈{missing,malformed,unknown_key,wrong_surface} | Rejected `/v1` requests; `wrong_surface` is shared with `/api/v1`'s mirror case (ADR 0012 §3, FR-7.2) | 060 |
| `vkllm_gateway_cancelled_streams_total` | counter | none | Streamed requests ended by a client disconnect before the upstream finished (same event as `requests_total{code="499"}`); FR-3.2, NFR-10.1 | 060 |
| `vkllm_mgmt_requests_total` | counter | `op`∈{start,status,stop,cost,other}; `code`∈{200,202,400,401,403,404,405,413,429,500,503} | Management API requests by operation and status code (ADR 0012 §1) | 055 |
| `vkllm_mgmt_request_seconds` | histogram | `op` (same set) | Management API handling time | 055 |
| `vkllm_gpu_state` | gauge | `state`∈{STOPPED,ALLOCATING,STARTING,LOADING_MODEL,READY,DRAINING,DELETING,FAILED} | One-hot: 1 for the current public state (ADR 0011 §6), 0 for the other seven | 050 |
| `vkllm_provision_attempts_total` | counter | `outcome`∈{ok,no_offer,provider_error,deadline_exceeded,attempts_exhausted,budget_exhausted,failed_status,lost,cancelled} | Provisioning attempts by how they ended (ADR 0003 §4–5) | 050 |
| `vkllm_coldstart_stage_seconds` | histogram | `stage`∈{search,create,boot,link,download,load,ready,first_token} | Duration of each cold-start stage (architecture §4) | 050 |
| `vkllm_gpu_uptime_seconds_total` | counter | none | Cumulative seconds an instance has existed | 050 |
| `vkllm_gpu_idle_seconds` | gauge | none | Seconds since `max(readyAt, lastInferenceFinishedAt)` while Ready with 0 in flight; 0 otherwise (ADR 0011 §1) | 050 |
| `vkllm_gpu_idle_deadline_seconds` | gauge | none | `idleDeadline` as unix seconds; 0 when STOPPED or no deadline set (ADR 0011 §1, FR-3.1) | 050 |
| `vkllm_gpu_last_inference_finished_seconds` | gauge | none | `lastInferenceFinishedAt` as unix seconds; 0 before the first `RequestEnd` (ADR 0011 §2, FR-3.2) | 050 |
| `vkllm_shutdowns_total` | counter | `reason`∈{idle,manual,lifetime,budget,failure,orphan} | Once per drain or failure entry, post-commit (ADR 0011 §1, §3) | 050 |
| `vkllm_destroy_attempts_total` | counter | `outcome`∈{ok,retryable,terminal,fatal} (provider `Error.Class`, spec 020) | Destroy calls by outcome | 050 |
| `vkllm_orphans_destroyed_total` | counter | none | Instances destroyed because the provider listed them and the store did not (ADR 0003 §4) | 050 |
| `vkllm_reconcile_runs_total` | counter | `outcome`∈{ok,error} | Reconcile loop ticks | 050 |
| `vkllm_instance_hourly_price_usd` | gauge | none | `dph_total` of the current instance, 0 when none | 050 |
| `vkllm_provider_requests_total` | counter | `op`∈{search,create,get,list,destroy,balance}; `outcome`∈{ok,not_found,retryable,terminal,fatal,adopted} | Vast.ai API calls | 020 |
| `vkllm_provider_request_seconds` | histogram | `op` (same set) | Vast.ai API call latency | 020 |
| `vkllm_provider_rate_limited_total` | counter | none | HTTP 429 responses from Vast.ai | 020 |
| `vkllm_store_conflicts_total` | counter | none | Optimistic-concurrency conflicts on the state ConfigMap | 040 |
| `vkllm_store_ops_total` | counter | `op`∈{load,save,transition}; `outcome`∈{ok,conflict,exhausted,too_large,corrupt,error,reset,installation,missing} | State store operations | 040 |
| `vkllm_inference_prompt_tokens_total` | counter | none | `llamacpp:prompt_tokens_total` re-exported | 070 |
| `vkllm_inference_prompt_seconds_total` | counter | none | `llamacpp:prompt_seconds_total` re-exported | 070 |
| `vkllm_inference_generated_tokens_total` | counter | none | `llamacpp:tokens_predicted_total` re-exported | 070 |
| `vkllm_inference_generation_seconds_total` | counter | none | `llamacpp:tokens_predicted_seconds_total` re-exported | 070 |
| `vkllm_inference_prompt_tokens_per_second` | gauge | none | `llamacpp:prompt_tokens_seconds` re-exported | 070 |
| `vkllm_inference_generated_tokens_per_second` | gauge | none | `llamacpp:predicted_tokens_seconds` re-exported | 070 |
| `vkllm_inference_requests_processing` | gauge | none | `llamacpp:requests_processing` re-exported | 070 |
| `vkllm_inference_requests_deferred` | gauge | none | `llamacpp:requests_deferred` re-exported | 070 |
| `vkllm_inference_up` | gauge | none | 1 after a successful `/metrics` pull, 0 otherwise | 070 |
| `vkllm_inference_scrape_errors_total` | counter | none | Failed `GET /metrics` pulls over the tunnel | obs (070 R11 names it; 050's pull loop increments it) |
| `vkllm_budget_used_ratio` | gauge | none | Month-to-date estimated spend ÷ `budget.monthlyUSD` | obs (CORE-050 sets it after the store commit, from CORE-100 values) |
| `vkllm_cost_estimated_usd_total` | counter | none | Monotonic estimated spend; CORE-100 rolls it forward, never resets it within this process's lifetime (CORE-100 R6) | obs (CORE-050 sets it after the store commit, from CORE-100 values) |
| `vkllm_cost_instance_usd` | gauge | none | Estimated cost of the current instance so far; 0 when no instance exists | obs (CORE-050 sets it after the store commit, from CORE-100 values) |
| `vkllm_provider_balance_usd` | gauge | none | Last observed Vast.ai account balance (research §1.5) | obs (CORE-050 sets it after the store commit, from CORE-100 values) |

43 metric families total; 40 of them register unconditionally, the 3 `vkllm_provider_*` series only when the real Vast.ai provider is wired (see AC6). `vkllm_coldstart_stage_seconds` is pre-initialized to zero for all 8 stages at registration time (R9); only `first_token` is observed with a real duration today, the other 7 stages' instrumentation is out of this spec's scope (see Open questions). The pull interval for the inference metrics is 15 s over
the tunnel while the instance is `Ready` or `Serving` (ADR 0006
§Consequences: llama-server metrics are pulled and re-exported, never
scraped directly); the loop itself is CORE-050's.

### Histogram buckets

| Metric | Buckets (seconds) |
|---|---|
| `vkllm_gateway_wait_seconds`, `vkllm_coldstart_stage_seconds` | 1, 5, 15, 30, 60, 120, 240, 480, 900 |
| `vkllm_gateway_request_seconds`, `vkllm_gateway_ttft_seconds`, `vkllm_provider_request_seconds`, `vkllm_mgmt_request_seconds` | `prometheus.DefBuckets` |
| `vkllm_gateway_output_tokens_per_second` | 1, 5, 10, 20, 40, 80, 160 |

CORE-140's acceptance run records whether these need retuning (Open
questions).

### Structured logging

`slog` JSON via `obs.NewLogger`. Envelope fields present on every line:
`ts`, `level`, `msg`. Added by whichever package is logging, from this
fixed vocabulary:

| Field | Meaning | Emitted by |
|---|---|---|
| `request_id` | from `RequestIDMiddleware` | gateway, controller |
| `route` | matched route pattern | gateway |
| `status_code` | HTTP or probe status | gateway, worker |
| `duration_ms` | elapsed time of the logged operation | gateway, provider, store, worker |
| `state` | controller state-machine state | controller |
| `stage` | cold-start / worker stage | controller, worker |
| `instance_gen` | generation fencing token | controller, worker |
| `op` | provider or store operation name | provider, store |
| `outcome` | how the operation ended | provider, store, controller |
| `key_id` | first 8 characters of a key after its `sk-vkllm-` prefix (ADR 0009 §5) | gateway (auth only) |

Never logged, anywhere, under any field name: `authorization`, `prompt`,
`messages`, `content`, or the literal value of any key, token, or SSH
material (C-16, ADR 0009 §5). `RedactingHandler` is the enforcement
backstop; it must not be the only line of defense, so code review also
checks for these before a logging change merges.

Log levels: `debug` — one line per external call attempt (provider, store,
tunnel); `info` — state transitions, completed requests, reconcile ticks;
`warn` — retryable failures, auth failures, approaching a budget or
attempt limit; `error` — fatal failures and anything the alerts in this
spec also fire on. Level is fixed at `info` in production; a configurable
`log.level` key is not in architecture §9 (Open questions).

### Request id propagation

`RequestIDMiddleware` is first in the gateway's chain (ADR: middleware
order in spec 060), ahead of auth, so even a 401 carries a request id.
Inbound `X-Request-Id` is reused if it matches `^[0-9a-f]{32}$`, otherwise
replaced — a client cannot inject an arbitrary string into logs via this
header. The id is attached to the request context, read by the logging
middleware, and echoed on the response header on every status code.

`internal/gateway`'s existing request-id generator predates this spec and produces 16 hex characters, not this section's required 32 (`^[0-9a-f]{32}$}`); CORE-090 fixes this by having `internal/gateway`'s existing middleware call site delegate to `obs.NewRequestID`/`obs.RequestIDMiddleware` internally, and has `internal/mgmt`'s already-documented 'the caller wraps it' design actually wired up for the first time, in `wire.go`.

### Cardinality audit

`AuditCardinality` runs in CI against a registry that has every package's
real collector registered (fake provider/store/clock, no network): it
fails the build if any metric family carries a label named `request_id`,
`key`, `instance_id` or `ip` (case-insensitive). This is additive to the
enumerations above, which are reviewed by hand; the audit catches a label
added later without a spec update.

### Grafana dashboard

`ConfigMap` `<release>-dashboard-observability`, label `grafana_dashboard:
"1"` (the platform's kube-prometheus-stack dashboard-sidecar convention;
not itself verified in research.md, see Open questions), key
`dashboard.json`, namespace `{{ .Release.Namespace }}`. Nine panels:

| Panel | Type | Query |
|---|---|---|
| GPU state timeline | state timeline | `vkllm_gpu_state` |
| Cold-start stages | bar gauge | `vkllm_coldstart_stage_seconds` (p50/p95 per `stage`) |
| Time to first token | time series | `histogram_quantile(0.95, rate(vkllm_gateway_ttft_seconds_bucket[5m]))` |
| Tokens per second | time series | `vkllm_inference_generated_tokens_per_second` and `vkllm_gateway_output_tokens_per_second` |
| Spend MTD vs budget | stat + gauge | `vkllm_budget_used_ratio` and `vkllm_cost_instance_usd` |
| Provisioning outcomes | bar chart | `sum by (outcome) (increase(vkllm_provision_attempts_total[1h]))` |
| Orphan events | time series | `increase(vkllm_orphans_destroyed_total[1h])` |
| Idle deadline countdown | stat | `vkllm_gpu_idle_deadline_seconds - time()` (ADR 0011 §1; 0 or negative while STOPPED or no deadline) |
| Shutdowns by reason | bar chart | `sum by (reason) (increase(vkllm_shutdowns_total[1h]))` |

The JSON itself is written when the dashboard is built against a running
Grafana (Open questions); this spec fixes the panel list and queries so
that step is mechanical.

### ServiceMonitor

Referenced only. CORE-080 renders a `ServiceMonitor` selecting this
Deployment's `:9090` port with path `/metrics`, scrape interval per
CORE-080. Nothing in this spec depends on its exact YAML.

### Alerts

Four alerts. C-4 treats a persistently failing destroy as its own paged condition, distinct from a
provisioning (create-side) failure: the remediation is different (a stuck
destroy risks unbounded GPU billing; a provisioning failure risks no
service). `VkllmDestroyStuck` uses `vkllm_gpu_state` with a `for:` clause
rather than a new counter, because "destroy keeps failing" and "the state
machine has been stuck in `DELETING` for 10 minutes" are the same
observable fact and the state gauge already exists.

The `PrometheusRule` CRD wraps one `groups` list under `spec`, which is
what `promtool check rules` needs fed to it directly (the AC below strips
the envelope first):

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: <release>-vkllm
  namespace: {{ .Release.Namespace }}
spec:
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

`no_offer` is deliberately excluded from `VkllmProvisioningFailing`:
architecture §8 treats it as the expected response to an unmatched filter,
retried on the next demand, not a failure.

## Requirements

- R1. The catalog table above MUST be the only source of metric names; a package MUST NOT register a `vkllm_`-prefixed series not listed here without updating this spec in the same change. (source: NFR-10.1, NFR-10.2, NFR-10.4, C-34)
- R2. Every metric label value set MUST be the bounded enumeration listed; no label MAY carry a request id, key id, instance id or IP address. (source: NFR-10.5, C-34, C-16)
- R3. `AuditCardinality` MUST run in CI against a registry built from every real collector and MUST fail the build on a denylisted label name. (source: NFR-10.5, C-34)
- R4. `RedactingHandler` MUST strip `authorization`, `prompt`, `messages` and `content` from a log record whether set via `Handle` or via a logger built with `Logger.With(...)`. (source: C-16, ADR 0009 §5)
- R5. No log line MAY contain the literal value of a key, token or SSH private key material. (source: C-16, ADR 0009 §5)
- R6. `/metrics`, `/healthz` and `/readyz` MUST be served on `:9090` only; `:9090` MUST NOT be exposed through the gateway's `:8080` mux. (source: architecture §6)
- R7. `RequestIDMiddleware` MUST generate a 32-character lowercase-hex id when the inbound `X-Request-Id` does not match `^[0-9a-f]{32}$`, and MUST echo the id on every response. (source: NFR-10.5)
- R8. `internal/obs` MUST register `vkllm_budget_used_ratio`, `vkllm_cost_estimated_usd_total`, `vkllm_cost_instance_usd`, `vkllm_provider_balance_usd` and `vkllm_inference_scrape_errors_total` directly, pre-initialized, so the catalog is complete independent of CORE-100's and CORE-070's status. (source: NFR-10.4, FR-6.4, FR-6.8)
- R9. Every metric with a label MUST have every declared label-value combination initialized to zero at process start, so `/metrics` always lists the full combination set. (source: C-34, golden-list testability)
- R10. The `PrometheusRule` MUST contain exactly four alerts in one group and MUST pass `promtool check rules` with exit code 0. (source: NFR-10.6)
- R11. The dashboard ConfigMap MUST carry the label `grafana_dashboard: "1"` and exactly nine panels. (source: NFR-10.6)
- R12. `vkllm_gpu_state`'s `state` label MUST carry only the eight public states; the controller MUST emit `PublicState()`, never an internal stage name. (source: ADR 0011 §6)

## Implementation steps

1. `obs.NewRegistry`, `obs.NewServer` (`/healthz`, `/readyz`, `/metrics`). Test: `TestServerEndpoints` (httptest, all three paths).
2. `obs.NewLogger`, `RedactingHandler`, `DeniedLogFields`. Test: `TestRedactionInlineAttrs`, `TestRedactionWithAttrsChain` (a logger built via `.With("authorization", "secret")` then logging a line; assert `secret` does not appear in the output bytes).
3. `RequestIDMiddleware`, context helpers. Test: `TestRequestIDRoundTrip` (no header → 32-hex generated and echoed; malformed header → replaced; valid header → reused).
4. `AuditCardinality`, `DeniedMetricLabels`. Test: `TestAuditCatchesDenylistedLabel` (register a throwaway `CounterVec` with label `instance_id`, assert the audit returns an error naming it).
5. The five obs-owned metrics and setters. Test: `TestObsOwnedMetricsPreInitialized` (fresh registry, `Gather()` before any `Set`/`Add`/`Inc` call shows all five families present with zero value).
6. `Catalog()` plus the golden-list test. Test: `TestCatalogGolden` (register one fake collector per package per the catalog, call every setter/initializer so every label combination exists once, `Gather()`, assert the sorted family-name list has exactly 43 entries equal to `Catalog()`'s names).
7. Scrape-error delta test. Test: `TestInferenceScrapeErrorIncrement` (fixture: a 500 from the worker `/metrics` endpoint increments `vkllm_inference_scrape_errors_total` by 1 and `vkllm_inference_up` reads 0 — the rest of the mapping is CORE-070 AC11, not repeated here).
8. The dashboard ConfigMap's `dashboard.json` with the nine panels above. Test: `TestDashboardSchema` — `jq -e '(.panels | length) == 9 and (.panels | all(.targets[0].expr | test("vkllm_")))' dashboard.json` exits 0.
9. The `PrometheusRule`'s four alerts. Test: `yq '.spec' prometheusrule.yaml | promtool check rules /dev/stdin` exits 0 and reports `4 rules found`.

## Tests / acceptance criteria

- AC1. `TestCatalogGolden` passes: `Gather()` returns exactly 43 family names, equal to `Catalog()`'s 43 names, sorted; `vkllm_coldstart_stage_seconds` carries exactly 8 `stage` label values; `vkllm_gpu_state` carries exactly 8 `state` label values, all from the public-state enumeration. (R1, R9, R12, NFR-10.1, NFR-10.2, NFR-10.4, FR-2.4)
- AC2. `TestAuditCatchesDenylistedLabel`: a `CounterVec{"instance_id"}` registered into a scratch registry makes `AuditCardinality` return a non-nil error whose message contains `instance_id`. A registry built only from the real catalog returns nil. (R2, R3, NFR-10.5)
- AC3. `TestRedactionWithAttrsChain`: `logger.With("authorization", "Bearer sk-vkllm-xxxx").Info("auth check")` produces JSON output that does not contain the substring `sk-vkllm-xxxx`. Same for `prompt`, `messages`, `content`. (R4, R5, NFR-8.2, NFR-8.7)
- AC4. `TestRequestIDRoundTrip`: a request with no `X-Request-Id` gets a response header matching `^[0-9a-f]{32}$`; a request with `X-Request-Id: not-valid` gets a *different* 32-hex value back; a request with `X-Request-Id: ` + a valid 32-hex value gets that exact value back. (R7, NFR-10.5)
- AC5. `TestObsOwnedMetricsPreInitialized`: immediately after `NewRegistry()` plus obs's own registration, `Gather()` shows `vkllm_budget_used_ratio` 0, `vkllm_cost_estimated_usd_total` 0, `vkllm_cost_instance_usd` 0, `vkllm_provider_balance_usd` 0, `vkllm_inference_scrape_errors_total` 0. (R8)
- AC6. On a kind cluster with the fake provider, `curl :9090/metrics` returns HTTP 200 and `grep -c '^# TYPE vkllm_'` on the body equals `40`: `internal/provider/vastai`'s 3 series (`vkllm_provider_requests_total`, `vkllm_provider_request_seconds`, `vkllm_provider_rate_limited_total`) only register as a side effect of constructing the real Vast.ai client, which the fake provider path never does. A deployment with `config.provider.name=vastai` shows all 43. (R1, R6)
- AC7. `jq` schema check on the rendered dashboard JSON exits 0 for the expression in implementation step 8; a dashboard with 8 panels fails it. (R11, NFR-10.6)
- AC8. `promtool check rules deploy/helm/gpu-on-demand/files/alerts.yaml` exits 0 and its output reports `4 rules found`. The file's top-level `groups:` key is `promtool`'s own native rule-file format, so no Helm render or `yq` envelope-stripping step is needed; the chart's `PrometheusRule` template pulls this same file verbatim via `.Files.Get`. A fifth bogus rule added to a scratch copy changes that count, proving the test is not vacuous. (R10, NFR-10.6)
- AC9. `TestInferenceScrapeErrorIncrement`: a fixture upstream returning 500 on `GET /metrics` leaves `vkllm_inference_up` at 0 and `vkllm_inference_scrape_errors_total` at 1 after one pull cycle. (R8, NFR-10.3)
- AC10. `promtool test rules` against `deploy/helm/gpu-on-demand/files/alerts_test.yaml` that sets `vkllm_budget_used_ratio` to 0.79 at t=0 then 0.8 at t=1m: `VkllmBudgetHigh` is `inactive` at t=0 and `firing` at t=1m. (R10, FR-6.4, NFR-10.6)
- AC11. `promtool test rules` against `deploy/helm/gpu-on-demand/files/alerts_test.yaml` that sets `vkllm_gpu_state{state="DELETING"}` to 1 at t=0 and holds it: `VkllmDestroyStuck` is `inactive` at t=9m and `firing` at t=11m. (R10, NFR-10.6)

## Definition of done

- [ ] AC1–AC11 pass in CI.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE in the same pull request, folder renamed `090-D-observability`.
- [ ] Evidence section filled with the real `/metrics` output from a kind run and the rendered dashboard/rule files.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Histogram bucket boundaries above are a starting guess, not measured | Wrong buckets make `histogram_quantile` noisy without being wrong enough to fail a test | CORE-140's acceptance run | Keep the buckets above; CORE-140 records whether `_bucket` samples cluster at the edges and this spec is amended if so |
| Exact Grafana dashboard JSON (panel ids, Grafana schema version, datasource UID templating) | Needs a real Grafana instance to author against | Implementation time, against the platform's kube-prometheus-stack Grafana | Panel list and queries above are fixed; JSON structure is mechanical from them |
| The `grafana_dashboard: "1"` sidecar label and the log level being fixed (no `log.level` key) are not confirmed facts in research.md | C-32 forbids unverified facts as code assumptions | Verify the sidecar label against the platform's kube-prometheus-stack chart values at implementation time; add `log.level` to architecture §9 if runtime-configurable logging turns out to be needed | Keep the label as written; keep log level fixed at `info` |
| CORE-050's own Observability table and simulation ACs (e.g. AC1, AC3, AC22) still emit `vkllm_gpu_state` with the nine internal `State.String()` values (`Idle`, `Searching`, `Creating`, `Booting`, `Loading`, `Ready`, `Serving`, `Draining`, `Destroying`), not the eight public states this catalog defines (R12, ADR 0011 §6) | Two specs disagree on the only label of a one-hot gauge; whichever ships first makes the other's dashboard panel and `VkllmDestroyStuck` alert wrong | CORE-050, before either spec leaves DRAFT | **closed** (CORE-050 DONE, Design Decision 6): the shipped setter uses `PublicState()` and the eight public-state names, matching this catalog; CORE-050's own spec text and fixtures were corrected to match in that write-back |
| R9's "every label combination initialized to zero at process start" is only tested by AC1 for 2 of the catalog's labeled metrics (`vkllm_coldstart_stage_seconds`, `vkllm_gpu_state`) | Pre-initializing every other labeled metric's full combination set (e.g. `vkllm_gateway_requests_total`'s `route`×`code`) is real, untested-by-this-spec work | Implementation time, CORE-090 | Scoped to AC1's 2 metrics for this spec; a future spec or CI assertion can widen it |
| Only `first_token` is observed with a real duration in `vkllm_coldstart_stage_seconds` today, the other 7 stages' instrumentation is out of this spec's scope | Metric pre-initializes all 8 stage labels but only 1 gets recorded observations; the rest are unexercised | Implementation time, CORE-090 | Scoped to `first_token` for this spec; a future spec can add instrumentation for the other 7 stages |
| `vkllm_gateway_cancelled_streams_total` is added here to satisfy NFR-10.1 ("cancelled streams"), but CORE-060's own text and AC13 describe only `requests_total{code="499"}` on disconnect, not this counter | A metric this spec lists but no implementing spec increments fails AC6/AC1 trivially (always zero, never exercised) | CORE-060, before either spec leaves DRAFT | **closed** (CORE-090): CORE-060 shipped `DONE` without it, so this spec adds the counter itself, in `internal/gateway`, incrementing at the same point `vkllm_gateway_requests_total`'s `code` label is computed as `"499"` — a one-line addition to the single place that already decides the metric's code label, not a new call site per disconnect path. |

## Evidence

None yet.
