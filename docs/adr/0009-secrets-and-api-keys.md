# ADR 0009: Secrets are referenced by name; the platform delivers them from KMS ciphertext through SSM

## Status

**Proposed** (2026-11-01). Becomes Accepted when the platform pull request in
spec 130 (SSM prefix and External Secrets policy) is merged. The chart-side
contract (§1–2) is final regardless.

## Context

The MVP needs five secret values: the Vast.ai API key (issued by Vast), one or
more inference client keys (generated), one or more management tokens
(generated), a worker seed (generated), and an SSH key pair for the worker tunnel (generated; the public half is registered in
the Vast account). The brief forbids committing keys, asks for Kubernetes or
externally managed Secrets, bearer-token compatibility, no OAuth, and a
rotation story.

The platform already has a secrets path: a KMS-encrypted ciphertext file per
value committed under `secrets/<project>/` in `vk-lab-platform`, decrypted
into SSM Parameter Store, delivered by External Secrets Operator into
Kubernetes Secrets. Its IAM policy on Hetzner currently allows exactly two
parameters ([research §5](../research.md)). The user asked whether to use
that path or GitHub repository secrets.

## Decision

### 1. The chart references existing Secrets by name

| Value | Secret keys | Used by |
|---|---|---|
| `existingSecrets.provider` | `VAST_API_KEY` | provider client |
| `existingSecrets.apiKeys` | `client-keys` (inference, one per line), `management-tokens` (one per line), `worker-seed` (32 random bytes) | gateway auth, management API auth, worker key derivation (ADR 0006 §4, ADR 0012 §3) |
| `existingSecrets.ssh` | `id_ed25519` (private key), optional `known_hosts` seed | tunnel dialer |

The chart never creates these Secrets from values. Anyone can install the
chart with `kubectl create secret` and no platform at all. On the platform
this hand-created path is the supported **first deployment**: Argo CD does
not manage the three Secrets, so nothing overwrites them; a cluster rebuild
requires recreating them by hand. §3 is the path that survives rebuilds.

### 2. Key format and validation

Client keys and management tokens are `sk-vkllm-` followed by 32 random bytes encoded
base64url, generated locally with `openssl rand -base64 32 | tr '+/' '-_' | tr -d '='`
(a `make keygen` target prints one). The gateway loads the keys at start and
on Secret change (file watch on the mounted volume), compares with
`crypto/subtle.ConstantTimeCompare`, and never logs them. The MVP stores
plaintext keys in the Secret; storing only hashes is the recorded upgrade.

### 3. Delivery on the platform

Source of truth for the platform installation is SSM Parameter Store under
`/<project>/apps/vk-llm-on-demand/{vast-api-key,client-keys,management-tokens,worker-seed,ssh-private-key}`,
populated from committed ciphertext files
`secrets/<project>/vk-llm-on-demand-<name>.enc`, one file per value, exactly
as the platform does for its own secrets. The platform pull request in spec
130 widens the External Secrets IAM policy to `ssm:GetParameter` on that
prefix, and this repository's `gitops/` renders the three `ExternalSecret`
objects (`externalSecrets.enabled: true` on the platform target).

GitHub repository secrets are **not** used for runtime secrets: this
repository's CI never deploys the runtime (Argo CD does), a GitHub secret
cannot be read back for rotation or audit, and it would be a second source
of truth. They remain available for CI-only needs.

### 4. Rotation

- **Client keys:** add the new key as a new line, roll it out (the pod
  reloads on Secret change), move clients, remove the old line. Zero
  downtime.
- **Management tokens:** same list procedure as client keys; the CLI and
  GitHub Actions can hold different tokens. **Vast key:** replace the value;
  the pod reloads. A failed Vast key is detected by the next provider call and
  raised as an alert, not a crash. **Worker seed:** rotate only while the GPU
  is STOPPED; a running instance's derived key would stop matching.
- **SSH key:** rotate only while the GPU is STOPPED (`desired` STOPPED, no instance):
  generate a new pair, register the public key in the Vast account, replace
  the Secret, then start again. Instances created afterwards use the
  new key. There is no dual-key window in the MVP.
- Rotation steps are a runbook in spec 110.

### 5. What is never logged or persisted

Authorization headers, prompts, completions, provider keys, SSH material.
Logs carry request ids, key ids (the first 8 characters after the prefix),
sizes and timings. An inference key is never accepted on `/api/v1/*` and a
management token never on `/v1/*` (ADR 0012 §3).

## Alternatives considered

- **GitHub secrets as source of truth, pushed by CI.** Needs an AWS role for
  this repository's CI and a push step; duplicates the platform path.
  Rejected.
- **Secrets in the chart values, encrypted with SOPS.** Adds a tool and a key
  distribution problem; the platform already solved this with KMS. Rejected.
- **HashiCorp Vault or OIDC-issued tokens.** Out of MVP scope by the brief.
- **API-key validation in Envoy** (SecurityPolicy with an external auth or
  API key filter). Would tie the chart to Envoy Gateway. The gateway
  validates; an optional Envoy rate-limit policy can be layered (spec 110).

## Rationale

Reference-by-name keeps the chart portable; the platform path keeps the
platform installation consistent with every other secret there. One
decision serves both audiences.

## Consequences

- Spec 130 carries a platform pull request before the first platform
  deployment; local and kind installs need only `kubectl create secret`.
- The pod mounts Secrets as files, not environment variables, so reloads
  work without a restart.
- The Vast account SSH key list becomes an operator-managed resource;
  documented in the deployment runbook.

## Risks

- The platform IAM policy change is a cross-repository dependency. The MVP
  can be verified end to end on the platform with hand-created Secrets
  before the policy lands.
- A leaked client key can start GPUs and spend the budget. Mitigation:
  budget caps (spec 100), per-key rate limit, and rotation.
