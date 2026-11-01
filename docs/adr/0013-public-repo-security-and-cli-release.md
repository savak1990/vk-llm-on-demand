# ADR 0013: Public-repository security controls in CI and a GoReleaser-built CLI release

## Status

Accepted (2026-11-01). Adds to [ADR 0008](0008-release-pipeline-and-chart.md);
ADR 0008's Status carries a pointer here.

## Context

The repository is public. Every commit, workflow, chart and release is
visible, and a leaked provider key or management token spends real money.
The reference repositories differ: `vk-ahorro` pins every action to a
commit SHA, starts workflows with `permissions: {}`, runs dependabot, and
verifies downloaded binaries with sha256, but runs no secret, dependency or
image scanner; `vk-lab-platform` runs `gitleaks/gitleaks-action@v2`
unpinned in its PR gate ([research §6](../research.md)). Neither publishes
GitHub Releases. The brief asks for secret scanning, dependency and image
vulnerability scanning, pinning, least privilege, a release checklist, and
CLI binaries on GitHub Releases.

## Decision

### 1. Scanning in the PR gate (`ci.yml`, spec 170)

| Control | Tool | How |
|---|---|---|
| Secret scanning | `gitleaks` **binary**, pinned version, sha256-verified download (ahorro's binary pattern, not the unpinned action) | on every PR over the diff and the full history; weekly scheduled full-history run |
| Go vulnerabilities | `govulncheck` (`golang.org/x/vuln/cmd/govulncheck`, pinned module version, run via `go run`) | on every PR; fails on reachable vulnerabilities |
| Static analysis | golangci-lint v2 with `gosec` (exists, ADR 0008) | unchanged |
| Image vulnerabilities | `trivy` binary, pinned, sha256-verified (the action was compromised in 2026, ahorro's `ci.yml` header) | on the PR-built image, fail on HIGH/CRITICAL with fixes available |
| Actions pinning | SHA-pinned actions with version comments; `actionlint` (exist) | unchanged |
| Dependency updates | dependabot: github-actions, docker, gomod (exists) | unchanged |
| Chart | `helm lint`, kubeconform (exist) | unchanged |
| Repository hygiene | `.gitignore` for local config and secret files; `examples/` hold placeholders only; CI greps for `sk-vkllm-`, `vast_`, private-key headers | spec 170 |

Scanning jobs run with `permissions: contents: read` and never upload
findings as artifacts that could contain secrets.

### 2. Release checklist (public)

`SECURITY.md` at the repository root states the supported versions, how
to report a vulnerability, and the checklist a release must pass: scans
green, actions pinned, images by digest, no `latest`, chart renders with
placeholders, CHANGELOG entry, checksums published, GHCR packages public.

### 3. CLI binaries

`release.yml`'s tag job runs GoReleaser (pinned action SHA, pinned
GoReleaser version) after the tag exists: builds `vk-llm` for
`darwin/arm64`, `darwin/amd64`, `linux/amd64`, `linux/arm64` with
`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`, publishes a GitHub Release
`vX.Y.Z` with archives and `checksums.txt`, no signing in the MVP
(cosign is the documented next step). The release version is the same
`X.Y.Z` as the image and chart (ADR 0008 §1). The control-plane image is
unchanged and still built by `publish.yml`.

### 4. Runtime hardening (restated for the public context)

Non-root distroless image, read-only root filesystem, no capabilities,
least RBAC (ADR 0004 §3), optional NetworkPolicy, TLS at Envoy, management
and inference credentials separate (ADR 0012 §3), no credentials in URLs,
flags or logs, request size and rate limits, safe error bodies.

## Alternatives considered

- **`gitleaks-action`.** Unpinned in the platform and license terms for
  organisations are unclear; the binary with a checksum has neither issue.
- **`trivy-action`.** Compromised once; the binary pattern already exists
  in ahorro for kubeconform and actionlint.
- **Snyk or GitHub Advanced Security.** Paid or organisation features; not
  needed at this size.
- **Signing releases with cosign now.** Worth doing; deferred to keep the
  first release small. Listed in `SECURITY.md` as planned.

## Rationale

Reuse the sibling conventions that are already safe (SHA pinning,
`permissions: {}`, dependabot, checksummed binaries) and fill the three
gaps the brief names with the smallest tools that close them.

## Consequences

- Spec 170 (new) owns the scanning jobs, `SECURITY.md`, `.gitignore`
  rules and the checklist; spec 120 gains the GoReleaser job and the
  CLI matrix; spec 010 gains `cmd/vk-llm`.
- CI time grows by a few minutes per PR (govulncheck, trivy).
- The platform repository's unpinned gitleaks action is a finding for that
  repository, recorded in `research.md`, not fixed here.

## Risks

- Scanner false positives block PRs. Mitigation: a committed
  `.gitleaks.toml` allowlist for fixture patterns, reviewed in the same PR.
- GoReleaser's own supply chain. Mitigation: SHA-pinned action, pinned
  version, checksums compared in the release job log.
