---
id: "CORE-120"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-010", "CORE-080"]
adrs: ["0008"]
---

# 120 — Release pipeline

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** S — adapts three proven workflow files and one doc from
`vk-ahorro`; no new design, one component instead of two.
**Risk:** low — the pattern already runs in production in the sibling
repository (ADR 0008); the only new risk is action-SHA drift between the
two repos (ADR 0008 Risks).
**Estimated effort:** one session (3–5 h)
**Recommended model:** standard — mechanical adaptation, no novel design.
**Depends on:** CORE-010 (Go module, Makefile skeleton, `ci.yml`), CORE-080
(the `gpu-on-demand` chart these workflows publish)

## Outcome

A commit merged to `main` publishes an image and a chart tagged
`X.Y.Z-main.<sha7>` to GHCR with no human action and no cluster write. A
maintainer runs the `release` GitHub Action by hand (or `make release
BUMP=patch`) and gets a clean `X.Y.Z` image, chart and git tag `vX.Y.Z`,
with the tag written only after both artifacts exist. Anyone, logged in or
not, can `docker pull` or `helm pull` any published version from GHCR. A
pull request run never reaches GHCR.

## Scope

- `.github/workflows/publish.yml`: reusable `workflow_call` that builds-or-
  copies the `gpu-on-demand` image and packages-and-pushes the
  `gpu-on-demand` chart, both under one input `version`.
- `.github/workflows/deploy.yml`: triggers on push to `main`, computes the
  `main` version, calls `publish.yml`. No cluster deploy.
- `.github/workflows/release.yml`: `workflow_dispatch` with a `bump`
  choice, computes the release version, calls `publish.yml`, then tags the
  repository — tag job last, gated on publish success.
- `docs/delivery.md` for this repository: channels, versioning, promotion
  to `vk-lab-platform`, artifact retention, invariants.
- GHCR package visibility (public) and the untagged-manifest cleanup
  policy for `gpu-on-demand` (image) and `charts/gpu-on-demand` (chart).

This spec calls, but does not define, `scripts/version.sh` and the
Makefile targets `version`, `release`, `buildx-init`, `image-build`,
`image-push`, `helm-package`, `helm-push` — CORE-010 owns all of them, it
lands first (wave-1 review finding #11).

Excludes: `ci.yml`'s lint/test/kubeconform/specs-check jobs and the
Makefile targets above (CORE-010); the chart's templates and values
(CORE-080); any `-dev` or `-pr` installation and any preview/PR deploy
channel — this repository runs exactly one installation and has neither
(`docs/testing-strategy.md`), a pull request only runs CI; a dev/staging
namespace deployed by CI — ADR 0008 §2 rejects it for the MVP; the
fake-worker image CORE-080's kind integration tests use
(`docs/testing-strategy.md` tier 2) — it is a CI-only test double that
`publish.yml` never builds or pushes; the platform-side pointer
`Application` and `AppProject` that consume the published chart
(CORE-130); mobile/Android artifacts — not applicable here.

## Design and contracts

### Versioning

CORE-010 owns `scripts/version.sh`, copied unchanged from
`vk-ahorro/scripts/version.sh` (ADR 0008 §1; research §6). This spec's
workflows only call it. One call per channel:

| Call | Produces | Used by |
|---|---|---|
| `version.sh base` | `X.Y.Z` (next patch after the last tag) | — |
| `version.sh main <sha>` | `X.Y.Z-main.<sha7>` | `deploy.yml` |
| `version.sh release <bump>` | bumped `X.Y.Z` (`patch`\|`minor`\|`major`) | `release.yml` |
| `version.sh pr <n> <sha>` | `X.Y.Z-pr-<n>.<sha7>` | unused: no `-pr` installation or preview workflow in this repository (`docs/testing-strategy.md`); kept because the script is copied unchanged |

No `+build` metadata is ever emitted: Helm rewrites `+` to `_`, which is not
valid semver (`vk-ahorro/scripts/version.sh` header comment, copied
unchanged by CORE-010). The seed tag `v0.1.0` is created by CORE-010 before
the first run; `version.sh` exits 1 with no tag present.

### Workflow shapes

`publish.yml` (`workflow_call`), adapted from `vk-ahorro/.github/workflows/publish.yml`:

| Input | Type | Default | Meaning |
|---|---|---|---|
| `version` | string | required | the version every artifact is published under |
| `go` | string | `'false'` | whether `internal/`, `cmd/`, `go.mod`, `go.sum`, the Dockerfile changed, so the image is rebuilt instead of copied |

Two jobs:

- `images`: `strategy.matrix` with one entry (`svc: gpu-on-demand`,
  `changed: ${{ inputs.go }}`) — kept as a matrix, not a flat job, because
  it is the shape `ci.yml` and `publish.yml` already share, and a second
  image is the only future change that must not touch this job's skeleton.
  Pushes `ghcr.io/${{ github.repository }}/gpu-on-demand:${{ github.sha }}`
  and `:${{ inputs.version }}` via `docker/build-push-action` when changed;
  otherwise copies the image the last commit that touched those paths
  already published, via `docker buildx imagetools create -t <repo>:<version>
  <repo>:<src-sha>`, where `<src-sha>` comes from `git log -1 --format=%H --
  cmd internal go.mod go.sum deploy/docker/gpu-on-demand.Dockerfile`.
  `provenance: false`; `platforms: linux/amd64,linux/arm64` (ADR 0008 §3).
- `charts`: `needs: images`; no change detection — `helm package` and
  `helm push` run every time, because packaging is cheap
  (`vk-ahorro/docs/delivery.md` §2.1; research §6). Pushes
  `oci://ghcr.io/${{ github.repository }}/charts/gpu-on-demand:<version>`
  with `appVersion` set to `${{ github.sha }}` via `helm package --app-version`.

`deploy.yml`, adapted from `vk-ahorro/.github/workflows/deploy.yml`:

```yaml
on:
  push:
    branches: [main]
    paths-ignore: ["docs/**", "specs/**", "gitops/**"]
permissions: {}
jobs:
  version:   # computes version.sh main <sha>; dorny/paths-filter on 'go'
  publish:   # needs: version; uses ./.github/workflows/publish.yml
```

No `deploy-dev` job and no mobile job — ADR 0008 §2 ("No `helm upgrade`
into a dev namespace in the MVP; there is one installation and Argo owns
it"). This repository has no `-dev` or `-pr` namespace and no preview
workflow at all (`docs/testing-strategy.md`): the only two channels that
exist are "CI only" (a pull request) and "publish" (`main` or a release).
Nothing in this spec triggers a Kubernetes write from CI.

The `go` filter paths and the `images` job's copy-source `git log` paths
(above) are the same list, deliberately: `cmd/**`, `internal/**`, `go.mod`,
`go.sum`, `deploy/docker/gpu-on-demand.Dockerfile`. `Makefile` is in
neither list — a `Makefile`-only change neither forces a rebuild nor
leaves the copy step pointing at a commit older than the last real build
(wave-1 review finding #31).

The fake-worker image CORE-080's kind integration tests use
(`docs/testing-strategy.md` tier 2, a Go SSE server that imitates
llama-server) is a CI-only test double. It has no row in the `images`
matrix, `publish.yml` never builds it and never pushes it to GHCR, and it
carries no version tag.

`release.yml`, adapted from `vk-ahorro/.github/workflows/release.yml`:

```yaml
on:
  workflow_dispatch:
    inputs:
      bump: { required: true, type: choice, options: [patch, minor, major] }
permissions: {}
jobs:
  version:   # computes version.sh release <bump>
  publish:   # needs: version; uses ./.github/workflows/publish.yml, no go/flutter flags → full copy
  tag:       # needs: [version, publish]; permissions: contents: write
             # git tag vX.Y.Z && git push origin vX.Y.Z
```

The `tag` job runs only if `publish` succeeds — GitHub Actions' default
`needs` behavior skips a job when a dependency fails or is cancelled — so
a failed publish produces no tag. This is the same ordering
`vk-ahorro/.github/workflows/release.yml`'s `tag` job comment states
("The tag is written only after every artifact exists, so a failed
publish never leaves a tag naming a version nobody can pull"; research
§6).

### Permissions and pinning

Every workflow file starts `permissions: {}` (C-22). Jobs grant only what
they use: `contents: read` + `packages: write` for `images`/`charts`;
`contents: write` for `tag` only. `actions/checkout` uses
`persist-credentials: false` everywhere except the `tag` job, which needs a
credential to push the tag.

Every `uses:` step is pinned to a full commit SHA with the released version
in a trailing comment, copied at implementation time from the current
`vk-ahorro/.github/workflows/{publish,deploy,release}.yml` — not from this
spec or from `docs/research.md` §6, which the research page itself warns
against (research §6: "Copy the SHAs from the source files at
implementation time, not from this page"). As of 2026-11-01 those files
pin: `actions/checkout` v7.0.1, `docker/login-action` v4.6.0,
`docker/setup-buildx-action` v4.4.1, `docker/build-push-action` v7.4.0,
`azure/setup-helm` v5.0.1, `dorny/paths-filter` v4.0.3 (research §6).

### Make targets this spec calls

CORE-010 defines `version`, `release BUMP=patch|minor|major`,
`buildx-init`, `image-build`, `image-push`, `helm-package`, `helm-push`
(wave-1 review finding #11). `release.yml`'s steps and
`make release BUMP=` invoke them as CORE-010 defines them; this spec adds
no Makefile target of its own and does not fix their parameters (`SVC=`,
`CHART=`, or neither — CORE-010's call to make, since this repository has
exactly one image and one chart).

### GHCR package visibility and cleanup

Both `ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand` and
`.../charts/gpu-on-demand` are set public by hand once, after the first
successful `publish.yml` run, the same way `vk-ahorro`'s equivalent
packages are public (`vk-ahorro/docs/delivery.md` §5: "The packages are
public, so Argo and a laptop both pull with no credential."; research §6).
Whether a new GHCR package defaults to private, requiring this manual step
before an anonymous pull works, is not stated in `docs/research.md` — see
Open questions.

Untagged manifests left by multi-arch image pushes, since nothing else
removes them, are removed by a scheduled cleanup — the same invariant
`vk-ahorro/docs/delivery.md` §5 states for its own packages, which does
not name a count or a mechanism. The exact mechanism (GitHub's
package-level cleanup-policy setting vs. a scheduled workflow) is not
fixed by any file in `vk-ahorro` either — see Open questions.

## Requirements

- R1. `publish.yml` MUST tag every image it pushes with both the full
  commit SHA and `inputs.version`, and MUST NOT push `latest`.
  (source: ADR 0008 §3; C-21)
- R2. The chart version pushed to `oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand`
  MUST equal the image tag published in the same run. (source: ADR 0008 §4; C-21)
- R3. Every workflow file in scope MUST start with `permissions: {}` and
  grant each job only the permissions it uses. (source: C-22)
- R4. Every `uses:` step MUST reference a full commit SHA with the version
  in a trailing comment, never a tag or branch. (source: C-22; ADR 0008 §2)
- R5. `deploy.yml` MUST NOT perform any `helm upgrade`, `helm install`, or
  other write to a Kubernetes cluster. (source: ADR 0008 §2, Alternatives)
- R6. `release.yml`'s `tag` job MUST depend on `publish` succeeding, so a
  failed publish leaves no git tag. (source: NFR-8.3)
- R7. `publish.yml` MUST NOT rebuild the image when the `go` input is
  `'false'`; it MUST copy the prior commit's manifest via `docker buildx
  imagetools create` instead. (source: ADR 0008 §2; research §6)
- R8. Workflows in scope MUST NOT trigger on `pull_request`; a pull
  request run MUST produce no GHCR artifact. (source: ADR 0008 §2; C-21)
- R9. The published chart MUST install with `helm pull
  oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand --version
  X.Y.Z` with no `helm registry login`. (source: NFR-8.3; C-23)
- R10. `docs/delivery.md` MUST state the promotion path from a released
  version to the platform as: bump the chart version pinned in
  `gitops/values.yaml` in this repository, then open a one-line pull
  request in `vk-lab-platform`. This repository MUST hold no platform code.
  (source: ADR 0008 §5; C-24)

## Implementation steps

1. Confirm CORE-010 has landed `scripts/version.sh` and the Makefile
   targets this spec calls (`version`, `release`, `buildx-init`,
   `image-build`, `image-push`, `helm-package`, `helm-push`). Test:
   `make version` prints a three-part semver with no error.
2. Write `publish.yml` with the `images` and `charts` jobs. Test:
   `actionlint .github/workflows/publish.yml` reports zero findings.
3. Write `deploy.yml` (version job with `dorny/paths-filter` on `go`,
   publish job). Test: `actionlint .github/workflows/deploy.yml` passes;
   `grep -c "helm upgrade\|helm install" .github/workflows/deploy.yml`
   prints `0`.
4. Write `release.yml` (version, publish, tag jobs). Test: `actionlint`
   passes; the `tag` job's `needs:` array contains `publish`.
5. Write `docs/delivery.md` with sections: versions and channels, the
   pipeline per channel, promotion to the platform, artifact retention,
   invariants. Test: `grep` finds a heading matching each of the five
   topics by keyword (`versions`, `pipeline`, `promotion`, `artifact`,
   `invariant`), not merely a count of `^# ` lines (wave-1 review
   finding #32).
6. Set both GHCR packages public and configure the untagged-manifest
   cleanup after the first `publish.yml` run succeeds once in CI. Test:
   `docker pull ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand:<version>`
   and `helm pull oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand
   --version <version>` both succeed from a shell with no `docker login`
   or `helm registry login` run.

## Tests / acceptance criteria

- AC1. A pull request run: no workflow file in `.github/workflows/` that
  reaches GHCR declares `pull_request` under its top-level `on:`. Checked
  by `grep -L "pull_request:" .github/workflows/{publish,deploy,release}.yml`
  returning all three files (none match). No image or chart tag appears
  under the PR's commit SHA in GHCR. (R8)
- AC2. A push to `main` with a Go change: `deploy.yml` runs, `go.outputs`
  is `'true'`, and GHCR ends up with image tags `<sha>` (40 hex chars) and
  `X.Y.Z-main.<sha7>` (7 hex chars), and chart tag `X.Y.Z-main.<sha7>` under
  `charts/gpu-on-demand`. Verified with `docker buildx imagetools inspect
  ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand:X.Y.Z-main.<sha7>`
  returning exit code 0. (R1, R2, NFR-8.3)
- AC3. `workflow_dispatch` with `bump=patch` on a repository at `v0.3.0`:
  `release.yml`'s `version` job outputs `0.3.1`; the `tag` job runs only
  after `publish` reports success; the result is git tag `v0.3.1` pointing
  at the dispatched commit, and `helm pull
  oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand --version
  0.3.1` exits 0. (R6, R9, NFR-8.3)
- AC4. The same dispatch, with the `images` job forced to fail: the `tag`
  job's status is `skipped` (GitHub Actions default `needs` behavior), no
  new git tag exists afterward, and `git tag --points-at HEAD` before and
  after the run are identical. (R6)
- AC5. `helm pull oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand
  --version <any published version>` run from a shell with no prior
  `helm registry login` call exits 0. (R9, C-23)
- AC6. `grep -c "permissions: {}" .github/workflows/{publish,deploy,release}.yml`
  reports exactly one match per file, at the top of the file. (R3)

## Definition of done

- [ ] AC1–AC6 pass, demonstrated by a real run of each workflow (deploy on
      a scratch branch merged to a throwaway `main`, or GitHub's "re-run"
      on the actual repository once seeded) — not `act`, which cannot run
      `docker buildx imagetools create` or OCI `helm push` realistically.
- [ ] `actionlint` clean on all three workflow files.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE in the same pull request; folder renamed
      `120-D-release-pipeline`.
- [ ] Evidence section filled with the actual run links and the first
      published version number.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Is GHCR's untagged-manifest cleanup a native package setting or a scheduled workflow? No file in `vk-ahorro` implements either — only the stated invariant in its `docs/delivery.md` §5. | Decides whether this spec needs a fourth workflow file (`cleanup.yml`) or only a manual toggle. | Implementation step 6, checked against `vk-ahorro`'s current GHCR package settings at that time | No new workflow file; use GitHub's package-level cleanup-policy UI setting, matching the absence of such a workflow in `vk-ahorro`. |
| Exact commit SHAs for pinned actions. | R4 requires a full SHA, and `docs/research.md` §6 explicitly forbids copying SHAs from itself. | Implementation steps 2–4, read directly from `vk-ahorro`'s current workflow files | Copy verbatim from `vk-ahorro/.github/workflows/{publish,deploy,release}.yml` at implementation time. |
| Does a brand-new GHCR package default to private, requiring the manual toggle in step 6 before AC5 can pass on the first release? | AC5 is an anonymous-pull test; if visibility isn't public yet it fails for a reason unrelated to the pipeline. | Implementation step 6, first real run | Assume private-by-default; toggle public manually once, immediately after the first successful `publish.yml` run. |
| Does the required status check (`ci-ok`) and branch protection setup belong to this spec or to CORE-010? | ADR 0008 Consequences mentions `make repo-settings` for the required check; CORE-010 owns `ci.yml` itself. | CORE-010's spec | CORE-010 owns `repo-settings` and the `ci-ok` check; this spec only adds the GHCR visibility and cleanup steps. |

## Evidence

None yet.
