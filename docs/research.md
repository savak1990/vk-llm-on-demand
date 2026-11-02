# Research: verified external facts

Evidence base for the ADRs and specs. Every statement carries a confidence tag:

- **V** — verified against an official or primary source on the date given.
- **S** — secondary source (blog, third-party docs, our own API snapshot).
- **U** — unverified. Listed in [`open-questions.md`](open-questions.md) with the spec that must settle it.

Date of research: 2026-11-01. Re-verify V items older than six months before relying on them in code.

## 1. Vast.ai

### 1.1 API basics

| Fact | Tag | Source |
|---|---|---|
| Base URL `https://console.vast.ai/api/v0`, header `Authorization: Bearer <api key>` | V | https://docs.vast.ai/api-reference/introduction |
| OpenAPI 3.1 spec published | V | https://docs.vast.ai/api-reference/openapi.yaml |
| Official SDK/CLI is Python (`vastai`, `vastai_sdk`); no Go SDK in the `vast-ai` GitHub organisation | V | https://github.com/vast-ai/vast-cli |
| Rate limits are per endpoint and per identity, expressed as a minimum interval; a hit returns HTTP 429 with the text `API requests too frequent endpoint threshold=N`; **no `Retry-After` header** | V | https://docs.vast.ai/api-reference/rate-limits-and-errors |
| Threshold units in the 429 message are not documented | U | — |

### 1.2 Offer search

| Fact | Tag | Source |
|---|---|---|
| `POST /api/v0/bundles` with a JSON body of `{field: {operator: value}}`; operators `eq neq gt lt gte lte in notin` | V | https://docs.vast.ai/api-reference/search/search-offers |
| Filter fields include `gpu_name, num_gpus, gpu_ram, cpu_cores, cpu_cores_effective, cpu_ram, disk_space, disk_bw, dph_total, reliability, inet_down, inet_up, inet_down_cost, inet_up_cost, geolocation, rentable, verified, rented, direct_port_count, static_ip, cuda_max_good, driver_version, compute_cap, datacenter, external, vms_enabled, storage_cost, duration, machine_id, order, limit` | V | OpenAPI spec |
| `verified` and `rentable` default to `{eq: true}`; `type` is `ondemand` (default), `bid` (interruptible) or `reserved` | V | OpenAPI spec |
| Units: `gpu_ram` and `cpu_ram` in MB in the REST API (GB in the CLI); `disk_space` GB; `disk_bw` MB/s; `inet_*` MB/s; `storage_cost` $/GB/month | V (REST units stated in spec) / U for exact `inet_*` unit | OpenAPI spec; CORE-020 verifies by comparing a live offer against the console |
| `reliability2` appears in responses ("alternative reliability score") but is not documented as a request filter; `reliability` is the documented filter. New machines start at 60 % | V | https://docs.vast.ai/guides/concepts |
| Unauthenticated search returned offers in our test | S | observed 2026-11-01 |
| Live snapshot: 7 verified, rentable 1×L40S on-demand offers at $0.54–$0.92/h, median ≈ $0.80; `reliability2` 0.955–0.999; `cuda_max_good` 12.8–13.4; all 7 had `static_ip=false` | S | our API snapshot, 2026-11-01 |
| CORE-020 read-only probe, real L40S offers: `inet_down` ~272–4626, `inet_up` ~269–4347, `disk_bw` ~528–20992, `cpu_ram` ~64123–257985. These magnitudes are consistent with the existing MB/s (`inet_*`, `disk_bw`) and MB (`cpu_ram`) assumption, but the probe did not fetch the Vast console UI for the same offer, so this is not an independent console cross-check | S | our API probe, 2026-11-02 |

### 1.3 Create instance

| Fact | Tag | Source |
|---|---|---|
| `PUT /api/v0/asks/{offer_id}` returns `{"success": true, "new_contract": <instance id>}`. The spec's prose says POST, the path definition and the CLI use PUT | V | https://docs.vast.ai/api-reference/instances/create-instance ; vast-cli `vast.py` |
| Body fields: `image` (required), `template_hash_id`, `label`, `disk` (GB, fixed after creation), `runtype` (`ssh`, `jupyter`, `args`, `ssh_proxy`, `ssh_direct`, `jupyter_proxy`, `jupyter_direct`), `target_state` (`running`/`stopped`), `price` (bid only), `env`, `onstart` (≤ 4048 characters; docs suggest gzip+base64 for longer), `args`/`args_str`, `image_login` (private registry), `volume_info`, `cancel_unavail`, `vm`, `python_utf8`, `lang_utf8` | V | same |
| The CLI sends `env` as a JSON object, for example `{"-p 8080:8080": "1", "KEY": "val"}`; the docs type it as a string of Docker flags | V | vast-cli `parse_env` |
| `onstart_cmd` is a CLI flag name only; the API field is `onstart` | V | vast-cli |
| SSH and Jupyter launch modes inject setup scripts and **replace the image's entrypoint**; Entrypoint (`args`) mode keeps it | V | https://docs.vast.ai/guides/instances/docker-environment |
| Whether `onstart` executes in `args` mode | U | CORE-030 |
| Vast's SSH launch mode builds a derived image on top of the one named in `create`: the `running` event's `status_msg` sequence shows a BuildKit run (`#0 building with "default" instance…`) that `apt-get install`s packages including `openssh-sftp-server`, before the final line `success, running <image>/ssh`. Confirmed in both CORE-030 spike runs (research §7.1, Q12). Settles the "injects a working SSH server" half of Q1; the official `server-cuda` image did not already carry one. | V | our spike run A1/A2, 2026-11-02 (research §7.1) |
| SSH keys added at the account level are added to every new instance automatically | V | Vast console, "Manage Keys" page (user screenshot, 2026-11-01) |
| Vast base images are "pre-cached on many machines"; loading is "normal for 30 seconds with cached images"; "fresh pulls may take 10–60 minutes". No guarantee that `ghcr.io/ggml-org/llama.cpp` is cached on a given host | V (statements) / U (our image) | https://docs.vast.ai/guides/instances/manage-instances |

### 1.4 Instance state and fields

| Fact | Tag | Source |
|---|---|---|
| `GET /api/v1/instances` with keyset pagination, 25 per page, `select_cols`, `select_filters` (for example `{"actual_status": {"eq": "running"}}`); `GET /api/v0/instances/{id}` for one | V | https://docs.vast.ai/api-reference/instances/show-instances |
| Fields: `actual_status`, `intended_status`, `cur_state`, `next_state`, `status_msg`, `public_ipaddr`, `ssh_host`, `ssh_port`, `direct_port_start`, `direct_port_end`, `dph_total`, `duration`, `start_date` (epoch float), `machine_id`, `host_id`, `storage_cost`, `reliability2`, `label`, `ports` (Docker shape `{"8080/tcp": [{"HostIp": "0.0.0.0", "HostPort": "33526"}]}`, present only when running) | V | same |
| Quoted from the spec: "if `actual_status` becomes `exited`, `unknown`, or `offline` it will never reach `running` — destroy and retry" | V | same |
| Console states: Creating, Loading (image download), Connecting, Running, Inactive (stopped), Scheduling (restart waiting for GPU), Offline (machine disconnected) | V | https://docs.vast.ai/guides/instances/manage-instances |
| An exhaustive enum of `actual_status` values | U | CORE-020 records what it observes |
| `GET /api/v1/instances` list page pagination field is named `next_token`, not `next_cursor` (the original, never externally verified code and fixtures used `next_cursor`); fixed in `internal/provider/vastai/client.go` and its pagination fixtures (commit 6ee38b4) | S | our API probe, 2026-11-02 |
| Request-side pagination query parameter name sent when requesting page 2+ (`&cursor=` in `client.go`'s `listPage`) | U | the probe account had zero instances, so a second page was never actually requested; still to confirm, likely in CORE-050's reconcile loop or CORE-030's spike |
| CORE-020 probe: the account used had zero running instances (`GET /api/v1/instances` returned `instances_found: 0`), so no live `actual_status` value was observed; the enum remains U pending a future observation against a real running instance | S | our API probe, 2026-11-02 |

### 1.5 Stop, destroy, billing

| Fact | Tag | Source |
|---|---|---|
| `PUT /api/v0/instances/{id}` with `{"state": "stopped"}` or `{"state": "running"}`; `DELETE /api/v0/instances/{id}` destroys irreversibly and deletes all data | V | https://docs.vast.ai/api-reference |
| Stopped instances keep paying storage; GPU time is not charged; "stopped instances have no GPU reservation" | V | https://docs.vast.ai/guides/instances/pricing |
| A restart enters `SCHEDULING`; "if stuck > 30 seconds, GPU likely rented by another user… consider creating new instance"; it can wait indefinitely | V | https://docs.vast.ai/guides/instances/manage-instances |
| Storage is billed per second in every state except offline; typically priced higher for stopped instances | V | https://docs.vast.ai/guides/instances/pricing |
| GPU time is billed per second while the instance is active/connected; **not charged while Loading**; bandwidth billed per byte in any state; no minimum charge stated | V | https://docs.vast.ai/guides/reference/faq/billing |
| Prepaid credit model; at $0 balance "instances, storage volumes, and data will be scheduled for deletion" | V | same |
| `GET /api/v0/users/current` has a `balance` field | V | https://docs.vast.ai/api-reference/accounts/show-user |
| CORE-020 probe: `GET /api/v0/users/current` returned the expected shape with a populated `billing_threshold` and zero current balance | S | our API probe, 2026-11-02 |
| `GET /api/v0/charges` **exists**: the probe got HTTP 401 with body `{"success":false,"msg":"Authorization Error. Your key lacks the billing_read permission group which is required for this action."}` — a permission gate, not a 404. The endpoint's existence is now confirmed; it remains unused, as the budget design does not depend on it (CORE-100 ledger handles the real spend estimate) | S | our API probe, 2026-11-02 |
| `PUT /api/v0/instances/recycle/{id}` recreates the container from a freshly pulled image; on-demand contracts have a maximum duration and expired instances "may be deleted 48 hours after expiration" | V | https://docs.vast.ai/guides/instances/choosing/instance-types |

### 1.6 Networking

| Fact | Tag | Source |
|---|---|---|
| Each `-p X:X` maps to a random external port on the machine's "(usually shared) public IP"; inside the container the mapping is in `VAST_TCP_PORT_X`; limit 64 open ports | V | https://docs.vast.ai/guides/instances/connect/networking |
| "Docker instances have full internet access but generally do not have unique IP addresses" | V | same |
| "Some instances have dynamic IPs that may change"; filter `static_ip`; `PUBLIC_IPADDR` is set at startup and not updated | V | same |
| Containers are unprivileged; Docker-in-Docker disabled; full VMs exist only in SSH launch mode on `vms_enabled` hosts | V | https://docs.vast.ai/guides/reference/faq/security ; https://docs.vast.ai/guides/instances/virtual-machines |
| TUN devices / WireGuard / Tailscale in Docker instances: no official statement; a secondary source says TUN is unavailable and suggests Tailscale userspace mode | U | — |
| Best-effort Cloudflare quick tunnels per open port exist in Vast base images via the Instance Portal, "may not always be available" | V | https://docs.vast.ai/guides/instances/connect/instance-portal |
| Overlay networks are for instances in the same physical cluster (multi-node training) only | V | https://docs.vast.ai/multi-node-training-using-torch-nccl |

### 1.7 Storage

| Fact | Tag | Source |
|---|---|---|
| Volumes are "physically tied to the machine they were created on and cannot be moved"; fixed size; Docker instances only; default mount `/data` | V | https://docs.vast.ai/guides/instances/storage/volumes |
| "Vast has no cross-host network volume. For data that persists across machines, use object storage" | V | https://docs.vast.ai/examples/migrations/runpod-to-vast |
| Cloud Sync (`vastai cloud copy`) supports S3, Google Drive, Backblaze, Dropbox and Hugging Face; works on stopped instances | V | https://docs.vast.ai/guides/instances/storage/cloud-sync |

### 1.8 Vast Serverless (positioning)

| Fact | Tag | Source |
|---|---|---|
| Endpoint → Workergroup → Workers with a PyWorker sidecar; scaling parameters `min_load, target_util, cold_mult, cold_workers, max_workers, inactivity_timeout`; scale to zero when `cold_workers=0, min_load=0` | V | https://docs.vast.ai/guides/serverless/serverless-parameters |
| Same per-second price as plain instances; Ready and Loading workers pay GPU, Inactive pay storage | V | https://docs.vast.ai/guides/serverless/pricing |
| OpenAI proxy `https://openai.vast.ai/<endpoint>` supports `/v1/chat/completions` and `/v1/completions` against **vLLM** endpoints only; text-only; no `parallel_tool_calls` | V | https://docs.vast.ai/guides/serverless/openai-compatible-api |
| No llama.cpp serverless template found | U | — |

## 2. llama.cpp `llama-server`

| Fact | Tag | Source |
|---|---|---|
| Release tag per merge (`bNNNN`), several per day; latest on 2026-11-01 was **b12032**; binaries for CUDA 12.4, 12.8, 13.x | V | https://github.com/ggml-org/llama.cpp/releases |
| Images `ghcr.io/ggml-org/llama.cpp:server-cuda` (CUDA 12.8.1 on Ubuntu 24.04 by default) and `:server-cuda13`; the image sets `LLAMA_ARG_HOST=0.0.0.0`, `LLAMA_ARG_PORT=8080`, a curl `HEALTHCHECK` on `/health`, entrypoint `/app/llama-server` | V | https://github.com/ggml-org/llama.cpp/blob/master/docs/docker.md ; `.devops/cuda.Dockerfile` |
| Binary default port is now **9931**; the Docker image overrides to 8080 → always pass `--port` | V | `tools/server/README.md` |
| OpenAI-compatible: `/v1/models`, `/v1/completions`, `/v1/chat/completions` (SSE with `stream: true`), `/v1/embeddings`, `/v1/responses`; also Anthropic `/v1/messages` | V | same |
| `--jinja` on by default; Qwen3-Coder XML tool-call handler is reused for Qwen3.5; parallel tool calls need `"parallel_tool_calls": true` | V | `common/chat.cpp` |
| Qwen3.6 specifically detected by the tool-call parser | U | CORE-030 runs a tool-call round trip |
| `--reasoning-format none|deepseek|deepseek-legacy` (default `auto`); `deepseek` puts thinking in `message.reasoning_content`; `--reasoning-budget N`; per-request `chat_template_kwargs: {"enable_thinking": false}` | V | `tools/server/README.md` |
| `GET /health` needs no API key: **503** `{"error":{"code":503,"message":"Loading model","type":"unavailable_error"}}` while loading, **200** `{"status":"ok"}` when ready | V | same |
| `/metrics` needs `--metrics`; exposes `llamacpp:prompt_tokens_total`, `prompt_seconds_total`, `prompt_tokens_seconds`, `tokens_predicted_total`, `tokens_predicted_seconds_total`, `predicted_tokens_seconds`, `requests_processing`, `requests_deferred`, spec-decode counters | V | same |
| `/slots` on by default (`?fail_on_no_slot=1` → 503 when busy); `/props` reports `total_slots`, `is_sleeping`; `--sleep-idle-seconds N` | V | same |
| Flags: `--api-key` / `--api-key-file` (comma-separated list), `--host` (default 127.0.0.1), `-c/--ctx-size` (0 = model default, 262144 for Qwen3.6), `-np/--parallel` (-1 auto), `-kvu` unified KV (on when `-np` auto), `--kv-unified-per-slot N`, `-ngl` (`auto`/N/`all`), `--fit on` (default; sizes unset args to VRAM with 1024 MiB margin), `-fa on|off|auto`, `-ctk/-ctv` (f16), `-hf user/repo[:quant]` (default Q4_K_M; mmproj auto-downloaded unless `--no-mmproj`), `-hff`, `-mu/--model-url`, `--cache-reuse N`, `--slot-save-path`, `-to/--timeout` (3600 s), `-t`, `-n/--predict` (-1), `--mlock`, `--no-mmap`, `--cont-batching` (on) | V | same |
| Without `-kvu` each slot gets `n_ctx / n_parallel` (padded to 256); with `-kvu` slots share one pool, each capped at the training context | V | `src/llama-context.cpp` ≈ lines 390–393 |
| Client disconnect cancels generation (`req.is_connection_closed` → `SERVER_TASK_TYPE_CANCEL`) | V | `tools/server/server-http.cpp`, `server-queue.cpp` |
| When no slot is free the task is deferred and picked up later; `llamacpp:requests_deferred` is the queue depth; no documented queue length cap or queue timeout besides `--timeout` | V / U (caps) | same |
| Whether the published image is built with SSL support (`--ssl-key-file`, `--ssl-cert-file`) | U | matters only for fallback B in ADR 0006 |

## 3. Qwen3.6-35B-A3B

| Fact | Tag | Source |
|---|---|---|
| `Qwen/Qwen3.6-35B-A3B` exists; Apache-2.0; Hugging Face `createdAt` 2026-04-15; siblings `Qwen/Qwen3.6-35B-A3B-FP8` and dense `Qwen/Qwen3.6-27B` (2026-04-21) | V | https://huggingface.co/Qwen/Qwen3.6-35B-A3B ; https://huggingface.co/api/models/Qwen/Qwen3.6-35B-A3B |
| Architecture `Qwen3_5MoeForConditionalGeneration`, `model_type qwen3_5_moe`; vision-language MoE; 35B total, 3B active; 40 layers in the pattern 10 × (3 × Gated DeltaNet + 1 × Gated Attention); Gated Attention 16 Q heads, 2 KV heads, head_dim 256; DeltaNet 16 QK heads, 32 V heads, head_dim 128; 256 experts, 8 routed + 1 shared active | V | `config.json` on the model repo |
| Native context 262,144 tokens; ≈ 1,010,000 with YaRN | V | model card |
| Thinking on by default; `enable_thinking: false` disables; `preserve_thinking` supported; tool parser `qwen3_coder` in vLLM/SGLang examples | V | model card |
| Hosted as `qwen3.6-flash` on Alibaba Cloud Model Studio | S | https://www.alibabacloud.com/blog/qwen3-6-35b-a3b-agentic-coding-power-now-open-to-all_603043 |
| No official GGUF under the Qwen organisation | V | HF listing |
| `bartowski/Qwen_Qwen3.6-35B-A3B-GGUF` → `Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf`, 25,913,185,920 bytes (24.1 GiB), single file; mmproj ≈ 0.9 GB separate; only BF16 is split | V | HF file listing |
| `unsloth/Qwen3.6-35B-A3B-GGUF` → `Qwen3.6-35B-A3B-UD-Q5_K_M.gguf`, 26,456,194,016 bytes | V | HF file listing |
| The sha256 of the chosen file is recorded when CORE-070 pins it | — | CORE-070 |
| Qwen3.5 architecture support merged in llama.cpp PR #19468 (commit `fc0fe40`, tag **b7990**, 2026-02-10); an earlier attempt was reverted in b7976; later PRs (for example #19375) added speedups → use a current build | V | https://github.com/ggml-org/llama.cpp/pull/19468 |
| Sampling (model card): thinking general temp 1.0, top_p 0.95, top_k 20, min_p 0, presence_penalty 1.5; thinking precise coding temp 0.6, presence 0.0; non-thinking temp 0.7, top_p 0.80, top_k 20, presence 1.5 | V | model card |

### 3.1 VRAM estimate (our calculation, not an official figure)

Only the 10 full-attention layers keep a KV cache. Per token: 10 layers × 2 (K,V) × 2 KV heads × 256 dims × 2 bytes (f16) = **20,480 bytes**.

| Context | KV cache (f16) |
|---|---|
| 32,768 | ≈ 640 MiB |
| 65,536 | ≈ 1.25 GiB |
| 262,144 | ≈ 5 GiB |

DeltaNet recurrent state is fixed per slot: 30 layers × 32 heads × 128 × 128 × 4 bytes ≈ 60 MiB (assumes f32 state — U), plus ≈ 3 MiB conv state. Weights Q5_K_M ≈ 24.1 GiB; compute buffers ≈ 0.5–2 GiB (U). Total at 64K context ≈ **26–28 GiB**, inside a 48 GB L40S with room for a second slot. CORE-030 measures the real number from `nvidia-smi`.

## 4. OpenAI-compatible clients

| Fact | Tag | Source |
|---|---|---|
| Python SDK default timeout 600 s with a **5 s connect timeout** (`DEFAULT_TIMEOUT = Timeout(600, connect=5.0)`); Node 600,000 ms | V | https://github.com/openai/openai-python `_constants.py` ; https://github.com/openai/openai-node `src/client.ts` |
| Both retry 2 times with exponential backoff (0.5 s → 8 s) on connection errors, 408, 409, 429, 5xx; Node also retries timeouts; Python does not retry once a stream has started | V | SDK sources |
| Both read `retry-after-ms` / `retry-after` and `x-should-retry`; Python honours values ≤ 120 s and **does not retry** above; Node honours ≤ 60 s and falls back to default backoff above | V | SDK sources |
| OpenCode provider config: `"npm": "@ai-sdk/openai-compatible"`, `options.baseURL: "http://host/v1"`, `apiKey` (supports `{env:VAR}`), `models` map; the model id must match `/v1/models` | V | https://opencode.ai/docs/providers/ |
| Continue: `provider: openai`, `apiBase`, `apiKey` | V | https://docs.continue.dev/customize/model-providers/top-level/openai |
| Cursor: "Override OpenAI Base URL"; requests originate from Cursor's servers, so the URL must be public; Agent/Tab do not use the custom key | S | https://docs.api7.ai/ai-gateway/integrations/coding-agents/cursor |

## 5. Platform facts (from `vk-lab-platform`, read 2026-11-01)

| Fact | Tag | Where |
|---|---|---|
| k3s `v1.36.4+k3s1`, control plane `cx23` tainted, workers `cx43`, autoscaler 1–2 workers | V | `terraform/modules/hcloud-nodes/variables.tf`, `Makefile` |
| Envoy Gateway chart v1.2.1; Gateway `platform-gateway` in ns `envoy`; listeners `http` (80) and `https` (443, cert `platform-public-tls`, wildcard `<fqdn>` and `*.<fqdn>`); `allowedRoutes.namespaces.from: All` | V | `gitops/templates/platform/shared/envoy-gateway/` |
| cert-manager v1.21.1 with Let's Encrypt DNS-01 via Route 53; external-dns 1.21.1 creates records from HTTPRoute hostnames | V | same tree, `tls/issuers.yaml` |
| kube-prometheus-stack 88.5.4 with `serviceMonitorSelectorNilUsesHelmValues: false` (any namespace); Loki 7.3.0 + Alloy 1.12.0 DaemonSet tail all pod stdout | V | `gitops/templates/platform/shared/observability/` |
| One CloudNativePG cluster `lab-postgres` (ns `cnpg-system`), db `vkdb`; no per-app database mechanism | V | `gitops/templates/platform/shared/postgres/cluster.yaml` |
| External Secrets Operator 2.9.0, ClusterSecretStore `aws-parameter-store`; on Hetzner the IAM policy allows `ssm:GetParameter` on exactly the Postgres and Grafana passwords | V | `terraform/modules/rolesanywhere/main.tf` ≈ 126–127 |
| Firewall: inbound 22, 6443, icmp; no outbound rules → egress open; every server has a public IPv4; no reserved IP, the load balancer gets a new IP on each `make up` | V | `terraform/modules/hcloud-firewall/main.tf`; constitution §20 |
| Apps enter via `AppProject` + pointer `Application` at wave 5 pointing at the app repo's `gitops/` path; optional parameters are omitted rather than passed as `""` | V | `gitops/templates/apps/vk-ahorro/` |
| The platform already applies an Envoy Gateway traffic policy to a route (Grafana), so the policy CRDs are installed and in use on this Envoy Gateway version | V | `gitops/templates/platform/shared/observability/grafana-traffic-policy.yaml` |
| No NetworkPolicy, Redis, Kafka, GPU or LLM references anywhere | V | grep |

## 6. Pipeline facts (from `vk-ahorro`, read 2026-11-01)

See ADR 0008. Key files: `scripts/version.sh`, `.github/workflows/{ci,publish,deploy,release,preview}.yml`, `deploy/docker/ahorro-api.Dockerfile`, `deploy/helm/ahorro-api/templates/_helpers.tpl`, `docs/delivery.md`, `scripts/specs-check.sh`. Pinned actions on 2026-11-01: `actions/checkout` v7.0.1, `docker/setup-buildx-action` v4.4.1, `docker/build-push-action` v7.4.0, `docker/login-action` v4.6.0, `azure/setup-helm` v5.0.1 (helm v4.3.0), `dorny/paths-filter` v4.0.3, `actions/setup-go` v7.0.0, `golangci/golangci-lint-action` v9.3.0 (golangci v2.13.2), kubeconform 0.8.0, actionlint 1.7.12. Go builder image `golang:1.27`, runtime `gcr.io/distroless/static-debian12:nonroot`. Copy the SHAs from the source files at implementation time, not from this page.

Scanning and release conventions compared (read 2026-11-01):

| Convention | vk-ahorro | vk-lab-platform | Recommendation for this repo |
|---|---|---|---|
| Action pinning | every action pinned to a commit SHA with a version comment; `permissions: {}` | mostly pinned | SHA-pin everything (ADR 0013) |
| Secret scanning | none | `gitleaks/gitleaks-action@v2`, **unpinned**, in `.github/workflows/lifecycle-test.yml` | run the `gitleaks` binary, pinned and sha256-verified, on PRs and weekly over history |
| Dependency vulnerabilities | dependabot (actions, docker, gomod); gosec via golangci | dependabot | add `govulncheck` |
| Image scanning | none (header notes the 2026 trivy-action compromise) | none | `trivy` binary, pinned and sha256-verified, not the action |
| Downloaded tools | kubeconform and actionlint as release binaries with `sha256sum -c` | — | same pattern for gitleaks and trivy |
| GitHub Releases | none; a release is a git tag plus GHCR artifacts | none | GoReleaser for the `vk-llm` binaries (ADR 0013), a new convention |
| Secret delivery | ConfigMap only, no Secrets | KMS ciphertext → SSM → External Secrets | ADR 0009 |

Both repositories are public (verified via `gh repo view`, 2026-11-01).

## 7. Spike results (CORE-030)

Two real runs on real 1×L40S Vast.ai instances, each independently
verified destroyed (`GET /api/v0/instances/{id}` → `{"instances": null}`,
confirmed again by `--sweep` → 0 instances):

- **A1** (label `vkllm:spike:1793629767`, instance 58834177, requested
  runtype `ssh`), run 2026-11-02.
- **A2** (label `vkllm:spike:1793633988`, instance 58843281, requested
  runtype `ssh_direct`), run 2026-11-02, about 32 minutes after A1's
  instance was destroyed.

Every figure below is read from the two runs' `summary` JSONL line or an
earlier event in the same file; tagged **S** (our own observation, not a
vendor-published fact), source "spike run A1/A2, 2026-11-02". No IP,
hostname or key appears below (leak-checked before commit; instance ids
are not in that prohibited list and are kept for traceability to the now-
destroyed instances); the raw JSONL files live only in the laptop's
`$TMPDIR` and were never committed.

### 7.1 Cold-start stages

| Stage | A1 (s) | A2 (s) | Definition | Tag |
|---|---|---|---|---|
| search | 0.58 | 0.74 | E1−E0 | S |
| create | 0.92 | 1.16 | E2−E1 | S |
| boot | 305.50 | 114.90 | E3−E2 | S |
| link | 5.11 | 8.68 | E4−E3 | S |
| download | 403.90 | 289.26 | E6−E5, host clock; **includes sha256 verification** per the event table's own definition (E6 = `sha_end`) | S |
| load | 5.33 | 9.27 | E8−E7, host clock | S |
| ready | 0.79 | 0.93 | E9−E8 | S |
| first_token | 1.19 | 1.49 | E10−E9 | S |
| **Cold start total** (E10 − E0) | **715.04** | **425.78** | laptop clock | S |

The eight stage durations above do not sum exactly to the cold start
total: `download`/`load` are host-clock differences while `search`
through `link` and `ready`/`first_token` are laptop-clock differences, the
two clocks are not synchronized, and the spec's own design says download
can start before `ssh_ok` and that overlap is recorded, not corrected
(spec 030's Event timeline section). The total is the authoritative number
(laptop `first_token` minus laptop `search_start`); the per-stage rows are
each individually accurate but not additive across the clock boundary.

`boot` is driven almost entirely by a BuildKit image build Vast runs for
SSH launch mode, not a plain `docker pull`: the `running` event's
`status_msg` sequence shows layer pulls followed by `#0 building with
"default" instance…`, an `apt-get install` (installing, among other
packages, `openssh-sftp-server`), and a final `success, running
<image>/ssh` line. A1's build also ran `noble-security` upgrades; A2's did
not — real host-to-host variance in what was already cached. This settles
the open "does Vast inject an SSH server into an image that lacks one"
question (research §1.3): yes, via a derived-image build, in both runs.
No IP or hostname appeared in either `status_msg` array (checked before
writing this, including the public Ubuntu package-mirror hostnames those
arrays contain, which are not instance-identifying). Ordered milestones
seen before `running`, both runs: image layer pull/verify → BuildKit
build start (`#0 building with "default" instance…`) → `apt-get`
package install/upgrade → (A1 only) locale/package setup steps →
`success, running <image>/ssh`.

Within `download`, the fetch itself (`download_end − download_start`,
host clock) and the sha256 check after it were:

| | A1 | A2 |
|---|---|---|
| fetch-only seconds | 387.17 | 259.92 |
| fetch-only throughput | 66.93 MB/s (63.83 MiB/s) | 99.70 MB/s (95.08 MiB/s) |
| sha256 seconds (after fetch) | 16.73 | 29.34 |
| `apt-get install aria2` seconds | 3.58 | 8.58 |
| fetcher used | aria2c (`-x 16 -s 16`) | aria2c (`-x 16 -s 16`) |
| `Content-Length` vs expected | 25,913,185,920 = 25,913,185,920 (match) | 25,913,185,920 = 25,913,185,920 (match) |

Fetch-only throughput (not the sha-inclusive stage total) is the honest
answer to Q8's "download throughput"; the stage total above is kept
sha-inclusive only because that is literally how the spec's event table
defines the `download` stage (`E6 − E5`, `E6` = `sha_end`). Both numbers
are recorded so neither reading is lost.

### 7.2 Open questions Q1–Q3, Q7–Q12

| Q | Question | Answer | Tag | Source |
|---|---|---|---|---|
| Q1 | SSH mode works with `server-cuda` | **Yes.** `ssh_ok` reached without any manual step in both runs: 5.11 s after `running` in A1, 8.68 s in A2 (both via the account key, auto-added by Vast). | S | §7.1 |
| Q2 | `onstart` runs in `args` mode | **Moot: A accepted.** Fallback B (Entrypoint/`args` mode) was never built; the project owner decided, after both A runs, that it was not worth the Caddy/TLS build for a sub-second latency gap (§7.3, §7.6). | — | §7.3, §7.6 |
| Q3 | SSH endpoint direct or proxied; channel throttling | **Proxy, in both runs** — the script's own direct/proxy test (`ssh_host == public_ipaddr`) classified both A1 (requested `ssh`) and A2 (requested `ssh_direct`) as `proxy`; requesting `ssh_direct` did not change the observed SSH routing for this host, in this one sample. A genuinely direct connection was never actually observed in either run, so direct mode's effect on latency/throughput remains **unconfirmed**, not "tested and found no different". Channel drops: 0 in both runs, from `ssh_ok` through the end of M8. Bulk throughput over a plain SSH exec channel (`head -c 1G /dev/zero`, a separate session from the HTTP tunnel channel) was low in both: 2.17 MB/s (A1), 1.59 MB/s (A2) — well under the HF fetch-only throughput above (66.9–99.7 MB/s). The proxy appears to throttle sustained bulk transfer specifically, not interactive completions through the actual tunnel (M4's TTFT/tokens-per-s stayed close to on-host); see §7.4 M8 and ADR 0006's Risks section for the reading on this. One more concrete thing for spec 140: A2's scan found the container's own `22/tcp` port mapping open on the instance's public IP, suggesting a direct SSH path to the container may exist at that port even though the API's `ssh_host` pointed at the proxy — untried in this spike. | S | §7.4 M1, M8 |
| Q7 | Qwen3.6 tool-call round trip | **Yes, 10/10.** 5/5 two-leg round trips passed in each run (leg 1 `finish_reason: tool_calls` with a well-formed `get_weather` call, leg 2 `finish_reason: stop`). | S | §7.4 M5 |
| Q8 | HF download throughput | **66.9–99.7 MB/s measured (fetch-only), both via aria2c**, no curl fallback needed in either run (`apt-get install aria2` succeeded in 3.6 s / 8.6 s). See the fetch-only figures in §7.1; real host-to-host variance, not a single number. `apt-get` reached a mirror quickly in both runs, settling that research §1.3 open item in aria2c's favour (the default-curl fallback was never exercised). | S | §7.1 |
| Q9 | VRAM at 32K, 64K, `-np 2` | **Open, not answered.** Every restart attempt (V1–V4) failed with "did not become healthy after restart": A1 timed out at the then-default 180 s per config; A2 (run with `SPIKE_RESTART_HEALTH_TIMEOUT_SECONDS=420`) timed out at the full 420 s in each of V1–V3 (not early — ruling out "just needs more time" within that budget), and V4 was skipped outright as the 40-minute hard cap approached. No `nvidia-smi` reading was ever taken post-restart. Two candidate root causes were identified by code review only (no further live test): (a) the restart path authenticates with `--api-key-file` while the original boot uses inline `--api-key`, and the key-file's exact format/failure handling was never confirmed; (b) `onstart.sh`'s `unset LLAMA_ARG_HOST` (needed because the image sets `LLAMA_ARG_HOST=0.0.0.0`) is not replicated on the restart path. Diagnostic logging (env-var capture, key-file-exists check, post-restart process-alive and log-tail capture, `WORKER_API_KEY` always redacted) was added to `launch.sh`'s restart cycle in commit `0643ea6` (2026-11-02, before `hack/spike/` was deleted in this write-back) so a *future* real run would be conclusive, but no such run happened before the script was removed. Spec 140 (repeated acceptance runs) is the natural place to retry with that diagnostic build. | S | §7.4 M7 |
| Q10 | Decode tokens/s, warm TTFT | **Measured, both runs; tunnel TTFT is 0.8–0.94 s slower than on-host, tokens/s is not affected.** A1: tunnel TTFT median 0.889 s vs host 0.085 s (Δ 0.804 s); tokens/s 161.9 tunnel vs 162.6 host (99.57 %). A2: tunnel TTFT median 1.085 s vs host 0.144 s (Δ 0.941 s); tokens/s 143.5 tunnel vs 142.6 host (100.66 % — tunnel measured *faster* for throughput here). `/metrics`' `predicted_tokens_seconds` agreed closely with the sampled tokens/s in both runs (160.93 A1, 141.93 A2). | S | §7.4 M4 |
| Q11 | Image built with SSL | **No**, both runs (`/app/llama-server --help` has no `--ssl-key-file`/`--ssl-cert-file`). Expected and irrelevant for bundle A. | S | §7.4 M6 |
| Q12 | Image pull under 2 min | **1 of 2 samples under 2 min: 305.5 s (A1, over) vs 114.9 s (A2, under), highly variable.** Not reliably under 2 minutes. Neither sample is "a plain pull"; both are the BuildKit-driven derived-image build described in §7.1. One sample pair, as the spec anticipates; spec 140 adds more. | S | §7.1 |

### 7.3 ADR 0006 decision gate

| Gate | Threshold | A1 | A2 | Detail |
|---|---|---|---|---|
| G1 SSH | `ssh_ok` within 5 min of `running`, no manual step | **Pass** | **Pass** | 5.11 s / 8.68 s, both via the account key |
| G2 onstart | events log exists, `server_start` logged | **Pass** | **Pass** | full event sequence present both times: `apt_start`→`apt_ok`→`content_length`→`download_start`→`download_end`→`sha_end`→`server_start`; sha256 matched the pinned value exactly both times |
| G3 loopback | port 8080 bound only to `127.0.0.1`; scan shows no open mapped port except SSH | **Inconclusive** | **Pass** | A1: `/proc/net/tcp` confirmed loopback-only, but nmap produced no output at all (verdict `inconclusive: nmap did not produce output`) — no evidence of exposure, but also no confirming scan. A2: nmap found one open port (29203), which matches the instance's own SSH port mapping and nets to 0 "extra" open ports → pass |
| G4 readiness | `/health` 200, `/v1/models` lists the alias through the tunnel | **Pass** | **Pass** | `/health` 200 without a key both times; `/v1/models` 200 with the worker key, 401 without it, both times |
| G5 latency | tunnel TTFT − host TTFT ≤ 300 ms **and** tunnel tokens/s ≥ 95 % of host | **Fail** | **Fail** | latency half failed both runs (804 ms / 941 ms delta, cap 300 ms); throughput half passed both runs (99.57 % / 100.66 %). G5 needs both; one miss is a fail |
| G6 stability | 0 channel drops, `ssh_ok` through end of M8 | **Pass** | **Pass** | `channel_drops_total: 0` both runs |

**Reading this gate.** The gate's own decision rule ("G1/G2 fail → run B;
only G5/G6 fail and A1 was proxied → run A2 with `ssh_direct`; A2 passes →
Accepted with `ssh_direct` pinned; otherwise run B") was triggered exactly
as written — A1 was proxied and failed G5, so A2 ran. But A2 did not
cleanly pass either: it still failed G5, and it never achieved a genuinely
direct connection to test the rule's "Accepted with `ssh_direct` pinned"
branch (§7.2 Q3). The rule's branches do not have a clean exit for "A2 also
fails G5, with no bundle-A sample found that passes it." The resolution
taken (§7.6) is the project owner's explicit, informed decision to accept
bundle A anyway and amend the G5 threshold, not a mechanical outcome of
this table.

### 7.4 M1–M10 measurements

| # | Measurement | A1 | A2 |
|---|---|---|---|
| M1 | Port scan | loopback-only confirmed via `/proc/net/tcp` both times; external scan inconclusive (A1) / pass, one open port matching the SSH mapping (A2) | see G3 above |
| M2 | `/health`, `/v1/models` with/without key | 200 / 200 (with key) / 401 (without) | 200 / 200 (with key) / 401 (without) |
| M3 | Streamed chat completion shape | 410 chunks, terminal `[DONE]`, `reasoning_content` present (thinking on), `finish_reason: stop` | 392 chunks, terminal `[DONE]`, `reasoning_content` present, `finish_reason: stop` |
| M4 | Tunnel vs on-host TTFT / tokens-per-s | see Q10 | see Q10 |
| M5 | Tool-call round trips | 5/5 | 5/5 |
| M6 | `--help` SSL flags | none found | none found |
| M7 | Q9 VRAM matrix (V1–V4) | all 4 configs: restart did not become healthy (180 s timeout each) | V1–V3: restart did not become healthy (420 s timeout each, full duration); V4 skipped (hard-cap budget) |
| M8 | Bulk throughput, channel drops, `max_tokens: 4096` stream | bulk 2.17 MB/s; drops 0; the 4096-token stream sample itself returned 0 chunks (`ttft: -1`, `finish_reason: none`) — llama-server was most likely still unresponsive right after the four failed M7 restores, so this sub-measurement is **not obtained**, not "fast" | bulk 1.59 MB/s; drops 0; the script logged `m8_stream_4096_skipped` and then, ~4 min later, recorded `stream_4096_chunks: 111` in the same summary field — the code path that reaches `m8_bulk_and_stream` after a skip reuses whatever value the variable last held, so this is a **definitely stale** value carried over from an earlier step, not a genuine post-restart stream measurement. The bulk figure, taken independently of llama-server's health over its own exec channel, is the only M8 number trusted here |
| M9 | 60 s single-stream HF download sample | **not measured** — the script's 32-minute skip-ahead to M10 fired before M9 ran | **not measured**, same reason |
| M10 | Self-exit step (a/b/c) | step a (`pkill -x llama-server`) and step b (`kill -TERM 1`): `actual_status` stayed `running` through 120 s of polling each, in both phases — **did not reach `exited`**. Step c (balance-while-exited) never ran, because its own precondition (`exited`) was never met | identical outcome: steps a and b both stayed `running` for 120 s each; step c never ran |

Both runs agree: **neither of the two in-container actions this spike
tested (`pkill -x llama-server` alone, then also `kill -TERM 1`) turned a
real SSH-mode instance `exited` within the tested window.** Only the
script's own external `DELETE` (never dependent on in-container state)
actually ended both instances, independently confirmed both times via a
direct `GET` (`{"instances": null}`) and `--sweep` (0 instances).

**Flag for a future session, not fixed here:** [ADR 0003](adr/0003-gpu-lifecycle-management.md)
§7 states that "an exited instance stops GPU billing even when the control
plane is down." This spike's M10 data (2/2 real samples) suggests the
in-container half of that assumption — that killing the server process and
then PID 1 is sufficient to reach `exited` — does not hold, at least within
120 s per step on this provider. ADR 0003 is **not amended by this spec**
(out of this spike's explicit scope); this is recorded here so spec 070 or
a dedicated review picks it up deliberately.

### 7.5 Cost and safety

| | A1 | A2 | Combined |
|---|---|---|---|
| `dph_total` | $0.55/h | $0.8111/h | — |
| Lifetime since `create_end` | 2316.49 s | 2387.69 s | — |
| Estimated cost (`dph_total × lifetime`) | $0.3539 | $0.5380 | **$0.8919** |

Well under the $5.00 cap (R5, AC13). Both instances independently
confirmed destroyed by direct `GET` and by `--sweep`.

**AC13 is only partly evidenced.** The figures above are the script's own
`dph_total × lifetime` estimate, not a provider-confirmed balance delta.
The only two real balance readings captured are each run's own
`balance_start` (read via `read_balance()`, which reads `.credit` before
falling back to `.balance` — this already answers the "controller
concern" the pre-flight Evidence raised in spec.md, about `.balance`
staying pinned at 0 while `.credit` moves): **$25.0000** before A1, **$24.5727**
before A2, roughly 32 minutes after A1's destroy. That is a real, measured
delta of **$0.4273** for A1 — about 21 % above A1's own $0.3539 estimate,
plausibly the extra bandwidth cost of the 25.9 GB download, which Vast
bills separately from GPU time. No reading was taken **≥ 1 hour after
A2's destroy**, so AC13's exact stated check ("balance read ≥ 1 h after
the last destroy") was not performed in this session; the $0.4273 real
data point and the $0.8919 combined estimate both sit far enough under the
$5.00 cap that R5 is not in doubt, but the specific AC13 reading is a gap,
recorded here rather than checked off from the estimate alone.

The search used `dph_total {lte: 1.00}` for both runs, the user-authorized
deviation from the architecture's $0.80 default already recorded in
`specs/core/030-D-worker-launch-spike/spec.md`'s Evidence section. A2's
real price, $0.8111/h, is itself above the $0.80 figure in requirement R6's
literal text but within the $1.00 ceiling that authorization raised for
this run only.

The account balance backstop was **not** reduced to ≤ $10 before these
runs, a recorded deviation from this spec's own Safety and cost controls
table (research §1.5; spec.md "Backstop" row): the user was told the
backstop caps worst-case loss if every other safeguard fails simultaneously,
not the expected spend, and chose to keep it at $25 anyway. Both runs'
actual spend ($0.3539, $0.5380) shows the backstop was never close to being
tested.

### 7.6 ADR 0006 decision: Accepted (bundle A), with an amendment

Fallback B (Entrypoint/`args` mode, Caddy, TLS) was **explicitly skipped**
by the project owner after the gate review in §7.3. The rationale recorded
here: both real samples show the tunnel adding under one second of extra
TTFT (804 ms A1, 941 ms A2) with tokens-per-second essentially unaffected
(≥ 99.6 % of on-host speed in both runs, and tunnel *faster* in A2) — a
custom image with Caddy TLS was judged not worth building for a sub-second
latency miss. This is a deliberate project-owner decision, not something
the gate's own pass/fail logic produced; §7.3's table shows the gate did
not cleanly resolve to an "Accepted" branch on its own.

[ADR 0006](adr/0006-worker-connectivity.md) is flipped to **Accepted
(bundle A, SSH launch mode)**, with an **explicit amendment to the G5
latency threshold**: the 300 ms cap was not met in either real sample
(804–941 ms measured, with no throughput cost), so G5's threshold is
amended rather than silently treated as passed or left pending. See the
ADR itself for the amendment text.

### 7.7 Recommended values

| Key | Current Accepted default | Recommended | Needs ADR amendment? |
|---|---|---|---|
| `provider.filter.minInetDownMBps` | 500 (ADR 0005 §2 / `docs/architecture.md`) | **≥ 1100 (A1) / ≥ 750 (A2).** Neither run's `search_end` event recorded the chosen offer's actual advertised `inet_down` (no offer-census event was logged), but both runs' search used the production filter `inet_down {gte: 500}`, so the advertised value was at least 500 in both cases. Applying the rule with that floor (`r = measured MB/s ÷ 500`, `value = (25,913 MB ÷ 180 s) ÷ r`, rounded up to 50): A1 (fetch-only 66.93 MB/s) → r = 0.1339 → value ≥ 1100; A2 (fetch-only 99.70 MB/s) → r = 0.1994 → value ≥ 750. Both are **lower bounds** — the true advertised `inet_down` could be higher than 500, which would push the recommended value higher still, not lower. Caveat: this all assumes `inet_down`'s documented unit is MB/s, which Q4 (`docs/open-questions.md`) still leaves open as MB/s vs Mbps; if it turns out to be Mbps, these specific numbers need re-deriving. Taking the MB/s assumption at face value, two real hosts that passed a `≥500` filter delivered only 67–100 MB/s fetch-only — this reproduces, in 2 of 2 runs, exactly the risk ADR 0005 already names ("a host advertises bandwidth it does not deliver"). | **Yes** — both lower-bound figures exceed the 500 default |
| `onDemand.maxWait` | 480 s (ADR 0007 §4) | **540 s** (the rule's clamp ceiling) — 1.25 × cold start rounds up to the nearest 30 s to 900 s (A1) and 540 s (A2); both clamp to the [300, 540] s ceiling, so the two runs agree on 540 s despite their very different cold-start totals | **Yes** — 540 s exceeds the 480 s Accepted default. Caveat: the rule's stated input is "a run that passed the gate" (§7.3); neither run cleanly did (G5 failed both). Recorded as the best available number from two independent samples that both saturate the clamp ceiling regardless of the exact cold-start figure; spec 140 should confirm once a run meets the amended G5 threshold |
| `lifecycle.startupTimeout` | 15 min (ADR 0003 §Consequences / `docs/architecture.md`) | **Inconclusive by this spike's own rule.** 2 × (E9 − E2) rounds to 24 min (A1) and 15 min (A2) — real host variance, both within the [10, 30] min clamp. A1's figure would exceed the current 15 min default if adopted | **Not recommended as a change here** — the rule's "passed the gate" precondition was not met in either run, and the two samples disagree by 9 minutes; spec 140 should compute this from a qualifying run rather than applying either number now |

The census from the search table (spec design) would show how many offers
each candidate `inet_down` threshold leaves; this spike did not run that
census call (the production filter's `inet_down` clause was simply applied
without a parallel unfiltered count), so that part of the rule's input is
also unavailable from this data and is not fabricated here.
