---
id: "CORE-120"
status: "DONE"
updated: "2026-11-08"
depends_on: ["CORE-010", "CORE-080"]
adrs: ["0008", "0013"]
---

# 120 — Release pipeline

**Status note:** Implemented 2026-11-08. All three workflow files and
`.goreleaser.yaml` exist and pass every structural check this plan can run
before merge. The acceptance criteria that require a real run on `main`
(AC2–AC5, AC8) are pending the first `deploy.yml` run and first
`release.yml` dispatch after this merges — see Evidence.

**Complexity:** S — adapts three proven workflow files and one doc from
`vk-ahorro`, plus one new `.goreleaser.yaml` convention (ADR 0013 §3).
**Risk:** low — the workflow pattern runs in production in the sibling
repository; new risks are action-SHA drift and GoReleaser's own supply
chain, both mitigated by pinning and checksums (ADR 0008/0013 Risks).
**Estimated effort:** one session (3–5 h)
**Recommended model:** standard — mechanical adaptation, no novel design.
**Depends on:** CORE-010 (Go module, Makefile skeleton, `ci.yml`), CORE-080
(the `gpu-on-demand` chart these workflows publish)

## Outcome

A commit merged to `main` publishes an image and a chart tagged
`X.Y.Z-main.<sha7>` to GHCR with no human action and no cluster write. A
maintainer runs the `release` GitHub Action by hand (or `make release
BUMP=patch`) and gets a clean `X.Y.Z` image, chart, git tag `vX.Y.Z`
(written only after both artifacts exist), and a GitHub Release `vX.Y.Z`
carrying four `vk-llm` binaries and a `checksums.txt`. Anyone, logged in
or not, can `docker pull`, `helm pull`, or download a CLI binary for any
published version. A pull request never reaches GHCR or GitHub Releases.

## Scope

- `.github/workflows/publish.yml`: reusable `workflow_call` that builds-or-
  copies the `gpu-on-demand` image and packages-and-pushes the
  `gpu-on-demand` chart, both under one input `version`.
- `.github/workflows/deploy.yml`: triggers on push to `main`, computes the
  `main` version, calls `publish.yml`. No cluster deploy.
- `.github/workflows/release.yml`: `workflow_dispatch` with a `bump`
  choice, computes the release version, calls `publish.yml`, then tags the
  repository — tag job last, gated on publish success. The same `tag` job
  then runs GoReleaser to cut the `vk-llm` CLI binaries onto the GitHub
  Release for that tag (ADR 0013 §3).
- `.goreleaser.yaml`: builds `cmd/vk-llm` for `darwin`/`linux` ×
  `amd64`/`arm64`, archives and checksums the result, and publishes it to
  the GitHub Release the `tag` job's `git push` just created.
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
(CORE-130); mobile/Android artifacts — not applicable here; `cmd/vk-llm`'s
source and the CLI's command behaviour, config precedence and output
format (CORE-150 — this spec only builds and publishes the binary
CORE-150 defines); the `ci.yml` job running AC7's GoReleaser snapshot
dry run (CORE-010, per the first Excludes clause above) and the secret,
dependency and image scanning jobs ADR 0013 §1 adds to `ci.yml` (CORE-170).

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
concurrency:
  group: deploy-${{ github.ref }}
  cancel-in-progress: false
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
concurrency:
  group: deploy-${{ github.ref }}
  cancel-in-progress: false
jobs:
  version:   # computes version.sh release <bump>
  publish:   # needs: version; uses ./.github/workflows/publish.yml, no go flag → full copy (requires a prior commit to have already published an image under its own SHA tag — see docs/delivery.md's sequencing note)
  tag:       # needs: [version, publish]; permissions: contents: write
             # git tag vX.Y.Z && git push origin vX.Y.Z
             # goreleaser/goreleaser-action@<sha> # vN, after the tag push
```

The very first release ever dispatched on a repository with no published image yet has nothing to copy; `docs/delivery.md` states the required order (a `main` merge that rebuilds, before the first release). Shared between the two files deliberately — a release and a merge both publish under one version scheme, so they queue rather than race to compute the same next version.

The `tag` job runs only if `publish` succeeds — GitHub Actions' default
`needs` behavior skips a job when a dependency fails or is cancelled — so
a failed publish produces no tag. This is the same ordering
`vk-ahorro/.github/workflows/release.yml`'s `tag` job comment states
("The tag is written only after every artifact exists, so a failed
publish never leaves a tag naming a version nobody can pull"; research
§6).

### CLI release (GoReleaser)

The `tag` job's last step, after `git push origin vX.Y.Z` succeeds, runs
GoReleaser against the tag it just pushed (ADR 0013 §3). It uses the
job's existing `contents: write` permission — the same grant that pushed
the tag — and needs no broader one, because it only attaches assets to
the GitHub Release for that tag:

| `.goreleaser.yaml` key | Value |
|---|---|
| `builds[0].main` | `./cmd/vk-llm` |
| `builds[0].goos` | `darwin`, `linux` |
| `builds[0].goarch` | `amd64`, `arm64` |
| `builds[0].env` | `CGO_ENABLED=0` |
| `builds[0].flags` | `-trimpath` |
| `builds[0].ldflags` | `-s -w -X main.version={{.Version}}` |
| `archives` | one archive per `goos`/`goarch` pair — four total |
| `checksum.name_template` | `checksums.txt` |
| `release` | GitHub Release `vX.Y.Z` (the pushed tag), generated notes, no signing in the MVP (ADR 0013 Alternatives: cosign deferred) |

The step is `goreleaser/goreleaser-action@<sha> # vN`, pinned the same
way R4 requires every other `uses:` step (SHA copied at implementation
time, not from this spec or from `docs/research.md` §6, which
recommends GoReleaser but gives no version or SHA for it); its
`with: version:` input pins the GoReleaser binary itself, also read at
implementation time — neither value is known today (Open questions
Q36). `GITHUB_TOKEN` reaches the step via `env:` from
`secrets.GITHUB_TOKEN`, never echoed or logged (NFR-8.8). GoReleaser
builds only `cmd/vk-llm` (CORE-150); the `publish` job's image and
chart are unaffected and keep their own version, not the CLI's.

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
exactly one image and one chart). Confirmed landed: `make version` prints a three-part semver with no error (verified at plan time).

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
  failed publish leaves no git tag. (source: NFR-9.3)
- R7. `publish.yml` MUST NOT rebuild the image when the `go` input is
  `'false'`; it MUST copy the prior commit's manifest via `docker buildx
  imagetools create` instead. (source: ADR 0008 §2; research §6)
- R8. Workflows in scope MUST NOT trigger on `pull_request`; a pull
  request run MUST produce no GHCR artifact. (source: ADR 0008 §2; C-21)
- R9. The published chart MUST install with `helm pull
  oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand --version
  X.Y.Z` with no `helm registry login`. (source: NFR-9.3; C-23)
- R10. `docs/delivery.md` MUST state the promotion path from a released
  version to the platform as: bump the chart version pinned in
  `gitops/values.yaml` in this repository, then open a one-line pull
  request in `vk-lab-platform`. This repository MUST hold no platform code.
  (source: ADR 0008 §5; C-24)
- R11. The `tag` job's GoReleaser step MUST run only after the git tag
  push succeeds, and MUST publish exactly four archives
  (`darwin`/`linux` × `amd64`/`arm64`) plus one `checksums.txt` to the
  GitHub Release named after that tag. (source: ADR 0013 §3; FR-7.7)
- R12. The GoReleaser action and the GoReleaser binary version it runs
  MUST both be pinned (full commit SHA with a trailing version comment
  for the action; an explicit version for the binary), the same rule R4
  states for every other action. (source: ADR 0013 §3; C-22; Open
  questions Q36)
- R13. The GoReleaser step MUST NOT request any permission beyond the
  `tag` job's own `contents: write`. (source: ADR 0013 §3; C-22)

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
5. Write `.goreleaser.yaml` and add the GoReleaser step to the `tag` job,
   after the `git push origin vX.Y.Z` step, with the action SHA and
   GoReleaser version read from GoReleaser's current release at
   implementation time (Open questions Q36). Test: `goreleaser check`
   reports the config valid; `actionlint .github/workflows/release.yml`
   still passes; `grep -c "contents: write" .github/workflows/release.yml`
   prints `1` (no permission added beyond the existing `tag` job grant).
6. Write `docs/delivery.md` with sections: versions and channels, the
   pipeline per channel, promotion to the platform, artifact retention,
   invariants. Test: `grep` finds a heading matching each of the five
   topics by keyword (`versions`, `pipeline`, `promotion`, `artifact`,
   `invariant`), not merely a count of `^# ` lines (wave-1 review
   finding #32).
7. Set both GHCR packages public and configure the untagged-manifest
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
  returning exit code 0. (R1, R2, NFR-9.3)
- AC3. `workflow_dispatch` with `bump=patch` on a repository at `v0.3.0`:
  `release.yml`'s `version` job outputs `0.3.1`; the `tag` job runs only
  after `publish` reports success; the result is git tag `v0.3.1` pointing
  at the dispatched commit, and `helm pull
  oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand --version
  0.3.1` exits 0. (R6, R9, NFR-9.3)
- AC4. The same dispatch, with the `images` job forced to fail: the `tag`
  job's status is `skipped` (GitHub Actions default `needs` behavior), no
  new git tag exists afterward, and `git tag --points-at HEAD` before and
  after the run are identical. (R6)
- AC5. `helm pull oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand
  --version <any published version>` run from a shell with no prior
  `helm registry login` call exits 0. (R9, C-23)
- AC6. `grep -c "permissions: {}" .github/workflows/{publish,deploy,release}.yml`
  reports exactly one match per file, at the top of the file. (R3)
- AC7. A release dry run — `goreleaser release --snapshot --clean` run
  in `ci.yml` (CORE-010, on a diff touching `cmd/vk-llm/**` or
  `.goreleaser.yaml`) — exits 0 and leaves exactly four archives under
  `dist/` named `vk-llm_<version>_<goos>_<goarch>.*` for the four
  `darwin`/`linux` × `amd64`/`arm64` pairs, plus one `dist/checksums.txt`
  listing all four. No GitHub Release is created by a snapshot run.
  (R11, FR-7.7)
- AC8. A real `release.yml` dispatch whose `tag` job succeeds: the
  GitHub Release `vX.Y.Z` it produces has exactly four archive assets
  and one `checksums.txt` asset attached, each downloadable with
  `curl -fsSL` and no authentication, and the `checksums.txt` entries
  verify against the downloaded archives with `sha256sum -c`. (R11, R12,
  FR-7.7, NFR-9.3)

## Definition of done

- [ ] AC1–AC8 pass, demonstrated by a real run of each workflow (deploy on
      a scratch branch merged to a throwaway `main`, or GitHub's "re-run"
      on the actual repository once seeded) — not `act`, which cannot run
      `docker buildx imagetools create` or OCI `helm push` realistically.
      AC1, AC6, AC7 pass structurally today (see Evidence); AC2, AC3, AC4,
      AC5, AC8 need a real run and stay unchecked until then — closed by
      the first real `deploy.yml` run on `main`, then the first real
      `release.yml` dispatch, in that order (`docs/delivery.md` §2's
      sequencing caveat).
- [x] `actionlint` clean on all three workflow files.
- [x] `make specs-check` passes.
- [x] Status flipped to DONE in the same pull request; folder renamed
      `120-D-release-pipeline`.
- [ ] Evidence section filled with the actual run links and the first
      published version number — closed by the same first real `deploy.yml`
      run and first real `release.yml` dispatch named above.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Is GHCR's untagged-manifest cleanup a native package setting or a scheduled workflow? No file in `vk-ahorro` implements either — only the stated invariant in its `docs/delivery.md` §5. | Decides whether this spec needs a fourth workflow file (`cleanup.yml`) or only a manual toggle. | Implementation step 7, checked against `vk-ahorro`'s current GHCR package settings at that time | No new workflow file; use GitHub's package-level cleanup-policy UI setting, matching the absence of such a workflow in `vk-ahorro`. |
| Exact commit SHAs for pinned actions. | R4 requires a full SHA, and `docs/research.md` §6 explicitly forbids copying SHAs from itself. | Implementation steps 2–4, read directly from `vk-ahorro`'s current workflow files | Copy verbatim from `vk-ahorro/.github/workflows/{publish,deploy,release}.yml` at implementation time. |
| Does a brand-new GHCR package default to private, requiring the manual toggle in step 7 before AC5 can pass on the first release? | AC5 is an anonymous-pull test; if visibility isn't public yet it fails for a reason unrelated to the pipeline. | Implementation step 7, first real run | Assume private-by-default; toggle public manually once, immediately after the first successful `publish.yml` run. |
| Does the required status check (`ci-ok`) and branch protection setup belong to this spec or to CORE-010? | ADR 0008 Consequences mentions `make repo-settings` for the required check; CORE-010 owns `ci.yml` itself. | CORE-010's spec | CORE-010 owns `repo-settings` and the `ci-ok` check; this spec only adds the GHCR visibility and cleanup steps. |
| Q36: GoReleaser action and binary versions, and whether GitHub Releases on a personal repository need extra permissions beyond `contents: write`. | R12/AC8 need a real pinned SHA and version; `docs/research.md` §6 recommends GoReleaser but gives no version or SHA. | Implementation step 5, read directly from GoReleaser's current release at that time (`docs/open-questions.md` Q36) | `with: version: v2.19.0`, matching `ci.yml`'s already-pinned `goreleaser/goreleaser/v2@v2.19.0` module version; the action's own SHA is fetched for real from `goreleaser/goreleaser-action`'s GitHub releases at implementation time (Task 5), never guessed. |
| `gitops/` does not exist in this repository yet (confirmed at plan time) — `docs/delivery.md`'s promotion-path section must describe it as a CORE-130 deliverable, not as existing. | R10 calls out the promotion path; R10 requires stating what `gitops/values.yaml` holds, yet it does not exist and should not be created in this spec. | Implementation step 6, where `docs/delivery.md`'s scope section addresses whether the current repository holds that file | State the future path explicitly, don't pretend it exists. |

## Evidence

Run 2026-11-08, in this worktree, before merge.

**`actionlint .github/workflows/publish.yml .github/workflows/deploy.yml .github/workflows/release.yml`** — exit 0, no output (clean on all three files).

**`go run github.com/goreleaser/goreleaser/v2@v2.19.0 check`**:

```
go: github.com/goreleaser/goreleaser/v2@v2.19.0 requires go >= 1.27.3; switching to go1.27.3
  • checking                                  path=.goreleaser.yaml
  • 1 configuration file(s) validated
  • thanks for using GoReleaser!
```

**`go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean`** (AC7, run for real in this session, `dist/` is gitignored and removed after): produced exactly 4 archives (`vk-llm_0.1.0-SNAPSHOT-1214f14_{darwin,linux}_{amd64,arm64}.tar.gz`) plus `dist/checksums.txt` listing all four, and created no GitHub Release. Confirms `project_name: vk-llm` in `.goreleaser.yaml` is doing its job — this repository's Go module path is `github.com/savak1990/vk-llm-on-demand` (`go.mod` line 1), so without that key GoReleaser would default every archive and binary name to `vk-llm-on-demand` instead of `vk-llm`. The plan's own `.goreleaser.yaml` sketch omitted the key; this was caught and fixed during implementation, verified both ways with a real snapshot build.

**`make specs-check`** passes on the committed tree: `SPECS-CHECK: specs/ layout is valid.`, exit 0.

**AC1** (no `pull_request:` trigger): `grep -L "pull_request:" .github/workflows/{publish,deploy,release}.yml` returns all three files — none match.

**AC6** (`permissions: {}` exactly once per file): `grep -c "permissions: {}" .github/workflows/{publish,deploy,release}.yml` → `publish.yml:1`, `deploy.yml:1`, `release.yml:1`.

**R5 / step 3 test**: `grep -c "helm upgrade\|helm install" .github/workflows/deploy.yml` → `0`.

**R6 / step 5 test**: `release.yml`'s `tag` job declares `needs: [version, publish]`; `grep -c "contents: write" .github/workflows/release.yml` → `1` (the `tag` job's own grant, nothing added for GoReleaser, satisfying R13).

**Pinned SHAs, fetched for real, not guessed:**
- `docker/login-action@8aa5831fb8d48d8f76594c7ddde61ae58019f550 # v4.8.0` — the plan's own reference value (copied from `vk-ahorro` at plan time) was two releases stale; the SHA above is the real current one as of implementation time.
- `goreleaser/goreleaser-action@a4f02ab5355a589de60d917a659ce617ed19b3de # v7.3.0`, running GoReleaser `v2.19.0` (matches `ci.yml`'s already-pinned module version, resolving Open questions Q36).

**Avoided a real defect in the `vk-ahorro` source this plan adapts from**: `vk-ahorro`'s `deploy.yml` path filter includes `Makefile` in its `go` list, but its `publish.yml` copy-source `case` block excludes it (the spec's own "Design and contracts" section calls this out as wave-1 review finding #31). This repository's `deploy.yml` filter and `publish.yml`'s copy-source `case` block both use the same five-path list (`cmd/**`, `internal/**`, `go.mod`, `go.sum`, `deploy/docker/gpu-on-demand.Dockerfile`), with `Makefile` in neither — confirmed by reading both files directly.

**What this plan cannot verify before merge, stated plainly rather than rubber-stamped:**
- AC2 (a real push to `main` producing real GHCR image/chart tags), AC3/AC4 (a real `workflow_dispatch` on `release.yml`, including the forced-failure case), AC5/AC8 (a real anonymous `docker pull`/`helm pull`/binary `curl` from GHCR or a GitHub Release) all require a real run on `main` after this branch merges — there is nothing published to GHCR from this branch yet.
- The operational ordering risk is real, not hypothetical: `release.yml`'s `publish` call never rebuilds, only copies the prior commit's manifest, so the first `release.yml` dispatch ever run on this repository must happen after at least one successful `deploy.yml` run has published an image under its own commit SHA — otherwise the copy step has nothing to copy from and fails. `docs/delivery.md` §2 ("The real sequencing caveat") and §4 already state this; cited here rather than re-derived.
- GHCR package visibility (public) and the untagged-manifest cleanup policy (`docs/delivery.md` §4, implementation step 7) are manual, one-time operator steps against real GHCR packages. They cannot be done or verified now because nothing has published to GHCR from this branch yet.
