# Open questions and risk register

Every unverified fact (tag U in [`research.md`](research.md)) and every
decision left open, each with the spec that settles it. A question closes
when that spec records the answer in its evidence section and this page is
updated.

## 1. Open questions

| # | Question | Why it matters | Settled by | Default until settled |
|---|---|---|---|---|
| Q1 | Does Vast's SSH launch mode inject a working SSH server into the official `server-cuda` image? | ADR 0006 bundle A depends on it | 030 | **closed** (research §7.2, §7.1): yes, via a derived-image build run by Vast's SSH launch mode; confirmed in both 2026-11-02 runs with no manual step |
| Q2 | Does `onstart` execute in Entrypoint (`args`) mode? | needed only for fallback B | 030 | **closed**: moot, bundle A accepted (research §7.2, §7.6); fallback B was never built |
| Q3 | Is the Vast SSH endpoint for our instances direct or proxied, and does the proxy throttle long-lived channels? | SSE latency and reliability through the tunnel | 030 (partly), 140 (direct mode) | **partly closed** (research §7.2 Q3): proxied, confirmed in both real runs; 0 channel drops both runs; bulk SSH-channel throughput measured low (1.6–2.2 MB/s) while interactive TTFT/tokens-per-s were close to on-host. **Still open:** requesting `ssh_direct` did not change the observed routing for that host in this one sample, so direct mode's effect on latency/throughput remains unconfirmed, not ruled out; spec 140 is the next place to try it |
| Q4 | What is the unit of `inet_down`, `inet_up`, `disk_bw` in the REST search API (MB/s vs Mbps)? | offer filter thresholds | 020 | treat as MB/s; CORE-020's probe found observed magnitudes consistent with MB/s but did not cross-check against the console UI for the same offer, so this stays open pending that independent confirmation |
| Q5 | Does a per-instance charges endpoint exist and does it cover destroyed instances over a date range? | real billing vs estimated ledger for the budget | 020 | **settled**: `GET /api/v0/charges` exists (HTTP 401, permission-gated on `billing_read`, not a 404); kept unused — ledger estimate + prepaid balance floor (spec 100) |
| Q6 | What is the exhaustive set of `actual_status` values? | state mapping in the provider client | 020, 030 | only literal `exited`/`offline`/`unknown` are terminal; anything else is `StatusOther`, non-terminal, bounded by the startup deadline. CORE-020's probe could not settle this: the probe account had zero running instances, so no live value was observed. CORE-030's spike (research §7.4 M10) observed only `running` across both real runs — `exited` was targeted but never reached despite two independent attempts per run, so this stays open, not settled by the spike either |
| Q7 | Does llama.cpp's tool-call parser detect Qwen3.6 (same architecture as Qwen3.5)? | FR-1.4 | 030 | **closed** (research §7.2): yes, 10/10 tool-call round trips passed across both real runs |
| Q8 | Real download throughput from Hugging Face on hosts passing the filters? | ADR 0005; cold-start budget | 030 | **closed** (research §7.1, §7.2): measured 66.9 MB/s and 99.7 MB/s (fetch-only) on two real hosts, both via aria2c; real host-to-host variance, not a fixed number |
| Q9 | Real VRAM use at 32K and 64K context, and whether `-np 2` fits with headroom? | future concurrency 2 | 030, 140 | **still open**: all restart attempts in both runs failed to become healthy (research §7.2, §7.4 M7); no `nvidia-smi` reading was taken; two unconfirmed root-cause candidates and new diagnostics are recorded in research §7.2 for spec 140 to retry with |
| Q10 | Decode tokens/s and warm time to first token on an L40S? | NFR-10.1, 10.2 | 030 | **closed** (research §7.2): measured ~142–163 tokens/s and 85–144 ms host TTFT (tunnel TTFT 0.9–1.1 s) on two real hosts |
| Q11 | Is the published `server-cuda` image built with SSL support? | only fallback B | 030 | **closed** (research §7.2): no, confirmed in both real runs; expected, irrelevant for the accepted bundle A |
| Q12 | Does the host image cache hold `server-cuda` often enough to keep image pull under 2 minutes? | cold start | 030, 140 | **1 of 2 samples under 2 min** (research §7.1, §7.2): 114.9 s (under) and 305.5 s (over) on two real hosts; not reliably under 2 minutes, and neither sample is a plain pull (both are a derived-image build); one sample pair, spec 140 adds more |
| Q13 | Availability of 1×L40S offers at ≤ $0.80/h over time? | FR-2.6 frequency | 140 (and runtime metric) | snapshot median ≈ $0.80; the cap sits at the median |
| Q14 | Envoy route timeout on the platform: the policy CRDs exist (research §5, Grafana precedent); the exact fields for a request timeout ≥ 1080 s or disabled, plus a 120 s stream idle timeout, on Envoy Gateway v1.2.1 are still to confirm | ADR 0007 §4 envelope | 130 PR-C | CRD confirmed; value open until PR-C |
| Q15 | Will the platform accept an app-scoped SSM prefix in the External Secrets IAM policy? | ADR 0009 Proposed | 130 | hand-created Secrets until then |
| Q16 | Does the platform's Hetzner cluster stay up permanently as the user intends, or does it get recreated? | state loss path in ADR 0004 §6 | user | assume permanent; the configured `installationId` makes recreation safe |
| Q18 | Which action makes a Vast SSH-mode container `exited`: the onstart script exiting, or `kill -TERM 1`? | dead-man timer (C-8) | 030 (measured), 140 (distinguish cause) | **measured, not fully closed** (research §7.4 M10): `actual_status` stayed `running` for 120 s after each action, in both real runs. The `kill -TERM 1` result was discarded and PID 1's identity under Vast's SSH launch mode was never confirmed, so whether the kill itself failed or Vast simply doesn't reflect in-container death in `actual_status` was not distinguished. Only the external `DELETE` call ended either instance. ADR 0003 §7's self-stop/billing assumption needs its own review as a result (flagged, not amended, in research §7.4); spec 140 should confirm what PID 1 is under Vast's SSH launch mode before retrying |
| Q19 | Does a freshly created instance appear in `ListInstances` immediately, and what is the maximum delay? | create idempotency by label (C-2) | 020 (design avoids needing the answer); 030 did not test it | spec 020 lists before every create and again after an unclear failure |
| Q20 | Vast label maximum length | label `vkllm:<installationId>:<generation>` | 030 (22-char spike label proven), 020 (production label length) | the spike's own label `vkllm:spike:<unix-ts>` (22 characters) round-tripped in both real runs (research §7); longer `installationId`-based production labels are untested — keep ids short to stay under Vast's undocumented limit |
| Q21 | `cpu_ram` GB→MB factor (1000 or 1024), the instance-list keyset cursor parameter name, whether `select_filters` supports a prefix operator on `label`, the `geolocation` value format, and what GET returns for a destroyed id | provider client request shapes | 020 step 1 | 1000; cursor from the first response; filter client-side; two-letter country; 404 = gone. The response field is confirmed `next_token` (fixed in commit 6ee38b4); the request-side query parameter name for page 2+ is still unverified — the probe's account had zero instances, so a second page was never requested |
| Q22 | Are `env` values readable back through the instance GET (which would expose the worker API key to anyone with the Vast key)? | worker key exposure | 030 (not checked), 140 (next live run) | not directly checked in either real run (spec 030 Evidence); assume readable — the Vast key already grants destroy, so no new trust boundary |
| Q23 | SSH user name on Vast instances, and SSH keepalive tolerance of the Vast SSH proxy | tunnel dialer | 030 (user, settled), 050/140 (keepalive tolerance) | user: **settled `root`**, both real runs; keepalive tolerance (15 s, 3 misses) is a configured default, not measured — 0 channel drops were observed over the ~40-minute real runs (research §7.3 G6), but the proxy's actual tolerance threshold was never tested |
| Q24 | Does `curl -sfIL` on the Hugging Face URL return the final `Content-Length` after redirects? | download progress and size check | 030 | **closed** (research §7.1): yes, both real runs logged a `Content-Length` of 25,913,185,920 matching the expected size exactly |
| Q25 | How quickly does `balance` reflect a running instance's charges? | spend measurements in 030/140 | 030 | **settled** (research §1.5, §7.5): the `.balance` field itself never reflects charges — it stays pinned at 0 on this account; real spend shows only in the undocumented `.credit` field, which moved both times it was checked (~32 min and 63 min after destroy). Speed within the minute was never isolated — only coarse multi-minute gaps were sampled. The shipped `internal/provider/vastai/client.go`'s `Balance()` had the same `.balance`-only bug in production, fixed in [PR #6](https://github.com/savak1990/vk-llm-on-demand/pull/6) (research §1.5) |
| Q26 | Does `server-cuda` start with `-ngl 0` on a host with no GPU driver, which tiny public GGUF to use, and does `/metrics` require the API key? | the no-GPU docker test in 070 | 070 | pick a < 100 MB GGUF; mark the test optional if the image refuses to start |
| Q27 | GHCR: does a new package default to private, and which cleanup policy removes untagged manifests? | anonymous `helm pull`; registry hygiene | 120 | set visibility public by hand once; native package cleanup policy |
| Q28 | Kubernetes API details the store relies on: `resourceVersion` on PUT, 409 on stale version, service-account token rotation and mount paths, the 1 MiB ConfigMap limit | state store client | 040 kind job (done); 140 (in-pod mount path, live 401) | **mostly closed** (research §8): `resourceVersion`/409 on stale PUT, keyless-ConfigMap-is-fresh-document and RBAC enforcement all confirmed live against a real kind cluster. **Still open:** token rotation on 401 (no 401 occurred in the run; still only unit-tested) and the real in-pod service-account mount path (the job mints a token file by hand rather than running inside a pod) — 140 is the first spec that runs the binary as a real pod against a live cluster; the 1 MiB ConfigMap limit was never approached, so it stays a documented, not live-tested, fact |
| Q29 | OpenAI error vocabulary (`type`/`code` values), and whether the official SDKs raise on an in-stream `data: {"error": …}` event | gateway error mapping | 060 AC22 (optional SDK job) | OpenAI-shaped bodies; SDK raises on the error event |
| Q30 | llama-server handling of `max_completion_tokens`, and the `usage` field in streamed chunks | token clamp and tokens/s metric | 060 (030 did not test this) | clamp both names; count chunks when `usage` is absent |
| Q31 | llama-server default log level (does it print prompts?), and which wins between `--host` and `LLAMA_ARG_HOST` | prompt leakage on the worker; loopback bind | 070 AC8 (log level, which-wins); 030 (unset only, not a precedence test) | 030 unset `LLAMA_ARG_HOST` and proved loopback-only (research §7.3 G3), which avoids needing to know which wins but does not test precedence directly; pass `--log-disable`-equivalent if needed; always pass `--host` explicitly and unset `LLAMA_ARG_HOST` |
| Q32 | Vast publishes no egress IP ranges; the NetworkPolicy egress for SSH must therefore allow any destination on the SSH port range | NetworkPolicy value | 080 | allow 0.0.0.0/0 on the SSH range, documented |
| Q33 | External Secrets Operator API group/version in use on the platform (`external-secrets.io/v1`?) | ExternalSecret templates | 130 | read from the platform's ESO chart version 2.9.0 |
| Q34 | Grafana sidecar label for dashboards on the platform (`grafana_dashboard: "1"`) and histogram bucket tuning | dashboard discovery; metrics | 090, 140 | platform convention; default buckets until measured |
| Q35 | Is `dph_total` stable for an instance's whole life (no mid-life repricing)? | ledger accuracy | 140 (030 recorded create-time price only, not re-checked mid-life) | assume stable; ledger stores the create-time price |
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

## 4. Known spec inconsistencies (review pass deferred, 2026-11-01)

The update for explicit provisioning was committed without a full
cross-spec review. These items are known and must be fixed before the
named spec moves to READY:

| Spec | Fix |
|---|---|
| 050 | `vkllm_gpu_state{state}` must use the eight public states of spec 090, not the internal stage names — **closed** (050 DONE, Design Decision 6): the shipped `PublicState()` is the metric label's sole source; spec 050's own text corrected to match |
| 050 / 100 | budget reasons must use ADR 0012's vocabulary (`budget_exhausted`, `balance_low`, `balance_unknown`); spec 100 still says `monthly_budget_exceeded`, `balance_floor` — **closed** (050 DONE, Design Decision 7): `internal/controller` defines its own `Budget`/`Worker` interfaces in 050's own vocabulary (`no_offer`/`budget_exhausted`/`balance_low`/`attempts_exhausted`) and maps whatever reason CORE-100's `Allow` returns onto it at that boundary (spec 050 §"Events, definitions and transition table", reason mapping for `Unavailable`); spec 100 does not need to adopt ADR 0012's literal strings |
| 050 | close the tunnel at `drainTimeout` and stay DRAINING until then (spec 060 relies on it) — **closed** (050 DONE): confirmed already correct in the merged Phase 1 `internal/tunnel` package plus the loop's own `processEvent` post-transition hook (T26) |
| 055 | add the `result` field and `nextCreateAt` per ADR 0012 §1; return 202 during a create backoff — **closed** (055 READY, §"Open questions"): both fields added; 202 during backoff already correct (see 050 Design Decision 7 on the distinct backoff paths) |
| ADR 0012 §1 | `balance_unknown` mentioned in ADR 0012 §1 is superseded: the shipped `internal/controller.Budget.Allow` folds an unknown balance into `balance_low`; the reason enum is `no_offer`, `budget_exhausted`, `balance_low`, `attempts_exhausted` (4 values, not 5) |
| 060 | describe when `vkllm_gateway_cancelled_streams_total` increments (spec 090 catalog) |
| 070 | readiness citations point at FR-3.1; readiness is now FR-2.7 |
| 080 / 110 | require at least one management token at install (spec 060 fails readiness without one) |
| 080 | spec 080's `config.*` table was rewritten against the real shipped `internal/config` env vars post-PR #15/#16 (43 `config.*` rows plus the 2 fixed keys, 45 total, not 39+2; two invented keys deleted — `config.lifecycle.safety.maxInstances`, `config.lifecycle.shutdown.policy`; 6 missing keys added — `config.provider.apiKeyFile`, `config.tunnel.sshKeyFile`, `config.auth.clientKeysFile`, `config.auth.managementTokensFile`, `config.auth.workerSeedFile`, `config.mgmt.rateLimitPerKeyRPM`; `VKLLM_BUDGET_MONTHLY_USD` corrected to `VKLLM_BUDGET_MONTH_USD`) — see spec 080's own amendment for the full diff |
| 150 / 160 | name the shared fake management server test package |
| 070 / 100 | `internal/controller` defines its own `Budget`/`Worker` interfaces (050's own vocabulary: `Allow` returns one of `no_offer`/`budget_exhausted`/`balance_low`/`attempts_exhausted`); CORE-070 and CORE-100 implement against them, not the other way round — so neither spec should redesign this contract when it lands |
| 100 | `config.Budget.MonthlyUSD` (env `VKLLM_BUDGET_MONTH_USD`) and `config.Mgmt.BudgetMonthUSD` (env `VKLLM_MGMT_BUDGET_MONTH_USD`) read independent env vars for the same dollar figure; settled by whoever wires `cmd/gpu-on-demand/main.go`, who must assign the former into the latter rather than letting both read their own env var; default behavior until then: both default to 30, so they agree unless one is overridden without the other — **closed** (commit 92aeaf8): mgmtCfg.BudgetMonthUSD assigned from budgetCfg.MonthlyUSD in cmd/gpu-on-demand/main.go |
| all | one full cross-spec review pass, as in `.superpowers` ledgers, before implementation of the affected spec |
