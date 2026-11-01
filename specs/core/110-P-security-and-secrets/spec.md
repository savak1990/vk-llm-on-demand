---
id: "CORE-110"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-060", "CORE-080"]
adrs: ["0004", "0006", "0007", "0008", "0009", "0010", "0012", "0013"]
---

# 110 — Security and secrets

**Status note:** DRAFT, written 2026-11-01 before any code exists. update-1:
the admin key is gone; credentials are three independent Secret keys
(`client-keys`, `management-tokens`, `worker-seed`, ADR 0012 §3), and the
worker API key derives from `worker-seed`, not a management credential
(ADR 0006 §4, amended in place). Neither derivation nor the RBAC shape
(ADR 0004 §3) is re-argued here.

**Complexity:** M
**Risk:** medium — a wrong rotation runbook or a missed log call leaks a
credential that spends real money
**Estimated effort:** one to two sessions (4–6 h), including one kind run
of each runbook
**Recommended model:** strong — RBAC shape, key derivation and redaction
must be exact
**Depends on:** [CORE-060](../060-P-gateway-openai-api/spec.md), [CORE-080](../080-P-helm-chart/spec.md)

## Outcome

Every secret this project touches (client keys, management tokens, the Vast key, the SSH key pair, the worker seed) has a documented format, storage location, reload path and rotation runbook an operator can follow without reading Go source. A client key gets 403 on every `/api/v1/*` route and a management token gets 403 on every `/v1/*` route, decided in one shared place, not duplicated per handler. The rendered RBAC Role has exactly the verbs ADR 0004 §3 allows, checked by a script, not a reviewer's eyeball. A reviewer reads the threat model table and sees, per asset, what already stops the attack and what residual risk is accepted on purpose.

## Scope

- The threat model table.
- Key format, `make keygen` (already built by CORE-010; this spec fixes its regex and the key-id convention for logs) and the Secret layout.
- `internal/auth`: the shared loader both the gateway and the management API call — two lists, hot-reload by polling (not fsnotify), and surface tagging so a key valid on one list is still rejected, not silently accepted, on the other surface.
- Five rotation runbooks: client keys, management tokens, worker seed, Vast key, SSH key pair.
- The worker API key's final design (ruling) and its rotation consequence.
- Request limits (C-19) with their configured values, shared across both surfaces.
- Optional Envoy policy examples, explicitly not chart templates.
- `scripts/check-rbac.sh` (`helm` CI job, CORE-080).
- `internal/obs/obstest.AssertNoSecret`, a shared test helper for redaction tests.
- `automountServiceAccountToken` justification and NetworkPolicy's role as defence in depth.

Excludes: the gateway's and the management API's auth *middleware* wiring (CORE-060 and CORE-055 own calling `internal/auth`; this spec states what the shared loader must do, not how each handler invokes it); the chart's Role/NetworkPolicy templates (CORE-080 owns the YAML; this spec states the shape they must render); the SSH `Dialer` implementation (CORE-050); the platform's External Secrets/SSM delivery (CORE-130, ADR 0009 §3); hashed-at-rest keys (ADR 0009 §2, recorded upgrade, not MVP); CI secret scanning — `gitleaks` is CORE-170's job (ADR 0013 §1); this spec requires only the invariant it enforces (R12) and owns no script for it.

## Design and contracts

### Threat model

| Asset | Threat | Control | Residual risk |
|---|---|---|---|
| Client (inference) key | Leaked via misconfiguration, a pasted log, or phishing | Mounted file, `crypto/subtle.ConstantTimeCompare`, never logged (ADR 0009 §2, §5; C-16); per-key rate limit `gateway.rateLimitPerKeyRPM` 60; cannot start or stop anything — explicit mode never lets the inference path provision (ADR 0010 §2); runbook A | Can exhaust an already-READY instance's rate limit and budget until rotated; bounded by `budget.monthlyUSD` (FR-6.3) and the rate limit, not eliminated |
| Management token | Leaked, or abused by repeated calls, to start/stop/cost on `/api/v1/*` | Separate `management-tokens` list, same compare/log controls; 403 on `/v1/*` (NFR-8.1); `lifecycle.safety.maxProvisionAttemptsPerHour` 3 (FR-6.5) bounds repeated starts; budget cap; runbook B | Up to 3 starts/hour are still purchasable before the attempt cap stops new creates; the budget cap is the floor |
| GitHub Actions secret exposure | `VK_LLM_TOKEN` echoed in a workflow log, or the workflow re-dispatched to abuse start/stop | `permissions: {}`; the `action` input passed via `env:`, never interpolated into shell (ADR 0012 §5); concurrency group `gpu-control` without cancel; 25-minute timeout; the API never returns a token in any response (ADR 0012 §5, §6) | A compromised account dispatching the workflow repeatedly is bounded by the same attempt cap and budget as any other management-token use |
| Public-repo leak via fixtures/examples | A committed example config or test fixture contains a real-shaped secret | Placeholders only (`sk-vkllm-REPLACE_ME`, `<fqdn>`); R20 binds every file this spec adds; CORE-170's `gitleaks` runs on every PR and weekly over full history (ADR 0013 §1) | A secret committed before scanning existed, or encoded to evade the pattern, surfaces only on the next scheduled full-history run |
| CLI config file on a laptop | A stolen or synced laptop exposes `~/.config/vk-llm/config.yaml` | The file holds only `api.endpoint` and `api.tokenEnv`; it never holds the token itself (ADR 0012 §4; FR-7.6) | The token still lives wherever the user's shell environment puts it; outside this file and this spec's control |
| Cross-surface key use | An inference key is tried against `/api/v1/*`, or a management token against `/v1/*` | `internal/auth.Verify` tags every match with its surface before either handler decides 200 vs 403 (NFR-8.1, FR-7.2); path-level 403 tests (AC4) | A defect in the one shared `Verify` call is the single point that could grant cross-surface access — narrower than two independently written checks |
| Vast.ai API key | Leak lets an attacker create/destroy instances and read account balance | `existingSecrets.provider` key `VAST_API_KEY`, mounted file, never logged or echoed by the provider client | A leak is treated as a full account compromise until rotated (Open questions: whether a scoped token exists); the prepaid-balance floor (research §1.5) is the hard stop |
| SSH private key | Leak lets an attacker open a tunnel to any worker the account can reach | Mounted file, private half never leaves the cluster (ADR 0006 Consequences); only the public half reaches Vast | The account-level key is shared across every instance (research §1.3); rotation is permitted only while state is `STOPPED`, so no running instance depends on the old key (runbook E) |
| Worker host compromise (shared Vast machine) | A co-tenant process on the same host reaches the loopback port or reads env | `--api-key` derived from `worker-seed` required on llama-server (ADR 0006 §4) blocks an unauthenticated local caller; loopback-only bind (C-17); env values never echoed to disk or stdout (CORE-070 R7) | A host-level (hypervisor/co-tenant) compromise is outside this project's control surface; bounded by `maxLifetime` (12h) and idle destroy, not prevented |
| Prompt/completion disclosure in transit | A party on the Vast network segment reads request or response bytes | SSH-encrypted tunnel end to end (ADR 0006 §2); TLS from client to Envoy (architecture §6) | None known while both hops hold; a tunnel implementation defect is covered by CORE-050 tests, not this spec |
| SSRF via store tampering | A compromised ConfigMap or an over-privileged writer makes the controller dial an attacker host | The Dialer only ever receives `Instance.SSHHost/Port` as last written from a `Provider.GetInstance` response (ADR 0006 §5, C-18); RBAC restricts ConfigMap writes to this release's own ServiceAccount (ADR 0004 §3); a host-key change on a pinned instance is a hard failure and a destroy (ADR 0006 §3) | A pod that already holds this RBAC's `update` verb could poison the host on the very first write of a generation, before any fingerprint is pinned; accepted, because that pod already controls the Dialer directly at that point |
| Budget drain | Sustained abuse or a stuck instance accrues cost beyond intent | `budget.monthlyUSD` 30 with `budget.warnFraction` 0.8 alert (FR-6.4); `maxLifetime` 12h forced drain; worker dead-man timer independent of the control plane (FR-4.5) | The ledger is an estimate, rebuilt from zero on store loss (ADR 0004 §6); Vast's prepaid balance (research §1.5) is the real floor |
| Log leakage of secrets or prompts | A debugging change or a library default logs a header or body | Authorization, prompts, completions, provider keys and SSH material are never logged (NFR-8.7; C-16); redaction tests below | A new call site added later is caught only where a redaction test already exists; coverage is enumerated, not exhaustive (open question) |
| Supply chain (image digest, pinned actions) | A compromised base image or Action substitutes code at build or release time | `worker.image` and the distroless runtime pinned by digest, never a tag (ADR 0008 §3); every `uses:` step pinned to a full commit SHA (C-22); `gitleaks`/`trivy` run as sha256-verified binaries, not actions (ADR 0013 §1); Dependabot tracks both | A digest pin is only as good as the one verification that it is the intended artifact; Dependabot pull requests still need human review (ADR 0008 Risks) |

### Key format and `make keygen`

Client keys and management tokens are `sk-vkllm-` followed by 32 random bytes, base64url, no padding: 43 characters (ADR 0009 §2).

```
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

`make keygen` (CORE-010) already runs this and prints a `sk-vkllm-`-prefixed key; this spec fixes the acceptance regex: `^sk-vkllm-[A-Za-z0-9_-]{43}$`. For logs, the key id is the first 8 characters after the prefix (ADR 0009 §5); nothing else is ever logged.

`worker-seed` is a different shape: 32 random bytes, base64-encoded, with no `sk-vkllm-` prefix (ADR 0009 §1) — it is HMAC key material, never presented as a bearer credential, so R1's regex does not apply to it, and it is generated once at install (`openssl rand -base64 32`, no prefix-stripping) rather than by `make keygen`.

### Secret layout

```
existingSecrets.apiKeys:
  client-keys:       "sk-vkllm-<43>\nsk-vkllm-<43>\n…"  # one per line
  management-tokens: "sk-vkllm-<43>\nsk-vkllm-<43>\n…"  # one per line; CLI and GitHub Actions may each hold a different line
  worker-seed:       "<32 random bytes, base64>"          # not sk-vkllm-; never presented as a bearer credential
existingSecrets.provider:
  VAST_API_KEY: "<vast key>"
existingSecrets.ssh:
  id_ed25519: "<private key, PEM>"
  known_hosts: "<optional seed>"
```

(ADR 0009 §1; ADR 0012 §3.) Secrets are mounted as files under `/etc/vkllm/secrets/{apiKeys,provider,ssh}` (CORE-080), never as environment variables, so a reload needs no restart.

Hand-created Secrets via `kubectl create secret` in the `llm` namespace are a supported first-deployment path: Argo CD never manages them, since `existingSecrets.*` are references the chart reads, not objects it creates (ADR 0009 §1). They survive an `Application` sync but not a cluster rebuild — a rebuilt namespace needs every Secret recreated by hand before the chart installs. The External Secrets / SSM path (ADR 0009 §3, CORE-130) exists for survival across a cluster rebuild, not for the first deployment.

### `internal/auth`: one loader, two lists, surface-tagged

```go
package auth

type Surface string

const (
	Inference  Surface = "inference"  // /v1/*
	Management Surface = "management" // /api/v1/*
)

// Loader polls client-keys and management-tokens (os.Stat mtime, 10 s)
// and re-parses on change.
type Loader struct{ /* two []string, a mutex, a last-mtime per file */ }

// Verify reports which surface presented matches, if any; a match on the
// other surface still returns ok=true so the caller answers 403, not 200.
func (l *Loader) Verify(presented string) (surface Surface, ok bool)
```

`internal/gateway` accepts only `surface == Inference`; `internal/mgmt` (CORE-055) accepts only `surface == Management`; any other combination is a 403 (`ok` true, wrong surface) or a 401 (`ok` false) — one loader, one comparison path, the single place a cross-surface defect could live (the "Cross-surface key use" threat row).

`fsnotify` is not in the pre-approved module table (C-25); polling needs no watcher goroutine and works regardless of the volume plugin's update mechanism (Open questions). Ten seconds of staleness is fine: rotation is an operator action, not a per-request path. `worker-seed` is not part of this reload loop — it feeds the worker key derivation, not a presented credential; rotating it is runbook C, STOPPED-only, not a live reload.

### Rotation runbooks

Each step below is observable without reading source.

**A. Client key**

1. `make keygen` → new key. 2. Append it as a new line in `client-keys`. 3. Within 10 s, verify: the new key on `GET /v1/models` returns 200. 4. Move clients to the new key. 5. Remove the old line. 6. Verify: the removed key now returns 401 and `vkllm_gateway_auth_failures_total{reason="unknown_key"}` increments.

Zero downtime (ADR 0009 §4).

**B. Management token**

1. `make keygen` → new token (same command, same shape). 2. Append it as a new line in `management-tokens` — the CLI's token and the GitHub Actions token can be two different lines, rotated independently without invalidating each other (R19). 3. Within 10 s, verify: the new token on `GET /api/v1/gpu/status` returns 200. 4. Move the CLI config's `VK_LLM_TOKEN` or the GitHub repository secret to it. 5. Remove the old line. 6. Verify: the removed token now returns 401 on `GET /api/v1/gpu/status`.

Zero downtime: a management token never seeds the worker key, so rotating
one never forces a destroy (ADR 0006 §4, amended; ADR 0012 §3).

**C. Worker seed**

1. Rotate only while `desired` and state are both `STOPPED` — see Worker API key below for why a running instance cannot survive this rotation. 2. Generate a new 32-byte value (`openssl rand -base64 32`, no prefix-stripping) and replace `worker-seed`. 3. Within 10 s reload. Verify: `GET /api/v1/gpu/status` still reports `STOPPED`. 4. `POST /api/v1/gpu/start`. Verify: the new instance reaches `READY` normally — both the gateway and the onstart script derive its key from the already-rotated seed.

**D. Vast API key**

1. Issue a new key in the Vast console. 2. Replace `VAST_API_KEY`. 3. Within 10 s reload, plus up to one `controller.reconcileInterval` (60 s) for the next provider call to use it. Verify: `GET /api/v1/gpu/status` after one reconcile interval shows no new provider-error alert. 4. Revoke the old key in the Vast console.

A call still in flight on the old key is surfaced as a provider alert, not a crash (ADR 0009 §4).

**E. SSH key pair**

1. Rotate only while `desired` and state are both `STOPPED` (ADR 0009 §4: no dual-key window). 2. `ssh-keygen -t ed25519 -a 100 -N "" -f id_ed25519 -C vkllm-<release>`. 3. Register the new public key in the Vast console, "Manage Keys" page (research §1.3); do not remove the old one yet. 4. Replace `id_ed25519` in the `ssh` Secret. 5. Verify: a newly created instance connects with the new key (`vkllm_coldstart_stage_seconds{stage="link"}` records a normal value, not a fingerprint-mismatch destroy). 6. Remove the old public key from the Vast console once that instance links successfully.

### SSH specifics

Host-key policy is trust-on-first-use, pinned per instance in `Instance.HostKeyFingerprint` for that instance's lifetime (ADR 0006 §3, CORE-040 Design); a changed host key for a known instance is a hard failure and destroys the instance — not re-decided here, only required to be tested (AC7). Keepalive: the dialer sends one at least every 15 s, a fixed interval independent of any on-demand-only config key, and treats repeated keepalive failure as instance loss feeding the ADR 0003 §4 terminal-status path (R14; CORE-050 owns the `Dialer` code). Reconnect: a dropped tunnel to a still-Ready instance is retried with backoff by CORE-050; a reconnect MUST re-verify the pinned fingerprint before any request is proxied, never skip pinning on reconnect.

### Worker API key (ruling, final)

```go
// base64url(HMAC-SHA256(workerSeed, "worker:"+installationID+":"+generation)).
func WorkerKey(workerSeed []byte, installationID string, generation int64) string
```

Decided in wave 1, amended for update-1; ADR 0006 §4 already carries this. Never generated randomly, never persisted: the gateway (upstream `Authorization` header) and the onstart script's environment (`WORKER_API_KEY`, CORE-070) each recompute it from values already in the state document plus `worker-seed` already in memory. Consequence: rotating `worker-seed` while an instance is Ready changes the key the gateway computes but not the one llama-server was started with, so the next request fails upstream auth — runbook C's STOPPED-only rule exists because of this, not as a suggestion. Rotating a management token never touches this derivation and is zero-downtime (runbook B).

### Request limits (C-19)

| Limit | Value | Enforcement |
|---|---|---|
| Body size | `gateway.maxBodyBytes` = 1,048,576 | 413 before JSON decode |
| `max_tokens` / `max_completion_tokens` | `gateway.maxTokensCap` = 8,192 | silently clamped, clamp logged (not the body) |
| Per-key rate | `gateway.rateLimitPerKeyRPM` = 60 | 429 + `Retry-After`; applies to inference keys on `/v1/*` and management tokens on `/api/v1/*` alike (architecture §9) |

(architecture §9; NFR-8.5; C-19.) CORE-060 and CORE-055 each implement the middleware; this spec fixes the values and requires all three to be on by default, with no configuration path that disables them entirely.

### Optional Envoy policies (illustrative, not chart templates)

Marked optional per ADR 0009 Alternatives ("an optional Envoy rate-limit policy can be layered"). Not rendered by the chart; an operator applies these by hand or via the platform's own GitOps tree. CRD field names below are illustrative for Envoy Gateway v1.2.1 (research §5) and are **not** verified against that version's API — see Open questions.

```yaml
# Illustrative only; verify field names against the platform's Envoy
# Gateway v1.2.1 CRDs before applying (spec 130).
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
spec:
  targetRefs: [{group: gateway.networking.k8s.io, kind: HTTPRoute, name: gpu-on-demand}]
  timeout: {http: {requestTimeout: 660s}}  # explicit mode (MVP): model.generationTimeout+60s, or disabled; ADR 0010 Consequences
  # stream idle timeout 120s; exact field name verified against the CRD in CORE-130
  rateLimit: {type: Global, global: {rules: [{limit: {requests: 60, unit: Minute}}]}}
```

The timeout value is a hard requirement wherever such a policy is applied (R18); the rate-limit block is an optional second layer over the gateway's own per-key bucket, not a replacement for it. The 1,080 s figure of ADR 0007 §4 (`onDemand.maxWait + model.generationTimeout`) applies only if on-demand mode is ever enabled and is not rendered by the MVP chart.

### Redaction tests

`internal/obs/obstest` gains one test helper, used by every package that
holds a secret value in memory:

```go
// AssertNoSecret fails t if any secret appears in logOutput.
func AssertNoSecret(t *testing.T, logOutput []byte, secrets ...string)
```

It lives in its own `_test`-only-imported package, not in `internal/obs` itself, so `testing` never reaches the production binary. Each of `internal/auth` (both lists), CORE-020 (Vast key), CORE-050 (SSH material) and CORE-070 (worker key) adds at least one test that captures its own `slog` output and calls `AssertNoSecret`. This spec owns the helper and `internal/auth`'s own test; the sibling specs own calling it, per their own "never logged" requirement (NFR-8.7, C-16).

### RBAC minimality (ADR 0004 §3)

```yaml
kind: Role
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["<release>-state"]
    verbs: ["get", "update"]
```

One rule, no `create`, no `patch`, no `delete`, no `list`: the chart template creates the ConfigMap shell at install time (CORE-080), so the controller never needs `create` (ADR 0004 §3). `scripts/check-rbac.sh` renders the chart, extracts the `Role` with `yq`, and asserts this shape exactly — any extra rule or verb anywhere fails the check. Wired into the existing `helm` CI job (CORE-080).

### `automountServiceAccountToken`

True, because the Store's two HTTP verbs against the in-cluster API server are the only thing the pod needs it for (ADR 0008 §4, ADR 0004 §3). The ServiceAccount is dedicated to this release with only the Role above; no other outbound call in the gateway, controller, provider or tunnel code may attach this token — a review finding one built outside `internal/store/configmap` is a defect, not a style note.

### NetworkPolicy as defence in depth

CORE-080 renders an optional `NetworkPolicy` (`networkPolicy.enabled`, default `false`): egress to DNS, the kube-apiserver, Vast API, and a configurable SSH port range; ingress from the gateway's namespace only. It is defence in depth, not a substitute for C-18's SSRF boundary or C-16's auth check — both must hold with it disabled, the default on a cluster without a NetworkPolicy-enforcing CNI (C-23). Recommended on; the operator's choice, not a requirement this spec adds.

## Requirements

- R1. A client key or management token MUST match `^sk-vkllm-[A-Za-z0-9_-]{43}$`; `worker-seed` MUST NOT be required to match it. (source: ADR 0009 §1, §2)
- R2. The gateway and the management API MUST compare every presented credential with `crypto/subtle.ConstantTimeCompare` and MUST log at most the first 8 characters after the prefix. (source: ADR 0009 §2, §5; C-16)
- R3. `internal/auth` MUST reload `client-keys` and `management-tokens` by polling each mounted file's mtime with `os.Stat` at an interval of 10 s or less, and MUST NOT add an fsnotify-class dependency to do it. (source: C-25; ADR 0009 §2)
- R4. A client key presented on any `/api/v1/*` route MUST receive 403; a management token presented on any `/v1/*` route MUST receive 403; a missing or invalid credential on either surface MUST receive 401; `internal/auth.Verify` MUST be the single place both surfaces decide this. (source: NFR-8.1, FR-7.2)
- R5. The worker API key MUST equal `base64url(HMAC-SHA256(workerSeed, "worker:"+installationID+":"+generation))` and MUST NOT be written to the state document or any Secret. (source: ADR 0006 §4, amended)
- R6. Worker-seed rotation MUST be performed only while `desired` is `STOPPED` and state is `STOPPED`; the rotation runbook MUST state this consequence. (source: ADR 0006 §4 consequence)
- R7. The controller MUST dial only the `sshHost`/`sshPort` last written to the state document from a `Provider.GetInstance` response for the instance it created. (source: C-18, ADR 0006 §5)
- R8. A host-key fingerprint mismatch on a pinned instance MUST be a hard failure that destroys the instance, including on reconnect. (source: ADR 0006 §3)
- R9. The rendered Role MUST grant exactly `get` and `update` on ConfigMap `<release>-state` by `resourceNames`, and MUST NOT grant `create`, `patch`, `delete` or `list`. (source: ADR 0004 §3, C-20)
- R10. `automountServiceAccountToken` MUST be true only for the Store's API calls; no other outbound call in the pod MUST attach that token. (source: ADR 0008 §4, C-20)
- R11. Request bodies above 1,048,576 bytes MUST receive 413; `max_tokens`/`max_completion_tokens` above 8,192 MUST be clamped with the clamp logged; a key above 60 requests/minute on either surface MUST receive 429 with `Retry-After`. (source: C-19, NFR-8.5)
- R12. No tracked file MUST contain a string matching `sk-vkllm-[A-Za-z0-9_-]{43}` or a PEM private-key header; this is enforced in CI by CORE-170's `gitleaks` job (ADR 0013 §1), not by a script this spec owns. (source: C-14, NFR-8.6)
- R13. Every package holding a secret value in memory, including `internal/auth`, MUST have at least one test asserting, via `obstest.AssertNoSecret`, that its own captured log output never contains that value. (source: C-16, NFR-8.7)
- R14. The SSH dialer MUST send a keepalive at an interval of 15 s or less and MUST treat repeated keepalive failure as instance loss. (source: ADR 0006 Consequences; C-27)
- R15. Each rotation runbook MUST end with a verification step observable from outside the process (an HTTP response, a metric, a console page), not a source-code read. (source: ADR 0009 §4)
- R16. After an SSH key rotation completes, the Vast account's public-key list MUST hold only the new public key. (source: ADR 0009 §4)
- R17. Any Envoy policy example in this spec MUST be marked optional and MUST NOT be a chart template the chart renders. (source: C-23, ADR 0009 Alternatives)
- R18. Any Envoy route timeout applied to this service in explicit mode MUST be disabled or at least `model.generationTimeout + 60s` (660 s); the 1,080 s figure (`onDemand.maxWait + model.generationTimeout`) applies only if on-demand mode is ever enabled. (source: ADR 0010 Consequences; ADR 0007 §4)
- R19. Rotating one entry in `management-tokens` MUST NOT invalidate any other entry in the list, identically to `client-keys`. (source: ADR 0009 §4, FR-7.2)
- R20. No file this spec adds or edits MUST contain a real domain, key or token value. (source: C-14, common brief)

## Implementation steps

1. Add `internal/auth.Loader` with `Verify` (surface-tagged, R1–R4) and `internal/obs/obstest.AssertNoSecret`. Tests: `TestVerifyAcceptsOwnSurface`, `TestVerifyRejectsCrossSurface` (ok=true, wrong surface), `TestVerifyRejectsUnknown` (ok=false); `TestAssertNoSecretCatches`, `TestAssertNoSecretClean`.
2. Add `scripts/check-rbac.sh`; add a step calling it to the `helm` job in `.github/workflows/ci.yml`. Test: run against a fixture Role containing `patch` (exit 1) and against the shape in R9 (exit 0).
3. Fix `make keygen`'s acceptance regex (R1) as a test on CORE-010's existing target; no code change to the target itself.
4. Write the five rotation runbooks and the SSH keygen/registration steps into this document (done above); no code.
5. Draft the optional Envoy YAML examples, marked illustrative (done above); no code, no chart template.
6. Run runbook A and runbook B once on a kind cluster with the fake provider; record the observed verification outputs in Evidence.
7. Dry-run runbook C's steps 1–4 (STOPPED check, seed replacement, reload, restart) and runbook E's steps 1, 2, 4 and 5 (STOPPED check, keygen, Secret replacement, verify) on kind with the fake provider; runbook D and runbook E's steps 3 and 6 touch the real Vast console and are not exercised without a real account. Record in Evidence; whoever holds a real account records those steps, per Open questions.

## Tests / acceptance criteria

- AC1. `make keygen` output matches `^sk-vkllm-[A-Za-z0-9_-]{43}$` on 20 runs. (R1)
- AC2. A log buffer containing the literal client key fails `AssertNoSecret`; the same buffer with only the 8-character key id passes. (R2, R13)
- AC3. Setup: a mounted `client-keys` file is rewritten in place (simulating a Secret volume update). Action: wait 10 s. Expected: the next auth check accepts the new key and rejects the removed one, with no process restart. (R3)
- AC4. Table test over `internal/auth.Verify` and both HTTP surfaces: client key on `POST /api/v1/gpu/stop` → 403; no key on `POST /v1/chat/completions` → 401; management token on `POST /v1/chat/completions` → 403; valid management token on `GET /api/v1/gpu/status` → 200; valid client key on `GET /v1/models` → 200. (R4)
- AC5. `WorkerKey` with a different `generation` yields a different value; the same inputs are stable; the value never appears in the store document's JSON encoding. (R5)
- AC6. Kind run, fake provider: rotating `worker-seed` while state is `STOPPED` causes no destroy, and the next start reaches `READY` with a matching upstream key. Unit/sim, not kind (the kind fake worker never checks the bearer key): CORE-060's fake upstream returns 401 on the recomputed worker key → `ReportUpstreamFailure`; CORE-050's simulation shows two consecutive non-Ready probes → destroy-and-reprovision. (R6)
- AC7. Simulation: a Ready instance's pinned host-key fingerprint no longer matches on reconnect. Expected: the dial attempt fails and the instance moves toward Destroying; no request is proxied to the unverified host. (R8)
- AC8. `helm template` on the chart as specified by CORE-080, piped through `scripts/check-rbac.sh`, exits 0 only when the rendered Role matches R9 exactly; a fixture Role with `patch` makes it exit 1. (R9)
- AC9. Code search: no `Authorization: Bearer` header construction outside `internal/store/configmap` reads the service-account token path. (R10)
- AC10. A 1,048,577-byte body returns 413; `max_tokens: 100000` is served with the response reflecting 8,192 and one clamp log line; a 61st request in a minute from one key returns 429 with `Retry-After`, on both `/v1/*` and `/api/v1/*`. (R11)
- AC11. `git grep -InE 'sk-vkllm-[A-Za-z0-9_-]{43}|-----BEGIN (OPENSSH|RSA|EC) PRIVATE KEY-----' -- . ':!specs/**/spec.md'` over this repository returns no match today; CORE-170's `gitleaks` job is the CI enforcement going forward. (R12)
- AC12. Kind runs of runbook A and runbook B each complete with every verification step in the runbook observed and recorded; a kind dry run of runbook C's steps 1–4 completes with the Secret reload observed within 10 s. (R15, R6, AC3)
- AC13. Two entries are added to `management-tokens`; removing one leaves `GET /api/v1/gpu/status` with the other still returning 200. (R19)

## Definition of done

- [ ] AC1–AC13 pass in CI or on kind as marked; `make specs-check` passes.
- [ ] Threat model table reviewed with the user; no row without a control.
- [ ] Runbook A and B executed once on kind; runbook C and E dry-run executed once on kind; outputs recorded in Evidence.
- [ ] Status flipped to DONE in the same pull request, folder renamed to `110-D-security-and-secrets`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Exact `BackendTrafficPolicy`/`SecurityPolicy` field names for Envoy Gateway v1.2.1 are unverified | the optional example (R17, R18) could be syntactically wrong | CORE-130 | illustrative only; verify before applying |
| Real Vast account availability to execute runbook D (Vast key) and runbook E's console steps | AC12 covers only A, B, and the dry runs of C and E | implementer, at execution time | record as not yet executed until available |
| Whether a Kubernetes Secret volume update is an atomic symlink swap, and its exact inotify behaviour, is not in research.md | affects nothing here (polling needs no event), but matters if a future spec adds a watcher | implementer, on kind | assume kubelet's documented atomic-update behaviour; keep polling regardless |
| Whether Vast.ai offers any scoped or limited API token, beyond the one account-wide Bearer key research §1.1 documents | changes the blast radius of a Vast key leak | CORE-020, if it checks the Vast account UI | assume none exists; treat any leak as a full account compromise |

## Evidence

None yet.
