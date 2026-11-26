# vk-llm-on-demand

A self-hosted, Kubernetes-native control plane that exposes an
OpenAI-compatible LLM API backed by GPU instances rented on demand. In the
default mode, you start the GPU, use any OpenAI-compatible client, and
the GPU is destroyed 30 minutes after your last inference or the moment
you stop it. A chat client can also start the GPU itself — see
["On-demand mode"](#on-demand-mode). The control plane is one small Go
service in an existing cluster.

```bash
vk-llm start      # rent one RTX 6000 Ada GPU on Vast.ai, load the model, wait for READY
opencode          # or any client with base URL https://llm.<fqdn>/v1
vk-llm stop       # release the GPU now (otherwise: 30 min after the last inference)
```

- Design intent: [`docs/architecture.md`](docs/architecture.md)
- Decisions: [`docs/adr/`](docs/adr/)
- Requirements and acceptance criteria: [`docs/requirements.md`](docs/requirements.md)
- Verified external facts with sources: [`docs/research.md`](docs/research.md)
- Implementation specs: [`specs/`](specs/)

## Status

The code for the control plane is merged to `main`. These parts are
done:

- The GPU lifecycle controller
- The OpenAI-compatible gateway
- The management API
- The worker runtime
- Budget controls
- The Helm chart
- Observability
- The release pipeline
- The `vk-llm` CLI
- Remote control from GitHub Actions

These parts are not yet done:

- Security hardening
- Platform wiring
- End-to-end acceptance tests
- CI and release hardening
- Persistent-volume caching

See [`specs/`](specs/) for the full list. Each spec's status is `DONE` or
`PROPOSED`.

## How it works

A Go binary, `gpu-on-demand`, runs as one Deployment in a k3s cluster.
Requests reach it through an existing Envoy gateway.

The management API, at `/api/v1/gpu/*`, uses a management token. It
starts and stops one NVIDIA RTX 6000 Ada GPU (48 GB) on Vast.ai. The GPU runs
`llama-server` with the `Qwen3.8-27B` model (Q8_0 GGUF, 28.6 GB).

The inference API, at `/v1/*`, uses an inference key. It proxies
OpenAI-compatible chat completions, streaming, and tool calls to the
worker over an SSH tunnel. In explicit mode (the default) it returns
`backend_unavailable` while no GPU runs; in on-demand mode it starts the
GPU itself instead — see ["On-demand mode"](#on-demand-mode).

The system stores state in one ConfigMap. A reconcile loop compares the
provider's instance list with that state every minute. It destroys any
instance it does not recognize. It enforces a 30-minute rolling
inactivity timeout, a monthly budget, a maximum hourly price, and a
maximum lifetime.

The `vk-llm` CLI and a GitHub Actions workflow are thin clients of the
management API.

The [vk-lab-platform](https://github.com/savak1990/vk-lab-platform)
project manages the Hetzner k3s cluster. That cluster is the first
deployment target. The Helm chart does not depend on it.

## How to use

1. Create the necessary secrets in the cluster (see "Intended install").
2. Install the Helm chart (see "Intended install" below).
3. Run `vk-llm start`. Wait for the `READY` state.
4. Configure any OpenAI-compatible client with the base URL
   `https://llm.<fqdn>/v1`.
5. Run `vk-llm stop` when you are done. If you do not, the 30-minute
   idle timeout stops it.

## Intended install

```bash
helm install gpu-on-demand \
  oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand \
  --namespace llm --create-namespace \
  -f values.yaml
```

Secrets (Vast.ai API key, inference keys, management tokens, worker seed,
the SSH key for the worker tunnel) are referenced by name from existing
Kubernetes Secrets and never appear in `values.yaml`. This repository is
public; see [`SECURITY.md`](SECURITY.md).

## Install the CLI

Find the version on the [releases
page](https://github.com/savak1990/vk-llm-on-demand/releases), download the
archive for your platform, verify it against `checksums.txt`, and put the
`vk-llm` binary on your `PATH`.

```bash
# Linux, amd64 (use linux_arm64 on arm64) — VERSION is the release number, no "v"
VERSION=0.3.0
curl -LO https://github.com/savak1990/vk-llm-on-demand/releases/download/v${VERSION}/vk-llm_${VERSION}_linux_amd64.tar.gz
curl -LO https://github.com/savak1990/vk-llm-on-demand/releases/download/v${VERSION}/checksums.txt
grep " vk-llm_${VERSION}_linux_amd64.tar.gz\$" checksums.txt | sha256sum -c -
tar -xzf vk-llm_${VERSION}_linux_amd64.tar.gz vk-llm
sudo install -m 0755 vk-llm /usr/local/bin/vk-llm
```

```bash
# macOS, arm64 (use darwin_amd64 on an Intel Mac)
VERSION=0.3.0
curl -LO https://github.com/savak1990/vk-llm-on-demand/releases/download/v${VERSION}/vk-llm_${VERSION}_darwin_arm64.tar.gz
curl -LO https://github.com/savak1990/vk-llm-on-demand/releases/download/v${VERSION}/checksums.txt
grep " vk-llm_${VERSION}_darwin_arm64.tar.gz\$" checksums.txt | shasum -a 256 -c -
tar -xzf vk-llm_${VERSION}_darwin_arm64.tar.gz vk-llm
sudo install -m 0755 vk-llm /usr/local/bin/vk-llm
```

Point it at your cluster's management API and a management token:

```bash
export VK_LLM_API_ENDPOINT=https://api.lab.example.com
export VK_LLM_TOKEN=<your management token>
vk-llm status
```

Show or change the monthly budget. The new value applies at once, with no
redeploy. It cannot exceed the ceiling set by `config.budget.maxMonthlyUSD`
(default $100).

```bash
vk-llm budget            # current budget and spend this month
vk-llm budget set 40     # raise or lower the monthly budget
vk-llm rentals           # one row per rental: where, $/h, boot, download, load, total
```

After you set the budget with `vk-llm budget set`, the stored value
replaces `config.budget.monthlyUSD`. A later `helm upgrade` does not change
it. If `config.budget.monthlyUSD` is above `config.budget.maxMonthlyUSD`
(default $100), the controller refuses to start. Raise `maxMonthlyUSD` in
the same `helm upgrade`.

## Call the API directly

Every route below is served on one host, `https://llm.<your-domain>`. The
inference routes under `/v1` take the inference key. The management routes
under `/api/v1/gpu` take the management token. The two values are different
keys; the server refuses to start if one value appears in both lists.

```bash
export H=https://llm.lab.example.com
export INF=<your inference key>          # sk-vkllm-…, the key OpenCode sends
export MGMT=<your management token>      # sk-vkllm-…, the key vk-llm sends
```

Check the route and the certificate. The model list needs no GPU:

```bash
curl -sv "$H/v1/models" -H "Authorization: Bearer $INF" 2>&1 | grep -E 'issuer|^< HTTP|"id"'
```

Expected: `HTTP/2 200`, an issuer line that names Let's Encrypt, and
`"id": "qwen3.8-27b"`.

Read the GPU state and the running cost:

```bash
curl -s "$H/api/v1/gpu/status" -H "Authorization: Bearer $MGMT" | jq '{state, ready, secondsUntilIdleShutdown, installationId}'
curl -s "$H/api/v1/gpu/cost"   -H "Authorization: Bearer $MGMT" | jq
curl -s "$H/api/v1/gpu/rentals" -H "Authorization: Bearer $MGMT" | jq
```

Expected while no GPU runs: `"state": "STOPPED"`, `"ready": false`,
`"secondsUntilIdleShutdown": null`.

Send a chat request while the GPU is stopped:

```bash
curl -s -X POST "$H/v1/chat/completions" \
  -H "Authorization: Bearer $INF" -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.8-27b","messages":[{"role":"user","content":"Say hello."}]}'
```

Expected, in explicit mode (the default): HTTP 503 with
`"code": "backend_unavailable"`. This is the normal answer while the GPU
is off; it costs nothing. In on-demand mode the same request instead
returns `503 gpu_cold_start` and starts the GPU — see
["On-demand mode"](#on-demand-mode).

Start the GPU. This rents an RTX 6000 Ada GPU on Vast.ai and costs money until you stop
it or the 30-minute idle timeout stops it:

```bash
curl -s -X POST "$H/api/v1/gpu/start" -H "Authorization: Bearer $MGMT" | jq '{result, state}'
```

Expected: HTTP 202 with `"result": "accepted"`, then the state moves through
`ALLOCATING`, `STARTING`, `LOADING_MODEL` to `READY` over 5 to 15 minutes.
Poll `/api/v1/gpu/status` until `"ready": true`, or run `vk-llm start`,
which polls for you.

Send the same chat request again. Expected: HTTP 200 with a `choices`
array. Add `"stream": true` to the body to receive server-sent events.

Stop the GPU:

```bash
curl -s -X POST "$H/api/v1/gpu/stop" -H "Authorization: Bearer $MGMT" | jq '{result, state}'
```

Expected: HTTP 202 with `"result": "accepted"` and `"state": "DRAINING"`,
or HTTP 200 with `"result": "already_stopped"`. Poll `/api/v1/gpu/status`
until `"state": "STOPPED"`.

Common errors: `401` means the key is missing or wrong for that route.
`429` means the per-key rate limit (60 requests per minute). `503` with
`"code": "gpu_unavailable"` on `start` means the provider had no matching
offer or a safety limit blocked the start; the `reason` field says which.

## On-demand mode

Everything above describes the chart default, `explicit` mode: you run
`vk-llm start` and the GPU stays off until you do. Setting
`config.lifecycle.provisioningMode: on-demand` starts the GPU from a chat
request instead; nobody runs `vk-llm start`, and a streaming chat client
needs no retry — it stays on the same held connection until the answer
arrives. Progress lines show a download percentage using the model size
resolved automatically from the chosen offer, falling back to GB
downloaded if the size is unknown.

What a user sees depends on the request:

- A **streaming** chat request gets `200 text/event-stream` at once, then
  one visible line with no prefix — "⏳ Starting the GPU (3–6 min).
  Progress is in the thinking block." — since OpenCode hides the thinking
  block by default and a stream with nothing visible looks stuck. While
  the GPU comes up, each progress checkpoint (offer found, GPU rented,
  model downloading, checksum verified, model loading) arrives as a
  `reasoning_content` delta prefixed `vk-llm › `, which most chat clients
  render as a collapsible "thinking" block (in OpenCode: Settings →
  Session → Thinking = show). The model's real answer follows in the
  same response.
- A **non-streaming** request gets `503 gpu_cold_start` with a
  `Retry-After` header at once; the client is expected to retry.
- If 32 streams are already held, a 33rd streaming request gets
  `429 queue_full`.

**Cost risk.** Any valid inference key can start the GPU this way — there
is no per-key "may start" permission. The monthly budget
(`config.budget.monthlyUSD`) is the hard cap regardless of who or what
triggered the start. Every start logs one `gpu_start` line naming its
origin (`explicit` or `demand`) and, for a demand start, the inference
key's id.

**Cancellation.** A start triggered by a chat request is cancelled if
nothing keeps waiting on it for `config.onDemand.demandGrace` (default
60s) — for example, the user closes the app before the GPU is ready. A
cancelled create still counts against the attempts-per-hour cap. A start
triggered by `vk-llm start` or the management API is never cancelled this
way.

**Envoy route timeout.** In on-demand mode the Envoy route's total timeout
must be at least 1200s (600s `config.onDemand.maxWait` + 600s
`config.model.generationTimeout`), not the 660s that is enough in
explicit mode.

**Timeouts per client:**

| Client | Total timeout | Idle timeout | How the design fits |
|---|---|---|---|
| Open WebUI | none (`AIOHTTP_CLIENT_TIMEOUT` unset) | none (`AIOHTTP_CLIENT_STREAM_IDLE_TIMEOUT` unset) | fits; if an operator sets an idle cap, 15s comments stay below it |
| Conduit | Open WebUI's (tokens arrive over Socket.IO) | Open WebUI's | same as Open WebUI |
| OpenCode | none; `headerTimeout` 300s | `chunkTimeout` 900s | `200` is sent at once; byte gaps ≤ 15s |
| Chatbox | none found | none found | fits |
| Msty | unconfirmed (closed source) | unconfirmed | manual test |
| Envoy route | 1200s (raised from 660s) | n/a | 600s `maxWait` + 600s `model.generationTimeout` |

See [ADR 0019](docs/adr/0019-on-demand-mode-progress-stream.md) for the
full design and [`specs/`](specs/) for the implementation spec (`CORE-220`).

## Remote control from GitHub Actions

Run the GPU lifecycle from the Actions tab, with the `gpu-control` workflow.

1. Go to the repository's Settings, then Environments. Create an
   environment named `gpu-control`. Under Deployment branches and tags,
   select "Protected branches only". `make repo-settings` does this
   step for you.
2. Nothing else to add here. Neither the management token nor the API
   URL is stored in GitHub: the workflow reads the token and the lab's
   domain from AWS SSM and builds `https://llm.<domain>` itself, masked
   in the log. If you run this project's own `vk-lab-platform`
   deployment, its `make account-up` creates the AWS role this workflow
   assumes and sets this repository's `vars.AWS_ROLE_ARN` for you. If you
   deploy against a different backend, point `gpu-control.yml`'s SSM step
   at it instead.
3. Go to the Actions tab, select `gpu-control`, and run it with the
   `action` input set to `start`, `status`, `stop`, or `budget`.

To change the budget from the Actions tab, set `action` to `budget` and
type the new amount in `monthly_usd`. Leave `monthly_usd` blank to show
the current budget.

The workflow reaches your installation over the network. Pick the option
that matches your setup:

| Access | When to use it | Note |
|---|---|---|
| HTTPS (default) | Your installation accepts connections from the internet. | The token is the only gate. The same rate limit and budget cap apply as for any other client. |
| VPN | You keep your installation off the public internet. | The workflow needs a self-hosted runner with access to the VPN. |
| Self-hosted runner | Your API host is not reachable from the internet. | Change `runs-on` in `gpu-control.yml` to point at your runner. The role and SSM setup does not change. |

Run one dispatch at a time. A second dispatch does not start until the
first run is complete. A `stop` can wait up to 20 minutes behind a
`start`, and the GPU continues to cost money during this time. A newer
dispatch replaces an older dispatch that waits in the queue. Wait for
each run to complete before you start the next run.

`gpu-control` never holds a provider credential. It only calls the
management API, with the same idempotent, budget-gated behavior as
`vk-llm`. A GPU started from Actions idles out on the usual 30-minute
schedule — the workflow does not reset it.

## Concurrent sessions and VRAM

`config.model.contextSize` is the context per session, not a total: the
worker reserves `contextSize * config.model.parallel` tokens of KV cache, so
raising `parallel` to run more sessions at once costs more VRAM even with
`contextSize` unchanged.

VRAM is roughly the model file plus the KV cache plus about 1.5 GB of
buffers. These are estimates, not measured numbers — confirm on your own
GPU before relying on them. For the lab's `Qwen3.8-27B` Q8_0 model
(28.6 GB), an `f16` KV cache costs about 12 GB per 262144 tokens of total
reserved context; `config.model.kCacheType` and `config.model.vCacheType`
shrink that: `q8_0` is about 53% of the `f16` size, `q4_0` about 28%. Four
sessions of 131072 tokens each (`contextSize: 131072`, `parallel: 4`) with
`q8_0` comes to roughly 28.6 + 13 + 1.5 ≈ 43 GB on a 48 GB GPU. The K cache
is more sensitive to quantization than the V
cache: `kCacheType: q4_0` hurts quality the most of the three settings; if
you need to go below `q8_0`, quantize `vCacheType` first.

In an OpenAI-compatible client, set the context limit (OpenCode's
`limit.context`) to `config.model.contextSize`, the per-session value, not
the total.

## OpenCode

This example uses the lab's model alias, `qwen3.8-27b`, not the chart
default (`config.model.alias: qwen3.6-35b-a3b`) — set it to whatever
`config.model.alias` your own install uses.

Set the inference key in the shell:

```sh
export VK_LLM_INFERENCE_KEY=sk-vkllm-...
```

On the lab, this key comes from the platform's secret store; never commit
it.

Then, in `~/.config/opencode/opencode.jsonc`:

```jsonc
{
  "model": "vk-llm/qwen3.8-27b",
  "providers": {
    "vk-llm": {
      "name": "vk-llm (on-demand GPU)",
      "package": "@opencode/ai/providers/openai-compatible",
      "settings": {
        "baseURL": "https://llm.lab.example.com/v1",
        "apiKey": "{env:VK_LLM_INFERENCE_KEY}"
      },
      "models": {
        "qwen3.8-27b": {
          "name": "Qwen3.8 27B (RTX 6000 Ada)",
          "limit": { "context": 262144, "output": 16384 },
          "variants": [
            { "id": "low",    "body": { "reasoning_effort": "low" } },
            { "id": "medium", "body": { "reasoning_effort": "medium" } },
            { "id": "xhigh",  "body": { "reasoning_effort": "xhigh" } }
          ]
        }
      }
    }
  }
}
```

To see the start-up progress, add a top-level
`"settings": { "session": { "thinking": "show" } }`, or press `ctrl+p` in
OpenCode and set Thinking to show.

Notes:

- `"model"` must be set, or OpenCode uses its free hosted model.
- The model id must match the gateway alias (`config.model.alias`), or the
  gateway returns `404 model_not_found`.
- `limit.context` must match `config.model.contextSize` (per session, see
  ["Concurrent sessions and VRAM"](#concurrent-sessions-and-vram)), or
  OpenCode's own truncation disagrees with the worker's.
- This model's template accepts `reasoning_effort` `low`, `medium`,
  `xhigh` and `none`, but not `high` (the gateway relays the worker's
  rejection).
- In on-demand mode, the first message starts the GPU. In explicit mode,
  run `vk-llm start` first; while the GPU is stopped, requests return
  `backend_unavailable` — see ["On-demand mode"](#on-demand-mode).
