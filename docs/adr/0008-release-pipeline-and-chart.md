# ADR 0008: The release pipeline and chart conventions copy vk-ahorro

## Status

Accepted (2026-11-01).

## Context

The sibling repository `vk-ahorro` already solves image and chart delivery
to GHCR the way the user wants it: one version per repository derived from
a git tag, images tagged with the full commit SHA and the version, charts
pushed as OCI at the same version, SHA-pinned actions, least-privilege
workflow permissions, a single required check, copy-not-rebuild for
unchanged components, and a release button that tags last so a failed
publish never leaves a tag ([research §6](../research.md)). The user asked
for this to be redone the same way.

## Decision

### 1. Versioning

`scripts/version.sh` is copied unchanged: the base is the next patch after
the last tag; `main <sha>` yields `X.Y.Z-main.<sha7>`, `pr <n> <sha>` yields
`X.Y.Z-pr-<n>.<sha7>`, `release <bump>` yields the bumped triple; no `+build`
metadata. The initial seed tag is `v0.1.0`, created by spec 010.

### 2. Workflows

| Workflow | Copied from | Change |
|---|---|---|
| `ci.yml` | ahorro | jobs `changes`, `go`, `image`, `helm` (lint, kubeconform, package), `workflows`, `repo` (specs-check), `ci-ok`. No Flutter, no AWS OIDC, no domain-check (no domain is ever templated here). |
| `publish.yml` | ahorro | one image `gpu-on-demand`, one chart; same copy-or-build logic |
| `deploy.yml` | ahorro | on push to `main`: version + publish prerelease. **No** `helm upgrade` into a dev namespace in the MVP; there is one installation and Argo owns it. |
| `release.yml` | ahorro | `workflow_dispatch` with `bump`; publish, then tag |
| `dependabot.yml` | ahorro | github-actions, docker, gomod |

Actions are pinned to full commit SHAs with the version in a trailing
comment. Every workflow starts with `permissions: {}`. Checkout uses
`persist-credentials: false` except in the tag job.

### 3. Image

`deploy/docker/gpu-on-demand.Dockerfile`: `golang:1.27` builder with
`CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"`, cross-compiled for
`linux/amd64,linux/arm64`; runtime `gcr.io/distroless/static-debian12:nonroot`,
`USER 65532`, `EXPOSE 8080 9090`, OCI source label. Published as
`ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand:<sha>` and `:<version>`.
`latest` is never pushed.

### 4. Chart

One chart, `deploy/helm/gpu-on-demand`, published to
`oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand:<version>`.
Copied conventions: placeholder `version:` overridden by `--version`,
`appVersion` = commit SHA, `_helpers.tpl` with `svc.image`, `svc.pullPolicy`
(derived from the tag), required `image.tag`, `checksum/config` annotation,
pod and container security contexts (non-root 65532, read-only root FS,
no capabilities, `RuntimeDefault` seccomp), `automountServiceAccountToken`
**true** here because the state store needs the API (ADR 0004) with a
dedicated ServiceAccount and Role, CPU request + memory request and limit,
no CPU limit, probes on `/healthz` and `/readyz` on the internal port 9090.

New in this chart, all optional and off by default unless stated:

| Value | Default | Renders |
|---|---|---|
| `httpRoute.enabled` | false | Gateway API HTTPRoute (platform shape: `parentRefs platform-gateway/envoy`, `sectionName https`, `host` required) |
| `ingress.enabled` | false | `networking.k8s.io/v1` Ingress with class and annotations |
| `serviceMonitor.enabled` | false | ServiceMonitor on port 9090 |
| `networkPolicy.enabled` | false | egress to DNS, kube-apiserver, Vast API (443), SSH ports; ingress from the gateway namespace |
| `externalSecrets.enabled` | false | ExternalSecret objects for the three Secrets (ADR 0009) |
| `existingSecrets.{provider,apiKeys,ssh}` | required | names of pre-existing Secrets |
| `rbac.create` | true | ServiceAccount, Role, RoleBinding for the state ConfigMap |

`host` has no default and fails the render when `httpRoute.enabled` or
`ingress.enabled` is true. No hostname, domain or secret value appears in
`values.yaml`.

### 5. GitOps for the platform

`gitops/` in this repository is a tiny app-of-apps chart (as in ahorro) that
renders one Argo `Application` pointing at the OCI chart with an exact
pinned version, `host` built from the `fqdn` the platform passes, and
`ServerSideApply=true`. The platform-side pointer `Application` and
`AppProject` are spec 130's cross-repository deliverable.

### 6. Make targets

`go-build go-test go-lint specs-check helm-lint helm-template helm-package
helm-push image-build image-push version release buildx-init`, each with a
`##` doc line, copied from ahorro and trimmed.

## Alternatives considered

- **GitHub Releases and a changelog.** ahorro does not produce them and the
  user did not ask. Not added.
- **A dev namespace deployed by CI** (`llm-dev`). Would need a second set of
  real secrets and a second GPU budget. Rejected for the MVP; the kind-based
  install test with the fake provider (spec 080) covers the chart.
- **Chart in a separate repository.** Contradicts the brief.

## Rationale

The conventions are proven in the sibling repository, the user knows them,
and reusing them removes every delivery decision from this project's
critical path.

## Consequences

- The repository needs a seed tag before the first CI run, and the GitHub
  required check must be set to `ci-ok` by hand (`make repo-settings`).
- Multi-arch builds are kept although the cluster is amd64; the cost is
  build time only.
- Any divergence from ahorro's workflow files must be deliberate and noted
  in the file header, so future syncs stay easy.

## Risks

- Pinned action SHAs drift between the two repositories. Dependabot keeps
  both current; the research page warns not to copy SHAs from docs.
