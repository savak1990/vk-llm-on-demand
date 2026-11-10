# vk-llm-on-demand

A self-hosted, Kubernetes-native control plane that exposes an
OpenAI-compatible LLM API backed by GPU instances rented on demand. You
start the GPU, use any OpenAI-compatible client, and the GPU is destroyed
30 minutes after your last inference or the moment you stop it. The
control plane is one small Go service in an existing cluster.

```bash
vk-llm start      # rent one L40S on Vast.ai, load the model, wait for READY
opencode          # or any client with base URL https://llm.<fqdn>/v1
vk-llm stop       # release the GPU now (otherwise: 30 min after the last inference)
```

- Design intent: [`docs/architecture.md`](docs/architecture.md)
- Decisions: [`docs/adr/`](docs/adr/)
- Requirements and acceptance criteria: [`docs/requirements.md`](docs/requirements.md)
- Verified external facts with sources: [`docs/research.md`](docs/research.md)
- Implementation specs: [`specs/`](specs/)

## Status

The control plane works now. Its code is merged to `main`. These parts
are done:

- The GPU lifecycle controller
- The OpenAI-compatible gateway
- The management API
- The worker runtime
- Budget controls
- The Helm chart
- Observability
- The release pipeline

These parts are not yet done:

- Security hardening
- Platform wiring
- End-to-end acceptance tests
- The `vk-llm` CLI
- Remote control from GitHub Actions
- CI and release hardening
- Persistent-volume caching

See [`specs/`](specs/) for the full list. Each spec's status is `DONE` or
`PROPOSED`.

## How it works

A Go binary, `gpu-on-demand`, runs as one Deployment in a k3s cluster.
Requests reach it through an existing Envoy gateway.

The management API, at `/api/v1/gpu/*`, uses a management token. It
starts and stops one NVIDIA L40S GPU on Vast.ai. The GPU runs
`llama-server` with the `Qwen3.6-35B-A3B` model (Q5_K_M GGUF).

The inference API, at `/v1/*`, uses an inference key. It proxies
OpenAI-compatible chat completions, streaming, and tool calls to the
worker over an SSH tunnel. It returns `backend_unavailable` while no GPU
runs.

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

1. Install the Helm chart (see "Intended install" below).
2. Create the necessary secrets in the cluster (see "Intended install").
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

## OpenCode

```json
{
  "provider": {
    "vk-llm": {
      "npm": "@ai-sdk/openai-compatible",
      "options": {"baseURL": "https://llm.example.com/v1", "apiKey": "{env:VK_LLM_INFERENCE_KEY}"},
      "models": {"qwen3.6-35b-a3b": {"name": "Qwen3.6 35B-A3B (on-demand L40S)"}}
    }
  }
}
```

Run `vk-llm start` first; while the GPU is stopped, requests return
`backend_unavailable`.
