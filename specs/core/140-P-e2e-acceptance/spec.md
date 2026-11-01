---
id: "CORE-140"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-070", "CORE-080", "CORE-100"]
adrs: ["0001", "0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009"]
---

# 140 — End-to-end acceptance run

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** L
**Risk:** high — real GPU spend and real destroys against the single live
installation ([testing-strategy.md](../../../docs/testing-strategy.md)
tier 3); a bug here can both overspend and leave a GPU running
**Estimated effort:** one session to write (4–6 h); every run costs ≤
`VKLLM_E2E_BUDGET_USD` (default $10) and ≤ 60 min wall clock
**Recommended model:** strong — live results are judged against fifteen
ordered, stateful scenarios and the FR/NFR gates they prove
**Depends on:** [CORE-070](../070-P-worker-runtime/spec.md) (worker
contract), CORE-080 (chart), CORE-100 (budget and cost ledger)

## Outcome

Running `make e2e` (or the `e2e` GitHub Action) against a real installation
— the platform's one installation, or a temporary kind install on a laptop
with its own `installationId` (testing-strategy.md tier 3) — proves, with a
real Vast.ai GPU, every row of `docs/requirements.md` whose Spec column
names 140: cold start, warm performance, tool calls, long context,
cancellation, concurrency, restart recovery, idle destroy, orphan cleanup,
the worker's own dead-man exit, checksum failure, budget exhaustion and the
admin kill switch. When the run ends, no instance carries the
installation's label prefix, total spend is within the cap, and a Markdown
measurement table lands in this spec's Evidence and in `docs/research.md`
§7 as a new subsection after spec 030's spike results.

## Scope

- `test/e2e/`: one Go package behind build tag `e2e`, one `TestMain` with
  the safety guard, one test function per scenario group below.
- `make e2e`: runs `go test -tags e2e -timeout 60m ./test/e2e/...`, refusing
  to start if any required environment variable is unset.
- `.github/workflows/e2e.yml`: `workflow_dispatch` only, one GitHub
  Environment holding the keys, `concurrency: {group: e2e,
  cancel-in-progress: false}` so two runs never overlap
  (testing-strategy.md "Two tier 3 runs never overlap").
- The Markdown report appended to this spec's Evidence and to
  `docs/research.md` §7.

Excludes: the fake-provider, fake-clock unit and simulation suites of
specs 020–110 (tier 1, testing-strategy.md); the kind-with-fake-worker
chart tests (tier 2, spec 080); the Vast offer census and launch-mode
decision (spec 030's own job, already closed); a live worker-crash
mid-stream kill (FR-5.6) — the production `Dialer` offers an
`http.RoundTripper` only, never an exec channel (spec 070 "Open
questions"), and adding one for this spec alone would be new machinery for
a single test; FR-5.6 stays proven by the spec 050/060 simulations until an
ADR adds worker exec access. Official Python/Node SDK conformance (the
literal clause in FR-1.1) stays a manual, documented smoke step — this
suite is pure Go over the wire format spec 060 defines, which is the
CI-automatable proxy for it.

## Design and contracts

### Inputs

| Variable | Meaning | Source if not set |
|---|---|---|
| `VKLLM_E2E_BASE_URL` | `https://llm.<fqdn>/v1` or a kind port-forward base | required, no default |
| `VKLLM_E2E_CLIENT_KEY` | bearer key for `/v1/*` | required |
| `VKLLM_E2E_ADMIN_KEY` | bearer key for `/admin/v1/*` (ADR 0007 §3) | required |
| `VKLLM_E2E_BUDGET_USD` | hard spend cap for the run | default `10` |
| `VKLLM_E2E_VAST_API_KEY_FILE` | path to the Vast.ai API key, read on stdin like spec 030's `VAST_API_KEY_FILE`; used only by the orphan-injection scenario's direct `CreateInstance`/`DestroyInstance` calls through `internal/provider/vastai` (same module, importable) | required |
| `VKLLM_E2E_NAMESPACE`, `VKLLM_E2E_RELEASE` | the Helm release under test, for the config overrides and `kubectl rollout restart` below | required |
| `KUBECONFIG` | standard kubectl context for that cluster | required |

`VKLLM_E2E_NAMESPACE`, `VKLLM_E2E_RELEASE`, `KUBECONFIG` and
`VKLLM_E2E_VAST_API_KEY_FILE` are this spec's addition beyond the base
four, needed for the config overrides and the direct-provider scenario
(Open questions).

### Target and identity

Per testing-strategy.md, this suite never runs against a `-dev` or `-pr`
copy: it targets the one real platform installation, or a temporary laptop
kind install. `installationId` is the chart value ADR 0004 §6 defines,
default `sha256("<namespace>/<release>")[:12]`. That default is a hash of
the platform's own fixed namespace and release (`llm`/`gpu-on-demand`), so
a laptop install that leaves it unset would compute the platform's own
prefix if it ever used the same namespace and release. A laptop install
therefore MUST set `installationId` explicitly, for example `--set
installationId=e2e-<user>`. Before any scenario runs, `TestMain` computes
the platform's default (`sha256("llm/gpu-on-demand")[:12]`) and fails if
the prefix read from `/admin/v1/status` equals it — the laptop run's own
guard against the collision ADR 0004 §6 otherwise prevents only by
convention. `TestMain` opens by calling `GET /admin/v1/status` with the
admin key and reads `installationId`, `generation` and `labelPrefix` from
it (ruling 7 fixes these field names; binding: the orphan-injection
scenario and the exit guard use exactly this prefix, never one assembled
from a `kubectl`-read ConfigMap).

Platform caveat: on the platform installation the release is Argo-managed
(architecture §11); a `helm upgrade --set` made outside Argo either is not
the live release or is reverted on the next sync, and a `kubectl scale
--replicas=0` can self-heal back before scenario 11's wait elapses. The
config-override scenarios (9, 11, 12, 13, 14) therefore run only against a
kind target in this version of the spec; against the platform the suite
runs the override-free subset (1–8, 10, 15) and records the rest as
skipped. Tracked under Open questions, settled by spec 130.

### Safety: the exit guard

```go
func TestMain(m *testing.M) {
    ctx, cancel := context.WithTimeout(context.Background(), 55*time.Minute)
    defer cancel()
    // ... run m.Run() under ctx ...
    sweep(prefix) // always, even on panic or t.Fatal, via defer
}
```

- The internal 55-minute context is the real cap (C-1); `go test -timeout
  60m` is only the outer backstop, because Go's own `-timeout` kills the
  process without running deferred cleanup.
- `sweep(prefix)` lists every provider instance under the label prefix and
  destroys every one found, retried until `GetInstance` confirms absence
  (C-4), then fails the test run if the list was non-empty at entry.
- The `e2e.yml` workflow adds a separate `if: always()` job that re-runs
  the same sweep against the same prefix in case the runner itself was
  killed before `TestMain`'s defer ran.
- Every config override made for the run (idle timeout, drain timeout,
  dead-man timeout, model sha256, budget, attempt budget — the six of R4)
  is restored by a deferred `helm upgrade` back to the chart defaults
  (C-7: the defaults are chart values, never hard-coded), so a crashed run
  cannot poison the next one.

### Scenarios

One ordered run through the single installation. Group A reuses one
instance (scenarios 2–8) to avoid repeated cold starts; group B provisions
separately per scenario because each needs a different config override.
Admin calls use `/admin/v1/{warmup,status,leases,scale}` (ADR 0007 §3).
`controller.reconcileInterval` is left at its 60 s default throughout; only
the five values named in R4 are overridden, one at a time, and restored
before the next scenario provisions. `gpu.maxProvisionAttemptsPerHour` (3,
FR-6.5) is raised to 6, kind only, for the kind-only run (scenarios 9,
11–14 below), since scenarios 2, 11, 12 and 14 each create once, which
would otherwise exhaust the default budget on the fourth create and fail
#14 with `gpu_unavailable` for the wrong reason; this override is restored
in #15 along with the other five. The platform run (scenarios 1–8, 10, 15)
creates only once (#2) and never sets this override.

`/metrics` on `:9090` is never routed by Envoy (architecture §6); every
scenario that reads it does so over a `kubectl port-forward` to the
control-plane pod, torn down after the scenario.

| # | Scenario | Action | Pass criterion | Max duration |
|---|---|---|---|---|
| 1 | Preconditions | `GET /v1/models` 200; `GET /admin/v1/status` 200, state `Idle`, label prefix and `generation` captured; provider `Balance` read; `kubectl top pod` RSS reading | models list has exactly the configured alias; status `Idle`; prefix non-empty; RSS < 128 MiB recorded (NFR-10.4, **A**); if `VKLLM_E2E_BASE_URL` is `https://llm.<fqdn>/v1`, the TLS handshake succeeds with no client override (NFR-8.4) | 30 s |
| 2 | Scale-from-zero | first streamed chat completion, sent with the same JSON body the OpenAI Python/Node SDKs send for a streamed chat call | SSE heartbeats `: warming up <stage>` every ≤ 15 s (FR-3.3); 8 `vkllm_coldstart_stage_seconds` stages recorded (FR-2.4); cold start total < 8 min (FR-2.3); the request/response bodies match the OpenAI schema byte-for-byte on the fields the SDKs set (FR-1.1 wire-format proof, SDK conformance itself tracked under Open questions) | 8 min |
| 3 | Warm completion | 5 streamed 1K-token requests, `enable_thinking: false` | median TTFT and median tokens/s recorded (NFR-10.1, NFR-10.2 — **A**, recorded not gated) | 1 min |
| 4 | Tool-call round trip | one `get_weather(city)` two-leg exchange | leg 1 `finish_reason: "tool_calls"`; leg 2 `finish_reason: "stop"` (FR-1.4) | 30 s |
| 5 | Long-context prompt | if `model.contextSize ≥ 65536`, a 58K–64K token prompt, else a prompt sized to `contextSize − 2048` | 200; `usage.prompt_tokens` in the expected band | 2 min |
| 6 | Cancellation | start a long stream, close the client connection | `vkllm_gateway_active_requests` returns to 0 within 2 s (FR-3.4); worker's `vkllm_inference_requests_processing` (spec 070), pulled every 15 s (NFR-9.3), is observed at 0 on the next pull | 30 s |
| 7 | Concurrency | 5 parallel chat requests; `kubectl top pod` RSS reading mid-load | all 5 complete 200/`[DONE]`; `vkllm_gateway_pending_requests` > 0 observed during the window (FR-3.5); no 429 (5 < `gateway.maxPending` 32); RSS < 256 MiB recorded (NFR-10.4, **A**) | 2 min |
| 8 | Gateway restart mid-stream | start a stream; `kubectl rollout restart deployment/<release>` | the in-flight stream completes or ends with one in-stream error within the 30 s drain (FR-4.1); `/admin/v1/status` shows the same `generation` before and after (FR-4.4); a new request after the restart succeeds without a new `CreateInstance` call | 2 min |
| 9 | Idle destroy *(kind only)* | `helm upgrade --set config.gpu.idleTimeout=3m,config.gpu.drainTimeout=10s`; stop traffic; wait `idleTimeout + reconcileInterval` | instance destroyed; provider list for the prefix empty; status `Idle` (FR-4.2) | 5 min |
| 10 | Orphan injection | read `generation` from `/admin/v1/status`; direct `CreateInstance` with label `<prefix>:<generation-1>` (or `0` if `generation` is 1), a plain `image`/`runtype ssh`/`disk` request with no `env` and no `onstart` (nothing to expose, C-17), bypassing the gateway | the generation used is confirmed absent from the store and lower than the current one before injection (ADR 0003 §3: only then is it an orphan, not a protected newer instance); destroyed within 2 × `controller.reconcileInterval` (120 s); `vkllm_orphans_destroyed_total` incremented (FR-6.6, C-6) | 3 min |
| 11 | Dead-man exit *(kind only)* | `helm upgrade --set config.worker.deadManTimeout=90s`; trigger demand; as soon as `/admin/v1/status` reports a provider instance id (Creating/Booting, before Ready — the dead-man clock starts at onstart's own script start, spec 070), `kubectl scale deployment/<release> --replicas=0`; poll the provider every 15 s until the instance is `exited` or 15 min elapses; `--replicas=1` | provider reports the instance `exited` before the control plane returns, satisfying spec 070 AC13's literal wording even though the worker exits during download rather than after Ready (ADR 0003 §7, FR-4.7); reconcile destroys it on return | 15 min |
| 12 | Checksum failure *(kind only)* | `helm upgrade --set config.model.sha256=<64 zeros>`; trigger demand | onstart exits non-zero within the download time; instance destroyed; attempt counted; the failing `machine_id` excluded from the next search for 1 h (ADR 0003 §5, observed as absent from the next offer list if exposed); no instance left (FR-5.5) | 6 min |
| 13 | Budget exhaustion *(kind only)* | `helm upgrade --set config.budget.monthlyUSD=<current ledger spend>`; trigger demand | `503` with `code: "gpu_unavailable"`, no `Retry-After` (FR-6.3, ADR 0007 §2) | 1 min |
| 14 | Kill switch *(kind only)* | restore sha256/budget via `helm upgrade`; trigger demand, wait Ready; `POST /admin/v1/scale {"desired": 0}` | state moves Draining → Destroying → Idle; no instance left; ≥ 1 min backoff from #12's failed attempt is accounted for in this scenario's budget, not a new failure | 10 min |
| 15 | Final | restore all six overrides (R4) to chart defaults, release the port-forward | no instance under the prefix exists; provider balance delta ≤ `VKLLM_E2E_BUDGET_USD`; every metric this suite reads is present under the names fixed in the CORE-090 catalog; `helm get values` and the rendered ConfigMap show every overridden value back at its chart default | 2 min |

Scenarios marked *(kind only)* run only when the target is a laptop kind
install; against the platform installation they are skipped and recorded
as such (Open questions, Target and identity above).

Rough budget on kind, typical case: two full cold starts (#2, #14 ≈ 8 and
10 min), one partial one that stops at Creating/Booting and exits within
`deadManTimeout + a few polls` (#11, typically ≈ 2–3 min, capped at 15 min)
and one that fails at checksum (#12 ≈ 6 min) ≈ 8+10+3+6 = 27 min, plus the
smaller scenarios (≈ 15 min total) ≈ 42 min, inside the 55-minute internal
cap with margin for backoff and retries. If #11 runs to its full 15-minute
cap (an unusually slow host or a reconcile delay), the total rises to
≈ 54 min, leaving almost no margin; `TestMain`'s 55-minute context (Safety
section) is the hard stop for that case, not this estimate. On the
platform (scenarios 1–8, 10, 15 only) the run is one cold start plus the
smaller scenarios, well under 20 min. Spend: at most three GPU-seconds
windows at ≤ `gpu.maxHourlyPriceUSD` (0.80) for a combined well under an
hour of billed time, plus up to three 26 GB downloads' bandwidth (cents,
ADR 0005 Consequences) — within the $10 default cap.

### Report

The suite writes one JSON line per scenario outcome and timing to stdout
and collects them into a Markdown table: columns `scenario`, `FR/NFR id`,
`measured value`, `pass/fail or recorded`, `timestamp`. This table is
pasted into this spec's Evidence and appended as a new subsection of
`docs/research.md` §7 (after spec 030's spike results, same section, a
second run of the same measurements under real traffic rather than a
synthetic spike).

### Security

- No key, address or host name is written to the report or committed
  anywhere (C-14), matching spec 030's rule.
- `VKLLM_E2E_VAST_API_KEY_FILE` is read once into the provider client's
  `APIKey` closure (ADR 0009 §4 pattern) and never logged (C-16).
- The suite never mounts or reads the production `existingSecrets.*`
  Secrets directly; it only calls the already-authenticated `/v1` and
  `/admin/v1` endpoints and the provider API with its own key file.

## Requirements

- R1. `TestMain` MUST install the exit guard before any scenario runs and
  MUST destroy every instance under the installation's label prefix on
  exit, retried until the provider confirms absence. (source: C-1, C-4)
- R2. The suite MUST read the installation's label prefix and current
  `generation` from `GET /admin/v1/status`, never assemble either from a
  separate `kubectl`-read state document. (source: testing-strategy.md
  "Rules"; ADR 0003 §3)
- R3. The orphan-injection scenario MUST create its instance with the
  installation's own label prefix and a generation confirmed lower than
  the store's current `generation` and absent from it, never a value that
  could be mistaken for a newer, protected instance. (source: ADR 0003 §3,
  Risks)
- R4. Every config value this suite overrides (`gpu.idleTimeout`,
  `gpu.drainTimeout`, `worker.deadManTimeout`, `model.sha256`,
  `budget.monthlyUSD`, `gpu.maxProvisionAttemptsPerHour`) MUST be restored
  to the chart's own defaults before the suite exits, success or failure.
  (source: C-7)
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
- R9. The suite MUST import `internal/provider/vastai` directly rather than
  add a second module or a CLI wrapper. (source: C-25)
- R10. No key, address, or host name MUST appear in the committed report or
  in `docs/research.md` §7. (source: C-14)
- R11. FR-2.3, FR-2.4, FR-4.7, FR-5.5, NFR-10.1 and NFR-10.2 MUST each have
  a recorded measurement in the report; FR-1.1's wire-format proof and
  NFR-8.4's TLS check MUST be recorded when the run target makes them
  applicable, gated or recorded as the scenario table marks them. (source:
  `docs/requirements.md` Spec column "140")

## Implementation steps

1. Add `test/e2e/` with the build tag, `TestMain`, env-var validation that
   exits before any network call if a required variable is missing, and
   the sweep helper. Check: running `make e2e` with one variable unset
   prints its name and exits non-zero, no HTTP or provider call made.
2. Add the admin/provider HTTP helpers (`adminStatus`, `warmup`, `scale`,
   `leases`) and the direct provider client construction from
   `VKLLM_E2E_VAST_API_KEY_FILE`. Check: a unit test outside the `e2e` tag
   decodes a fixture `/admin/v1/status` body and extracts the label prefix.
3. Scenario 1 (preconditions) and the helm-override helper (`override`,
   `restore`, both `kubectl`/`helm` wrappers with their own timeouts).
   Check: `override` followed immediately by `restore` is a no-op diff on
   the release's values.
4. Scenarios 2–8 (one instance, in order). Check: each scenario's own
   assertions as in the table; a shared instance handle is passed between
   them so #8's restart check reads the generation captured by #2.
5. Scenario 9 (idle destroy) and scenario 10 (orphan injection). Check: the
   orphan's generation is verified distinct from the store's current one
   before injection.
6. Scenario 11 (dead-man) and scenario 12 (checksum failure). Check: each
   restores its own override before the next scenario starts.
7. Scenarios 13–14 (budget, kill switch) and scenario 15 (final sweep and
   report assembly). Check: the Markdown table has one row per scenario in
   the table above.
8. `e2e.yml`: `workflow_dispatch`, one Environment, `concurrency` group,
   `permissions: {}`, pinned action SHAs (ADR 0008 §2 pattern, spec 120),
   an `if: always()` sweep job. Check: `actionlint` clean; `grep -c
   "pull_request" .github/workflows/e2e.yml` is `0`.

## Tests / acceptance criteria

- AC1. Setup: a clean installation. Action: scenario 1. Expected: `/v1/models`
  200 with exactly the configured alias; `/admin/v1/status` 200, `Idle`,
  prefix and `generation` captured; RSS reading < 128 MiB recorded.
  (FR-1.2 implicit, NFR-10.4, NFR-8.4)
- AC2. Action: scenario 2. Expected: heartbeats every ≤ 15 s; 8 stage
  timestamps; cold start < 8 min; the request body matches the SDK wire
  format. (FR-2.3, FR-2.4, FR-3.3, FR-1.1)
- AC3. Action: scenario 3. Expected: 5 TTFT and 5 tokens/s samples recorded.
  (NFR-10.1, NFR-10.2)
- AC4. Action: scenario 4. Expected: leg 1 `tool_calls` with `get_weather`
  and a JSON `city` argument; leg 2 `stop`. (FR-1.4)
- AC5. Action: scenario 5. Expected: 200 and `usage.prompt_tokens` in the
  expected band for the active `model.contextSize`. (Q9 follow-on)
- AC6. Action: scenario 6. Expected: `vkllm_gateway_active_requests` at 0
  within 2 s of disconnect; the next 15 s pull of
  `vkllm_inference_requests_processing` is 0. (FR-3.4)
- AC7. Action: scenario 7. Expected: 5/5 complete; pending queue > 0
  observed; 0 responses with status 429; RSS < 256 MiB recorded. (FR-3.5,
  NFR-10.4)
- AC8. Action: scenario 8. Expected: in-flight stream resolves within 30 s
  of `SIGTERM`; `generation` unchanged; the next request proxies without a
  new `CreateInstance` call. (FR-4.1, FR-4.4)
- AC9. *(kind only)* Action: scenario 9. Expected: instance destroyed;
  provider list for the prefix empty; status `Idle`. (FR-4.2)
- AC10. Action: scenario 10. Expected: the injected generation is confirmed
  `< current` and absent from the store before injection; destroyed within
  120 s; `vkllm_orphans_destroyed_total` incremented by 1. (FR-6.6, C-6)
- AC11. *(kind only)* Action: scenario 11. Expected: the instance reaches
  `exited` before `--replicas=1`; reconcile destroys it within one
  `reconcileInterval` of the control plane's return. (FR-4.7)
- AC12. *(kind only)* Action: scenario 12. Expected: onstart exit non-zero
  within the download time; instance destroyed; no instance left; attempt
  counted in `/admin/v1/status`. (FR-5.5)
- AC13. *(kind only)* Action: scenario 13. Expected: `503`,
  `code: "gpu_unavailable"`, no `Retry-After` header. (FR-6.3)
- AC14. *(kind only)* Action: scenario 14. Expected: state sequence
  Draining → Destroying → Idle observed via `/admin/v1/status` polling; no
  instance left afterward. (ADR 0007 §3 `/admin/v1/scale`)
- AC15. Action: scenario 15. Expected: prefix empty; balance delta ≤
  `VKLLM_E2E_BUDGET_USD`; every metric this suite reads is present under
  its CORE-090 catalog name; `helm get values` and the rendered ConfigMap
  show all six overridden values back at chart defaults.
- AC16. Setup: `TestMain` guard. Action: force a scenario to `t.Fatal` mid-
  run (test-only flag). Expected: the deferred sweep still runs and the
  provider list for the prefix is empty afterward. (R1)
- AC17. Setup: a full run completes. Action: `helm get values <release> -n
  <namespace> -a`, cross-checked against the rendered ConfigMap (`helm get
  manifest` or `kubectl get configmap`). Expected: `config.gpu.idleTimeout`,
  `config.gpu.drainTimeout`, `config.worker.deadManTimeout`,
  `config.model.sha256`, `config.budget.monthlyUSD`,
  `config.gpu.maxProvisionAttemptsPerHour` all equal the chart's own
  defaults, not the overridden values; `/admin/v1/status` does not expose
  these values and is not used for this check. (R4)
- AC18. Action: run the suite twice in sequence against the same
  installation. Expected: both runs reach `state: Idle` with the prefix
  empty at the end, and no run leaves the installation worse off for the
  next. (R7)
- AC19. Action: sum the provider balance deltas recorded by scenarios 1 and
  15. Expected: the delta is ≤ `VKLLM_E2E_BUDGET_USD`. (R6, C-1)

## Definition of done

- [ ] AC1–AC19 observed on one real run against the target installation.
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
| How are the five runtime config overrides (idle, drain, dead-man, sha256, budget, attempt-budget) applied to the Argo-managed platform release without `helm upgrade` outside Argo being reverted or racing a sync? | scenarios 9, 11–14 need them; architecture §11 says Argo owns the release | spec 130 (platform integration) | not solved here: those five scenarios run on a kind target only; the platform run exercises scenarios 1–8, 10, 15 and records the rest as skipped |
| FR-1.1's literal clause names the official Python and Node SDKs; this suite is pure Go | CI cannot shell out to a separate language runtime without adding one | a documented manual smoke step, run once per release, outside this suite | the Go client's requests are byte-identical to what the SDKs send for the two calls FR-1.1 names; recorded as a one-time manual check, not CI-gated |
| Does the provider `balance` field reflect scenario 9–14's charges by the time scenario 15 reads it, or only after the ≥ 1 h delay spec 030 found? | AC19's timing | spec 030's own open question, inherited here | read balance at the end anyway; record a second read ≥ 1 h later as a non-gating follow-up line in the report |
| `VKLLM_E2E_NAMESPACE`, `VKLLM_E2E_RELEASE` and `KUBECONFIG` are needed beyond the base four inputs | every `helm upgrade`/`kubectl` step in scenarios 8, 9, 11–14 depends on them | this spec | documented in Inputs; no narrower alternative exists without removing those scenarios |

## Evidence

None yet.
