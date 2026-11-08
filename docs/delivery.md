# vk-llm-on-demand — Delivery

How a commit becomes a published artifact. This repository runs one chart
and one image, is installed into exactly one cluster by the platform
repository's Argo pointer, and writes nothing to a Kubernetes cluster
from CI. `docs/architecture.md` describes the shape; this document is the
pipeline that publishes it.

## 1. Versions and channels

One version number names the whole repository, carried by a git tag. The
image tag and the chart version are always the same string, and a version
never carries `+build` metadata — Helm rewrites `+` to `_` for an OCI tag,
and the rewritten string is no longer valid semver.

| Event | Version | Published |
|---|---|---|
| pull request | — | nothing (CI only) |
| merge to `main` | `X.Y.Z-main.<sha7>` | image and chart |
| `release` dispatch, bump `<level>` | `X.Y.Z` | image, chart, CLI binaries |

`X.Y.Z` is always the next patch above the last release tag, so a
prerelease sorts below the release it precedes. A maintainer picks the
real bump (`patch`, `minor`, `major`) only when dispatching `release.yml`.

## 2. The pipeline per channel

**A pull request** runs `ci.yml` only: build, test, lint, chart render,
`actionlint`, and a packaging dry run at a pull-request version that is
never pushed anywhere. Nothing reaches GHCR. `ci.yml` holds no
`packages: write`, so it cannot publish even by mistake.

**A merge to `main`** runs `deploy.yml`. It computes `X.Y.Z-main.<sha7>`,
decides whether the Go sources changed, and calls `publish.yml`, which
pushes the image (rebuilt if Go changed, otherwise copied from the prior
commit's manifest) and repackages the chart under that version. No step
in this channel touches a cluster.

**A `release.yml` dispatch**, run by hand from the Actions tab (or `make
release BUMP=patch`), computes the clean `X.Y.Z`, calls the same
`publish.yml`, and only after that publish succeeds writes the git tag
`vX.Y.Z` and pushes it. The last step then runs GoReleaser against the
tag it just pushed, attaching four `vk-llm` binaries and a
`checksums.txt` to the GitHub Release for that tag.

**The real sequencing caveat:** `release.yml` never rebuilds anything —
its call to `publish.yml` always takes the copy-the-prior-commit's-image
path. A release dispatched before `deploy.yml` has ever successfully
published an image has nothing to copy from, and fails. The first `main`
merge that touches a Go path must succeed through `deploy.yml` before the
first `release.yml` dispatch ever happens.

## 3. Promotion to the platform

`gitops/` does not exist in this repository yet. Once it is added,
promotion will mean bumping the pinned chart version in
`gitops/values.yaml` in this repository, then opening a one-line pull
request in `vk-lab-platform` that moves its `targetRevision` pointer to
match. This repository holds no platform code: no Argo `Application`, no
External Secrets policy, no SSM parameter. Until that file exists, a
released version sits in GHCR and nothing points the platform at it.

## 4. Artifact retention and GHCR runbook

Images and charts publish to `ghcr.io/savak1990/vk-llm-on-demand/`. Two
manual, one-time steps follow the first successful `publish.yml` run:

1. In the package settings for `gpu-on-demand` and
   `charts/gpu-on-demand`, set visibility to public, so `docker pull` and
   `helm pull` work with no credential.
2. On the same two packages, set the untagged-manifest cleanup policy
   through GitHub's package-level UI setting. No separate cleanup
   workflow file exists — a multi-arch push always leaves untagged
   manifests behind, and this setting is what removes them.

A `-main.<sha7>` tag is never deleted automatically; it is a useful record
of what each merge published. The full-SHA tag `publish.yml` also pushes
on every rebuild names the same manifest and is kept alongside it, not as
a separate cost. A pull request publishes no artifact to retain.

## 5. Invariants

1. No image is ever tagged or pushed as `latest`. Every image carries
   both the full commit SHA and the run's version.
2. The chart version pushed in a run always equals the image tag pushed
   in the same run.
3. Every workflow file starts `permissions: {}`; each job grants only the
   permissions its own steps use.
4. Every `uses:` step names a full commit SHA, never a tag or branch, with
   the human version in a trailing comment.
5. `deploy.yml` performs no `helm upgrade`, `helm install`, or any other
   write to a Kubernetes cluster.
6. `publish.yml` rebuilds an image only when its inputs changed; otherwise
   it copies the prior commit's manifest to the new tag.
7. No workflow that reaches GHCR triggers on `pull_request`; a pull
   request run produces no image or chart artifact.
8. The chart installs with a plain `helm pull --version X.Y.Z` and no
   `helm registry login`.
9. `release.yml`'s `tag` job writes and pushes the git tag only after
   `publish` has succeeded, so a failed publish leaves no tag naming a
   version nobody can pull.
10. The GoReleaser step runs only after that tag push succeeds, and
    publishes exactly four archives (`darwin`/`linux` ×
    `amd64`/`arm64`) plus one `checksums.txt` to the GitHub Release for
    that tag.
11. The GoReleaser action and the GoReleaser binary version it runs are
    both pinned, the same way every other action is pinned.
12. The GoReleaser step requests no permission beyond the `tag` job's own
    `contents: write`.
13. This repository holds no platform code; a released version reaches
    the cluster only through a pull request in `vk-lab-platform`.
