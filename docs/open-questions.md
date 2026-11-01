# Open questions and risk register

Every unverified fact (tag U in [`research.md`](research.md)) and every
decision left open, each with the spec that settles it. A question closes
when that spec records the answer in its evidence section and this page is
updated.

## 1. Open questions

| # | Question | Why it matters | Settled by | Default until settled |
|---|---|---|---|---|
| Q1 | Does Vast's SSH launch mode inject a working SSH server into the official `server-cuda` image? | ADR 0006 bundle A depends on it | 030 | assume yes; fallback B pre-approved |
| Q2 | Does `onstart` execute in Entrypoint (`args`) mode? | needed only for fallback B | 030 | unknown |
| Q3 | Is the Vast SSH endpoint for our instances direct or proxied, and does the proxy throttle long-lived channels? | SSE latency and reliability through the tunnel | 030 | measure |
| Q4 | What is the unit of `inet_down`, `inet_up`, `disk_bw` in the REST search API (MB/s vs Mbps)? | offer filter thresholds | 020 | treat as MB/s, verify against the console for one offer |
| Q5 | Does a per-instance charges endpoint exist and does it cover destroyed instances over a date range? | real billing vs estimated ledger for the budget | 020 | ledger estimate + prepaid balance floor (spec 100) |
| Q6 | What is the exhaustive set of `actual_status` values? | state mapping in the provider client | 020, 030 | only literal `exited`/`offline`/`unknown` are terminal; anything else is `StatusOther`, non-terminal, bounded by the startup deadline |
| Q7 | Does llama.cpp's tool-call parser detect Qwen3.6 (same architecture as Qwen3.5)? | FR-1.4 | 030 | assume yes |
| Q8 | Real download throughput from Hugging Face on hosts passing the filters? | ADR 0005; cold-start budget | 030 | assume 200–800 MB/s |
| Q9 | Real VRAM use at 32K and 64K context, and whether `-np 2` fits with headroom? | future concurrency 2 | 030, 140 | estimate 26–28 GiB at 64K |
| Q10 | Decode tokens/s and warm time to first token on an L40S? | NFR-10.1, 10.2 | 030 | targets are assumptions |
| Q11 | Is the published `server-cuda` image built with SSL support? | only fallback B | 030 | irrelevant for A |
| Q12 | Does the host image cache hold `server-cuda` often enough to keep image pull under 2 minutes? | cold start | 030, 140 | unknown; measure pull stage |
| Q13 | Availability of 1×L40S offers at ≤ $0.80/h over time? | FR-2.6 frequency | 140 (and runtime metric) | snapshot median ≈ $0.80; the cap sits at the median |
| Q14 | Envoy route timeout on the platform: the policy CRDs exist (research §5, Grafana precedent); the exact fields for a request timeout ≥ 1080 s or disabled, plus a 120 s stream idle timeout, on Envoy Gateway v1.2.1 are still to confirm | ADR 0007 §4 envelope | 130 PR-C | CRD confirmed; value open until PR-C |
| Q15 | Will the platform accept an app-scoped SSM prefix in the External Secrets IAM policy? | ADR 0009 Proposed | 130 | hand-created Secrets until then |
| Q16 | Does the platform's Hetzner cluster stay up permanently as the user intends, or does it get recreated? | state loss path in ADR 0004 §6 | user | assume permanent; the configured `installationId` makes recreation safe |
| Q18 | Which action makes a Vast SSH-mode container `exited`: the onstart script exiting, or `kill -TERM 1`? | dead-man timer (C-8) | 030 | spike measures both |
| Q19 | Does a freshly created instance appear in `ListInstances` immediately, and what is the maximum delay? | create idempotency by label (C-2) | 030 | spec 020 lists before every create and again after an unclear failure |
| Q20 | Vast label maximum length | label `vkllm:<installationId>:<generation>` | 030 | short ids keep it under 40 characters |
| Q21 | `cpu_ram` GB→MB factor (1000 or 1024), the instance-list keyset cursor parameter name, whether `select_filters` supports a prefix operator on `label`, the `geolocation` value format, and what GET returns for a destroyed id | provider client request shapes | 020 step 1 | 1000; cursor from the first response; filter client-side; two-letter country; 404 = gone |
| Q22 | Are `env` values readable back through the instance GET (which would expose the worker API key to anyone with the Vast key)? | worker key exposure | 030 | assume readable; the Vast key already grants destroy, so no new trust boundary |
| Q23 | SSH user name on Vast instances, and SSH keepalive tolerance of the Vast SSH proxy | tunnel dialer | 030 | `root`; keepalive 15 s, 3 misses |
| Q24 | Does `curl -sfIL` on the Hugging Face URL return the final `Content-Length` after redirects? | download progress and size check | 030 | fall back to the sha256 check alone |
| Q25 | How quickly does `balance` reflect a running instance's charges? | spend measurements in 030/140 | 030 | assume within one minute (research §1.5: "about once every few seconds") |
| Q26 | Does `server-cuda` start with `-ngl 0` on a host with no GPU driver, which tiny public GGUF to use, and does `/metrics` require the API key? | the no-GPU docker test in 070 | 070 | pick a < 100 MB GGUF; mark the test optional if the image refuses to start |
| Q27 | GHCR: does a new package default to private, and which cleanup policy removes untagged manifests? | anonymous `helm pull`; registry hygiene | 120 | set visibility public by hand once; native package cleanup policy |
| Q28 | Kubernetes API details the store relies on: `resourceVersion` on PUT, 409 on stale version, service-account token rotation and mount paths, the 1 MiB ConfigMap limit | state store client | 040 kind job | standard behaviour; verified by the two-writer kind test |
| Q29 | OpenAI error vocabulary (`type`/`code` values), and whether the official SDKs raise on an in-stream `data: {"error": …}` event | gateway error mapping | 060 AC22 (optional SDK job) | OpenAI-shaped bodies; SDK raises on the error event |
| Q30 | llama-server handling of `max_completion_tokens`, and the `usage` field in streamed chunks | token clamp and tokens/s metric | 030, 060 | clamp both names; count chunks when `usage` is absent |
| Q31 | llama-server default log level (does it print prompts?), and which wins between `--host` and `LLAMA_ARG_HOST` | prompt leakage on the worker; loopback bind | 070 AC8, 030 | pass `--log-disable`-equivalent if needed; always pass `--host` explicitly |
| Q32 | Vast publishes no egress IP ranges; the NetworkPolicy egress for SSH must therefore allow any destination on the SSH port range | NetworkPolicy value | 080 | allow 0.0.0.0/0 on the SSH range, documented |
| Q33 | External Secrets Operator API group/version in use on the platform (`external-secrets.io/v1`?) | ExternalSecret templates | 130 | read from the platform's ESO chart version 2.9.0 |
| Q34 | Grafana sidecar label for dashboards on the platform (`grafana_dashboard: "1"`) and histogram bucket tuning | dashboard discovery; metrics | 090, 140 | platform convention; default buckets until measured |
| Q35 | Is `dph_total` stable for an instance's whole life (no mid-life repricing)? | ledger accuracy | 030, 140 | assume stable; ledger stores the create-time price |
| Q36 | GoReleaser action and binary versions, and whether GitHub Releases on a personal repository need extra permissions beyond `contents: write` | CLI release (ADR 0013) | 120 | `contents: write` on the tag job only |
| Q37 | Does `gitleaks` need a license for history scans on a personal account when run as a binary? | secret scanning | 170 | the binary is free for personal use; verify at implementation |
| Q38 | Exact `curl`/`jq` availability on `ubuntu-latest` runners and the HTTP behaviour when the management endpoint is unreachable | `gpu-control.yml` | 160 | both present; fail the job on non-2xx |
| Q39 | Does OpenCode retry on a 503 without `Retry-After`, and how does it surface the `backend_unavailable` message to the user? | explicit-mode UX | 140 | documented in the OpenCode section of the README after one manual run |
| Q40 | Vast `volume_info` attach semantics and whether a volume can be attached to an instance created from a different offer on the same machine | P2 cache (ADR 0014) | 180 | not needed for the initial release |
| Q17 | Is an ARM build of the control-plane image needed at all? | pipeline cost | settled by 120: multi-arch kept | — |

## 2. Risk register

| # | Risk | Likelihood | Impact | Mitigation | Owner |
|---|---|---|---|---|---|
| R1 | Vast SSH mode does not work with the official image | medium | ADR 0006 flips to B: custom image + TLS proxy, +1 week | spike first, B pre-approved | 030 |
| R2 | Hugging Face throttles 26 GB downloads | low–medium | cold start > 8 min | S3 mirror fallback, stage metric names the cause | 070 |
| R3 | No L40S offer under the price cap | medium | requests fail with `gpu_unavailable` | configurable cap, alert, documented | 100 |
| R4 | A control-plane bug destroys a live instance | low | lost request, one extra cold start | generation fencing, two-observation rule, simulation tests | 050 |
| R5 | Control plane down while GPU runs | low | billing until someone notices | worker dead-man timer, `maxLifetime`, provider balance floor | 070, 100 |
| R6 | Leaked client key | low | budget spent by a stranger | budget caps, rate limit, rotation runbook | 110 |
| R7 | llama.cpp regression for this architecture in a newer build | medium | worker fails readiness | image digest pinned; bump only via acceptance run | 070, 140 |
| R8 | Envoy or an intermediary drops SSE comments or times out early | low | cold starts fail for streaming clients | timeout policy in 130; warm-up endpoint documented | 130 |
| R9 | Vast API rate limits during reconcile + provisioning | medium | slower state transitions | single list per interval, backoff with jitter | 020 |
| R10 | Qwen3.6 tool calls malformed through llama.cpp | medium | coding assistants degrade | spike verifies; `--reasoning-format` and template flags adjustable | 030 |
| R11 | A client streams without end and holds the GPU | low | spend up to `maxLifetime` | `model.generationTimeout` ends any single request; `maxLifetime` ends the instance; idle rule counts only finished requests | 050, 060 |
| R12 | A leaked management token starts GPUs from anywhere | low | budget drained | per-list rotation, rate limit on `/api/v1`, budget caps, gitleaks on every PR | 110, 170 |
| R13 | The public GitHub Actions workflow is dispatched by someone with repository write access by mistake | low | one cold start, idles out in 30 min | `workflow_dispatch` only; concurrency group; the idle rule applies regardless of who started it | 160 |

## 3. Decisions deliberately deferred

| Topic | Deferred to |
|---|---|
| On-demand provisioning mode | after the initial release; ADR 0007 is the design |
| `stop --force` | after the initial release; `lifecycle.shutdown.allowForce` reserved |
| Leader election / `replicas > 1` | when availability requires it; `Store` interface is the seam |
| Postgres for history and reporting | when multi-month reporting is wanted (ADR 0004 alternatives) |
| Hashed API keys | after MVP; format already supports it |
| `-np 2` concurrency | after the measurement in Q9 |
| 64K context default | after the measurement in Q9 |
| Second provider (Runpod, Civo) | after the first user beyond the author |
