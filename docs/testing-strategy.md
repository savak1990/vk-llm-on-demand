# Testing strategy

Unlike `vk-ahorro`, this project runs **one installation** per provider
account. Every additional running copy is another process that can rent a
GPU against the same account and the same monthly budget. There are no
`-dev` or `-pr` installations and no preview workflow.

## Tiers

| Tier | Runs where | Provider and worker | Cost | Trigger |
|---|---|---|---|---|
| 1 Unit and simulation | `go test ./...` | fake provider, fake store, fake clock, fake dialer; deterministic scenarios for the whole lifecycle (spec 050) | $0 | every pull request |
| 2 Chart on kind | CI job (spec 080) | `provider.name=fake` plus a fake worker: a small Go SSE server that imitates llama-server's `/health`, `/v1/models`, `/v1/chat/completions` and `/metrics`, reached through the `direct` dialer | $0 | every pull request |
| 3 Acceptance | the single platform installation, or a temporary kind install on a laptop with its own `installationId` | real Vast.ai, budget-capped (`VKLLM_E2E_BUDGET_USD`, default 10) | ≤ $10 per run | manual (`make e2e` or `workflow_dispatch` with an environment that holds the keys) |

The spike (spec 030) is a one-off tier 3 run with a $5 cap.

## Rules

- Tier 1 and 2 never touch the network outside the cluster. A test that
  needs a real provider belongs to tier 3.
- Tier 3 runs with a dedicated client key and the admin key of the target
  installation. It shortens the idle timeout through configuration for the
  run, uses `POST /admin/v1/scale {desired: 0}` as the kill switch, and ends
  by asserting that no instance with the installation's label prefix exists.
- A laptop kind install for tier 3 sets `installationId` to a distinct
  value (for example `e2e-<user>`) so its first-boot orphan sweep stays
  inside its own prefix (ADR 0004 §6).
- Two tier 3 runs never overlap: the acceptance workflow uses a concurrency
  group with `cancel-in-progress: false`.
- Measurements from tier 3 (cold-start stages, TTFT, tokens/s, VRAM) are
  recorded in `docs/research.md` §7 and in the spec's Evidence section.

## What each spec proves

| Spec | Tier | Proves |
|---|---|---|
| 020 provider client | 1 (+ one read-only live check) | request shapes, filter, backoff |
| 030 spike | 3 | launch mode, tunnel, cold-start stages (settles ADR 0006) |
| 040 store | 1 + 2 | CAS semantics, RBAC, two-writer race on kind |
| 050 controller | 1 | every lifecycle scenario under a fake clock |
| 060 gateway | 1 | auth, limits, streaming, heartbeats, cancellation, shutdown |
| 070 worker | 1 (+ docker without GPU) | script render, checksum failure, dead-man exit |
| 080 chart | 2 | install, upgrade, uninstall, three route variants, kubeconform |
| 090 observability | 1 + 2 | catalog golden test, label audit, redaction, scrape on kind |
| 100 cost | 1 | budget, balance floor, max instances, month rollover |
| 110 security | 1 + 2 | redaction, RBAC minimality, rotation runbooks on kind |
| 120 pipeline | dry run | artifact names and tag ordering |
| 130 platform | 3 | route, certificate, ExternalSecrets, Argo health |
| 140 acceptance | 3 | the end-to-end scenarios in `requirements.md` |
