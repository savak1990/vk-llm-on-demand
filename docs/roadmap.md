# Roadmap

## 1. Documentation phases

| Phase | Deliverable | State |
|---|---|---|
| 1 Discovery and architecture | `overview.md`, `requirements.md`, `architecture.md`, `research.md`, `open-questions.md`, `testing-strategy.md`, ADR 0001–0009 | done (2026-11-01) |
| 1b Lifecycle update | ADR 0010–0014; explicit provisioning, 30-minute rolling idle, management API, `vk-llm`, GitHub Actions, public-repo security, P2 cache | done (2026-11-01) |
| 2 Technical specifications | `specs/core/000`–`180` | drafted and reviewed; user approval pending |
| 3 Implementation planning | per-spec steps and tests; this roadmap | drafted |
| 4 Implementation | starts after the user approves phases 1–3 | not started |

## 2. Priorities

| Priority | Scope | Specs |
|---|---|---|
| **P0 core and safety** | Go control plane, Vast.ai client, provisioning and deletion, desired-state reconciliation, inference gateway with streaming and tool calls, separate management and inference auth, explicit mode, 30-minute rolling idle, immediate stop with drain, cost-safety controls, persisted lifecycle state, Helm chart, baseline scanning | 000, 010, 020, 030, 040, 050, 055, 060, 070, 080, 090, 100, 110, 170 |
| **P1 initial-release developer experience** | `vk-llm` CLI with start/status/stop and readiness polling, GitHub Releases binaries, GitHub Actions remote control, OpenCode documentation, cost and lifecycle status reporting, README, security and release documentation, platform integration, acceptance run | 120, 130, 140, 150, 160 |
| **P2 cold-start and storage** | Vast.ai persistent volume (40–50 GB), GGUF cache reuse, preferred-host scheduling with price premium and fallback, `vk-llm cache *` | 180 (ADR 0014, Proposed) |
| **P3 future** | on-demand provisioning mode (ADR 0007), OpenHands, Open WebUI, multiple models, multiple slots, more providers, advanced scheduling, stateful agent sessions, MCP, long-term memory, vLLM | none yet |

P0 and P1 together are the initial release. P2 and P3 do not move earlier
without an explicit decision.

## 3. Implementation milestones

Each milestone ends with something that runs. GPU spend is bounded per
milestone.

| Milestone | Specs | Proves | Budget | Estimate |
|---|---|---|---|---|
| M1 Rent a GPU from a laptop | 010, 020, 030 | the provider client searches, creates, observes and destroys a real instance; the spike settles ADR 0006 and records cold-start stages | ≤ $5 | 3–4 sessions |
| M2 Controller with fake provider | 040, 050, 055 | start/stop through the management API against fakes; 30-minute rolling idle, immediate stop, restart recovery, orphan and budget rules hold under a fake clock | $0 | 4 sessions |
| M3 First completion from zero | 060, 070 | `curl` start, then a streamed completion through the gateway on a real GPU; idle-out observed with a shortened timeout | ≤ $5 | 3–4 sessions |
| M4 Installable and scanned | 080, 170, 120 | `helm install` from GHCR on kind with fakes; PR gate with gitleaks, govulncheck, trivy; `main` publishes prereleases; release cuts image, chart and `vk-llm` binaries | $0 | 3 sessions |
| M5 Operable | 090, 100, 110 | dashboard and alerts; budget stop; token rotation runbooks; threat model closed | ≤ $3 | 2–3 sessions |
| M6 Clients and platform | 150, 160, 130 | `vk-llm start/status/stop` from a laptop; `gpu-control.yml` from GitHub; `https://llm.<fqdn>` served with the platform certificate | ≤ $3 | 3 sessions |
| M7 Acceptance | 140 | the §12 workflow of `requirements.md` end to end, measurements recorded | ≤ $10 | 2 sessions |

Order within M1: 010 → 020 → 030; the spike runs as early as possible
because ADR 0006 waits on it. If the spike selects fallback B, add one
session to M3 for the custom image.

**Recommended next milestone:** M1. It is cheap, it de-risks the two
Proposed ADRs, and every later milestone depends on its measurements.

## 4. Cross-repository work (vk-lab-platform)

Spec 130 lists the pull requests: `AppProject` + pointer `Application` at
wave 5, namespace `llm`, External Secrets IAM policy for the app-scoped SSM
prefix, KMS ciphertext files for the five secret values, Envoy timeout
policy for the route (660 s or disabled in explicit mode), golden render
updates. Hand-created Secrets are the supported first deployment; none of
these block M1–M5.

## 5. After the initial release

1. P2 cache (spec 180) once the spike's download numbers justify it.
2. On-demand provisioning mode (ADR 0007) behind `lifecycle.provisioningMode`.
3. Concurrency 2 and 64K context once measured.
4. Hashed API keys; cosign-signed releases.
5. A second provider implementation behind the same `Provider` interface.
6. vLLM worker profile behind the same readiness contract.
7. Multiple models; one worker per requested model.
8. OpenHands and Open WebUI documentation.
9. OIDC and per-user quotas; a small dashboard page.
