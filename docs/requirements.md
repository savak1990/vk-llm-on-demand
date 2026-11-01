# Requirements

Functional (FR) and non-functional (NFR) requirements with measurable
acceptance criteria (AC). Each AC names the spec that proves it. Tags:

- **V** the requirement rests on a verified fact in [`research.md`](research.md)
- **A** the requirement rests on a stated assumption
- **O** an open question in [`open-questions.md`](open-questions.md) affects it

## 1. OpenAI-compatible API

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-1.1 | `POST /v1/chat/completions` accepts the OpenAI request schema and returns the OpenAI response schema, streaming and non-streaming | The official Python and Node SDKs complete a chat call and a streamed chat call with no client-side changes other than `base_url` and `api_key` | 060, 140 | V |
| FR-1.2 | `GET /v1/models` lists exactly the configured alias and answers without a worker | p99 < 50 ms with the GPU Idle | 060 | — |
| FR-1.3 | Unknown `model` returns an OpenAI-shaped 404 error body | SDK raises `NotFoundError` | 060 | — |
| FR-1.4 | `reasoning_content` and tool calls pass through unchanged | A tool-call round trip with the model succeeds through the gateway | 030, 060 | O (tool parser) |
| FR-1.5 | Errors use `{"error": {"message","type","code"}}` | Every non-2xx body validates against that shape in tests | 060 | — |

## 2. Provisioning and scale-from-zero

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-2.1 | A completion request with the GPU Idle starts exactly one instance | Fake provider sees one `CreateInstance` for 50 concurrent first requests | 050 | — |
| FR-2.2 | Offers are filtered by price, GPU, CPU, RAM, disk, reliability, bandwidth, CUDA version and optional region, and ordered by price | Unit tests with recorded offers select the expected one; offers above `gpu.maxHourlyPriceUSD` are never chosen | 020 | V |
| FR-2.3 | Cold start to first token on a passing host is under 8 minutes | Measured in the spike and the acceptance run; recorded per stage | 030, 140 | A (host speed) |
| FR-2.4 | Each cold-start stage is timed and exported | `vkllm_coldstart_stage_seconds` has all eight stages after one cold start | 050, 090 | — |
| FR-2.5 | A second provisioning cannot start while one is in progress or an instance exists | Simulation with two racing demands yields one instance | 050 | — |
| FR-2.6 | Provisioning stops when no offer qualifies and reports it | `503 gpu_unavailable`; metric `vkllm_provision_attempts_total{outcome="no_offer"}` | 050, 060 | — |

## 3. Readiness, streaming, cancellation, concurrency

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-3.1 | The worker is Ready only after `/health` 200 and `/v1/models` lists the alias | Simulation: a 503 from `/health` keeps the state Loading | 050, 070 | V |
| FR-3.2 | Streaming responses are forwarded chunk by chunk with no buffering | Time between upstream chunk and client chunk < 50 ms in a loopback test | 060 | — |
| FR-3.3 | While waiting for a cold start a streaming client receives an SSE comment at least every 15 s | Test observes heartbeats; the Python SDK stream does not time out at 60 s | 060 | V |
| FR-3.4 | Client disconnect cancels the upstream request within 1 s | Fake upstream observes context cancellation | 060 | V |
| FR-3.5 | Requests beyond the worker's slots queue in the gateway up to `gateway.maxPending`; beyond that `429` with `Retry-After` | Load test with 40 concurrent requests: 32 queued, 8 rejected | 060 | — |
| FR-3.6 | Non-streaming requests that outlive `gateway.maxWait` get `503` + `Retry-After: 30` | Test with a never-ready fake worker | 060 | — |

## 4. Shutdown, termination, recovery

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-4.1 | Graceful shutdown drains in-flight requests up to 30 s and never destroys the instance | SIGTERM during a stream: the stream completes or gets an in-stream error; the fake provider sees no destroy | 060 | — |
| FR-4.2 | The instance is destroyed after `gpu.idleTimeout` with no requests, leases or in-flight work | Fake clock test: destroy at 20:00 ± reconcile interval | 050 | — |
| FR-4.3 | A workflow lease blocks idle destruction until it expires or is released | Lease of 30 min → no destroy at 20 min, destroy at 50 min | 050, 060 | — |
| FR-4.4 | After a process restart a Ready instance is re-adopted, not destroyed | Restart test with fake provider and fake store | 050 | — |
| FR-4.5 | After a store loss the controller destroys any instance with our label prefix and starts clean | Test: empty store + one labelled fake instance → destroy | 040, 050 | — |
| FR-4.6 | Destroy is retried until the provider confirms absence | Fake provider failing destroy 3 times → 4 calls, then Idle | 050 | — |
| FR-4.7 | The worker exits on its own after `worker.deadManTimeout` without requests | Spike/acceptance: instance reaches `exited` with the control plane stopped | 030, 070, 140 | V (exited stops billing) |

## 5. Provider and infrastructure failures

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-5.1 | Provider 429 is retried with jittered backoff and no `Retry-After` dependency | Recorded 429 fixture; next call after ≥ 1 s | 020 | V |
| FR-5.2 | A failed `ListInstances` never triggers a destroy | Simulation with list errors | 050 | — |
| FR-5.3 | An instance observed `exited`, `offline` or `unknown` is destroyed and the attempt counted | Simulation | 050 | V |
| FR-5.4 | Startup deadline expiry destroys the instance, excludes the machine for 1 h, backs off | Simulation with fake clock | 050 | — |
| FR-5.5 | A model checksum mismatch fails the worker fast | Spike: altered sha256 → onstart exits non-zero within the download time | 070 | — |
| FR-5.6 | A worker crash mid-stream yields an in-stream error and a destroyed instance | Simulation with fake upstream and fake provider | 050, 060 | — |

## 6. Cost safety

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| FR-6.1 | No offer above `gpu.maxHourlyPriceUSD` (0.80) is ever created | Unit test; acceptance run log shows chosen `dph` | 020 | — |
| FR-6.2 | At most `gpu.maxInstances` (1) instances with our label exist at any time | Reconcile destroys extras; simulation | 050 | — |
| FR-6.3 | Month-to-date estimated spend ≥ `budget.monthlyUSD` (30) stops new provisioning and drains the running instance | Simulation with ledger | 100 | — |
| FR-6.4 | At `budget.warnFraction` a metric and an alert fire | `vkllm_budget_used_ratio` ≥ 0.8 triggers rule | 090, 100 | — |
| FR-6.5 | At most `gpu.maxProvisionAttemptsPerHour` (3) creates per hour; then Idle + alert | Simulation | 050, 100 | — |
| FR-6.6 | Orphans (our label, unknown to store) are destroyed within 2 reconcile intervals | Fake provider injection | 050, 100 | — |
| FR-6.7 | Instances older than `gpu.maxLifetime` (6 h) are drained and destroyed even with leases | Simulation | 050 | — |
| FR-6.8 | Estimated cost per instance and month is visible in `/admin/v1/status` and metrics, with documented estimation limits | Fields present; `cost-model` section in spec 100 | 100 | V (billing semantics) |

## 7. Security

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| NFR-7.1 | Every `/v1` and `/admin/v1` request needs a valid bearer key; admin endpoints need the admin key | 401 without key, 403 with a client key on admin | 060, 110 | — |
| NFR-7.2 | Keys are compared in constant time and never logged | Code review item; log capture test shows no key | 110 | — |
| NFR-7.3 | The worker's inference port is never reachable from the internet | Spike: external scan of the instance shows only SSH | 030, 070 | O (launch mode) |
| NFR-7.4 | The controller dials only provider-returned addresses for instances it created; the gateway holds no address | Unit test: a crafted store document with a foreign address is rejected | 050 | — |
| NFR-7.5 | Request body limit 1 MiB; `max_tokens` capped; per-key rate limit | 413 / clamped value / 429 tests | 060 | — |
| NFR-7.6 | No secret, domain or `fqdn` in Git; chart values hold references only | CI grep; kubeconform render with placeholders | 080, 120 | — |
| NFR-7.7 | Prompts and completions are never logged or persisted | Log capture test | 060, 110 | — |

## 8. Deployment and operations

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| NFR-8.1 | `helm install` and `helm upgrade` succeed on kind with the fake provider and three Secrets | CI job | 080 | — |
| NFR-8.2 | The chart renders without any ingress controller, and optionally an HTTPRoute or an Ingress | kubeconform on all three variants | 080 | — |
| NFR-8.3 | Image and chart publish from `main` as prereleases and from the release button as `X.Y.Z` | A dry run of each workflow | 120 | V (ahorro) |
| NFR-8.4 | The platform deployment reaches `https://llm.<fqdn>/v1/models` with the platform certificate | Acceptance run | 130, 140 | — |
| NFR-8.5 | Pod resources: cpu request, memory request and limit, no cpu limit; one replica | Chart test | 080 | — |

## 9. Observability

| Id | Requirement | Acceptance criterion | Spec | Tag |
|---|---|---|---|---|
| NFR-9.1 | Gateway metrics: requests, active, pending, failures, latency, time to first token, tokens per second | Catalog present and scraped | 090 | — |
| NFR-9.2 | Lifecycle metrics: provisioning attempts and failures, stage durations, uptime, idle time, destroys, orphans, estimated cost | Catalog present | 090 | — |
| NFR-9.3 | Inference metrics from llama-server re-exported: prompt and generated tokens, rates, processing and deferred requests | Pulled over the tunnel every 15 s | 090 | V |
| NFR-9.4 | Structured JSON logs with request id; no high-cardinality labels | Log sample; label audit | 090 | — |
| NFR-9.5 | One Grafana dashboard and four alerts (orphan destroyed, provisioning failing, budget 80 %, destroy stuck) | Rendered by the chart when `serviceMonitor.enabled` | 090 | — |

## 10. Performance targets (initial, to be measured)

| Id | Target | Where measured | Tag |
|---|---|---|---|
| NFR-10.1 | Warm time to first token < 1.5 s for a 1K-token prompt | 030, 140 | A |
| NFR-10.2 | Decode ≥ 40 tokens/s at concurrency 1 on an L40S (3B active parameters, Q5_K_M) | 030, 140 | A |
| NFR-10.3 | Gateway overhead < 5 ms p99 per request excluding upstream | 060 | — |
| NFR-10.4 | Control plane pod under 128 MiB RSS at idle, under 256 MiB under load | 080, 140 | A |

## 11. Explicitly out of scope for the MVP

Multiple providers, multiple models, dynamic model selection, vLLM, multi-GPU,
more than one worker, agent orchestration, RAG, batching schedulers, warm
pools, CRDs and an operator, GPU nodes joining the cluster, OAuth or
per-user quotas, a web dashboard. See [`overview.md`](overview.md) §4.
