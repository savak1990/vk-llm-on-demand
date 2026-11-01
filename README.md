# vk-llm-on-demand

A self-hosted, Kubernetes-native control plane that exposes an
OpenAI-compatible LLM API backed by GPU instances rented on demand. The
GPU exists only while someone is using it: the first request starts one,
twenty idle minutes destroy it. The control plane itself is one small Go
service in an existing cluster.

- Design intent: [`docs/architecture.md`](docs/architecture.md)
- Decisions: [`docs/adr/`](docs/adr/)
- Requirements and acceptance criteria: [`docs/requirements.md`](docs/requirements.md)
- Verified external facts with sources: [`docs/research.md`](docs/research.md)
- Implementation specs: [`specs/`](specs/)

## Status

Design phase. No code yet. The repository holds the architecture, nine
ADRs and fifteen implementation specs. Implementation starts with
[`specs/core/010-P-repo-skeleton-and-ci`](specs/core/010-P-repo-skeleton-and-ci/spec.md)
once the specifications are reviewed.

## MVP in one paragraph

A Go binary (`gpu-on-demand`) runs as one Deployment in a k3s cluster behind
an existing Envoy gateway. It validates bearer API keys, and when a chat
completion arrives with no GPU running it rents one NVIDIA L40S on
Vast.ai, waits for `llama-server` to load `Qwen3.6-35B-A3B` (Q5_K_M GGUF),
then proxies the request and streams tokens back. State lives in one
ConfigMap. A reconcile loop compares the provider's instance list with that
state every minute, destroys anything it does not recognise, and enforces a
monthly budget, a maximum hourly price, an idle timeout and a maximum
lifetime. The first deployment target is the Hetzner k3s cluster managed by
[vk-lab-platform](https://github.com/savak1990/vk-lab-platform); the Helm
chart does not depend on it.

## Intended install

```bash
helm install gpu-on-demand \
  oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand \
  --namespace llm --create-namespace \
  -f values.yaml
```

Secrets (Vast.ai API key, gateway API keys, the SSH key for the worker
tunnel) are referenced by name from existing Kubernetes Secrets and never
appear in `values.yaml`.
