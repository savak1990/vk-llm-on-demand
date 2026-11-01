# Requirements

Functional (FR) and non-functional (NFR) requirements with measurable
acceptance criteria (AC). Each AC names the spec that proves it. Tags:

- **V** the requirement rests on a verified fact in [`research.md`](research.md)
- **A** the requirement rests on a stated assumption
- **O** an open question in [`open-questions.md`](open-questions.md) affects it

Priorities follow [`roadmap.md`](roadmap.md): P0 and P1 form the initial
release; P2 and P3 are future.

## 1. OpenAI-compatible inference API (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-1.1 | `POST /v1/chat/completions` accepts the OpenAI request schema and returns the OpenAI response schema, streaming and non-streaming | The official Python and Node SDKs complete a chat call and a streamed chat call with only `base_url` and `api_key` set | 060, 140 | V |
| FR-1.2 | `GET /v1/models` lists exactly the configured alias, answers without a GPU, never triggers provisioning and never touches the idle deadline | p99 < 50 ms with the GPU STOPPED; `lastInferenceFinishedAt` unchanged after 100 calls | 060 | — |
| FR-1.3 | Unknown `model` returns an OpenAI-shaped 404 | SDK raises `NotFoundError` | 060 | — |
| FR-1.4 | `reasoning_content` and tool calls pass through unchanged | A tool-call round trip with the model succeeds through the gateway | 030, 060 | O (tool parser) |
| FR-1.5 | Errors use `{"error": {"message","type","code"}}` | Every non-2xx body validates against that shape | 060 | — |
| FR-1.6 | In explicit mode an inference request with no READY backend returns `503` `backend_unavailable` without `Retry-After` and without starting anything | Fake provider sees zero `CreateInstance`; body matches ADR 0010 §2 | 060 | — |
| FR-1.7 | The gateway stays available while the GPU is STOPPED | `/v1/models` 200 and `/healthz` 200 with no instance | 060, 080 | — |

## 2. Explicit lifecycle and provisioning (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-2.1 | `POST /api/v1/gpu/start` provisions exactly one instance and is idempotent | 50 concurrent starts → one `CreateInstance`; repeats return 200/202 with the current state | 050, 055 | — |
| FR-2.2 | Offers are filtered by price, GPU, CPU, RAM, disk, reliability, bandwidth, CUDA version and optional region, ordered by price | Table test with recorded offers; nothing above `maxHourlyPriceUSD` is chosen | 020 | V |
| FR-2.3 | Cold start to READY on a passing host is under 8 minutes | Measured in the spike and the acceptance run, per stage | 030, 140 | A |
| FR-2.4 | Each cold-start stage is timed and exported | `vkllm_coldstart_stage_seconds` has all eight stages after one cold start | 050, 090 | — |
| FR-2.5 | Start returns promptly; readiness is observed by polling status | start p99 < 200 ms; status reflects each public state in order | 055, 150 | — |
| FR-2.6 | When no offer qualifies the state returns to STOPPED with `unavailable: no_offer` in status; a start while that reason is fresh returns `503 gpu_unavailable` with `reason: no_offer` | Metric `vkllm_provision_attempts_total{outcome="no_offer"}` increments; second start within the backoff → 503 | 050, 055 | — |
| FR-2.7 | A successful provider create does not imply readiness | State is STARTING or LOADING_MODEL until health 200 and alias listed | 050 | V |
| FR-2.8 | Start during DRAINING records `desired=RUNNING` and provisions a new generation after the destroy confirms | Simulation: one destroy, then one create, generation +1 | 050 | — |

## 3. Rolling inactivity timeout and manual stop (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-3.1 | A GPU that receives no inference is destroyed 30 minutes after readiness | Fake clock: destroy at `readyAt + 30m` ± one reconcile interval | 050 | — |
| FR-3.2 | Every completed, failed, cancelled or disconnected inference request sets the deadline to `now + 30m` | After `RequestEnd` at t, destroy at t+30m, not before | 050, 060 | — |
| FR-3.3 | Active inference prevents idle shutdown | A request spanning the deadline is not interrupted; destroy happens 30 m after it ends | 050 | — |
| FR-3.4 | Repeated start on a running GPU does not extend the deadline | `idleDeadline` identical before and after 10 start calls | 050, 055 | — |
| FR-3.5 | Status, cost, models, health, readiness, metrics, reconcile and Actions polling never touch the deadline | `idleDeadline` unchanged after 100 of each | 050, 055, 060, 160 | — |
| FR-3.6 | Manual stop begins shutdown immediately | State DRAINING within one reconcile tick of the call; no 30-minute wait | 050, 055 | — |
| FR-3.7 | Manual stop drains in-flight requests for at most `drainTimeout` (60 s), then destroys | Simulation with a 90 s request: in-stream error at 60 s, destroy at 60 s | 050, 060 | — |
| FR-3.8 | Admission and shutdown are atomic: a request admitted before DRAINING is drained; one after gets `backend_unavailable` | Race test with the fake clock at the exact deadline | 050, 060 | — |
| FR-3.9 | Concurrent requests cannot cause premature termination | 10 overlapping requests; destroy only after the last `RequestEnd` + 30 m | 050 | — |
| FR-3.10 | Timeout enforcement survives a controller restart, including a deadline that passed while down | Restart with persisted deadline in the past → DRAINING on first tick | 050 | — |
| FR-3.11 | Stop during provisioning destroys as soon as an id exists; start during deletion queues a new generation | Simulations | 050 | — |
| FR-3.12 | `idleShutdown.enabled: false` is refused unless `maxLifetime` is set | Chart render fails; config validation fails | 080, 010 | — |

## 4. Shutdown, termination, recovery (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-4.1 | Pod shutdown drains in-flight requests up to 30 s and never destroys the instance | SIGTERM during a stream: the stream completes or gets an in-stream error; the fake provider sees no destroy | 060 | — |
| FR-4.2 | After a restart a READY instance is re-adopted, not destroyed | Restart test with fake provider and fake store | 050 | — |
| FR-4.3 | After a store loss the controller destroys any instance with its own label prefix and starts clean | Empty store + one labelled fake instance → destroy | 040, 050 | — |
| FR-4.4 | Destroy is retried until the provider confirms absence | Fake provider failing destroy 3 times → 4 calls, then STOPPED | 050 | — |
| FR-4.5 | The worker exits on its own after `worker.deadManTimeout` without requests | Spike: instance reaches `exited` with the control plane stopped | 030, 070 | V |
| FR-4.6 | Missing or stale instance metadata is reconciled from the provider, never trusted blindly | Simulation with a document naming an id the provider lacks → STOPPED after two observations | 050 | — |

## 5. Provider and infrastructure failures (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-5.1 | Provider 429 is retried with jittered backoff | Recorded 429 fixture; next call after ≥ 1 s | 020 | V |
| FR-5.2 | A failed `ListInstances` never triggers a destroy | Simulation with list errors | 050 | — |
| FR-5.3 | An instance observed `exited`, `offline` or `unknown` is destroyed and the attempt counted | Simulation | 050 | V |
| FR-5.4 | Startup timeout destroys the instance, excludes the machine for 1 h, backs off | Simulation with fake clock | 050 | — |
| FR-5.5 | A model checksum mismatch fails the worker fast | Spike: altered sha256 → onstart exits non-zero | 070 | — |
| FR-5.6 | A worker crash mid-stream yields an in-stream error and a destroyed instance | Simulation with fake upstream and fake provider | 050, 060 | — |
| FR-5.7 | A lost create response cannot produce a second instance | List-by-label before and after every create; at most one instance per label, ever | 020 | — |

## 6. Cost safety (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-6.1 | No offer above `maxHourlyPriceUSD` (0.80) is ever created | Unit test; acceptance run log shows chosen price | 020, 100 | — |
| FR-6.2 | At most `maxInstances` (1) instances with our label exist | Reconcile destroys extras; simulation | 050 | — |
| FR-6.3 | Month-to-date estimated spend ≥ `budget.monthlyUSD` (30) stops new provisioning and drains the running instance | Simulation with ledger | 100 | — |
| FR-6.4 | At `budget.warnFraction` a metric and an alert fire | `vkllm_budget_used_ratio` ≥ 0.8 triggers rule | 090, 100 | — |
| FR-6.5 | At most `maxProvisionAttemptsPerHour` (3) creates per hour; then STOPPED + alert | Simulation | 050, 100 | — |
| FR-6.6 | Orphans (our label, unknown to store) are destroyed within 2 reconcile intervals | Fake provider injection | 050, 100 | — |
| FR-6.7 | Instances older than `maxLifetime` (12 h) are drained and destroyed regardless of activity | Simulation | 050 | — |
| FR-6.8 | Estimated cost per instance and month is visible in `GET /api/v1/gpu/cost`, `vk-llm status` and metrics, labelled as estimates with documented limits | Fields present; cost-model section in spec 100 | 055, 100, 150 | V |
| FR-6.9 | No in-memory timer is the only thing standing between a rented GPU and its destruction | Every shutdown trigger is also evaluated from persisted state on the reconcile tick; proven by the restart tests | 050 | — |

## 7. Management API, CLI and GitHub Actions (P0 API, P1 clients)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-7.1 | `/api/v1/gpu/{start,status,stop,cost}` exist with the contract of ADR 0012 §1 | Contract tests for each code and body | 055 | — |
| FR-7.2 | Management and inference credentials are separate; each is rejected on the other surface | 403 matrix test | 055, 060, 110 | — |
| FR-7.3 | `vk-llm start` waits for readiness by default, shows state changes, supports `--detach` and `--timeout`, exits non-zero on failure | CLI tests against a fake management server | 150 | — |
| FR-7.4 | `vk-llm status` prints provider, state, GPU, model, readiness, price, uptime, estimated cost, last inference, seconds until shutdown | Golden output test | 150 | — |
| FR-7.5 | `vk-llm stop` requests immediate shutdown, waits for STOPPED by default, supports `--detach`, exits non-zero on failure | CLI tests | 150 | — |
| FR-7.6 | The CLI never holds provider credentials; configuration precedence is flags > env > file; the file never holds a token | Code review item; config test | 150 | — |
| FR-7.7 | Binaries for darwin/linux × amd64/arm64 are published on GitHub Releases with checksums | Release dry run | 120, 170 | — |
| FR-7.8 | `gpu-control.yml` performs start/status/stop through the management API with the same lifecycle behaviour as the CLI | actionlint; a dispatched `status` run against the fake server in CI; acceptance run for start | 160, 140 | — |
| FR-7.9 | Workflow polling never extends the deadline; concurrent dispatches do not conflict | Concurrency group test; FR-3.5 | 160 | — |

## 8. Security (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| NFR-8.1 | Every `/v1` request needs a valid inference key; every `/api/v1` request needs a valid management token | 401 without, 403 with the wrong kind | 055, 060, 110 | — |
| NFR-8.2 | Keys are compared in constant time and never logged | Log capture test | 110 | — |
| NFR-8.3 | The worker's inference port is never reachable from the internet | Spike: external scan shows only SSH | 030, 070 | O |
| NFR-8.4 | The controller dials only provider-returned addresses for instances it created; the gateway holds no address | Crafted store document with a foreign address is rejected | 050 | — |
| NFR-8.5 | Body limit 1 MiB; `max_tokens` cap; per-key rate limit on both surfaces | 413 / clamp / 429 tests | 055, 060 | — |
| NFR-8.6 | No secret, domain, token or `fqdn` in Git, chart values, examples, fixtures or CI logs | gitleaks on PR and history; CI grep; render with placeholders | 170, 080 | — |
| NFR-8.7 | Prompts and completions are never logged or persisted | Log capture test | 060, 110 | — |
| NFR-8.8 | Actions SHA-pinned, `permissions: {}`, secrets never echoed, untrusted input never interpolated | actionlint; review checklist | 160, 170 | V |
| NFR-8.9 | Dependency and image vulnerability scanning run on every PR; a release passes the `SECURITY.md` checklist | govulncheck and trivy jobs green; checklist in release notes | 170 | — |
| NFR-8.10 | Non-root, read-only, capability-less container; least RBAC | Chart tests | 080 | — |

## 9. Deployment and operations (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| NFR-9.1 | `helm install` and `helm upgrade` succeed on kind with the fake provider, the fake worker and three Secrets | CI job | 080 | — |
| NFR-9.2 | The chart renders without any ingress controller, and optionally an HTTPRoute or an Ingress | kubeconform on all variants | 080 | — |
| NFR-9.3 | Image and chart publish from `main` as prereleases and from the release button as `X.Y.Z`; the CLI release is cut in the same run | Dry runs | 120 | V |
| NFR-9.4 | The platform deployment serves `https://llm.<fqdn>/v1/models` and `/api/v1/gpu/status` with the platform certificate | Acceptance run | 130, 140 | — |
| NFR-9.5 | Pod resources: cpu request, memory request and limit, no cpu limit; one replica | Chart test | 080 | — |

## 10. Observability (P0)

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| NFR-10.1 | Gateway metrics: requests, active, failures, latency, time to first token, tokens per second, cancelled streams | Catalog present and scraped | 090 | — |
| NFR-10.2 | Lifecycle metrics: provisioning attempts and failures, stage durations, uptime, idle time, idle deadline, last inference, automatic vs manual shutdown counts, destroy failures, orphans, reconcile failures | Catalog present | 090 | — |
| NFR-10.3 | Inference metrics from llama-server re-exported | Pulled over the tunnel every 15 s | 090 | V |
| NFR-10.4 | Cost metrics: hourly price, uptime, estimated instance and month spend, budget ratio, provider balance, labelled as estimates | Catalog present | 090, 100 | — |
| NFR-10.5 | Structured JSON logs with request id; no high-cardinality labels | Label audit | 090 | — |
| NFR-10.6 | One Grafana dashboard and four alerts (orphan destroyed, provisioning failing, budget 80 %, destroy stuck) | Rendered when `serviceMonitor.enabled` | 090 | — |

## 11. Performance targets (initial, to be measured)

| Id | Target | Where measured | Tag |
|---|---|---|---|
| NFR-11.1 | Warm time to first token < 1.5 s for a 1K-token prompt | 030, 140 | A |
| NFR-11.2 | Decode ≥ 40 tokens/s at concurrency 1 on an L40S | 030, 140 | A |
| NFR-11.3 | Gateway overhead < 5 ms p99 per request excluding upstream | 060 | — |
| NFR-11.4 | Control plane pod under 128 MiB RSS at idle, under 256 MiB under load | 080, 140 | A |

## 12. End-to-end acceptance (the brief's §18, proven by spec 140)

1. Install the chart; configure provider, management and inference credentials.
2. `vk-llm status` shows STOPPED.
3. `vk-llm start` provisions one L40S; status walks ALLOCATING → STARTING → LOADING_MODEL → READY; `secondsUntilIdleShutdown` ≈ 1800 at READY.
4. OpenCode streams a completion and a tool call; the deadline moves to `now + 30m` after each.
5. Status, cost, models, health and metrics calls leave the deadline unchanged.
6. With no further inference, the instance is destroyed at the deadline (shortened timeout in the run); the provider list is empty.
7. A second `vk-llm start`, then `vk-llm stop` before the deadline: DRAINING within one tick, STOPPED after the destroy confirms.
8. `gpu-control.yml` start shows the same behaviour.
9. Controller restart mid-lifecycle: the instance is re-adopted and the deadline is enforced from persisted timestamps.
10. gitleaks over the repository and its history finds nothing; CI workflows echo no secret.

## 13. Explicitly out of scope for the initial release

On-demand provisioning (ADR 0007), persistent-volume cache and preferred
host (ADR 0014, P2), `vk-llm cache *`, multiple providers, multiple
models, dynamic model selection, vLLM, multi-GPU, more than one worker,
agent orchestration, RAG, warm pools, CRDs and an operator, GPU nodes
joining the cluster, OAuth or per-user quotas, a web dashboard,
OpenHands and Open WebUI integrations. See [`roadmap.md`](roadmap.md).
