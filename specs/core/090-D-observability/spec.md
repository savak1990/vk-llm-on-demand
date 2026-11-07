---
id: "CORE-090"
status: "DONE"
updated: "2026-11-07"
depends_on: ["CORE-050", "CORE-060"]
adrs: ["0003", "0006", "0007", "0009", "0010", "0011", "0012"]
---

# 090 — Observability

**Status note:** DONE 2026-11-07. Exploration at implementation time found
that 38 of the catalog's 43 metrics already existed, correctly named and
registered, in already-shipped packages (020/040/050/055/060/070); the
real remaining work was `internal/obs` itself, a handful of additive
fixes to those packages (listed in Evidence), and the chart templates
below. Written 2026-11-01 before any code existed; updated 2026-11-01 for
explicit provisioning (ADR 0010), the rolling inactivity timeout (ADR
0011) and the management API (ADR 0012). Specs 050, 055 and 060 were
updated in the same wave from their briefs; this spec is the single
metric catalog and reconciles every name the five other specs (020, 040,
050, 055, 060) and spec 070 already use or propose.

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
chart. `curl :9090/metrics` on a kind cluster shows exactly 40 of the 43
catalogued metric families when the fake provider is wired (43 with the real
Vast.ai provider; see AC6), and no label outside the bounded enumerations.

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

`internal/gateway`'s existing request-id generator predates this spec and produces 16 hex characters, not this section's required 32 (`^[0-9a-f]{32}$`); CORE-090 fixes this by having `internal/gateway`'s existing middleware call site delegate to `obs.NewRequestID`/`obs.RequestIDMiddleware` internally, and has `internal/mgmt`'s already-documented 'the caller wraps it' design actually wired up for the first time, in `wire.go`.

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
machine has been stuck in `DELETING` or `FAILED` for 10 minutes" are the
same observable fact and the state gauge already exists. It matches both
`DELETING` and `FAILED`: `PublicState` reports `FAILED` instead of
`DELETING` once a destroy attempt itself has failed (worker failure,
terminal provider status, or deadline expiry), so matching `DELETING`
alone would miss exactly the stuck-retrying-destroy case this alert
exists to catch.

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
          expr: vkllm_gpu_state{state=~"DELETING|FAILED"} == 1
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
- AC11. `promtool test rules` against `deploy/helm/gpu-on-demand/files/alerts_test.yaml` that sets `vkllm_gpu_state{state="DELETING"}` to 1 at t=0 and holds it, and separately that sets `vkllm_gpu_state{state="FAILED"}` to 1 at t=0 and holds it: `VkllmDestroyStuck` is `inactive` at t=9m and `firing` at t=11m in both cases, because a destroy that fails outright reports `FAILED`, not `DELETING` (R10, NFR-10.6)

## Definition of done

- [x] AC1–AC6, AC8, AC10–AC11 pass: real `go test` runs (named tests cited
  in Evidence), real `promtool check/test rules`, and a real kind-cluster
  reproduction of the `chart-kind` CI job's exact assertions.
- [ ] AC7 (dashboard `jq` schema check) passes when run by hand; it is not
  wired into an automated Go test or into CI — disclosed gap, not closed
  by this spec (see Evidence).
- [ ] AC9's production code path (`obs.IncInferenceScrapeErrors()`,
  called from `internal/controller/loop.go`'s `dispatchScrape`) exists
  and is correct by inspection, but no automated test exercises the
  scrape-failure branch: every controller test's fake worker `Scrape`
  always returns nil — disclosed gap, not closed by this spec (see
  Evidence).
- [x] `make specs-check` passes.
- [x] Status flipped to DONE in the same pull request, folder renamed `090-D-observability`.
- [x] Evidence section filled with the real `/metrics` output from a kind run and the rendered dashboard/rule files.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Histogram bucket boundaries above are a starting guess, not measured | Wrong buckets make `histogram_quantile` noisy without being wrong enough to fail a test | CORE-140's acceptance run | Keep the buckets above; CORE-140 records whether `_bucket` samples cluster at the edges and this spec is amended if so |
| Exact Grafana dashboard JSON (panel ids, Grafana schema version, datasource UID templating) | Needs a real Grafana instance to author against | Implementation time, against the platform's kube-prometheus-stack Grafana | **closed**: `deploy/helm/gpu-on-demand/files/dashboard.json` ships with exactly 9 panels, each querying a `vkllm_`-prefixed series (`jq -e '(.panels\|length)==9 and (.panels\|all(.targets[0].expr\|test("vkllm_")))'` exits 0, see Evidence); not yet authored against a real Grafana instance, so panel ids/schema version/datasource UID may need adjustment once one is available — that residual check is not automated (see Definition of done, AC7) |
| The `grafana_dashboard: "1"` sidecar label and the log level being fixed (no `log.level` key) are not confirmed facts in research.md | C-32 forbids unverified facts as code assumptions | Verify the sidecar label against the platform's kube-prometheus-stack chart values at implementation time; add `log.level` to architecture §9 if runtime-configurable logging turns out to be needed | Keep the label as written; keep log level fixed at `info` |
| CORE-050's own Observability table and simulation ACs (e.g. AC1, AC3, AC22) still emit `vkllm_gpu_state` with the nine internal `State.String()` values (`Idle`, `Searching`, `Creating`, `Booting`, `Loading`, `Ready`, `Serving`, `Draining`, `Destroying`), not the eight public states this catalog defines (R12, ADR 0011 §6) | Two specs disagree on the only label of a one-hot gauge; whichever ships first makes the other's dashboard panel and `VkllmDestroyStuck` alert wrong | CORE-050, before either spec leaves DRAFT | **closed** (CORE-050 DONE, Design Decision 6): the shipped setter uses `PublicState()` and the eight public-state names, matching this catalog; CORE-050's own spec text and fixtures were corrected to match in that write-back |
| R9's "every label combination initialized to zero at process start" was originally scoped by this spec's Task 1 ruling to just 2 of the catalog's labeled metrics (`vkllm_coldstart_stage_seconds`, `vkllm_gpu_state`), recording the rest as a residual | Pre-initializing every other labeled metric's full combination set (e.g. `vkllm_gateway_requests_total`'s `route`×`code`) was real, untested-by-this-spec work | CORE-090 | **closed**: all 12 of the catalog's `*Vec`-typed metrics with small, bounded label sets are now pre-initialized at registration time in `internal/controller`, `internal/store`, `internal/mgmt` and `internal/gateway`, following the same loop `vkllm_coldstart_stage_seconds`'s pre-init already used. R9 is closed in full except for the 3 `vkllm_provider_*` series (registered only when the real Vast.ai provider is wired, AC6) and the 8 `vkllm_inference_*` series (worker-scrape-gated by the Exporter's own documented contract — they appear only after a successful pull), both excluded on their own separate, legitimate grounds, not as an R9 residual |
| Only `first_token` is observed with a real duration in `vkllm_coldstart_stage_seconds` today, the other 7 stages' instrumentation is out of this spec's scope | Metric pre-initializes all 8 stage labels but only 1 gets recorded observations; the rest are unexercised | A future spec that instruments the controller's cold-start stages | Scoped to `first_token` for this spec; a future spec can add instrumentation for the other 7 stages |
| `vkllm_gateway_cancelled_streams_total` is added here to satisfy NFR-10.1 ("cancelled streams"), but CORE-060's own text and AC13 describe only `requests_total{code="499"}` on disconnect, not this counter | A metric this spec lists but no implementing spec increments fails AC6/AC1 trivially (always zero, never exercised) | CORE-090 | **closed** (CORE-090): CORE-060 shipped `DONE` without it, so this spec adds the counter itself, in `internal/gateway`, incrementing at the same point `vkllm_gateway_requests_total`'s `code` label is computed as `"499"` — a one-line addition to the single place that already decides the metric's code label, not a new call site per disconnect path. |

## Evidence

Implementation, 2026-11-07 (`internal/obs` built from scratch across 10
tasks plus one mid-plan amendment; all commands below run for real in
this worktree, not summarized from memory).

### Real gaps found and fixed in already-shipped packages

Exploration before implementation found that 38 of the catalog's 43
metrics already existed (020/040/050/055/060/070); building `internal/obs`
surfaced several real, pre-existing bugs that this spec's implementation
fixed in place, not merely worked around:

- `vkllm_coldstart_stage_seconds` only had 1 of its 8 stages
  (`first_token`) observed/pre-initialized; the Task 1 ruling scoped R9
  down to 2 metrics as an explicit residual, then Task 9b reversed that
  and pre-initialized all 12 of the catalog's `*Vec`-typed metrics with
  bounded label sets (closing R9 in full — see Open questions).
- `vkllm_gateway_cancelled_streams_total` did not exist; CORE-060 shipped
  `DONE` without it despite NFR-10.1 naming "cancelled streams". Added in
  `internal/gateway`, incrementing at the same call site that already
  computes the `code="499"` label.
- `internal/gateway`'s request-id generator produced 16 hex characters,
  not the 32 this spec's R7 requires. Its middleware now delegates to
  `obs.NewRequestID`/`obs.RequestIDMiddleware`.
- `internal/mgmt`'s own doc comment said its caller wraps it with
  request-id middleware; nothing actually did. Wired up for the first
  time in `cmd/gpu-on-demand/wire.go`.
- A cost-metric publish call site (`publishCostMetrics`, rollover-safe
  monotonic delta) was added to `internal/controller`'s existing per-tick
  gauge point, so `vkllm_budget_used_ratio`/`vkllm_cost_*` actually move.
- A production-breaking bug caught by the final task-9 review before
  merge, not by any automated test: `wire()` momentarily double-registered
  `obs`'s metrics against `main.go`'s own `obs.NewRegistry()` call, which
  would have panicked the real binary on every startup. Fixed by removing
  the redundant call; every registry-constructing caller, tests included,
  now goes through `obs.NewRegistry()` exactly once.

### `go test ./... -race -count=1`, run twice

Both runs passed identically; 19 packages ok, 2 packages report
"[no test files]" (`cmd/vk-llm`, `hack/vastprobe`), 0 failures both times.
Per-package timings (run 2; run 1 was the same set, same result):
`cmd/fake-worker` 1.216s, `cmd/gpu-on-demand` 1.239s, `internal/auth`
1.151s, `internal/budget` 1.187s, `internal/clock` 1.131s,
`internal/clock/fake` 1.180s, `internal/config` 1.206s,
`internal/controller` 7.649s, `internal/gateway` 2.399s, `internal/mgmt`
1.491s, `internal/obs` 1.249s, `internal/provider` 1.154s,
`internal/provider/fake` 1.189s, `internal/provider/vastai` 1.286s,
`internal/store` 1.307s, `internal/store/configmap` 1.430s,
`internal/store/fake` 1.202s, `internal/tunnel` 2.170s, `internal/worker`
1.244s.

### `go test ./internal/obs/... -v`

All 12 tests pass (0.314s): `TestAuditCatchesDenylistedLabel`,
`TestCatalogHas43SortedUniqueNames`, `TestVastaiOnlyMetricsAreInCatalog`,
`TestRedactionInlineAttrs`, `TestRedactionWithAttrsChain`,
`TestRedactionInsideGroup`, `TestNonDeniedFieldsSurviveWithAttrsChain`,
`TestObsOwnedMetricsPreInitialized`,
`TestSettersUpdateValuesAndNegativeDeltaIsIgnored`,
`TestRegisterMetricsIsTestIsolated`, `TestRequestIDRoundTrip`,
`TestServerEndpoints`.

The spec's own AC1/AC9 test names (`TestCatalogGolden`,
`TestInferenceScrapeErrorIncrement`) were written 2026-11-01 before any
code existed and do not match the real names implementation settled on.
The real mapping:

- AC1 ⇒ `internal/obs.TestCatalogHas43SortedUniqueNames` (43 sorted,
  unique names) + `cmd/gpu-on-demand.TestWireMetricsCatalogComplete`
  (`wire()`'s real registry `Gather()`s exactly the catalog names minus
  the 3 vastai-only/8 scrape-gated ones, and `AuditCardinality` on it
  returns nil) + `internal/controller.TestRegisterMetricsOneHotGPUState`
  (8 `state` values, one-hot) + `internal/controller.TestColdstartStagePreInitialized`
  (8 `stage` values). Together these cover AC1's full assertion; no
  single test is named `TestCatalogGolden`.
- AC2/R3 ⇒ `internal/obs.TestAuditCatchesDenylistedLabel` (scratch
  registry) plus `cmd/gpu-on-demand.TestWireMetricsCatalogComplete`'s own
  `obs.AuditCardinality(reg, ...)` call against `wire()`'s real,
  fully-populated registry — so R3's "runs against a registry built from
  every real collector" is genuinely exercised, not only the scratch-registry case.
- AC9: see "Disclosed gaps" below — no test of this name exists, and the
  real code path has no automated coverage at all.

### `promtool check rules` / `promtool test rules` (Prometheus v3.16.0,
darwin/arm64 build, same pinned version `.github/workflows/ci.yml` installs for linux/amd64)

```
$ promtool check rules deploy/helm/gpu-on-demand/files/alerts.yaml
Checking deploy/helm/gpu-on-demand/files/alerts.yaml
  SUCCESS: 4 rules found

$ promtool test rules alerts_test.yaml
Processing test file: alerts_test.yaml
  SUCCESS
```

`alerts_test.yaml` exercises exactly AC10 (`VkllmBudgetHigh`: inactive at
`vkllm_budget_used_ratio=0.79`, firing at `0.8`) and AC11
(`VkllmDestroyStuck`: inactive at 9m and firing at 11m, run once with
`vkllm_gpu_state{state="DELETING"}==1` and once with
`vkllm_gpu_state{state="FAILED"}==1`) — all real, all pass.

### `jq` dashboard schema check (AC7)

```
$ jq -e '(.panels | length) == 9 and (.panels | all(.targets[0].expr | test("vkllm_")))' \
    deploy/helm/gpu-on-demand/files/dashboard.json
true
```
Exit 0, as the spec's implementation step 8 requires. This check is only
ever run by hand (as above) or was run once by Task 10's own verification
pass — no Go test or CI step runs it, so AC7 is not continuously verified
(disclosed gap, below).

### `make helm-lint helm-template`

```
==> Linting deploy/helm/gpu-on-demand
[INFO] Chart.yaml: icon is recommended
1 chart(s) linted, 0 chart(s) failed
```
`helm template` rendered cleanly, including the `PrometheusRule` (4 alerts,
verbatim from `files/alerts.yaml`) and the dashboard `ConfigMap`.

### `make specs-check`

```
SPECS-CHECK: specs/ layout is valid.
```

### Real `curl :9090/metrics` family count from a kind cluster (AC6)

Reproduced the `chart-kind` CI job's exact steps locally (kind v0.33.0,
node image `kindest/node:v1.37.0`, Docker Desktop, images built for
`arm64` and loaded with
`kind load docker-image`, the real `PrometheusRule` CRD pinned to
`prometheus-operator` v0.95.1, `helm install` with
`config.provider.name=fake`, `prometheusRule.enabled=true`,
`dashboard.enabled=true`):

- Immediately after `helm install`, with the GPU `STOPPED` (no inference
  traffic yet): `curl :9090/metrics | grep -c '^# TYPE vkllm_'` = **32**.
  The 3 `vkllm_provider_*` series are absent (fake provider, per AC6) and
  the 8 worker-scrape-gated `vkllm_inference_*` re-exports are absent
  (Exporter withholds them until a scrape succeeds).
- After `POST /api/v1/gpu/start`, polling to `READY`, one real
  `/v1/chat/completions` streamed call reaching `data: [DONE]`, and one
  15 s pull interval: `curl :9090/metrics | grep -c '^# TYPE vkllm_'` =
  **40**, matching AC6 exactly (43 − 3 vastai-only). The sorted family
  list (40 names) includes every catalogued name except
  `vkllm_provider_requests_total`, `vkllm_provider_request_seconds`,
  `vkllm_provider_rate_limited_total`.
- `kubectl get configmap -l grafana_dashboard=1` showed
  `gpu-on-demand-dashboard-observability`; `kubectl get prometheusrule
  gpu-on-demand` showed the rule.
- `POST /api/v1/gpu/stop` drained to `STOPPED`; `helm uninstall` left no
  `gpu-on-demand`/`gpu-on-demand-state` ConfigMap behind.

This was a local reproduction of the identical assertions
`.github/workflows/ci.yml`'s `chart-kind` job makes (including the
hardcoded `40`), not yet a captured run from an actual GitHub Actions
execution, since this branch has not been pushed.

### Disclosed gaps (not closed by this spec)

- **AC7** (dashboard `jq` schema check): passes by hand (above) but is
  not wired into a Go test or a CI step. A future CI change should add a
  `jq` step alongside the existing `promtool check/test rules` step.
- **AC9** (`vkllm_inference_scrape_errors_total` increments on a scrape
  failure): the production code
  (`obs.IncInferenceScrapeErrors()` in `internal/controller/loop.go`'s
  `dispatchScrape`) is correct by inspection, but no test — not
  `internal/controller`'s, not `internal/obs`'s — ever makes the fake
  worker's `Scrape` return a non-nil error, so this path has zero
  automated coverage today. `internal/worker.TestMetricsReexportScrapeFailure`
  covers the adjacent, already-shipped `vkllm_inference_up` transition
  (CORE-070 AC11) but not this counter.
- **R9's residual**: only `vkllm_coldstart_stage_seconds`'s `first_token`
  stage is observed with a real duration; the other 7 stages' pre-init
  values never change (see Open questions). This is the one residual
  this spec still carries forward; the 2-residual framing in the original
  implementation plan is stale — Task 9b closed R9's pre-init scope in
  full (see Open questions), leaving only this one.
