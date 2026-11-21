# Security

This repository is public. Everything committed, built or published here
is visible to anyone, and a leaked credential can rent GPUs at the owner's
expense. The controls below are requirements, specified in
`specs/core/170-P-security-ci-and-release/spec.md` and decided in
`docs/adr/0013-public-repo-security-and-cli-release.md`.

## Reporting a vulnerability

Open a private security advisory on GitHub for this repository, or email
the maintainer listed on the GitHub profile. Do not open a public issue
for a credential leak or an exploitable flaw. Expect an acknowledgement
within 7 days.

## Supported versions

The latest `X.Y.Z` release and `main`. Prereleases (`-main.<sha>`) are not
supported.

## What never enters Git

Vast.ai API keys, management tokens, inference keys, worker seeds, SSH
private keys, kubeconfigs, cloud credentials, real hostnames or `fqdn`
values, real prompts or completions. Examples use placeholders such as
`sk-vkllm-REPLACE_ME`, `https://llm.example.com` and `<fqdn>`. Local
configuration files are ignored by `.gitignore`.

## Controls in CI

| Control | Tool | When |
|---|---|---|
| Secret scanning | gitleaks (pinned binary, sha256-verified) | planned (spec 170): the `security` job is a stub until it lands |
| Go vulnerabilities | govulncheck | planned (spec 170) |
| Static analysis | golangci-lint with gosec | every PR |
| Image vulnerabilities | trivy (pinned binary, sha256-verified) | planned (spec 170) |
| Actions supply chain | SHA-pinned actions, `permissions: {}`, actionlint, dependabot | every PR, weekly updates |
| Chart | helm lint, kubeconform, RBAC shape check | every PR |

## Runtime posture

Non-root distroless image, read-only root filesystem, no capabilities,
one ConfigMap of RBAC, TLS at the platform gateway, separate inference and
management credentials, constant-time comparison, no credentials in URLs,
flags, logs or metrics, request size and rate limits on both surfaces and
on upstream error bodies, the inference runtime bound to loopback on the
GPU host and reached only through an SSH tunnel, `/readyz` never echoes an
internal error.

The chart rejects `image.tag: latest`, a worker image with no tag or with
tag `latest`, and an Ingress without TLS unless `ingress.insecurePlaintext`
is set. The optional NetworkPolicy, when enabled, allows DNS only to
kube-dns, HTTPS and SSH only to public addresses, and metrics ingress only
from the monitoring namespace.

## GitHub Actions credentials

`VK_LLM_TOKEN` lives in the GitHub Environment `gpu-control`, deployable
from `main` only (ADR 0015). A workflow on any other branch cannot read it.
`make repo-settings` creates the Environment with that policy.

## Review policy

One maintainer. Branch protection with `enforce_admins` on; no required
reviewer. Every change passes the full PR gate (`ci-ok`) before merge.

## Release checklist

A release is cut only when all of the following hold:

- [ ] `ci-ok` green on the release commit, including gitleaks, govulncheck and trivy.
- [ ] Every GitHub Action in `.github/workflows` is pinned to a commit SHA with a version comment.
- [ ] Container base images are pinned by digest; no `latest` tag is built or pushed.
- [ ] `helm template` renders with placeholder values only; `values.yaml` holds no hostname or secret.
- [ ] `vk-llm` archives and `checksums.txt` are attached to the GitHub Release; checksums match the build log.
- [ ] GHCR packages for the image and chart are public and carry the release version.
- [ ] `docs/open-questions.md` lists no Critical security question as open.
- [ ] The release notes name the specs closed and any ADR changed.

Planned, not yet in place: cosign signatures for images and archives, and
SBOM publication.
