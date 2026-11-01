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

`NNN-X-name`, for example `core/020-P-vastai-provider-client`.

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
| [010](core/010-P-repo-skeleton-and-ci/spec.md) | Go module, Makefile, CI gate, Dockerfile, lint | 000 |
| [020](core/020-P-vastai-provider-client/spec.md) | Vast.ai provider client and offer filter | 010 |
| [030](core/030-P-worker-launch-spike/spec.md) | Spike: launch mode, tunnel, cold-start timings | 020 |
| [040](core/040-P-state-store/spec.md) | ConfigMap state store with optimistic concurrency | 010 |
| [050](core/050-P-gpu-lifecycle-controller/spec.md) | GPU lifecycle controller and reconcile loop | 020, 040 |
| [060](core/060-P-gateway-openai-api/spec.md) | OpenAI-compatible gateway, auth, streaming, cold-start wait | 040, 050 |
| [070](core/070-P-worker-runtime/spec.md) | Worker onstart, model download, llama-server contract | 030 |
| [080](core/080-P-helm-chart/spec.md) | Helm chart `gpu-on-demand` | 060 |
| [090](core/090-P-observability/spec.md) | Metrics catalog, logs, dashboard, alerts | 050, 060 |
| [100](core/100-P-cost-controls/spec.md) | Budget, limits, orphan sweep, cost visibility | 050 |
| [110](core/110-P-security-and-secrets/spec.md) | Keys, rotation, limits, SSRF, threat model | 060, 080 |
| [120](core/120-P-release-pipeline/spec.md) | Publish, deploy and release workflows, OCI chart | 010, 080 |
| [130](core/130-P-platform-integration/spec.md) | Pointer Application and secrets path in `vk-lab-platform` | 080, 110, 120 |
| [140](core/140-P-e2e-acceptance/spec.md) | Scripted acceptance run on a real GPU instance | 070, 080, 100 |

## Order of work

Implement in numeric order, except that 030 (the spike) runs as soon as 020
can create an instance, because ADR 0006 waits on its result. Vertical
slices: after 060 a request can start a GPU and stream a completion; after
080 the chart installs it; after 120 it ships from `main`.
