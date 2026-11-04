---
id: "CORE-140"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-055", "CORE-070", "CORE-080", "CORE-100", "CORE-150", "CORE-160"]
adrs: ["0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009", "0010", "0011", "0012"]
---

# 140 — End-to-end acceptance run

**Status note:** DRAFT, written 2026-11-01 before any code exists; updated
2026-11-01 for explicit provisioning (ADR 0010), the 30-minute rolling idle
timeout and immediate manual stop (ADR 0011), and the management API,
`vk-llm` CLI and `gpu-control.yml` (ADR 0012) that replace `/admin/v1` and
workflow leases.

**Complexity:** L
**Risk:** high — real GPU spend and real destroys against the single live
installation ([testing-strategy.md](../../../docs/testing-strategy.md)
tier 3); a bug here can both overspend and leave a GPU running
**Estimated effort:** one session to write (5–7 h); every run costs ≤
`VKLLM_E2E_BUDGET_USD` (default $10) and ≤ 90 min wall clock
**Recommended model:** strong — live results are judged against seventeen
ordered, stateful scenarios and the FR/NFR gates they prove
**Depends on:** [CORE-055](../055-D-management-api/spec.md) (management
API), [CORE-070](../070-D-worker-runtime/spec.md) (worker contract),
CORE-080 (chart), CORE-100 (budget and cost ledger), CORE-150 (`vk-llm`
CLI binary), CORE-160 (`gpu-control.yml`)

## Outcome

Running `make e2e` (or the `e2e` GitHub Action) against a real installation
— the platform's one installation, or a temporary kind install on a laptop
with its own `installationId` (testing-strategy.md tier 3) — proves, with a
real Vast.ai GPU, every row of `docs/requirements.md` whose Spec column
names 140: the explicit `vk-llm start`/`stop` lifecycle, the rolling idle
deadline and what never moves it, the management-token/inference-key
separation, `gpu-control.yml`, restart recovery (both before and after the
deadline has passed), racing start/stop, orphan cleanup, checksum failure,
budget exhaustion, no-offer, concurrency and cancellation. When the run
ends, no instance carries the installation's label prefix, total spend is
within the cap, and a Markdown measurement table lands in this spec's
Evidence and in `docs/research.md` §7 as a new subsection after spec 030's
spike results.

## Scope

- `test/e2e/`: one Go package behind build tag `e2e`, one `TestMain` with
  the safety guard, one test function per scenario group below.
- `make e2e`: builds `cmd/vk-llm` once to a temp path, then runs `go test
  -tags e2e -timeout 90m ./test/e2e/...`, refusing to start if any required
  environment variable is unset.
- `.github/workflows/e2e.yml`: `workflow_dispatch` only, one GitHub
  Environment holding the keys, `concurrency: {group: e2e,
  cancel-in-progress: false}` so two runs never overlap
  (testing-strategy.md "Two tier 3 runs never overlap").
- The Markdown report appended to this spec's Evidence and to
  `docs/research.md` §7.

Excludes: the fake-provider/fake-clock suites of specs 020–110 (tier 1);
the kind-with-fake-worker chart tests (tier 2, spec 080); the Vast offer
census and launch-mode decision (spec 030, already closed); a live
worker-crash mid-stream kill (FR-5.6) — the production `Dialer` offers an
`http.RoundTripper` only, never an exec channel (spec 070 "Open
questions") — stays proven by the spec 050/060 simulations; full-repository
`gitleaks` scanning (spec 170's job); the worker's own dead-man exit
(FR-4.5 names this spec in `docs/requirements.md`, but the update-1
brief's scenario list omits it — flagged under Open questions, not
silently dropped); a long-context prompt (no FR tags 140 for it);
on-demand mode (ADR 0010 §4). Official Python/Node SDK conformance
(FR-1.1's literal clause) stays a manual, documented smoke step — this
suite is pure Go over the wire format spec 060 defines, the
CI-automatable proxy for it.

## Design and contracts

### Inputs

| Variable | Meaning | Source if not set |
|---|---|---|
| `VKLLM_E2E_BASE_URL` | scheme and host only, no path — `https://llm.<fqdn>` or a kind port-forward base; the suite builds `/v1/...` and `/api/v1/gpu/...` from it | required, no default |
| `VKLLM_E2E_CLIENT_KEY` | inference key for `/v1/*` | required |
| `VKLLM_E2E_MGMT_TOKEN` | management token for `/api/v1/*` (ADR 0012 §3) | required |
| `VKLLM_E2E_BUDGET_USD` | hard spend cap for the run | default `10` |
| `VKLLM_E2E_VAST_API_KEY_FILE` | path to the Vast.ai API key, read once, like spec 030's `VAST_API_KEY_FILE`; used only by the orphan-injection scenario's direct `CreateInstance`/`DestroyInstance` calls through `internal/provider/vastai` (same module, importable) | required |
| `VKLLM_E2E_NAMESPACE`, `VKLLM_E2E_RELEASE` | the Helm release under test, for the config overrides and `kubectl rollout restart` below | required |
| `KUBECONFIG` | standard kubectl context for that cluster | required |
| `VKLLM_E2E_GH_REPO` | `owner/repo` dispatched for the `gpu-control.yml` scenario via the `gh` CLI, already authenticated in the environment | required only when that scenario runs (Open questions) |

`VKLLM_E2E_NAMESPACE`, `VKLLM_E2E_RELEASE`, `KUBECONFIG` and
`VKLLM_E2E_VAST_API_KEY_FILE` are this spec's addition beyond the base
four, needed for the config overrides and the direct-provider scenario.
`VKLLM_E2E_MGMT_TOKEN` replaces `VKLLM_E2E_ADMIN_KEY`; `VKLLM_E2E_GH_REPO`
is new for `gpu-control.yml`. Two scenarios invoke the built `vk-llm`
binary as a subprocess with `VK_LLM_API_ENDPOINT=$VKLLM_E2E_BASE_URL` and
`VK_LLM_TOKEN=$VKLLM_E2E_MGMT_TOKEN` (ADR 0012 §4); every other management
call in this suite goes straight to `GET/POST /api/v1/gpu/*` over HTTP, to
keep assertions on raw status documents rather than CLI stdout.

### Target and identity

Per testing-strategy.md, this suite never runs against a `-dev` or `-pr`
copy: it targets the one real platform installation, or a temporary laptop
kind install. `installationId` is the chart value ADR 0004 §6 defines,
default `sha256("<namespace>/<release>")[:12]` — a hash of the platform's
own fixed namespace and release, so a laptop install that leaves it unset
could collide with the platform's prefix. A laptop install therefore MUST
set `installationId` explicitly, e.g. `--set installationId=e2e-<user>`.
`TestMain` computes the platform's default and fails if the prefix read
from `GET /api/v1/gpu/status` equals it. `TestMain` opens by calling the
management API's status route with the management token and reads
`installationId`, `generation` and `labelPrefix` from the status document
(ADR 0012 §1 field set; binding: the orphan-injection scenario and the
exit guard use exactly this prefix, never one assembled from a
`kubectl`-read ConfigMap).

Platform caveat: on the platform installation the release is Argo-managed
(architecture §11); a `helm upgrade --set` made outside Argo either is not
the live release or is reverted on the next sync, and the real 30m idle
timeout makes waiting out a deadline impractical here. The five
config-override scenarios (checksum, budget, no-offer, idle-out destroy,
restart-after-the-deadline) therefore run only against a kind target;
against the platform the suite runs the override-free subset (scenarios
1–7, 9–13, 16–17) and records the rest as skipped. Settled by spec 130.

### Safety: the exit guard

```go
func TestMain(m *testing.M) {
    ctx, cancel := context.WithTimeout(context.Background(), 85*time.Minute)
    defer cancel()
    // ... run m.Run() under ctx ...
    sweep(prefix) // always, even on panic or t.Fatal, via defer
}
```

- The internal 85-minute context is the real cap (C-1), raised from this
  spec's earlier 55-minute figure for the larger update-1 scenario list;
  `go test -timeout 90m` is only the outer backstop (Go's own `-timeout`
  skips deferred cleanup). Neither figure is fixed by an ADR; this spec
  sets both.
- `sweep(prefix)` lists every provider instance under the label prefix and
  destroys every one found, retried until `GetInstance` confirms absence
  (C-4), then fails the run if the list was non-empty at entry. `e2e.yml`
  adds a separate `if: always()` job that re-runs the same sweep in case
  the runner was killed before `TestMain`'s defer ran.
- Every override made for the run (the five of R4) is restored by a
  deferred `helm upgrade` back to the chart defaults (C-7: never
  hard-coded), so a crashed run cannot poison the next one.

### Scenarios

One ordered run through the single installation. Group A (1–8) reuses one
instance to avoid repeated cold starts; Group B (9–16) provisions
separately per scenario because each needs its own trigger or override.
Management calls use `GET/POST /api/v1/gpu/{start,status,stop,cost}`
(ADR 0012 §1), replacing the superseded `/admin/v1/{warmup,status,scale,
leases}`. `controller.reconcileInterval` stays at its 60 s default
throughout; only the five values of R4 are overridden, restored before
scenario 17. `lifecycle.safety.maxProvisionAttemptsPerHour` (3 default) is
raised to 15, kind only, since this run's cold starts and failed attempts
(scenarios 2, 9, 10, 11, 12, 14, 15) would otherwise exhaust the default
before scenario 16; the platform run creates only twice (2, 9) and never
sets this override.

`/metrics` on `:9090` is never routed by Envoy (architecture §6); every
scenario that reads it does so over a `kubectl port-forward` to the
control-plane pod, torn down after the scenario.

| # | Scenario | Action | Pass criterion | Max duration |
|---|---|---|---|---|
| 1 | Preconditions | `vk-llm status`; `GET /v1/models`; `kubectl top pod` RSS reading | status `STOPPED`; models list has exactly the configured alias; RSS < 128 MiB recorded (NFR-11.4, **A**); if `VKLLM_E2E_BASE_URL` is `https://llm.<fqdn>`, the TLS handshake succeeds with no client override (NFR-9.4) | 30 s |
| 2 | Start | `vk-llm start` (kind: `lifecycle.idleShutdown.timeout` overridden first, e.g. 4m); first streamed chat completion sent with the same JSON body the OpenAI Python/Node SDKs send | status walks `ALLOCATING → STARTING → LOADING_MODEL → READY` in that order (FR-2.5); `secondsUntilIdleShutdown` ≈ the configured timeout at `READY`, ± one reconcile interval; cold start < 8 min (FR-2.3); the request/response bodies match the OpenAI schema byte-for-byte on the fields the SDKs set (FR-1.1) | 8 min |
| 3 | Tool call and warm completion | one `get_weather(city)` two-leg exchange; 5 streamed 1K-token requests, `enable_thinking: false` | leg 1 `finish_reason: "tool_calls"`, leg 2 `"stop"`; `idleDeadline` moves to ≈ `now + timeout` after each leg and each request (FR-3.2, FR-3.3); median TTFT < 1.5 s and tokens/s ≥ 40 recorded (NFR-11.1, NFR-11.2, **A**) | 2 min |
| 4 | Deadline untouched | 100 calls total, 20 each of `vk-llm status`, `GET /api/v1/gpu/cost`, `GET /v1/models`, `/healthz`, `/metrics` | `idleDeadline` byte-identical across all 100 reads (FR-3.5) | 1 min |
| 5 | Concurrency | 5 parallel chat requests; `kubectl top pod` RSS reading mid-load | all 5 complete 200/`[DONE]`; no premature termination (FR-3.9); RSS < 256 MiB recorded (NFR-11.4, **A**) | 2 min |
| 6 | Cancellation | start a long stream, close the client connection | `RequestEnd` fires once and the deadline moves (FR-3.2); gateway active-requests gauge returns to 0 within 2 s | 30 s |
| 7 | Unauthorized matrix | `GET /v1/models` and `GET /api/v1/gpu/status` each with: no header, the inference key, the management token | inference key on `/api/v1/*` → 403; management token on `/v1/*` → 403; no header → 401 on both (NFR-8.1, FR-7.2) | 30 s |
| 8 | Idle-out or manual close | *(kind)* wait `idleTimeout + reconcileInterval` with no further inference; *(platform)* `vk-llm stop` instead, recorded not gated | *(kind)* instance destroyed, provider list for the prefix empty, status `STOPPED` (FR-3.1); *(platform)* `DRAINING` within one tick, `STOPPED` after destroy (FR-3.6, FR-3.7) | 5 min |
| 9 | Second start, manual stop | `vk-llm start`; once `READY`, `vk-llm stop` | `DRAINING` within one reconcile tick of the call; `STOPPED` after the destroy confirms (FR-3.6, FR-3.7, FR-7.5) | 10 min |
| 10 | `gpu-control.yml`, restart, deadline passed | `gh workflow run gpu-control.yml -f action=start --repo $VKLLM_E2E_GH_REPO`; poll `GET /api/v1/gpu/status`; once `READY`, `kubectl rollout restart deployment/<release>`; *(kind)* then scale the deployment to 0, wait past the (still shortened) `idleDeadline`, scale back to 1 | workflow dispatches without error; status shows the same walk to `READY`; polling never moves `idleDeadline` (FR-7.8, FR-7.9); after the quick restart the same generation is re-adopted and `idleDeadline` is unchanged (FR-4.2, FR-3.10 first half); *(kind)* after the deadline-passed restart the controller observes a deadline in the past and moves to `DRAINING` on its first tick, then `DELETING → STOPPED` (FR-3.10 second half) | 16 min |
| 11 | Stop during provisioning | `vk-llm start`; `vk-llm stop` while state is `ALLOCATING` or `STARTING` | the instance is destroyed as soon as a provider id exists; no `READY` state is ever observed for this generation (FR-3.11 first half) | 2 min |
| 12 | Start during deletion | `vk-llm start`; once `READY`, `vk-llm stop`; `vk-llm start` again while state is `DRAINING` or `DELETING` | `desired=RUNNING` is recorded at once; the destroy confirms before any `CreateInstance` for the next generation; generation increments by exactly 1 only after that confirmation (FR-3.11 second half, FR-2.8); closed with a final `vk-llm stop` | 12 min |
| 13 | Orphan injection | read `generation` from `GET /api/v1/gpu/status`; direct `CreateInstance` with label `<prefix>:<generation-1>` (or `0` if `generation` is 1), a plain `image`/`runtype ssh`/`disk` request with no `env` and no `onstart` (nothing to expose, C-17), bypassing the gateway | the generation used is confirmed absent from the store and lower than the current one before injection (ADR 0003 §3: only then is it an orphan, not a protected newer instance); destroyed within 2 × `controller.reconcileInterval` (120 s); `vkllm_orphans_destroyed_total` incremented (FR-6.6, C-6) | 3 min |
| 14 | Checksum failure *(kind only)* | `helm upgrade --set config.model.sha256=<64 zeros>`; `vk-llm start` | onstart exits non-zero within the download time; instance destroyed; attempt counted; the failing `machine_id` excluded from the next search for 1 h (ADR 0003 §5, observed as absent from the next offer list if exposed); no instance left (FR-5.5) | 6 min |
| 15 | Budget exhaustion *(kind only)* | `helm upgrade --set config.budget.monthlyUSD=<current ledger spend>`; `vk-llm start` | `503` with `code: "gpu_unavailable"`, `reason: "budget_exhausted"`, no `Retry-After` (FR-6.3) | 1 min |
| 16 | No offer *(kind only)* | `helm upgrade --set config.lifecycle.safety.maxHourlyPriceUSD=0.01`; `vk-llm start`; poll status; `vk-llm start` again within the attempt backoff | first start `202`, then status shows `unavailable: "no_offer"`, state `STOPPED` (FR-2.6); second start within the backoff → `503 gpu_unavailable`, `reason: "no_offer"` (FR-2.6) | 3 min |
| 17 | Final | restore all five overrides (R4) to chart defaults, release the port-forward | no instance under the prefix exists; provider balance delta ≤ `VKLLM_E2E_BUDGET_USD`; every metric this suite reads is present under the names fixed in the CORE-090 catalog; `helm get values` and the rendered ConfigMap show every overridden value back at its chart default (FR-6.9) | 2 min |

Scenarios marked *(kind only)* run only when the target is a laptop kind
install; against the platform installation they are skipped and recorded
as such (Open questions, Target and identity above). Scenario 10's
deadline-passed half is kind only for the same reason; its workflow-dispatch
and quick-restart halves run on both targets.

Rough budget on kind, worst case: four cold starts to `READY` (#2, #9,
#10, #12, ≤ 8 min each ≈ 32 min), one partial checksum download (#14, ≈
6 min), one quick cancelled provision (#11, ≈ 2 min), the smaller
scenarios and #10's deadline-pass wait (≈ 25 min) ≈ 65 min — inside the
85-minute internal cap with margin; `TestMain`'s context is the hard stop
if this runs long. The platform run (two cold starts, no overrides) is
well under 30 min. Spend: four GPU-time windows at ≤
`lifecycle.safety.maxHourlyPriceUSD` (0.80), well under an hour combined,
plus up to four 26 GB downloads' bandwidth (cents, ADR 0005 Consequences)
— a few dollars at most, inside the $10 cap; wall clock, not spend, is
this run's tighter constraint.

### Report

The suite writes one JSON line per scenario outcome and timing to stdout
and collects them into a Markdown table: columns `scenario`, `FR/NFR id`,
`measured value`, `pass/fail or recorded`, `timestamp`. This table is
pasted into this spec's Evidence and appended as a new subsection of
`docs/research.md` §7 (after spec 030's spike results, same section, a
second run of the same measurements under real traffic rather than a
synthetic spike).

### Security

- No key, token, address or host name is written to the report or
  committed anywhere (C-14), matching spec 030's rule.
  `VKLLM_E2E_VAST_API_KEY_FILE` (ADR 0009 §4 pattern) and `VK_LLM_TOKEN`
  passed to the `vk-llm` subprocesses are read once, via environment or a
  closure, never logged (C-16).
- The suite never mounts or reads the production `existingSecrets.*`
  Secrets; it only calls the already-authenticated `/v1` and `/api/v1/gpu`
  endpoints, the `gh` CLI, the `vk-llm` binary, and the provider API with
  its own key file.

## Requirements

- R1. `TestMain` MUST install the exit guard before any scenario runs and
  MUST destroy every instance under the installation's label prefix on
  exit, retried until the provider confirms absence. (source: C-1, C-4)
- R2. The suite MUST read the installation's label prefix and current
  `generation` from `GET /api/v1/gpu/status` with the management token,
  never assemble either from a separate `kubectl`-read state document.
  (source: testing-strategy.md "Rules"; ADR 0003 §3; ADR 0012 §1)
- R3. The orphan-injection scenario MUST create its instance with the
  installation's own label prefix and a generation confirmed lower than
  the store's current `generation` and absent from it, never a value that
  could be mistaken for a newer, protected instance. (source: ADR 0003 §3,
  Risks)
- R4. Every config value this suite overrides (`lifecycle.idleShutdown.
  timeout`, `model.sha256`, `budget.monthlyUSD`, `lifecycle.safety.
  maxHourlyPriceUSD`, `lifecycle.safety.maxProvisionAttemptsPerHour`)
  MUST be restored to the chart's own defaults before the suite exits,
  success or failure. (source: C-7)
- R5. The suite MUST NOT run under `pull_request`; `.github/workflows/
  e2e.yml` MUST declare only `workflow_dispatch` and a `concurrency` group
  that forbids an overlapping run. (source: testing-strategy.md "Two tier 3
  runs never overlap"; C-22)
- R6. The suite MUST stop issuing new provisioning actions once measured
  spend reaches `VKLLM_E2E_BUDGET_USD` and MUST fail loudly rather than
  continue silently over budget. (source: C-1, C-29)
- R7. Each scenario MUST be independently re-runnable: a second run against
  a clean installation MUST reach the same pass/fail outcomes. (source:
  testing-strategy.md "ends by asserting that no instance … exists"; C-29)
- R8. The suite MUST NOT busy-loop; every wait MUST be a bounded poll
  against a deadline derived from the scenario's max duration. (source:
  C-27; C-29 names this spec the one exception to the unit-test "no
  sleeps" rule, not to bounded waits)
- R9. The suite MUST import `internal/provider/vastai` directly for the
  orphan scenario and invoke the built `cmd/vk-llm` binary directly for
  scenarios 2, 9, 11, 12, rather than add a module or reimplement either
  client. (source: C-25)
- R10. No key, token, address, or host name MUST appear in the committed
  report or in `docs/research.md` §7. (source: C-14)
- R11. FR-2.3, FR-7.8, NFR-9.4, NFR-11.1, NFR-11.2 and NFR-11.4 MUST each
  have a recorded measurement; FR-1.1's wire-format proof MUST be
  recorded when the target makes it applicable. (source:
  `docs/requirements.md` Spec column "140")
- R12. Scenario 7 MUST observe 0 controller calls for every rejected
  request; credentials MUST stay rejected on each other's surface for the
  whole run. (source: ADR 0012 §3, FR-7.2)

## Implementation steps

1. Add `test/e2e/` with the build tag, `TestMain`, env-var validation that
   exits before any network call if a required variable is missing, the
   `vk-llm` build step, the sweep helper, the management HTTP helpers
   (`mgmtStatus`, `mgmtStart`, `mgmtStop`, `mgmtCost`), a thin `vk-llm`
   subprocess wrapper, and the direct provider client construction from
   `VKLLM_E2E_VAST_API_KEY_FILE`. Check: running `make e2e` with one
   variable unset prints its name and exits non-zero, no HTTP, `gh`, or
   provider call made; a unit test outside the `e2e` tag decodes a fixture
   status document and extracts the label prefix.
2. Scenario 1 (preconditions) and the helm-override helper (`override`,
   `restore`, both `kubectl`/`helm` wrappers with their own timeouts).
   Check: `override` followed immediately by `restore` is a no-op diff on
   the release's values.
3. Scenarios 2–8 (one instance, in order). Check: each scenario's own
   assertions as in the table; a shared instance handle passes the
   generation captured by #2 to #7 and #8.
4. Scenario 9 (second start, manual stop) and scenario 10 (`gpu-control.yml`,
   restart, deadline passed). Check: the restart in #10 reads the same
   generation from status before and after `kubectl rollout restart`.
5. Scenarios 11 and 12 (stop during provisioning, start during deletion)
   and scenario 13 (orphan injection). Check: #12 asserts the destroy's
   confirmation timestamp precedes the next `CreateInstance` call's
   timestamp; the orphan's generation is verified distinct from the
   store's current one before injection.
6. Scenarios 14–16 (checksum, budget, no-offer) and scenario 17 (final
   sweep and report assembly). Check: the Markdown table has one row per
   scenario in the table above.
7. `e2e.yml`: `workflow_dispatch`, one Environment, `concurrency` group,
   `permissions: {}`, pinned action SHAs (ADR 0008 §2 pattern, spec 120),
   an `if: always()` sweep job. Check: `actionlint` clean; `grep -c
   "pull_request" .github/workflows/e2e.yml` is `0`.

## Tests / acceptance criteria

- AC1. Setup: a clean installation. Action: scenario 1. Expected: `vk-llm
  status` and `/v1/models` 200 with exactly the configured alias; RSS
  reading < 128 MiB recorded. (NFR-11.4, NFR-9.4)
- AC2. Action: scenario 2. Expected: states in order; cold start < 8 min;
  `secondsUntilIdleShutdown` within one reconcile interval of the
  configured timeout; the request body matches the SDK wire format.
  (FR-2.3, FR-2.5, FR-1.1)
- AC3. Action: scenario 3. Expected: leg 1 `tool_calls` with `get_weather`
  and a JSON `city` argument; leg 2 `stop`; `idleDeadline` moved after each
  leg and each of the 5 warm requests; 5 TTFT and 5 tokens/s samples
  recorded. (FR-3.2, FR-3.3, NFR-11.1, NFR-11.2)
- AC4. Action: scenario 4. Expected: `idleDeadline` identical across all
  100 reads. (FR-3.5)
- AC5. Action: scenario 5. Expected: 5/5 complete; RSS < 256 MiB recorded.
  (FR-3.9, NFR-11.4)
- AC6. Action: scenario 6. Expected: active-requests gauge at 0 within 2 s
  of disconnect; `RequestEnd` called exactly once. (FR-3.2)
- AC7. Action: scenario 7. Expected: 401/403 matrix as specified; 0
  controller calls for every rejected request. (NFR-8.1, FR-7.2, R12)
- AC8. Action: scenario 8. Expected: *(kind)* instance destroyed, prefix
  empty, status `STOPPED`; *(platform)* `DRAINING` then `STOPPED`.
  (FR-3.1, FR-3.6, FR-3.7)
- AC9. Action: scenario 9. Expected: `DRAINING` within one tick of
  `vk-llm stop`; `STOPPED` after the destroy confirms. (FR-3.6, FR-3.7, FR-7.5)
- AC10. Action: scenario 10. Expected: workflow dispatch succeeds; status
  walk observed; `idleDeadline` unmoved by polling; generation unchanged
  across the quick restart; *(kind)* `DRAINING` on the first tick after
  the deadline-passed restart. (FR-7.8, FR-7.9, FR-4.2, FR-3.10)
- AC11. Action: scenario 11. Expected: destroyed as soon as a provider id
  exists; no `READY` state observed for that generation. (FR-3.11)
- AC12. Action: scenario 12. Expected: `desired=RUNNING` recorded at once;
  the next `CreateInstance` call's timestamp is after the destroy's
  confirmation; generation +1. (FR-3.11, FR-2.8)
- AC13. Action: scenario 13. Expected: injected generation confirmed `<` current and absent before injection; destroyed within 120 s; `vkllm_orphans_destroyed_total` incremented by 1. (FR-6.6, C-6)
- AC14. *(kind only)* Action: scenario 14. Expected: onstart exit non-zero
  within the download time; instance destroyed; no instance left; attempt
  counted in `GET /api/v1/gpu/status`. (FR-5.5)
- AC15. *(kind only)* Action: scenario 15. Expected: `503`,
  `code: "gpu_unavailable"`, `reason: "budget_exhausted"`, no
  `Retry-After` header. (FR-6.3)
- AC16. *(kind only)* Action: scenario 16. Expected: first start `202`
  then `unavailable: "no_offer"` in status; second start within the
  backoff `503 gpu_unavailable` `reason: "no_offer"`. (FR-2.6)
- AC17. Action: scenario 17. Expected: prefix empty; balance delta ≤
  `VKLLM_E2E_BUDGET_USD`; every metric this suite reads is present under
  its CORE-090 catalog name; `helm get values` and the rendered ConfigMap
  show all five overridden values back at chart defaults. (FR-6.9)
- AC18. Setup: `TestMain` guard. Action: force a scenario to `t.Fatal`
  mid-run (test-only flag). Expected: the deferred sweep still runs and the
  provider list for the prefix is empty afterward. (R1)
- AC19. Setup: a full run completes. Action: `helm get values <release> -n
  <namespace> -a`, cross-checked against the rendered ConfigMap. Expected:
  the five R4 config paths all equal the chart's own defaults, not the
  overridden values; `GET /api/v1/gpu/status` does not expose them and is
  not used for this check. (R4)
- AC20. Action: run the suite twice in sequence against the same
  installation. Expected: both runs reach `state: STOPPED` with the prefix
  empty at the end, and no run leaves the installation worse off for the
  next. (R7)
- AC21. Action: sum the provider balance deltas recorded by scenarios 1 and
  17. Expected: the delta is ≤ `VKLLM_E2E_BUDGET_USD`. (R6, C-1)

## Definition of done

- [ ] AC1–AC21 observed on one real run against the target installation.
- [ ] The Markdown report is in this spec's Evidence and appended to
      `docs/research.md` §7.
- [ ] `docs/open-questions.md` rows this spec settles (if any) are updated.
- [ ] `e2e.yml` runs only on `workflow_dispatch`, verified by a dry
      inspection (R5); no PR ever triggered it.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE and the folder renamed in the same PR.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| FR-4.5 (worker dead-man exit) still names spec 140 as a prover in `docs/requirements.md`, but task-140's update-1 scenario list does not include a dead-man-exit scenario | this spec no longer exercises that path live; `docs/requirements.md`'s Spec column for FR-4.5 is now inaccurate unless re-added or corrected | a `docs/requirements.md` edit outside this spec's scope, or a future revision of this spec adding the scenario back | excluded here; stays proven by spec 030's spike and spec 070's simulation; flagged for the reviewer, not silently dropped |
| How are the five runtime config overrides (idle timeout, sha256, budget, max price, attempt budget) applied to the Argo-managed platform release without `helm upgrade` outside Argo being reverted or racing a sync? | scenarios 10 (deadline-passed half), 14–16 need them; architecture §11 says Argo owns the release | spec 130 (platform integration) | not solved here: those scenario halves run on a kind target only; the platform run exercises the override-free subset and records the rest as skipped |
| What credential does `gh workflow run` use inside this suite, and is it distinct from the repository's `VK_LLM_TOKEN` secret the workflow itself reads? | scenario 10 needs `actions: write` on the dispatching identity, which is not the management token | spec 160 / CI environment setup | assumed: the runner's own `GITHUB_TOKEN` (inside Actions) or an operator's `gh auth login` session (on a laptop run), configured outside this suite; not sourced from any Secret this spec mounts |
| FR-1.1's literal clause names the official Python and Node SDKs; this suite is pure Go | CI cannot shell out to a separate language runtime without adding one | a documented manual smoke step, run once per release, outside this suite | the Go client's requests are byte-identical to what the SDKs send for the two calls FR-1.1 names; recorded as a one-time manual check, not CI-gated |
| Does the provider `balance` field reflect scenarios 9–16's charges by the time scenario 17 reads it, or only after the ≥ 1 h delay spec 030 found? | AC21's timing | spec 030's own open question, inherited here | read balance at the end anyway; record a second read ≥ 1 h later as a non-gating follow-up line in the report |

## Evidence

None yet.
