# Roadmap

## 1. Documentation phases

| Phase | Deliverable | State |
|---|---|---|
| 1 Discovery and architecture | `overview.md`, `requirements.md`, `architecture.md`, `research.md`, `open-questions.md`, ADR 0001–0009 | done (2026-11-01) |
| 2 Technical specifications | `specs/core/000`–`140` | drafted and reviewed twice (2026-11-01); user approval pending |
| 3 Implementation planning | per-spec implementation steps and tests inside each spec; this roadmap | drafted |
| 4 Implementation | starts after the user approves phases 1–3 | not started |

## 2. Implementation milestones

Each milestone ends with something that runs. Estimates assume one
engineer with an agentic workflow; GPU spend is bounded per milestone.

| Milestone | Specs | Proves | Budget | Estimate |
|---|---|---|---|---|
| M1 Rent a GPU from a laptop | 010, 020, 030 | the provider client searches, creates, observes and destroys a real instance; the spike settles ADR 0006 and records cold-start stages | ≤ $5 | 3–4 sessions |
| M2 Controller with fake provider | 040, 050 | the state machine provisions, re-adopts, drains and destroys under a fake clock; orphan and budget rules hold in simulation | $0 | 3 sessions |
| M3 First completion from zero | 060, 070 | `curl` through the gateway starts a real GPU, streams tokens with heartbeats, idles out and destroys | ≤ $5 | 3–4 sessions |
| M4 Installable | 080, 120 | `helm install` from GHCR on kind with the fake provider; `main` publishes prereleases; release button tags | $0 | 2 sessions |
| M5 Operable | 090, 100, 110 | dashboard and alerts; budget stop; key rotation runbook; threat model closed | ≤ $3 | 2–3 sessions |
| M6 On the platform | 130, 140 | `https://llm.<fqdn>/v1/models` served with the platform certificate; the scripted acceptance run passes end to end | ≤ $10 | 2 sessions |

Order within M1: 010 → 020 → 030. The spike runs as early as possible
because two ADRs wait on it. If the spike selects fallback B, add one
session to M3 for the custom image.

## 3. Cross-repository work (vk-lab-platform)

Spec 130 lists the pull requests: `AppProject` + pointer `Application` at
wave 5, namespace `llm`, External Secrets IAM policy for the app-scoped SSM
prefix, KMS ciphertext files for the four secrets, Envoy timeout policy for
the route, golden render updates. None blocks M1–M5.

## 4. After the MVP

In rough priority, none started, none promised:

1. Concurrency 2 and 64K context once measured (Q9).
2. Hashed API keys and per-key usage counters.
3. A second provider implementation (Runpod or Civo) behind the same
   `Provider` interface, chosen by the first external user's need.
4. vLLM backend as a second worker profile behind the same readiness
   contract.
5. Multiple models with a per-model worker profile and `/v1/models` listing
   them; dynamic selection starts a worker per requested model.
6. More than one worker and a warm pool, which brings leader election or a
   stronger store (ADR 0004 seam).
7. Kubernetes CRD and operator once the state outgrows one document.
8. OpenCode/agent integration beyond `baseURL`: workflow leases used
   automatically, usage reporting.
9. OIDC and per-user quotas; a small dashboard page on `/admin`.
10. RAG and orchestration are separate projects that consume this API.
