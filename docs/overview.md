# Overview

## 1. Vision

Run a powerful open-weight model for personal coding assistants and future
agent workflows without paying for an always-on GPU. The user sends an
ordinary OpenAI-compatible request; if no GPU is running, one is rented,
loaded and used within minutes; twenty idle minutes later it is gone. The
control plane is one small Go service in a Kubernetes cluster the user
already runs.

## 2. What the MVP does

- Exposes `POST /v1/chat/completions` (streaming and non-streaming) and
  `GET /v1/models` behind bearer API keys, through the existing Envoy
  gateway with the platform's TLS.
- Rents one NVIDIA L40S on Vast.ai when demand arrives, within a maximum
  hourly price, loads `Qwen3.6-35B-A3B` Q5_K_M into `llama-server`, and
  proxies requests over an SSH tunnel.
- Hides a normal cold start from default SDK clients with a bounded wait
  and SSE heartbeats; offers an admin API for explicit warm-up, status,
  workflow leases and a kill switch.
- Destroys the instance after 20 idle minutes, after 6 hours, when the
  monthly budget is reached, or when the operator says so; destroys anything
  carrying its label that it does not recognise.
- Keeps its state in one ConfigMap; survives its own restarts and the loss
  of that ConfigMap without leaking a GPU.
- Installs from one Helm chart on GHCR into any Kubernetes cluster with
  three Secrets; exports Prometheus metrics and JSON logs.

Defaults: $30 per month, $0.80 per hour, one instance, 20-minute idle
timeout.

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
| Providers | Runpod, Civo, multiple providers at once |
| Models | multiple configured models, dynamic selection, vLLM backend, multi-GPU |
| Scale | more than one worker, warm pools, batching schedulers |
| Platform | CRDs and an operator, GPU nodes joining the cluster |
| Access | OAuth/OIDC, many keys with per-key quotas, a web dashboard |
| Workflows | agent orchestration, OpenCode integration beyond `baseURL`, RAG |

## 5. Principles

1. Cost safety over aggressive scaling: every create is bounded, every
   destroy is confirmed, unknown instances die.
2. Simplicity over abstraction: one binary, one ConfigMap, four interfaces,
   stdlib Go.
3. Idempotent, recoverable infrastructure operations: labels as identity,
   compare-and-swap as the lock, provider as reality.
4. Separation of inference and infrastructure lifecycle: the worker knows
   nothing about the cluster; the controller knows nothing about prompts.
5. Provider independence without speculation: one interface, one
   implementation, one fake.
6. Testability: fake provider, fake store, fake clock, deterministic
   simulations of the whole lifecycle.
7. Secure defaults: loopback-only inference, keys by reference, no logging
   of content.
8. Observable transitions: every state change is a metric and a log line.
9. Explicit trade-offs: nine ADRs, two of them Proposed until measured.
10. Incremental delivery: fifteen specs, each a testable slice.

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
