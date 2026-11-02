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
| Whether Vast injects an SSH server into an image that lacks `openssh-server` (the docs say "if you run into obscure loading errors with SSH or Jupyter launch modes on a custom image, try the simpler Entrypoint launch mode and set up SSH yourself") | U | CORE-030 |
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
