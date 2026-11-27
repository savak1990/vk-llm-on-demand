---
id: "CORE-280"
status: "DONE"
updated: "2026-11-27"
depends_on: ["CORE-120", "CORE-130", "CORE-160", "CORE-220", "CORE-230"]
adrs: ["0021", "0008", "0009", "0012", "0020"]
---

# 280 — A dev environment that follows `main`

**Status note:** DONE (2026-11-27). Merged as PR #71 with the platform half
in vk-lab-platform#125 (SHARED-220); released as v0.4.2 and promoted in
PR #73; live checks recorded under Evidence. A CI `helm upgrade` design
(2026-11-23) was implemented, reviewed and withdrawn before merge (#57,
vk-lab-platform#122); this Argo CD design replaced it after a spike. Decisions are in
[ADR 0021](../../../docs/adr/0021-installations-are-argo-applications-from-one-chart.md).
The platform half lands in `vk-lab-platform` as SHARED-220.

**Complexity:** M — version format, a chart fallback and hook, two value
files and an Open WebUI connection here; an installation list in the
platform.
**Risk:** medium — a second controller rents GPUs on the same account; the
platform refactor touches production's pointer.
**Estimated effort:** one to two sessions (4–6 h), across two repositories.
**Recommended model:** strong.
**Depends on:** [CORE-120](../120-D-release-pipeline/spec.md),
[CORE-130](../130-D-platform-integration/spec.md),
[CORE-160](../160-D-github-actions-remote-control/spec.md), CORE-220
(on-demand mode), CORE-230 (chat front end).

## Outcome

Every merge to `main` that publishes a build is installed, within minutes and
with no manual step, as release `gpu-on-demand` in namespace `llm-dev` at
`llm-dev.<fqdn>`, with production's configuration and a `dev-` model alias,
and proves itself with three read-only calls. Open WebUI offers the dev
model next to production's. Production (`llm`) changes only through a
reviewed PR that pins a released chart version. A further installation is
one platform list entry plus one value file.

## Scope

Includes: the `main` version format; the chart's image-tag fallback and
smoke hook; `gitops/values-dev.yaml` and the changes `gitops/` needs to
merge value files; production's pin reduced to `chartVersion`; the second
Open WebUI connection; the `target` input on `gpu-control.yml`; docs.
Platform (SHARED-220): the `vkllm.instances` list rendering pointer,
AppProject, ExternalSecrets and traffic policy per entry; Argo's namespace
list from it; the Open WebUI key list.

Excludes: pinning the chat app's version (a separate spec right after this
one); per-PR previews; a CI job that waits for dev; separate dev secrets or
a separate Vast.ai account; a dashboard namespace selector.

## Design and contracts

### Installations

| | Production | Dev |
|---|---|---|
| Namespace, host | `llm`, `llm.<fqdn>` | `llm-dev`, `llm-dev.<fqdn>` |
| Value files | `values.yaml` | `values.yaml`, `values-dev.yaml` |
| `chartVersion` | an exact release, e.g. `"0.4.0"` | `">=0.0.0-0"` |
| Model alias | the `values.yaml` alias | `dev-` + that alias |
| Alerts, dashboard | on | off |
| Smoke hook | off | on |
| Mode, budget, idle timeout | on-demand, $35, 30 min | the same |
| AppProject | `vk-llm-on-demand`, `llm` only | `vk-llm-on-demand-dev`, `llm-dev` only |

The release name is `gpu-on-demand` in both; the default `installationId`
differs because the namespace differs.

### Versions

`scripts/version.sh main <sha> <run>` prints
`<next patch>-main.<run>.g<sha7>`; `deploy.yml` passes `github.sha` and
`github.run_number`. Release and PR formats do not change. Argo CD orders
`main.57.g…` above `main.56.g…`; a release `0.4.1` sorts above every
`0.4.1-main.*`, and `0.4.2-main.*` sorts above `0.4.1`.

Old-format builds of the current base (`0.4.2-main.<sha7>`) sort above
new-format builds of the same base, because an alphanumeric identifier
outranks a numeric one. A patch release cut right after the switch moves
every later build to a new base, where no old-format build exists.

### Chart (`deploy/helm/gpu-on-demand`)

- An empty `image.tag` falls back to `.Chart.AppVersion`, which packaging
  sets to the version. A non-empty tag still wins, and `latest` stays
  forbidden. The pull policy stays `IfNotPresent` for a 40-hex tag and
  `Always` otherwise.
- `smokeTest.enabled` (default false) renders a Job with
  `argocd.argoproj.io/hook: PostSync` and
  `argocd.argoproj.io/hook-delete-policy: BeforeHookCreation`. In a curl
  image pinned by digest, it calls `GET /v1/models` with the first client
  key, and `GET /api/v1/gpu/status` and `/api/v1/gpu/cost` with the first
  management token, from the `existingSecrets.apiKeys` Secret, against the
  in-cluster Service. It retries every 10 s for up to 5 minutes and exits
  non-zero on any non-200. Tokens never appear on a command line. It never
  calls `start`, `stop`, `budget` or `chat/completions`.

### `gitops/`

- The child Application's `valuesObject` comes from the merged value files;
  `tag` is no longer required. New values: toggles for alert rules and the
  dashboard (default on) and `smokeTest` (default off).
- `values-dev.yaml` holds only the dev overrides in the table above.
- `make gitops-check` renders production and dev, asserts that dev differs
  from production only in namespace, host, chart version, image tag, alias,
  alert rules, dashboard and smoke test, and asserts the dev render's
  kinds.

### Open WebUI (`chat/`)

`openaiBaseApiUrls` lists `http://gpu-on-demand.llm.svc/v1` then
`http://gpu-on-demand.llm-dev.svc/v1`; the matching key list comes from the
`open-webui-env` Secret (platform). The default model stays production's
alias; the task model is unset, so background tasks (titles, search
queries) use the chat's own current model, dev or production.

### `gpu-control.yml`

A `target` input (`prod` default, `dev`) selects the host prefix; the
concurrency group is per target.

### Platform (SHARED-220)

`vkllm.instances` in the platform's `gitops/values.yaml`, for example
`[{namespace: llm, project: vk-llm-on-demand, valueFiles: [values.yaml]},
{namespace: llm-dev, project: vk-llm-on-demand-dev, valueFiles:
[values.yaml, values-dev.yaml]}]`. One set of templates renders, per entry:
the pointer Application (source this repository, path `gitops`, the value
files, `fqdn`), an AppProject that allows only that namespace, the three
ExternalSecrets (production's SSM parameters, chat key included) and the
BackendTrafficPolicy (production's timeout, one template). `argo-up.sh`
builds `application.namespaces` from the list. Production's rendered objects
keep their names and contents. The chat ExternalSecret gains the key list
for two connections.

## Requirements

- R1. Every merge to `main` that publishes a build MUST be installed in
  `llm-dev` with no manual step (ADR 0021 §2).
- R2. A merge that changes only `docs/`, `specs/` or `gitops/` MUST NOT
  publish a build; dev keeps its build and picks up `gitops/` changes
  directly.
- R3. Production MUST change only through a merged PR that pins a released
  `chartVersion`; nothing MUST install a build into `llm` automatically.
- R4. No cluster credential MUST be stored in GitHub or SSM for this
  feature, and `vk-llm-ci-role` MUST NOT gain any permission.
- R5. Each installation's Application MUST belong to an AppProject that
  allows only its own namespace (ADR 0021 §5).
- R6. Dev MUST use production's client key, management token, worker seed,
  SSH key and Vast.ai API key. A dev token is a production token; this is
  accepted.
- R7. Dev MUST run in production's provisioning mode with production's
  budget, price cap and idle timeout.
- R8. Dev MUST be reachable at `llm-dev.<fqdn>` with production's request
  timeout, from the same template.
- R9. Versions of `main` builds MUST sort by build order and MUST always be
  valid semver (ADR 0021 §3).
- R10. Production MUST pin only `chartVersion`; the image tag MUST come from
  the chart version (ADR 0021 §4). Until production pins a release that has
  the fallback, `values.yaml` keeps its `tag`, and `values-dev.yaml` clears
  it.
- R11. After a cluster rebuild, dev MUST return with no manual step.
- R12. A new installation MUST need only one platform list entry and one
  value file here. One that should appear in Open WebUI or `gpu-control`
  also needs one chat URL (in the platform list's order) and, for
  `gpu-control`, a `target` option.
- R13. Every dev instance MUST carry dev's own `installationId` in its
  label.
- R14. After each dev sync, the smoke hook MUST call the three read-only
  endpoints and fail the sync on any non-200 after 5 minutes; it MUST NOT
  call any endpoint that starts, stops or reconfigures a GPU, nor send
  inference.
- R15. Dev MUST enable the ServiceMonitor and MUST NOT install alert rules
  or the dashboard.
- R16. Open WebUI MUST list the dev model as a second model, named `dev-`
  plus production's alias, and MUST keep production's model as the default.
- R17. `gpu-control.yml` MUST take a `target` input (`prod` default, `dev`)
  and MUST NOT run two actions on one target at once.

## Implementation steps

1. Version format and the `deploy.yml` run number.
2. Chart: image-tag fallback; production values drop `tag`.
3. Chart: smoke hook, off by default.
4. `gitops/`: merged value files, toggles, `values-dev.yaml`, checks.
5. `chat/`: second connection.
6. `gpu-control.yml` `target` input; docs.
7. Platform: installation list (production objects unchanged), dev entry,
   Open WebUI key list, Argo namespace list.
8. Rollout, in this order: merge the platform PR (it creates the Open
   WebUI key list the chat change reads, and the dev pointer); merge this
   PR right after; run `make argo-up` (Argo's namespace list); cut a patch
   release; a promotion PR pinning only `chartVersion`; record the
   acceptance checks. Expected symptom until the release: the dev pointer
   resolves an old-format chart that still needs `image.tag`, so the dev
   Application shows a `ComparisonError` and Open WebUI logs errors for the
   dev connection; this ends at the release.

## Tests / acceptance criteria

- AC1. `make gitops-check` passes with production and dev renders; dev
  differs only in the listed values, and the kind list matches.
- AC2. `scripts/version.sh main <sha> 57` with a sha starting `0123456`
  prints `…-main.57.g0123456`, and `helm package` accepts it.
- AC3. After a merge, the `llm-dev` Application syncs the new version within
  10 minutes and its PostSync hook succeeds.
- AC4. The platform golden diff for production's pointer, project, secrets
  and traffic policy is comment-only, except an explicit
  `valueFiles: [values.yaml]` on the pointer (Argo's default made visible).
- AC5. `GET https://llm-dev.<fqdn>/api/v1/gpu/status` returns 200, and the
  deploy creates no Vast.ai instance.
- AC6. Open WebUI shows `dev-<alias>` next to `<alias>`, with production's
  model as the default.
- AC7. `gpu-control` with `target=dev, action=status` succeeds; with
  `target=prod` it reports production.

## Definition of done

Both pull requests merged; AC1–AC7 recorded under Evidence; ADR 0021
Accepted; `docs/testing-strategy.md` no longer says "one installation";
status DONE and folder letter `D` in the pull request that records the
evidence.

## Open questions

None. Settled 2026-11-27: a separate AppProject per installation;
production pins only `chartVersion`; dev in on-demand mode; the dev model as
a `dev-` alias; a platform installation list.

## Evidence

- Spike 2026-11-24 (throwaway; kind and Argo CD v3.5.1 from chart 10.4.0):
  `>=0.0.0-0` resolved over OCI, anonymous GHCR and a local registry;
  `0.2.1-main.11.*` was chosen over `.10.*` and `.9.*`; `0.2.1` over its own
  builds; `0.2.2-main.12.*` over `0.2.1`; a new push was seen in 214 s
  without a refresh and under 5 s with one. An all-digit sha with a leading
  zero made `helm package` fail ("version segment starts with 0"). Today's
  GHCR tags resolve to the highest sha text, not the newest build.
- Rollout 2026-11-27, in order: vk-lab-platform#125 merged, then #71;
  `make argo-up` (the first run used a platform checkout from before #125
  and changed nothing; the re-run moved the Argo release to revision 2 with
  `application.namespaces` `ahorro,llm,llm-dev`, chart 10.4.0); v0.4.2 cut
  (release run 45977659466); #73 pinned only `chartVersion: "0.4.2"`.
- AC1: `make gitops-check` → `ok (chart 0.4.2)`; the dev render's
  differences and kind list are asserted there (CI on #71 and #73).
- AC2: `test/version` `TestMainVersionHasRunAndPrefixedSha` and
  `TestHelmAcceptsAllDigitSha` (`0.4.2-main.57.g0123456` packages).
- AC3: before the release the dev Application showed the expected
  ComparisonError (`/image/tag minLength`) on an old-format build; after it,
  `llm-dev/gpu-on-demand` was Synced/Healthy at `0.4.2`, operation
  Succeeded, image `gpu-on-demand:0.4.2` (`Always`), and the PostSync Job
  `gpu-on-demand-smoke` completed: `GET /v1/models 200`,
  `GET /api/v1/gpu/status 200`, `GET /api/v1/gpu/cost 200`.
- AC4: platform golden diff for production's objects was comment-only plus
  the explicit `valueFiles: [values.yaml]` (reviewed in #125); after the
  merge production `llm/gpu-on-demand` stayed Synced/Healthy.
- AC5: `gpu-control` `action=status target=dev` (run 45978829613):
  `state=STOPPED gpu=n/a model=dev-inkling-2-mini budgetMonthUSD=30
  estimatedMonthUSD=0` — the deploy rented nothing.
- AC6: from inside the Open WebUI pod, with its own `OPENAI_API_BASE_URLS`
  and `OPENAI_API_KEYS` (2 URLs, 2 keys): production `200
  ['inkling-2-mini']`, dev `200 ['dev-inkling-2-mini']`;
  `DEFAULT_MODELS=inkling-2-mini`, `TASK_MODEL_EXTERNAL` unset. The model
  dropdown itself was not inspected in a browser.
- AC7: `gpu-control` `action=status target=prod` (run 45978959306):
  `state=READY gpu=RTX 6000Ada model=inkling-2-mini`; that GPU had started
  before the promotion and kept running through the controller restart.
- Production after #73: `llm/gpu-on-demand` 0.4.2 Synced/Healthy, image
  `gpu-on-demand:0.4.2`, pod 1/1. Seen, not caused by this work: the
  controller logs `worker: /metrics returned status 401` while a GPU runs.
