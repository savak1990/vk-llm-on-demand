# Overview

## 1. Vision

Run a powerful open-weight model for personal coding assistants and future
agent workflows without paying for an always-on GPU. The user runs
`vk-llm start`, works with an ordinary OpenAI-compatible client such as
OpenCode, and runs `vk-llm stop` or lets a 30-minute rolling inactivity
timeout release the GPU. The control plane is one small Go service in a
Kubernetes cluster the user already runs.

```bash
vk-llm start      # rents one L40S, loads the model, waits for READY
opencode          # any OpenAI-compatible client, pointed at https://llm.<fqdn>/v1
vk-llm stop       # releases the GPU now; otherwise 30 min after the last inference
```

## 2. What the MVP does

- Exposes `POST /v1/chat/completions` (streaming, tool calls) and
  `GET /v1/models` behind inference keys, through the existing Envoy
  gateway with the platform's TLS. With no GPU running, inference returns a
  documented `backend_unavailable` error; nothing in the inference path
  rents a GPU (explicit provisioning, ADR 0010).
- Exposes a separate management API, `/api/v1/gpu/{start,status,stop,cost}`,
  behind management tokens, used by the `vk-llm` CLI and a GitHub Actions
  workflow (ADR 0012).
- Rents one NVIDIA L40S on Vast.ai on `start`, within a maximum hourly
  price, loads `Qwen3.6-35B-A3B` Q5_K_M into `llama-server`, and proxies
  requests over an SSH tunnel.
- Destroys the instance 30 minutes after the last inference finished
  (rolling, from readiness if none), immediately on `stop` after a 60-second
  drain, after 12 hours, or when the monthly budget is reached; destroys
  anything carrying its label that it does not recognise (ADR 0011).
- Keeps its state in one ConfigMap; survives its own restarts and the loss
  of that ConfigMap without leaking a GPU.
- Installs from one Helm chart on GHCR into any Kubernetes cluster with
  three Secrets; exports Prometheus metrics and JSON logs.

Defaults: $30 per month, $0.80 per hour, one instance, 30-minute rolling
inactivity timeout, 60-second drain on stop.

## 3. How it differs from Vast.ai Serverless

Vast Serverless already scales workers to zero and offers an OpenAI proxy,
but that proxy serves vLLM endpoints only, has no llama.cpp template, and
owns authentication, cold-start behaviour and budget policy
([research §1.8](research.md)). This project keeps those in the user's
cluster: GGUF models on llama.cpp, the user's keys, the user's budget rules,
and a provider interface that can later point at Runpod or Civo.

## 4. Explicitly not in the MVP

Documented in [`roadmap.md`](roadmap.md) as future work, not started:

| Area | Future capability |
|---|---|
| Lifecycle | on-demand provisioning mode (ADR 0007), `stop --force` |
| Storage (P2) | Vast.ai persistent-volume model cache, preferred-host scheduling, `vk-llm cache *` (ADR 0014) |
| Providers | Runpod, Civo, multiple providers at once |
| Models | multiple configured models, dynamic selection, vLLM backend, multi-GPU |
| Scale | more than one worker, warm pools, batching schedulers |
| Platform | CRDs and an operator, GPU nodes joining the cluster |
| Access | OAuth/OIDC, per-user quotas, hashed keys, a web dashboard |
| Clients | OpenHands, Open WebUI, MCP integrations, stateful agent sessions, RAG |

## 5. Principles

1. Cost safety over aggressive scaling: every create is bounded, every
   destroy is confirmed, unknown instances die.
2. Simplicity over abstraction: one binary, one ConfigMap, four interfaces,
   stdlib Go.
3. Idempotent, recoverable infrastructure operations: labels as identity,
   compare-and-swap as the lock, provider as reality.
4. Separation of inference and infrastructure lifecycle: the inference
   path never rents or releases a GPU; the management API never sees a
   prompt; the worker knows nothing about the cluster.
5. Provider independence without speculation: one interface, one
   implementation, one fake.
6. Testability: fake provider, fake store, fake clock, deterministic
   simulations of the whole lifecycle.
7. Secure defaults: loopback-only inference, keys by reference, no logging
   of content.
8. Observable transitions: every state change is a metric and a log line.
9. Explicit trade-offs: fourteen ADRs, three of them Proposed.
10. Incremental delivery: twenty specs, each a testable slice.
11. Public by default: every file is written as if an attacker reads it.

## 6. Documents

| Document | Purpose |
|---|---|
| [`architecture.md`](architecture.md) | components, diagrams, configuration, boundaries |
| [`requirements.md`](requirements.md) | measurable acceptance criteria mapped to specs |
| [`research.md`](research.md) | verified facts with sources and confidence |
| [`open-questions.md`](open-questions.md) | unresolved decisions and risk register |
| [`roadmap.md`](roadmap.md) | milestones, documentation phases, future work |
| [`testing-strategy.md`](testing-strategy.md) | three tiers, one real installation, budget caps |
| [`adr/`](adr/) | decisions with alternatives and consequences |
| [`../specs/`](../specs/) | implementation specs with tests and definitions of done |
