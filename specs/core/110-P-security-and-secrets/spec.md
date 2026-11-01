---
id: "CORE-110"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-060", "CORE-080"]
adrs: ["0009", "0006", "0007", "0008", "0004"]
---

# 110 — Security and secrets

**Status note:** DRAFT, written 2026-11-01 before any code exists. The
worker-API-key design below is a wave-1 ruling amending ADR 0006 §4; the
RBAC shape follows ADR 0004 §3 as already written. Neither is re-argued
here.

**Complexity:** M
**Risk:** medium — a wrong rotation runbook or a missed log call leaks a
credential that spends real money
**Estimated effort:** one to two sessions (4–6 h), including one kind run
of each runbook
**Recommended model:** strong — RBAC shape, key derivation and redaction
must be exact
**Depends on:** [CORE-060](../060-P-gateway-openai-api/spec.md), [CORE-080](../080-P-helm-chart/spec.md)

## Outcome

Every secret this project touches (client keys, the admin key, the Vast
key, the SSH key pair, the per-instance worker key) has a documented
format, storage location, reload path and rotation runbook an operator can
follow without reading Go source. CI fails a pull request introducing a
secret-shaped string. The rendered RBAC Role has exactly the verbs ADR
0004 §3 allows, checked by a script, not a reviewer's eyeball. A reviewer
reads the threat model table and sees, per asset, what already stops the
attack and what residual risk is accepted on purpose.

## Scope

- The threat model table.
- Key format, `make keygen` (already built by CORE-010; this spec fixes its
  regex and the key-id convention for logs) and the Secret layout.
- Hot-reload design: polling, not fsnotify.
- Four rotation runbooks: client key, admin key, Vast key, SSH key pair.
- The worker API key's final design (ruling) and its rotation consequence.
- Request limits (C-19) with their configured values.
- Optional Envoy policy examples, explicitly not chart templates.
- `scripts/secret-scan.sh`/`make secret-scan` (`repo` job) and
  `scripts/check-rbac.sh` (`helm` job), both CORE-010/080 CI jobs.
- `internal/obs/obstest.AssertNoSecret`, a shared test helper for redaction tests.
- `automountServiceAccountToken` justification and NetworkPolicy's role as
  defence in depth.

Excludes: the gateway's auth middleware code (CORE-060 owns it; this spec
states what it must do); the chart's Role/NetworkPolicy templates (CORE-080
owns the YAML; this spec states the shape they must render); the SSH
`Dialer` implementation (CORE-050); the platform's External Secrets/SSM
delivery (CORE-130, ADR 0009 §3); hashed-at-rest keys (ADR 0009 §2,
recorded upgrade, not MVP).

## Design and contracts

### Threat model

| Asset | Threat | Control | Residual risk |
|---|---|---|---|
| Client API key | Leaked via client misconfiguration, a pasted log, or phishing | Mounted file only, `crypto/subtle.ConstantTimeCompare`, never logged (ADR 0009 §2, §5; C-16); per-key rate limit `gateway.rateLimitPerKeyRPM` 60; runbook A | Can start GPUs and spend budget up to `budget.monthlyUSD` before rotation; bounded by the budget cap (FR-6.3) and the rate limit, not eliminated (ADR 0009 Risks) |
| Admin API key | Leak grants scale-to-zero, lease creation, warm-up | Same storage/compare controls; a client key on an admin route gets 403, never 200 (NFR-7.1) | Repeated warm-ups or scale calls can drain budget or deny service until rotated; runbook B bounds the window |
| Vast.ai API key | Leak lets an attacker create/destroy instances and read account balance | `existingSecrets.provider` key `VAST_API_KEY`, mounted file, never logged or echoed by the provider client | A leak is treated as a full account compromise until rotated (see Open questions: whether a scoped token exists); the prepaid-balance floor (research §1.5) is the hard stop |
| SSH private key | Leak lets an attacker open a tunnel to any worker the account can reach | Mounted file, private half never leaves the cluster (ADR 0006 Consequences); only the public half reaches Vast | The account-level key is shared across every instance (research §1.3); rotation is permitted only while `Idle`, so no running instance is left depending on the old key (ADR 0009 §4, runbook D) |
| Worker host compromise (shared Vast machine) | A co-tenant process on the same host reaches the loopback port or reads env | `--api-key` required on llama-server (ADR 0002 §3) blocks an unauthenticated local caller; loopback-only bind (C-17); env values never echoed to disk or stdout (CORE-070 R7) | A host-level (hypervisor/co-tenant) compromise is outside this project's control surface; bounded by `gpu.maxLifetime` (6 h) and idle destroy, not prevented |
| Prompt/completion disclosure in transit | A party on the Vast network segment reads request or response bytes | SSH-encrypted tunnel end to end (ADR 0006 §2); TLS from client to Envoy (architecture §6) | None known while both hops hold; a tunnel implementation defect is covered by CORE-050 tests, not this spec |
| SSRF via store tampering | A compromised ConfigMap or an over-privileged writer makes the gateway dial an attacker host | The Dialer only ever receives `Instance.SSHHost/Port` as last written by the controller from a `Provider.GetInstance` response (ADR 0006 §5, C-18); RBAC restricts ConfigMap writes to this release's own ServiceAccount (ADR 0004 §3); a host-key change on a pinned instance is a hard failure and a destroy (ADR 0006 §3) | A pod that already holds this RBAC's `update` verb could poison the host on the very first write of a generation, before any fingerprint is pinned; accepted, because that pod already controls the Dialer directly at that point |
| DoS via cold-start triggering | A valid or leaked client key floods demand to keep starting GPUs | `gpu.maxProvisionAttemptsPerHour` 3 (FR-6.5); `gateway.maxPending` 32; per-key rate limit 60 rpm; budget cap | Up to 3 instance-starts per hour are still purchasable before the attempt cap stops new creates; the budget cap is the floor |
| Budget drain | Sustained abuse or a stuck instance accrues cost beyond intent | `budget.monthlyUSD` 30 with `budget.warnFraction` 0.8 alert (FR-6.4); `maxLifetime` forced drain; worker dead-man timer independent of the control plane (FR-4.7) | The ledger is an estimate, rebuilt from zero on store loss (ADR 0004 §6); Vast's prepaid balance (research §1.5) is the real floor |
| Log leakage of secrets or prompts | A debugging change or a library default logs a header or body | Authorization, prompts, completions, provider keys and SSH material are never logged (NFR-7.7, C-16); redaction tests below | A new call site added later is caught only where a redaction test already exists; coverage is enumerated, not exhaustive (open question) |
| Supply chain (image digest, pinned actions) | A compromised base image or Action substitutes code at build or release time | `worker.image` and the distroless runtime pinned by digest, never a tag (ADR 0008 §3); every `uses:` step pinned to a full commit SHA (C-22); Dependabot tracks both | A digest pin is only as good as the one verification that it is the intended artifact; Dependabot pull requests still need human review (ADR 0008 Risks) |

### Key format and `make keygen`

Client and admin keys are `sk-vkllm-` followed by 32 random bytes, base64url,
no padding: 43 characters (ADR 0009 §2).

```
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='
```

`make keygen` (CORE-010) already runs this and prints a `sk-vkllm-`-prefixed
key; this spec fixes the acceptance regex: `^sk-vkllm-[A-Za-z0-9_-]{43}$`.
For logs, the key id is the first 8 characters after the prefix (ADR 0009
§5); nothing else is ever logged.

### Secret layout

```
existingSecrets.apiKeys:
  client-keys: "sk-vkllm-<43>\nsk-vkllm-<43>\n…"   # one per line
  admin-key:   "sk-vkllm-<43>"
existingSecrets.provider:
  VAST_API_KEY: "<vast key>"
existingSecrets.ssh:
  id_ed25519: "<private key, PEM>"
  known_hosts: "<optional seed>"
```

(ADR 0009 §1.) Secrets are mounted as files under
`/etc/vkllm/secrets/{apiKeys,provider,ssh}` (CORE-080), never as environment
variables, so a reload needs no restart.

Hand-created Secrets via `kubectl create secret` in the `llm` namespace are
a supported first-deployment path: Argo CD never manages them, since
`existingSecrets.*` are references the chart reads, not objects it creates
(ADR 0009 §1). They survive an `Application` sync but not a cluster
rebuild — a rebuilt namespace needs every Secret recreated by hand before
the chart installs. The External Secrets / SSM path (ADR 0009 §3,
CORE-130) exists for survival across a cluster rebuild, not for the first
deployment.

### Hot reload: polling, not fsnotify

The gateway polls each mounted secret file's mtime with stdlib `os.Stat`
every 10 s and re-parses on change; no `fsnotify`-class dependency is
added. `fsnotify` is not in the pre-approved module table (CORE-010,
go.mod) and needs an ADR to add (C-25); polling the resolved path needs no
watcher goroutine to own and works regardless of how the volume plugin
updates the file (see Open questions). Ten seconds of staleness is fine:
rotation is an operator action, not a per-request latency path.

### Rotation runbooks

Each step below is observable without reading source.

**A. Client key**

1. `make keygen` → new key.
2. Append the new key as a new line in the `client-keys` Secret value.
3. Within 10 s, verify: a request bearing the new key against `GET
   /v1/models` returns 200.
4. Move clients to the new key.
5. Remove the old line from the Secret.
6. Verify: a request bearing the removed key now returns 401 and
   `vkllm_gateway_auth_failures_total{reason="unknown_key"}` increments.

Zero downtime (ADR 0009 §4).

**B. Admin key**

1. Rotate only while `desired` is 0 and `GET /admin/v1/status` reports
   `Idle`. The admin key seeds every running instance's derived worker key
   (below); rotating while Ready leaves the gateway unable to recompute
   that instance's `--api-key`, forcing a destroy-and-reprovision anyway
   (ADR 0006 §4 consequence).
2. Replace the `admin-key` Secret value.
3. Within 10 s, verify: the old admin key on `GET /admin/v1/status` returns
   403; the new key returns 200.
4. Resume demand. Verify: the next provisioning reaches Ready normally
   (the worker key computed from the new admin key matches the instance's
   `--api-key`).

**C. Vast API key**

1. Issue a new key in the Vast console.
2. Replace `VAST_API_KEY` in the Secret.
3. Within 10 s reload, plus up to one `controller.reconcileInterval` (60 s)
   for the next provider call to use it. Verify: `GET /admin/v1/status`
   after one reconcile interval shows no new provider-error alert.
4. Revoke the old key in the Vast console.

A call still in flight on the old key is surfaced as a provider alert, not
a crash (ADR 0009 §4).

**D. SSH key pair**

1. Rotate only while `desired` is 0 and `GET /admin/v1/status` reports
   `Idle` (ADR 0009 §4: SSH rotation has no dual-key window, Idle only).
2. `ssh-keygen -t ed25519 -a 100 -N "" -f id_ed25519 -C vkllm-<release>`.
3. Register the new public key in the Vast console, "Manage Keys" page
   (research §1.3). Do not remove the old public key yet.
4. Replace `id_ed25519` in the `ssh` Secret with the new private key.
5. Verify: a newly created instance connects with the new key
   (`vkllm_coldstart_stage_seconds{stage="link"}` records a normal value,
   not a fingerprint-mismatch destroy).
6. Remove the old public key from the Vast console once the new instance
   links successfully.

### SSH specifics

Host-key policy is trust-on-first-use, pinned per instance in
`Instance.HostKeyFingerprint` for that instance's lifetime (ADR 0006 §3,
CORE-040 Design); a changed host key for a known instance is a hard
failure and destroys the instance — not re-decided here, only required to
be tested (AC7). Keepalive: the dialer sends one at least every 15 s, the
same cadence as `gateway.heartbeatInterval`, and treats repeated keepalive
failure as instance loss feeding the ADR 0003 §4 terminal-status path
(R14; CORE-050 owns the `Dialer` code). Reconnect: a dropped tunnel to a
still-Ready instance is retried with backoff by CORE-050; a reconnect MUST
re-verify the pinned fingerprint before any request is proxied, never skip
pinning on reconnect.

### Worker API key (ruling, final)

```go
// base64url(HMAC-SHA256(adminKey, "worker:"+installationID+":"+generation)).
func WorkerKey(adminKey []byte, installationID string, generation int64) string
```

Decided in wave 1; ADR 0006 §4 already carries this. Never generated
randomly, never persisted: the gateway (upstream `Authorization` header)
and the onstart script's environment (`WORKER_API_KEY`, CORE-070) each
recompute it from values already in the state document plus the admin key
already in memory. Consequence: rotating the admin key while an instance
is Ready changes the key the gateway computes but not the one llama-server
was started with, so the next request fails upstream auth — runbook B's
Idle-only rule exists because of this, not as a suggestion.

### Request limits (C-19)

| Limit | Value | Enforcement |
|---|---|---|
| Body size | `gateway.maxBodyBytes` = 1,048,576 | 413 before JSON decode |
| `max_tokens` / `max_completion_tokens` | `gateway.maxTokensCap` = 8,192 | silently clamped, clamp logged (not the body) |
| Per-key rate | `gateway.rateLimitPerKeyRPM` = 60 | 429 + `Retry-After` |

(architecture §9; NFR-7.5; C-19.) CORE-060 implements the middleware; this
spec fixes the values and requires all three to be on by default, with no
configuration path that disables them entirely.

### Optional Envoy policies (illustrative, not chart templates)

Marked optional per ADR 0009 Alternatives ("an optional Envoy rate-limit
policy can be layered"). Not rendered by the chart; an operator applies
these by hand or via the platform's own GitOps tree. CRD field names
below are illustrative for Envoy Gateway v1.2.1 (research §5) and are
**not** verified against that version's API — see Open questions.

```yaml
# Illustrative only; verify field names against the platform's Envoy
# Gateway v1.2.1 CRDs before applying (spec 130).
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
spec:
  targetRefs: [{group: gateway.networking.k8s.io, kind: HTTPRoute, name: gpu-on-demand}]
  timeout: {http: {requestTimeout: 1080s}}  # or disabled; maxWait+generationTimeout, ADR 0007 §4
  # stream idle timeout 120s; exact field name verified against the CRD in CORE-130
  rateLimit: {type: Global, global: {rules: [{limit: {requests: 60, unit: Minute}}]}}
```

The timeout value is a hard requirement wherever such a policy is applied
(R18); the rate-limit block is an optional second layer over the gateway's
own per-key bucket, not a replacement for it.

### CI secret scanning: grep allowlist, not gitleaks

Chosen: a small grep-based script. Rejected: gitleaks, which would add a
new pinned Action (or binary) to track, a config file and its own
false-positive triage, for a repository with exactly two secret shapes to
catch. A ten-line deterministic grep needs no new dependency (C-25's
stdlib-first reasoning extends to CI tooling) and is easier to review.

```sh
#!/usr/bin/env bash
set -euo pipefail
pat='sk-vkllm-[A-Za-z0-9_-]{43}|-----BEGIN (OPENSSH|RSA|EC) PRIVATE KEY-----'
if git grep -InE "$pat" -- . ':!specs/**/spec.md'; then
  echo "secret-scan: found a secret-shaped string" >&2
  exit 1
fi
echo "secret-scan: clean"
```

`specs/**/spec.md` is excluded because this file prints the pattern as
documentation, not a secret; no other path is excluded. `make secret-scan`
wraps this script as a step in the existing `repo` CI job (CORE-010),
running on every pull request regardless of changed paths (R19). Residual
risk: a creatively encoded or split secret is a false negative, accepted
given the narrow secret shape this repository has.

### Redaction tests

`internal/obs/obstest` gains one test helper, used by every package that
holds a secret value in memory (gateway auth, provider client, tunnel
dialer, worker key). It lives in its own `_test`-only-imported package, not
in `internal/obs` itself, so `testing` never reaches the production binary:

```go
// AssertNoSecret fails t if any secret appears in logOutput.
func AssertNoSecret(t *testing.T, logOutput []byte, secrets ...string)
```

Each of CORE-060 (client/admin keys), CORE-020 (Vast key), CORE-050 (SSH
material) and CORE-070 (worker key) adds at least one test that captures
its own `slog` output and calls `AssertNoSecret`. This spec owns the
helper; the sibling specs own calling it, per their own "never logged"
requirement (NFR-7.7, C-16).

### RBAC minimality (ADR 0004 §3)

```yaml
kind: Role
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["<release>-state"]
    verbs: ["get", "update"]
```

One rule, no `create`, no `patch`, no `delete`, no `list`: the chart
template creates the ConfigMap shell at install time (CORE-080), so the
controller never needs `create` (ADR 0004 §3). `scripts/check-rbac.sh`
renders the chart, extracts the `Role` with `yq`, and asserts this shape
exactly — any extra rule or verb anywhere fails the check. Wired into the
existing `helm` CI job (CORE-080).

### `automountServiceAccountToken`

True, because the Store's two HTTP verbs against the in-cluster API server
are the only thing the pod needs it for (ADR 0008 §4, ADR 0004 §3). The
ServiceAccount is dedicated to this release with only the Role above; no
other outbound call in the gateway, controller, provider or tunnel code
may attach this token — a review finding one built outside
`internal/store/configmap` is a defect, not a style note.

### NetworkPolicy as defence in depth

CORE-080 renders an optional `NetworkPolicy` (`networkPolicy.enabled`,
default `false`): egress to DNS, the kube-apiserver, Vast API, and a
configurable SSH port range; ingress from the gateway's namespace only. It
is defence in depth, not a substitute for C-18's SSRF boundary or C-16's
auth check — both must hold with it disabled, the default on a cluster
without a NetworkPolicy-enforcing CNI (C-23). Recommended on; the
operator's choice, not a requirement this spec adds.

## Requirements

- R1. A client or admin key MUST match `^sk-vkllm-[A-Za-z0-9_-]{43}$`. (source: ADR 0009 §2)
- R2. The gateway MUST compare every presented key with `crypto/subtle.ConstantTimeCompare` and MUST log at most the first 8 characters after the prefix. (source: ADR 0009 §2, §5; C-16)
- R3. The gateway MUST reload `client-keys` and `admin-key` by polling the mounted file's mtime with `os.Stat` at an interval of 10 s or less, and MUST NOT add an fsnotify-class dependency to do it. (source: C-25; ADR 0009 §2)
- R4. A client key on any `/admin/v1` route MUST receive 403; a missing or invalid key on any `/v1` or `/admin/v1` route MUST receive 401. (source: NFR-7.1)
- R5. The worker API key MUST equal `base64url(HMAC-SHA256(adminKey, "worker:"+installationID+":"+generation))` and MUST NOT be written to the state document or any Secret. (source: ADR 0006 §4, wave-1 ruling)
- R6. Admin key rotation MUST be performed only while `desired` is 0 and state is `Idle`; the rotation runbook MUST state this consequence. (source: ADR 0006 §4 consequence)
- R7. The controller MUST dial only the `sshHost`/`sshPort` last written to the state document from a `Provider.GetInstance` response for the instance it created. (source: C-18, ADR 0006 §5)
- R8. A host-key fingerprint mismatch on a pinned instance MUST be a hard failure that destroys the instance, including on reconnect. (source: ADR 0006 §3)
- R9. The rendered Role MUST grant exactly `get` and `update` on ConfigMap `<release>-state` by `resourceNames`, and MUST NOT grant `create`, `patch`, `delete` or `list`. (source: ADR 0004 §3, C-20)
- R10. `automountServiceAccountToken` MUST be true only for the Store's API calls; no other outbound call in the pod MUST attach that token. (source: ADR 0008 §4, C-20)
- R11. Request bodies above 1,048,576 bytes MUST receive 413; `max_tokens`/`max_completion_tokens` above 8,192 MUST be clamped with the clamp logged; a key above 60 requests/minute MUST receive 429 with `Retry-After`. (source: C-19, NFR-7.5)
- R12. No tracked file MUST contain a string matching `sk-vkllm-[A-Za-z0-9_-]{43}` or a PEM private-key header; CI MUST fail the pull request when one is found. (source: C-14, NFR-7.6)
- R13. Every package holding a secret value MUST have at least one test asserting, via `obstest.AssertNoSecret`, that its own captured log output never contains that value. (source: C-16, NFR-7.7)
- R14. The SSH dialer MUST send a keepalive at an interval of 15 s or less and MUST treat repeated keepalive failure as instance loss. (source: ADR 0006 Consequences; C-27)
- R15. Each rotation runbook MUST end with a verification step observable from outside the process (an HTTP response, a metric, a console page), not a source-code read. (source: ADR 0009 §4)
- R16. After an SSH key rotation completes, the Vast account's public-key list MUST hold only the new public key. (source: ADR 0009 §4)
- R17. Any Envoy policy example in this spec MUST be marked optional and MUST NOT be a chart template the chart renders. (source: C-23, ADR 0009 Alternatives)
- R18. Any Envoy route timeout applied to this service MUST be disabled or at least `gateway.maxWait + model.generationTimeout` (1,080 s), paired with a 120 s stream idle timeout. (source: ADR 0007 §4)
- R19. The secret scan MUST run on every pull request regardless of which paths changed. (source: C-14)
- R20. No file this spec adds or edits MUST contain a real domain, key or token value. (source: C-14, common brief)

## Implementation steps

1. Add `internal/obs/obstest.AssertNoSecret`. Test: `TestAssertNoSecretCatches` (buffer with the literal secret fails), `TestAssertNoSecretClean` (redacted buffer passes).
2. Add `scripts/secret-scan.sh` and `make secret-scan`; add a step calling it to the `repo` job in `.github/workflows/ci.yml` (CORE-010's file). Test: run against a fixture tree with one real-shaped key (exit 1) and one with only placeholders (exit 0).
3. Add `scripts/check-rbac.sh`; add a step calling it to the `helm` job in `.github/workflows/ci.yml`. Test: run against a fixture Role containing `patch` (exit 1) and against the shape in R9 (exit 0).
4. Fix `make keygen`'s acceptance regex (R1) as a test on CORE-010's existing target; no code change to the target itself.
5. Write the four rotation runbooks and the SSH keygen/registration steps into this document (done above); no code.
6. Draft the optional Envoy YAML examples, marked illustrative (done above); no code, no chart template.
7. Run runbook A and runbook B once on a kind cluster with the fake provider; record the observed verification outputs in Evidence.
8. Dry-run runbook D's steps 1, 2, 4 and 5 (Idle check, keygen, Secret replacement, verify) on kind with the fake provider; steps 3 and 6 touch the real Vast console and are not exercised without a real account. Record in Evidence. Runbook C needs a real Vast account and is recorded by whoever holds one, per Open questions.

## Tests / acceptance criteria

- AC1. `make keygen` output matches `^sk-vkllm-[A-Za-z0-9_-]{43}$` on 20 runs. (R1)
- AC2. A log buffer containing the literal client key fails `AssertNoSecret`; the same buffer with only the 8-character key id passes. (R2, R13)
- AC3. Setup: a mounted `client-keys` file is rewritten in place (simulating a Secret volume update). Action: wait 10 s. Expected: the next auth check accepts the new key and rejects the removed one, with no process restart. (R3)
- AC4. Table test: client key on `POST /admin/v1/scale` → 403; no key on `POST /v1/chat/completions` → 401; valid admin key on `GET /admin/v1/status` → 200. (R4)
- AC5. `WorkerKey` with a different `generation` yields a different value; the same inputs are stable; the value never appears in the store document's JSON encoding. (R5)
- AC6. Unit/sim, not kind (the kind fake worker never checks the bearer key): CORE-060's fake upstream returns 401 on the recomputed worker key → `ReportUpstreamFailure`; CORE-050's simulation shows two consecutive non-Ready probes → destroy-and-reprovision. The kind run covers only the Idle path: rotating the admin key while `Idle` causes no destroy. (R6)
- AC7. Simulation: a Ready instance's pinned host-key fingerprint no longer matches on reconnect. Expected: the dial attempt fails and the instance moves toward Destroying; no request is proxied to the unverified host. (R8)
- AC8. `helm template` on the chart as specified by CORE-080, piped through `scripts/check-rbac.sh`, exits 0 only when the rendered Role matches R9 exactly; a fixture Role with `patch` makes it exit 1. (R9)
- AC9. Code search: no `Authorization: Bearer` header construction outside `internal/store/configmap` reads the service-account token path. (R10)
- AC10. A 1,048,577-byte body returns 413; `max_tokens: 100000` is served with the response reflecting 8,192 and one clamp log line; a 61st request in a minute from one key returns 429 with `Retry-After`. (R11)
- AC11. `scripts/secret-scan.sh` exits 1 on a fixture containing a real-shaped key or a PEM header, and exits 0 on a fixture containing only `<fqdn>`/`lab.example.com` placeholders. (R12, R19)
- AC12. Kind runs of runbook A and runbook B each complete with every verification step in the runbook observed and recorded; a kind dry run of runbook D's steps 1, 2, 4 and 5 completes with the Secret reload (step 4) observed within 10 s. (R15, R6, AC3)
- AC13. `git grep -InE 'sk-vkllm-[A-Za-z0-9_-]{43}|-----BEGIN (OPENSSH|RSA|EC) PRIVATE KEY-----' -- . ':!specs/**/spec.md'` over this repository returns no match. (R20, C-14)

## Definition of done

- [ ] AC1–AC13 pass in CI or on kind as marked; `make specs-check` passes.
- [ ] Threat model table reviewed with the user; no row without a control.
- [ ] Runbook A and B executed once on kind; runbook D dry-run executed once on kind; outputs recorded in Evidence.
- [ ] Status flipped to DONE in the same pull request, folder renamed to `110-D-security-and-secrets`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Exact `BackendTrafficPolicy`/`SecurityPolicy` field names for Envoy Gateway v1.2.1 are unverified | the optional example (R17, R18) could be syntactically wrong | CORE-130 | illustrative only; verify before applying |
| Real Vast account availability to execute runbook C | AC12 only covers A, B, D | implementer, at execution time | record as not yet executed until available |
| Whether a Kubernetes Secret volume update is an atomic symlink swap, and its exact inotify behaviour, is not in research.md | affects nothing here (polling needs no event), but matters if a future spec adds a watcher | implementer, on kind | assume kubelet's documented atomic-update behaviour; keep polling regardless |
| Whether Vast.ai offers any scoped or limited API token, beyond the one account-wide Bearer key research §1.1 documents | changes the blast radius of a Vast key leak | CORE-020, if it checks the Vast account UI | assume none exists; treat any leak as a full account compromise |

## Evidence

None yet.
