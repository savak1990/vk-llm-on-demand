# Specs

Specs are the implementation requirements for this repository. Each spec is a
folder with a `spec.md`. The format copies `vk-ahorro/specs/` and
`vk-lab-platform/specs/` so the three repositories read the same way.

| Folder | Holds | `id` prefix |
|---|---|---|
| `core/` | The MVP: constitution, provider client, state store, controller, gateway, worker, chart, pipeline, platform integration, acceptance | `CORE-` |

Architecture intent lives in [`../docs/architecture.md`](../docs/architecture.md).
Decisions live in [`../docs/adr/`](../docs/adr/). A spec cites the ADR it
implements and never restates a decision; if a spec needs a different
decision, the ADR changes first.

## Folder name

`NNN-X-name`, for example `core/030-D-worker-launch-spike`.

- `NNN` and the front-matter `id` are the stable identifiers. Never renumber
  them. Numbers step by ten; insert new specs into the gaps.
- `X` is the status letter. When `status:` changes, rename the folder in the
  same commit and update links to it. `make specs-check` enforces this.

## Status letters

| Letter | Meaning | `status:` values |
|---|---|---|
| `D` | done | `DONE` |
| `A` | active | `IN_PROGRESS`, `IN_REVIEW` |
| `P` | planned | `DRAFT`, `READY`, `BLOCKED` |
| `Z` | closed, not done | `DEFERRED`, `SUPERSEDED`, `CANCELLED` |

The front-matter `status:` is the source of truth; the letter is a coarse
view of it. The pull request that finishes a spec carries it to `DONE` in
the same change: front matter, folder letter, and the evidence section.

## Body format

Front matter (`id`, `status`, `updated`, `depends_on`, `adrs`), then:

1. `# NNN — Title`
2. `**Status note:**` and the bold-label block (Complexity, Risk, Estimated
   effort, Recommended model, Depends on)
3. `## Outcome` — one paragraph: what works when this spec is done
4. `## Scope` with an explicit `Excludes:` paragraph
5. `## Design and contracts` — interfaces, data, state transitions, failure
   conditions, concurrency rules, security constraints, observability
6. `## Requirements` — numbered, MUST / MUST NOT, each citing the ADR,
   requirement id, or sibling spec the rule comes from
7. `## Implementation steps` — ordered, each independently testable
8. `## Tests / acceptance criteria` — observable checks with numbers, not tasks
9. `## Definition of done`
10. `## Open questions` — each with the spec or spike that answers it
11. `## Evidence` — filled in when the spec is executed

## Index

| Spec | Delivers | Depends on |
|---|---|---|
| [000](core/000-P-constitution/spec.md) | Constitution: engineering invariants | — |
| [010](core/010-D-repo-skeleton-and-ci/spec.md) | Go module, Makefile, CI gate, Dockerfile, lint | 000 |
| [020](core/020-D-vastai-provider-client/spec.md) | Vast.ai provider client and offer filter | 010 |
| [030](core/030-D-worker-launch-spike/spec.md) | Spike: launch mode, tunnel, cold-start timings | 020 |
| [040](core/040-D-state-store/spec.md) | ConfigMap state store with optimistic concurrency | 010 |
| [050](core/050-D-gpu-lifecycle-controller/spec.md) | GPU lifecycle controller, 30-minute rolling idle, immediate stop, reconcile loop | 020, 040 |
| [055](core/055-D-management-api/spec.md) | Management API `/api/v1/gpu/*`, management tokens | 050 |
| [060](core/060-D-gateway-openai-api/spec.md) | OpenAI-compatible gateway, inference keys, streaming, `backend_unavailable` | 040, 050 |
| [070](core/070-D-worker-runtime/spec.md) | Worker onstart, model download, llama-server contract | 030 |
| [080](core/080-D-helm-chart/spec.md) | Helm chart `gpu-on-demand` | 060 |
| [090](core/090-D-observability/spec.md) | Metrics catalog, logs, dashboard, alerts | 050, 060 |
| [100](core/100-D-cost-controls/spec.md) | Budget, limits, orphan sweep, cost visibility | 050 |
| [110](core/110-P-security-and-secrets/spec.md) | Keys, rotation, limits, SSRF, threat model | 060, 080 |
| [120](core/120-D-release-pipeline/spec.md) | Publish, deploy and release workflows, OCI chart, GoReleaser CLI binaries | 010, 080 |
| [130](core/130-D-platform-integration/spec.md) | Pointer Application and secrets path in `vk-lab-platform` | 080, 110, 120 |
| [140](core/140-P-e2e-acceptance/spec.md) | Scripted acceptance run on a real GPU instance | 070, 080, 100, 150, 160 |
| [150](core/150-D-vk-llm-cli/spec.md) | `vk-llm` CLI: start, status, stop | 055 |
| [160](core/160-D-github-actions-remote-control/spec.md) | `gpu-control.yml` manual workflow | 055 |
| [170](core/170-P-security-ci-and-release/spec.md) | gitleaks, govulncheck, trivy, SECURITY.md, release checklist | 010, 120 |
| [180](core/180-D-persistent-volume-cache/spec.md) | Model cache on Vast.ai volumes: preferred cached host, 72 h rolling retention, storage in the budget | 050, 100, 210, 220, 310, 320 |
| [190](core/190-D-runtime-budget-reconfiguration/spec.md) | Runtime-mutable budget cap, with a fixed ceiling | 055, 100, 150, 160 |
| [200](core/200-P-per-start-parameters/spec.md) | Problem statement: model, quantization, GPU, idle timeout and price cap as per-start parameters (no design yet) | 055, 130, 150, 190 |
| [210](core/210-D-offer-cost-model/spec.md) | Offer selection by estimated session cost; caps on download, storage and disk | 020, 050, 070, 100 |
| [220](core/220-A-automatic-gpu-start/spec.md) | On-demand mode: a chat message starts the GPU, progress in the thinking block, unattended starts cancelled | 050, 055, 060, 070, 100, 130, 210 |
| [240](core/240-P-voice-input-output/spec.md) | Requirements: speech to text and text to speech in the chat front end (no design yet) | 060, 230 |
| [250](core/250-P-code-execution/spec.md) | Requirements: sandboxed code execution in the chat front end (no design yet) | 060, 230 |
| [260](core/260-P-image-generation/spec.md) | Requirements: image generation and image understanding (no design yet) | 050, 060, 230 |
| [270](core/270-P-google-sign-in/spec.md) | Requirements: "Sign in with Google" for the chat front end, household accounts only (no design yet) | 230 |
| [280](core/280-D-dev-environment-auto-deploy/spec.md) | Every build of main is installed in `llm-dev` by Argo CD from the same `gitops/` chart as production (newest-build version range), with a smoke hook and a `dev-` model in Open WebUI | 120, 130, 160, 220, 230 |
| [290](core/290-P-large-models-multi-gpu-cpu-offload/spec.md) | Spike: larger models on multi-GPU packs or with MoE experts in CPU RAM, as a separate installation; offer filter sized to the model (no design yet) | 070, 210 |
| [300](core/300-A-concurrent-sessions-kv-cache/spec.md) | Concurrent sessions: KV cache type (f16/q8_0/q4_0) and context per session | 070, 080, 130 |
| [310](core/310-A-cold-start-recording/spec.md) | Cold-start recording: rental history in the state document, real stage durations, `vk-llm rentals` | 040, 050, 090, 210 |
| [320](core/320-D-vast-volume-spike/spec.md) | Spike: Vast.ai volumes as a model cache (survive destroy, billing, delete, label) | 020, 070 |
| [330](core/330-P-interruptible-bid-rentals/spec.md) | Opt-in interruptible (bid) rentals: `min_bid` plus margin, on-demand fallback, interruption destroys and the next request restarts | 020, 050, 100, 180, 210, 220, 310 |
| [340](core/340-P-prewarm-on-client-open/spec.md) | Start the GPU when a client opens (Open WebUI user header, OpenCode plugin), before the first prompt (research, no agreed design) | 060, 180, 220, 230 |

## Order of work

Implement in numeric order, except that 030 (the spike) runs as soon as 020
can create an instance, because ADR 0006 waits on its result, and 180 waits
for 310 and the 320 spike. After 320 the operator's order is 330, then
340, then the 290 spike. Vertical slices: after 055 the GPU can be started
and stopped through the API; after 060 a started GPU streams a completion;
after 080 the chart installs it; after 120 it ships from `main`; after 150
and 160 a laptop and GitHub can drive it.
