---
id: "CORE-130"
status: "DONE"
updated: "2026-11-22"
depends_on: ["CORE-080", "CORE-110", "CORE-120"]
adrs: ["0008", "0009", "0010", "0012", "0013"]
---

# 130 — Platform integration

**Status note:** DONE. Implemented 2026-11-22. PR-B was already half-shipped by vk-lab-platform #101/#102 before this spec ran; the design below is rewritten to what landed, not what was drafted. AC6 (10-minute stream) and AC7 are recorded as run/not-run in Evidence.

**Complexity:** L — two chart layers, three cross-repository pull requests touching Terraform, Argo CD and IAM, plus an operator-only prerequisite (Vast.ai account setup) that no test can automate.
**Risk:** high — a misplaced GitOps object (wrong `AppProject`, wrong namespace) wedges the platform's root Argo sync the way vk-lab-platform ADR 0045 §Context describes happening once already; a wrong IAM scope either denies ESO or widens it past the five values this spec names.
**Estimated effort:** one long session (5–7 h) across two repositories, plus operator time outside CI for the Vast.ai account and key generation.
**Recommended model:** strong — cross-repository IAM and Argo CD reasoning with no CI in this repository that can catch a platform-side mistake before merge.
**Depends on:** CORE-080 (the `gpu-on-demand` chart this spec's Application installs), CORE-110 (the three Secrets and their keys this spec delivers by reference), CORE-120 (the OCI chart publish this spec's Application reads an exact version of).

## Outcome

A commit to `main` in `vk-lab-platform` that merges this spec's three pull requests, followed by a chart-version bump in this repository's `gitops/values.yaml` and a sync, makes the one `HTTPRoute` answer both `https://llm.<fqdn>/v1/models` and `https://llm.<fqdn>/api/v1/gpu/status` with the platform's own certificate (NFR-9.4; ADR 0012 §6), with no further manual step on the cluster. The three runtime Secrets arrive from committed KMS ciphertext through SSM and ESO, never typed by hand except in the local/kind path ADR 0009 §3 keeps open. Stopping the GPU and then deleting the platform's pointer `Application` removes the installation (Rollback).

## Scope

In this repository (committed by this spec):

- `gitops/Chart.yaml`, `gitops/values.yaml` — the app-of-apps chart ADR 0008
  §5 assigns to this repository.
- `gitops/templates/application.yaml` — renders one Argo `Application`
  sourcing the OCI chart `gpu-on-demand` (CORE-120) at an exact version.
- `gitops/templates/validate.yaml` — fails the render when a required value
  is unset.
- `scripts/gitops-check.sh` — lint, render, and kubeconform for `gitops/`.

Against `vk-lab-platform` (designed here, not committed here — C-24):

- **PR-A** (vk-lab-platform [#109](https://github.com/savak1990/vk-lab-platform/pull/109)): the pointer `AppProject` and `Application` that make `vk-llm-on-demand` a platform application, plus the `BackendTrafficPolicy` (PR-C, same pull request).
- **PR-B** (vk-lab-platform [#107](https://github.com/savak1990/vk-lab-platform/pull/107), [#112](https://github.com/savak1990/vk-lab-platform/pull/112)): the three new SSM parameters, the three platform-rendered `ExternalSecret`s, and the ESO IAM policy that reads them.
- **PR-C**: the Envoy traffic policy setting `requestTimeout: 660s` (explicit mode's bound) on the `llm` `HTTPRoute`; shipped inside PR-A (#109), not a separate pull request.
- **vk-llm-on-demand [#34](https://github.com/savak1990/vk-llm-on-demand/pull/34) + vk-lab-platform [#109](https://github.com/savak1990/vk-lab-platform/pull/109)**: moves the child `Application` from namespace `argocd` to `llm`, per vk-lab-platform ADR 0046 (landed mid-plan; see "Child Application namespace" below).
- **2026-11-21** (`feature/vk-llm-ci-role`, ADR 0016): adds IAM role
  `vk-llm-ci-role`, trusted via GitHub OIDC only to the `gpu-control`
  Environment, reading `/*/persistent/llm/api_token`; moved to
  `/*/persistent/llm/management_tokens` by vk-lab-platform #114
  (2026-11-22).

Plus: ordering and verification for the three pull requests, a Vast.ai
account checklist for the operator, and rollback.

Excludes: the `gpu-on-demand` chart's own templates and values (CORE-080);
how the gateway or worker consume the three Secrets (CORE-110); the
workflows that publish the OCI chart this spec's `Application` reads
(CORE-120); any change to `vk-ahorro` or to the platform's base Envoy
Gateway, cert-manager or external-dns install, which already exist
(research §5); Vast.ai billing automation; a second environment (dev/PR) for
this application — ADR 0008 §2 rejected one for the MVP, matching why
`vk-ahorro`'s `ahorro-dev`/`ahorro-pr` split (vk-lab-platform ADR 0045) has no equivalent
here.

## Design and contracts

*Every `vk-lab-platform` file reference below is verified at implementation time against the real, merged pull requests, not the 2026-11-01 draft.*

### Two chart layers

The platform's `AppProject`/`Application` (wave 4/5) source this repository
over **git** at `path: gitops`. The `Application` it creates lives in
namespace `llm`, never `argocd` — see "Child Application namespace" below.
This spec's own `gitops/templates/application.yaml` is the one the platform
renders: it sources the published **OCI** chart `gpu-on-demand` (CORE-120)
at an exact version, also into namespace `llm`, through one
`helm.valuesObject` block. It passes no Helm parameters at all, so there is
no `value: ""` hazard. Two Argo `Application` layers, one layer deeper than
`vk-ahorro` because there is exactly one service, not several.

The platform owns one thing: which git revision of `gitops/` to render
(`vkllm.targetRevision` in `vk-lab-platform/gitops/values.yaml`, mirroring
`ahorro.targetRevision`) plus `fqdn`, passed as the only Helm parameter it
sets on this repository's chart. This repository's `gitops/values.yaml`
owns every other pin: `chartVersion`, `tag`, `model.url`, `model.sha256`,
`worker.image` (the five values), plus the three Secret names
(`secrets.provider`, `secrets.apiKeys`, `secrets.ssh`) the inner chart
mounts by reference (ADR 0008 §5). A public OCI Helm chart sourced by an
Argo `Application` with no `Repository`/`enableOCI` registration already
runs on the platform —
`vk-lab-platform/gitops/templates/platform/shared/postgres/barman-plugin-application.yaml`
(`repoURL: ghcr.io/cloudnative-pg/charts`, `chart: plugin-barman-cloud`, no
credential) — and this spec's child `Application` copies that exact
`source` shape.

### `gitops/values.yaml` (this repository)

| Key | Default | Meaning |
|---|---|---|
| `fqdn` | `""` | from the platform's helm parameter; required |
| `hostPrefix` | `llm` | fixed; `host` = `<hostPrefix>.<fqdn>` |
| `namespace` | `llm` | fixed |
| `charts.registry` | `ghcr.io/savak1990/vk-llm-on-demand/charts` | no `oci://` scheme (ADR 0008 §4) |
| `chartVersion` | pinned (`0.1.1-main.0ab826b`) | required; non-empty; the one place this value is pinned |
| `tag` | pinned (40-hex commit SHA used as the image tag) | required; non-empty; becomes `image.tag` |
| `secrets.provider` / `secrets.apiKeys` / `secrets.ssh` | `vast-ai-api-key` / `vk-llm-api-token` / `vk-llm-ssh-key` | the three Secret names the platform renders into `llm`; passed through to `existingSecrets.*` |
| `model.url` / `model.sha256` | pinned | required; non-empty; the pinned model artifact |
| `worker.image` | pinned (by digest) | required; non-empty; the pinned worker image |

The chart's own `externalSecrets.enabled`, `httpRoute.enabled`,
`serviceMonitor.enabled`, `prometheusRule.enabled`, `dashboard.enabled` are
set directly in `gitops/templates/application.yaml`'s `valuesObject`, not
in this table: `httpRoute`, `serviceMonitor`, `prometheusRule`, `dashboard`
are `true` (the chart default is `false` for each; without them the
`HTTPRoute`, the dashboard, and the four alerts never render on the only
installation — NFR-9.5; C-4; FR-6.4). `externalSecrets.enabled` is left at
the chart default `false`: the platform renders the three `ExternalSecret`
objects itself (see "PR-B: secrets" below), so the chart must not render
its own.

### `gitops/templates/application.yaml`

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: gpu-on-demand
  namespace: {{ .Values.namespace }}
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: vk-llm-on-demand
  source:
    repoURL: {{ .Values.charts.registry | quote }}
    chart: gpu-on-demand
    targetRevision: {{ .Values.chartVersion | quote }}
    helm:
      valuesObject:
        image:
          tag: {{ .Values.tag | quote }}
        host: {{ printf "%s.%s" .Values.hostPrefix .Values.fqdn | quote }}
        existingSecrets:
          provider: {{ .Values.secrets.provider | quote }}
          apiKeys: {{ .Values.secrets.apiKeys | quote }}
          ssh: {{ .Values.secrets.ssh | quote }}
        config:
          model:
            url: {{ .Values.model.url | quote }}
            sha256: {{ .Values.model.sha256 | quote }}
          worker:
            image: {{ .Values.worker.image | quote }}
        httpRoute:
          enabled: true
        serviceMonitor:
          enabled: true
        prometheusRule:
          enabled: true
        dashboard:
          enabled: true
  destination:
    server: https://kubernetes.default.svc
    namespace: {{ .Values.namespace }}
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
```

One `helm.valuesObject` block, not a parameter list: every value above is
set, so no Helm parameter can render with an empty `value: ""` — the
permanent-`OutOfSync` hazard the draft design guarded against with `{{- if
}}` no longer applies, because the hazard itself cannot occur with
`valuesObject`. `metadata.namespace` and `destination.namespace` are both
`.Values.namespace` (`llm`): the `Application` object lives in the app's
own namespace, never in `argocd` — see "Child Application namespace"
below.

### `gitops/templates/validate.yaml`

```yaml
{{- if not .Values.fqdn }}
{{ fail "fqdn is required (set by the platform's helm parameter)" }}
{{- end }}
{{- if not .Values.chartVersion }}
{{ fail "chartVersion is required (the gpu-on-demand chart version to install)" }}
{{- end }}
{{- if not .Values.tag }}
{{ fail "tag is required (the gpu-on-demand image tag to install)" }}
{{- end }}
```

Fails `helm template`/`lint`/`install` (nonzero exit, no manifest) when any
of the three is unset — same mechanism ADR 0008 §4 uses for the
`gpu-on-demand` chart's own `host` field (C-14; NFR-7.6).

### `scripts/gitops-check.sh`

| Step | Check | Why |
|---|---|---|
| 1 | `helm lint`/`helm template` the outer chart (`--set fqdn=lab.example.com`), then kubeconform the render | the child `Application` is well-formed before anything else runs |
| 2 | `helm template` with no `--set` MUST fail | proves `templates/validate.yaml` is wired |
| 3 | `grep -q '^fqdn: ""$' gitops/values.yaml` | no real `fqdn` is ever committed (C-14; NFR-7.6) |
| 4 | render the pinned **inner** chart (`chartVersion`, the exact `helm.valuesObject`) from the OCI registry, then kubeconform that render | the exact manifests Argo will render on the platform, validated here first |
| 5 | assert the inner render's `HTTPRoute` name is `gpu-on-demand` | the platform's `BackendTrafficPolicy` `targetRefs` that name |
| 6 | assert the child `Application`'s `metadata.namespace` is `llm` | R13; an `Application` in `argocd` may name any `AppProject` |

### PR-A: pointer `AppProject` and `Application`

New files `vk-lab-platform/gitops/templates/apps/vk-llm-on-demand/{appproject,application}.yaml`.
Per ADR 0046 (see "Child Application namespace" below), this `AppProject`
differs from `vk-ahorro`'s: `sourceNamespaces: [llm]` and `destinations:
[llm]` only, never `argocd`. The pointer `Application` passes one Helm
parameter, `fqdn` (from `.Values.envoyGateway.fqdn`, guarded by `{{- if
}}`); it passes no `chartVersion`/`tag` — this repository's own
`gitops/values.yaml` pins those. Waves and `syncPolicy` are unchanged.

Also: add a `vkllm: {targetRevision: main}` block to
`vk-lab-platform/gitops/values.yaml` (sibling to `ahorro:`); add
`AppProject__argocd__vk-llm-on-demand
Application__argocd__vk-llm-on-demand` and
`BackendTrafficPolicy__llm__gpu-on-demand-traffic-policy` to
`REQUIRED_OBJECTS_CIVO`/`_HETZNER`, and add `vk-llm-on-demand` to
`FORBIDDEN_APPLICATIONS_LOCAL`, in `gitops-render-check.sh`; run
`./scripts/gitops-render-check.sh update` to refresh the golden.

### PR-B: secrets

The platform already kept two SSM parameters,
`/<project>/persistent/llm/{vast_ai_api_key,api_token}` (vk-lab-platform
#101/#102, before this spec ran). PR-B adds three more under the same
prefix: `worker_seed`, `ssh_private_key`, `management_tokens`. All five
extend the shared `terraform/modules/persistent-secrets` module every
platform project uses — the draft design's separate module was dropped;
the module already carried the first two.

The platform renders three `ExternalSecret` objects into `llm`, shaped
exactly as the chart's `existingSecrets.{provider,apiKeys,ssh}` expect:
`vast-ai-api-key` (key `VAST_API_KEY`), `vk-llm-api-token` (keys
`client-keys`, `management-tokens`, `worker-seed`), `vk-llm-ssh-key` (key
`id_ed25519`). `client-keys` reads SSM parameter `api_token`;
`management-tokens` reads the separate `management_tokens` parameter. The
two MUST differ: the gateway refuses to start when the same key appears in
both lists (`internal/auth/keys.go`). The chart's own `ExternalSecret`s stay
disabled (`externalSecrets.enabled: false`, the chart default) — they are
for other installations, not this one.

Ciphertext is committed at **global** scope, `secrets/llm-on-demand-<name>.enc`
(not per-project — this app has one installation for the account, not one
per project), encrypted with the platform's documented procedure. The
value is never passed on a command line (it would land in shell history);
the procedure reads it from an environment variable instead.

The `eso` IAM policy already granted four ARNs (`postgres`, `grafana`, and
the two existing `llm` parameters). PR-B's three new ARNs bring both the
`ssm:GetParameter` statement and the `kms:Decrypt` condition to seven ARNs
each, still explicit, no wildcard.

Landed as vk-lab-platform #107 (`worker_seed` and `ssh_private_key`, the
three `ExternalSecret`s, two of the three new ARNs) and #112 (adding
`management_tokens`, splitting it from `api_token`, the third ARN).

### Child Application namespace

Per vk-lab-platform ADR 0046, a business application's child `Application`
lives in its own namespace (`llm`), never `argocd`: an `Application` in the
control-plane namespace may name any `AppProject`, so keeping the child out
of it narrows what a compromised chart can reach. The `AppProject` sets
`sourceNamespaces: [llm]` and `destinations: [llm]` only. Argo CD itself
runs with `application.namespaces=ahorro,llm` (set in `scripts/argo-up.sh`
and patched live on the running cluster) so it watches `llm` for
`Application` objects at all. Landed as vk-llm-on-demand #34 (this
repository's `application.yaml` moved from `argocd` to `llm`) and
vk-lab-platform #109 (the `AppProject`, the pointer `Application`, and
`application.namespaces`).

### PR-C: Envoy request timeout

ADR 0010 Consequences: the MVP runs `lifecycle.provisioningMode: explicit`
only, so no inference request ever waits for a cold start — the Envoy route
timeout MUST be `>= model.generationTimeout + 60 s` (600 s + 60 s = 660 s,
`docs/architecture.md` §9). On-demand mode's 1080 s figure (ADR 0007 §4) is
future-only and out of this spec's MVP scope. Envoy Gateway v1.2.1's
`BackendTrafficPolicy.spec.timeout.http` has only `requestTimeout`,
`connectionIdleTimeout`, `maxConnectionDuration` — no stream idle field
exists, so the draft design's 120 s stream idle timeout is dropped (Q14
settled). New file
`vk-lab-platform/gitops/templates/apps/vk-llm-on-demand/traffic-policy.yaml`,
same shape as the platform's existing grafana traffic policy:

```yaml
{{- if ne .Values.target "local" }}
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: BackendTrafficPolicy
metadata:
  name: gpu-on-demand-traffic-policy
  namespace: llm
  annotations:
    argocd.argoproj.io/sync-wave: "6"
    argocd.argoproj.io/sync-options: SkipDryRunOnMissingResource=true
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: HTTPRoute
      name: gpu-on-demand
  timeout:
    http:
      requestTimeout: 660s
{{- end }}
```

Sync-wave `6` (one after the pointer `Application`'s wave `5`) is a
best-effort hint, not a guarantee — namespace `llm` and its `HTTPRoute` are
created by a *different* `Application`'s own reconciliation, so waves don't
strictly serialize across the two. `SkipDryRunOnMissingResource=true` (same
reason the grafana file uses it) plus `automated.selfHeal` self-corrects a
first-apply miss. The `HTTPRoute` name is `gpu-on-demand`, confirmed live
(Argo CD defaults the Helm release name to the Application name, and the
chart derives the `HTTPRoute` name from the release name). The
`{{- if ne .Values.target "local" }}` guard keeps this
object, the pointer `Application`, and the `AppProject` all off `local`:
`local`/kind never deploys this pointer with real traffic. Landed inside
vk-lab-platform #109.

### Vast.ai prerequisites (operator checklist) and ordering

1. Create a Vast.ai account and fund it — `$0` balance schedules a running
   instance for deletion (research §1.5); generate an API key from the
   console → `vast-ai-api-key` (already committed; unchanged by this spec).
2. Generate an `ed25519` keypair; register the **public** half in the
   account's "Manage Keys" page (every new instance gets it automatically,
   research §1.3); the **private** half → `llm-on-demand-ssh-private-key`.
3. Generate inference client keys with `make keygen` (ADR 0009 §2,
   `sk-vkllm-` prefix) → `llm-on-demand-api-token`. Generate a separate
   management token the same way → `llm-on-demand-management-tokens`.
   Generate the worker seed (`openssl rand -base64 32`, ADR 0009 §1) →
   `llm-on-demand-worker-seed`. Encrypt each at global scope and commit the
   `.enc` files.

   The `gpu-control` workflow still reads
   `/<project>/persistent/llm/api_token` (ADR 0016). Since #112 that
   parameter holds the inference key, so the workflow cannot call the
   management API until the workflow, the `vk-llm-ci-role` policy and ADR
   0016 move to `/<project>/persistent/llm/management_tokens`. This is a
   follow-up, not part of this spec. 2026-11-22: done by vk-lab-platform
   #114 (IAM) and the vk-llm-on-demand workflow + ADR 0016 amendment.
4. Apply **#107**'s Terraform (the secrets module, live) and merge
   vk-llm-on-demand **#32** (this repository's `gitops/` chart) — order
   between these two does not matter, but both MUST land before step 6:
   secrets avoid the `ExternalSecret`-never-resolves failure mode (ADR
   0009 Risks), and the platform's pointer tracks `main`, so the chart
   must exist there before **#109** can sync it.
5. Merge **#107** (PR-B) to `vk-lab-platform`.
6. Merge **#109** last (`vk-lab-platform`'s `AppProject`, pointer
   `Application`, and `BackendTrafficPolicy` together — PR-A and PR-C) —
   creates the workload, its `HTTPRoute`, and the traffic policy that
   targets it in one pull request. **#112** (splitting the management
   token into its own SSM parameter) landed after #109, as a fix — see
   Evidence.
7. Here: bump `chartVersion`/`tag` only after CORE-120 publishes that
   version to GHCR.
8. `VK_LLM_API_ENDPOINT` and `AWS_ROLE_ARN` (both repository variables, the latter set by `vk-lab-platform`'s `make account-up` after the `vk-llm-ci-role` pull request merges) are GitHub repository configuration for `gpu-control.yml`, not repeated here — CORE-160 owns that setup. `VK_LLM_TOKEN` is never stored in GitHub at all; `gpu-control.yml` reads it from AWS SSM (ADR 0016).

### Rollback

0. `POST /api/v1/gpu/stop` (management token; ADR 0012 §1, superseding `/admin/v1/scale`); wait for `state: STOPPED` and an empty provider instance list for the installation's label prefix — a GPU must not keep billing through a rollback (C-1; FR-4.1 never destroys on shutdown, so this step cannot be skipped).
1. `kubectl -n argocd delete application vk-llm-on-demand` (cascade, via the finalizer) prunes the child `Application` and every resource it owns — the fast path. Whether cascade also removes namespace `llm` itself is an Argo CD behavior not stated in `docs/research.md` (open question below).
2. The durable rollback is `git revert` of PR-A (`#109`, which also carries
   PR-C's traffic policy) in `vk-lab-platform`; both are removed together.
   PR-B's secrets/SSM/IAM are left in place (unused, they cost nothing), so
   re-adding PR-A later re-attaches to the same values with no new setup.

## Requirements

- R1. `gitops/values.yaml` MUST default `fqdn` to the empty string and MUST commit non-empty `chartVersion`, `tag`, `model.url`, `model.sha256`, `worker.image` and the three Secret names; the inner `valuesObject` MUST set `httpRoute`, `serviceMonitor`, `prometheusRule`, `dashboard` `enabled: true` and leave `externalSecrets.enabled` at the chart default `false`. (source: ADR 0009 §3; ADR 0008 §4; ADR 0008 §5)
- R2. `gitops/templates/validate.yaml` MUST fail the Helm render when `fqdn`, `chartVersion`, or `tag` is empty. (source: C-14; NFR-7.6)
- R3. `gitops/templates/application.yaml` MUST render exactly one Argo `Application` with `spec.source.chart: gpu-on-demand`, `spec.source.targetRevision` equal to `.Values.chartVersion`, and MUST NOT render a Helm parameter with an empty `value`. (source: ADR 0008 §5; platform precedent `barman-plugin-application.yaml`)
- R4. That `Application`'s `syncPolicy` MUST set `automated.prune: true`, `automated.selfHeal: true`, `CreateNamespace=true`, and `ServerSideApply=true`, and MUST carry the finalizer `resources-finalizer.argocd.argoproj.io`. (source: ADR 0009 §3; `vk-ahorro` precedent)
- R5. No file committed in this repository's `gitops/` MUST contain a literal domain, `fqdn`, or secret value. (source: C-14; NFR-7.6)
- R6. The three new SSM parameters (`worker_seed`, `ssh_private_key`, `management_tokens`) MUST use `SecureString` with `alias/lab-secrets` in the same `persistent-secrets` module #101 extended; the ESO policy MUST list exactly the seven ARNs in both statements and in both modules. `client-keys` and `management-tokens` MUST read different SSM parameters. (source: ADR 0009 §1, §3; C-24)
- R7. PR-B's widened `eso` IAM policy document MUST grant `ssm:GetParameter` and the matching `kms:Decrypt` condition on the five `llm` ARNs under `/<project>/persistent/llm/`, bringing both statements to seven ARNs total, and MUST NOT alter the two pre-existing non-`llm` ARNs (`postgres_password_arn`, `grafana_password_arn`). (source: ADR 0009 §3; C-15)
- R8. `requestTimeout` MUST be `660s`; no idle-timeout requirement (field absent in Envoy Gateway v1.2.1). (source: ADR 0010 Consequences; architecture.md §9)
- R9. This repository MUST hold no platform-side file; PR-A, PR-B, and PR-C are pull requests against `vk-lab-platform`, never a file committed here. (source: C-24)
- R10. Secrets reach the pod only by the three `existingSecrets` names; the chart's own ExternalSecrets stay disabled on the platform. (source: C-15; ADR 0009 §1)
- R11. `scripts/gitops-check.sh` MUST exit nonzero when `gitops/` is rendered with no `fqdn` set, and MUST exit zero on the committed `chartVersion`/`tag` with a placeholder `fqdn`. (source: R2)
- R13. The child `Application` MUST live in namespace `llm`; the `AppProject` MUST list `llm` as its only source namespace and only destination; `gitops-check` MUST fail if the child renders into any other namespace. (source: vk-lab-platform ADR 0046)

## Implementation steps

1. Write `gitops/Chart.yaml`/`values.yaml`. Test: `helm lint gitops/ --set fqdn=lab.example.com --set chartVersion=0.1.0 --set tag=deadbee` exits 0.
2. Write `templates/validate.yaml`. Test: `helm template gitops/` with no `--set` exits nonzero, stderr contains `fqdn is required`.
3. Write `templates/application.yaml`. Test: the same `--set` render piped through `yq '.spec.source.chart'` prints `gpu-on-demand`; `yq '.spec.source.helm.valuesObject.image.tag'` prints the pinned 40-hex commit SHA.
4. Write `scripts/gitops-check.sh` (the six steps in Design). Test: exits 0 on repo HEAD; exits nonzero at the grep step against a copy with a literal domain hard-coded into `values.yaml`.
5. Draft PR-A's three files plus the `gitops-render-check.sh` list addition in a `vk-lab-platform` checkout. Test: `./scripts/gitops-render-check.sh update` then (check mode) exits 0; `git diff --stat` shows only the two new objects and the golden directory.
6. Draft PR-B's Terraform module, live unit, and `rolesanywhere` diff. Test: `terragrunt plan` in the live unit shows the expected resources to add; `terraform plan` in `rolesanywhere` shows the `eso` policy gaining ARNs in both statements until the total reaches exactly seven ARNs, nothing else.
7. Draft PR-C's traffic-policy file. Test: `helm template vk-lab-platform/gitops --set target=hetzner --set envoyGateway.fqdn=lab.example.com ... | yq 'select(.kind=="BackendTrafficPolicy") | .spec.timeout.http.requestTimeout'` prints `660s`.
8. Run the Vast.ai checklist and the `make secret-encrypt` calls. Test: `make secret-decrypt SCOPE=global NAME=vast-ai-api-key` prints a nonempty value, no error.

## Tests / acceptance criteria

- AC1. `curl -sv https://llm.<fqdn>/v1/models -H "Authorization: Bearer <client-keys line>"` returns HTTP 200 with a body listing `model.alias`; the same call with no key returns HTTP 401; `curl -sv -H "Authorization: Bearer <management-tokens line>" https://llm.<fqdn>/api/v1/gpu/status` returns HTTP 200 with a status document; both TLS certificate issuer lines in `curl -sv`'s stderr name Let's Encrypt. (NFR-9.4)
- AC2. `kubectl -n llm get externalsecret -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].reason}'` prints `SecretSynced` for all three `ExternalSecret` objects. (R7; R10)
- AC3. `kubectl -n argocd get application vk-llm-on-demand -o jsonpath='{.status.sync.status}{" "}{.status.health.status}'` and `kubectl -n llm get application gpu-on-demand -o jsonpath='{.status.sync.status}{" "}{.status.health.status}'` each print `Synced Healthy`. (R3; R4; R13)
- AC4. `make gitops-check` (this repository) exits 0 on a clean checkout. (R11)
- AC5. `grep -q '^fqdn: ""$' gitops/values.yaml` (the check `scripts/gitops-check.sh` runs) passes: no real `fqdn` is ever committed. (R5)
- AC6. Against a GPU already `READY` (started with `vk-llm start`, no cold-start wait in explicit mode — ADR 0010 §2), a streamed request that runs for `model.generationTimeout` (10 min) completes through Envoy with no `504` — proving the 660 s route timeout clears real generation timing. (R8; ADR 0010 Consequences)
- AC7. `terraform plan` in `terraform/live/persistent/secrets` (the shared module PR-B extends) and in every other project's same live unit both run clean (no forced changes) after PR-B merges, proving the extension only added resources. (R6)

## Definition of done

- [x] AC1–AC5 and AC7 pass on a real `vk-lab-platform` installation (not `act`, not a dry run — PR-A/B/C are infrastructure that only a real apply proves).
- [ ] AC6 (10-minute stream): needs a running GPU; run with the first real start.
- [x] PR-A, PR-B, PR-C merged to `vk-lab-platform` in the order in Design.
- [x] `gitops/`, `scripts/gitops-check.sh` committed here; `make gitops-check` wired into this repository's CI (CORE-010's `ci.yml`).
- [x] `make specs-check` passes.
- [x] Status flipped to DONE in the same pull request; folder renamed `130-D-platform-integration`.
- [x] Evidence section filled with the PR links and the first successful `curl` transcript (`<fqdn>` redacted back to the placeholder — C-14 applies to evidence too).

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| `BackendTrafficPolicy` timeout field for the MVP's 660 s bound. (Q14, settled) | R8, AC6 | vk-lab-platform #109, real render + sync | `timeout.http.requestTimeout: 660s`; no stream idle field exists on Envoy Gateway v1.2.1, so none is set |
| Will the platform accept the SSM prefix and IAM widening in PR-B? (Q15, settled) | R7; ADR 0009 | vk-lab-platform #107, #112, merged and applied | yes |
| Exact name of the `HTTPRoute` CORE-080's chart renders. (settled) | PR-C's `targetRefs.name` | live cluster, `kubectl -n llm get httproute` | `gpu-on-demand`, confirmed |
| Does `local`/kind ever enable `httpRoute` with a real `fqdn`? (settled) | whether `local` needs a carve-out | the shipped `{{- if ne .Values.target "local" }}` guard | no: the pointer `Application`, `AppProject`, and `BackendTrafficPolicy` are all guarded off `local` |
| Wire the new secrets live unit into `persistent-up` eventually? | convenience vs. blast radius (R6) | a later platform decision | no; stays a manual/CI-specific apply |
| Does cascade-deleting the pointer `Application` (Rollback step 1) actually prune namespace `llm`, or only the objects Argo tracks within it? | correctness of Rollback | the first real rollback exercise | assume cascade prunes the namespace too (`CreateNamespace=true` implies Argo owns it) |
| Move `gpu-control` and `vk-llm-ci-role` from `api_token` to `management_tokens` | CI cannot authenticate since #112 | a follow-up PR pair (vk-llm-on-demand workflow + ADR 0016; vk-lab-platform IAM) | settled 2026-11-22: vk-lab-platform #114 + the vk-llm-on-demand workflow PR; live once #114 is applied with `make account-up` |

## Evidence

Collected 2026-11-21/22. Domain redacted to `<fqdn>`.

### Pull requests, in merge order

| Repo | PR | What |
|---|---|---|
| vk-llm-on-demand | [#32](https://github.com/savak1990/vk-llm-on-demand/pull/32) | `gitops/` pointer chart, `gitops-check`, CI wiring |
| vk-lab-platform | [#107](https://github.com/savak1990/vk-lab-platform/pull/107) | `worker_seed`, `ssh_private_key` SSM parameters; chart-shaped ExternalSecrets; ESO ARNs |
| vk-llm-on-demand | [#34](https://github.com/savak1990/vk-llm-on-demand/pull/34) | child `Application` in namespace `llm` (ADR 0046 shape) |
| vk-lab-platform | [#109](https://github.com/savak1990/vk-lab-platform/pull/109) | `AppProject`, pointer `Application`, 660 s traffic policy, `application.namespaces=ahorro,llm` |
| vk-lab-platform | [#112](https://github.com/savak1990/vk-lab-platform/pull/112) | `management_tokens` SSM parameter (separate from `api_token`) |
| vk-llm-on-demand | [#35](https://github.com/savak1990/vk-llm-on-demand/pull/35) | README "Call the API directly" |

Upstream changes that shaped the design mid-plan: vk-lab-platform #104 (ADR
0046, children never in `argocd`), #108 (`vk-llm-ci-role`, OIDC for
`gpu-control`).

### Terraform (read-only plans, then applied by the operator)

- `persistent/secrets` (PR #107): 2 to add, 1 to change, 0 to destroy; (PR #112): 1 to add, 1 to change, 0 to destroy.
- `bootstrap/rolesanywhere`: 0 to add, 1 to change, 0 to destroy, both times.
- SSM after apply: `/vk-hetzner-lab/persistent/llm/{vast_ai_api_key v1, api_token v2, worker_seed v1, ssh_private_key v1, management_tokens v1}`, all `SecureString`.
- ESO role `vk-hetzner-lab-ra-eso` inline policy `consumer`: all five `llm` ARNs present in both the `ssm:GetParameter` and `kms:Decrypt` statements.

### Cluster (AC2, AC3)

```
$ kubectl -n llm get externalsecret
NAME               STORE                 STATUS         READY
vast-ai-api-key    aws-parameter-store   SecretSynced   True
vk-llm-api-token   aws-parameter-store   SecretSynced   True
vk-llm-ssh-key     aws-parameter-store   SecretSynced   True

$ kubectl -n argocd get application root vk-llm-on-demand   → Synced Healthy (both)
$ kubectl -n llm get application gpu-on-demand              → Synced Healthy, chart 0.1.1-main.0ab826b
$ kubectl -n llm get pods                                    → gpu-on-demand-6879db677d-wl6tb 1/1 Running 0 restarts
$ kubectl -n llm get httproute gpu-on-demand                 → hostnames ["llm.<fqdn>"]
$ kubectl -n llm get backendtrafficpolicy                    → gpu-on-demand-traffic-policy
```

Secret keys: `vast-ai-api-key{VAST_API_KEY}`; `vk-llm-api-token{client-keys,
management-tokens, worker-seed}`; `vk-llm-ssh-key{id_ed25519}`.
`worker-seed`: 32 bytes. `client-keys` != `management-tokens` (sha256
prefixes `db93fff2…` / `d6a07d22…`).

The first pod crash-looped (`auth: a key is present in both client-keys and
management-tokens`) while both keys read `api_token`; fixed by PR #112 plus
a forced `ExternalSecret` sync and a rollout restart.

### Route and certificate (AC1)

```
$ curl -sv https://llm.<fqdn>/v1/models
*  issuer: C=US; O=Let's Encrypt; CN=YE2
< HTTP/2 401
{"error":{"message":"no api key provided","type":"authentication_error","code":"invalid_api_key"}}

$ curl -s https://llm.<fqdn>/v1/models -H "Authorization: Bearer $INF"
{"data":[{"created":1795339845,"id":"qwen3.6-35b-a3b","object":"model","owned_by":"gpu-on-demand"}],"object":"list"}

$ curl -s https://llm.<fqdn>/api/v1/gpu/status -H "Authorization: Bearer $MGMT"
{"cost":{"budgetMonthUSD":30,"estimatedInstanceUSD":0,"estimatedMonthUSD":0},"desired":"STOPPED","generation":0,
 "idleDeadline":null,"installationId":"c38399a4de9d","instance":null,"labelPrefix":"vkllm:c38399a4de9d:",
 "lastInferenceFinishedAt":null,"model":{"alias":"qwen3.6-35b-a3b","contextSize":32768},"nextCreateAt":null,
 "provider":"vastai","ready":false,"secondsUntilIdleShutdown":null,"state":"STOPPED","unavailable":"","uptimeSeconds":0}
```

`installationId` = `sha256("llm/gpu-on-demand")[:12]` = `c38399a4de9d`
(CORE-140's contract holds).

### Acceptance criteria

| AC | Result |
|---|---|
| AC1 route + cert | curl transcript above; Let's Encrypt issuer, `401` without a key, `200` with one |
| AC2 ExternalSecrets | `SecretSynced` ×3 |
| AC3 Argo | `Synced Healthy` ×2 |
| AC4 `make gitops-check` | green in CI on every app PR (#32, #34) |
| AC5 no domain | `grep -q '^fqdn: ""$'` inside `gitops-check` |
| AC6 10-min stream | not run; needs a running GPU, costs money; run with the first real start |
| AC7 terraform plan clean | plan counts above: only the shared `persistent-secrets` module changed, only by addition |
