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

Design phase. No code yet. The repository holds the architecture, fourteen
ADRs and twenty implementation specs. Implementation starts with
[`specs/core/010-D-repo-skeleton-and-ci`](specs/core/010-D-repo-skeleton-and-ci/spec.md)
once the specifications are reviewed.

## MVP in one paragraph

A Go binary (`gpu-on-demand`) runs as one Deployment in a k3s cluster behind
an existing Envoy gateway. Its management API (`/api/v1/gpu/*`, management
token) starts and stops one NVIDIA L40S on Vast.ai running `llama-server`
with `Qwen3.6-35B-A3B` (Q5_K_M GGUF). Its inference API (`/v1/*`, inference
key) proxies OpenAI-compatible chat completions, streaming and tool calls,
to that worker over an SSH tunnel, and answers `backend_unavailable` while
no GPU runs. State lives in one ConfigMap. A reconcile loop compares the
provider's instance list with that state every minute, destroys anything it
does not recognise, and enforces the 30-minute rolling inactivity timeout,
a monthly budget, a maximum hourly price and a maximum lifetime. The
`vk-llm` CLI and a GitHub Actions workflow are thin clients of the
management API. The first deployment target is the Hetzner k3s cluster managed by
[vk-lab-platform](https://github.com/savak1990/vk-lab-platform); the Helm
chart does not depend on it.

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
