---
id: "CORE-130"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-080", "CORE-110", "CORE-120"]
adrs: ["0008", "0009", "0010", "0012", "0013"]
---

# 130 — Platform integration

**Status note:** DRAFT, written 2026-11-01 before any code exists. Three of this spec's seven deliverables are pull requests against a different repository (`vk-lab-platform`) and cannot be merged by this repository's own CI; they are designed here and executed by hand against that repository.

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

- **PR-A**: the pointer `AppProject` and `Application` that make
  `vk-llm-on-demand` a platform application, copying `vk-ahorro`'s shape.
- **PR-B**: the five committed secret ciphertext files, the SSM parameters
  they populate, and the ESO IAM policy that reads them.
- **PR-C**: the Envoy traffic policy that disables (or raises to 660 s,
  explicit mode's bound) the `llm` `HTTPRoute`'s request timeout and sets a
  120 s stream idle timeout (ADR 0010 Consequences).

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

*Every `vk-lab-platform` file:line reference below is as read on 2026-11-01; line references verified at implementation time. Q14 (PR-C's timeout CRD) stays open until PR-C renders.*

### Two chart layers

Platform PR-A's `AppProject`/`Application` (wave 4/5) source this
repository over **git** at `path: gitops`, rendering namespace `argocd`
(it creates further `Application` objects, not workloads). This spec's own
`gitops/templates/application.yaml` is the one it renders: it sources the
published **OCI** chart `gpu-on-demand` (CORE-120) at an exact version into
namespace `llm`. Two Argo `Application` layers, same shape `vk-ahorro` uses
for its own fan-out, one layer deeper here because there is exactly one
service, not several.

The platform owns one thing: which git revision of `gitops/` to render
(PR-A's `vkllm.targetRevision`, mirroring `ahorro.targetRevision` in
`vk-lab-platform/gitops/values.yaml` lines 206–217) plus `fqdn`,
`chartVersion`, `tag` passed through as Helm parameters. This repository's
`gitops/` owns the version pin (ADR 0008 §5). A public OCI Helm chart
sourced by an Argo `Application` with no `Repository`/`enableOCI`
registration already runs on the platform —
`vk-lab-platform/gitops/templates/platform/shared/postgres/barman-plugin-application.yaml`
lines 14–19 (`repoURL: ghcr.io/cloudnative-pg/charts`, `chart:
plugin-barman-cloud`, no credential) — and this spec's inner `Application`
copies that exact `source` shape.

### `gitops/values.yaml` (this repository)

| Key | Default | Meaning |
|---|---|---|
| `fqdn` | `""` | from the platform's helm parameter; required |
| `namespace` | `llm` | fixed |
| `charts.registry` | `ghcr.io/savak1990/vk-llm-on-demand/charts` | no `oci://` scheme (ADR 0008 §4) |
| `chartVersion` | `""` | required; exact `gpu-on-demand` chart version |
| `tag` | `""` | required; becomes `image.tag` |
| `hostPrefix` | `llm` | fixed; `host` = `<hostPrefix>.<fqdn>` |
| `externalSecrets.enabled` | `true` | chart default is `false` (ADR 0008 §4) |
| `httpRoute.enabled` | `true` | chart default is `false` |
| `serviceMonitor.enabled` | `true` | chart default is `false`; without it the dashboard and the four alerts never render on the only installation (NFR-9.5; C-4; FR-6.4) |

### `gitops/templates/application.yaml`

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: gpu-on-demand
  namespace: argocd
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: vk-llm-on-demand
  source:
    repoURL: {{ .Values.charts.registry | quote }}
    chart: gpu-on-demand
    targetRevision: {{ .Values.chartVersion | quote }}
    helm:
      parameters:
{{- if .Values.tag }}
        - name: image.tag
          value: {{ .Values.tag | quote }}
{{- end }}
{{- if .Values.fqdn }}
        - name: host
          value: {{ printf "%s.%s" .Values.hostPrefix .Values.fqdn | quote }}
{{- end }}
        - name: externalSecrets.enabled
          value: {{ .Values.externalSecrets.enabled | quote }}
        - name: httpRoute.enabled
          value: {{ .Values.httpRoute.enabled | quote }}
        - name: serviceMonitor.enabled
          value: {{ .Values.serviceMonitor.enabled | quote }}
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

`image.tag`/`host` are guarded by `{{- if }}` as defense in depth: a rendered
`value: ""` is a permanent `OutOfSync` on the platform
(`vk-lab-platform/scripts/gitops-render-check.sh` lines 27–40), the same
hazard the ahorro `Application` template's own comment describes, even
though `templates/validate.yaml` should already have failed the render
first. `externalSecrets.enabled`/`httpRoute.enabled`/`serviceMonitor.enabled`
default `"true"` and are never empty, so they need no guard.

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

| Step | Command | Why |
|---|---|---|
| 1 | `helm lint gitops/ --set fqdn=lab.example.com --set chartVersion=0.1.0 --set tag=deadbee` | well-formed |
| 2 | `helm template ... > rendered.yaml` (same `--set`) | positive render |
| 3 | `kubeconform -summary -strict -ignore-missing-schemas -schema-location default -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' rendered.yaml` | `argoproj.io` `Application` isn't in kubeconform's default schema set; flags copied from `vk-lab-platform/.github/workflows/lifecycle-test.yml` lines 313–318, which validates the same kind today |
| 4 | `! helm template gitops/` (no `--set`) | negative: must fail, proving `validate.yaml` is wired |
| 5 | `grep -RnE '[a-z0-9-]+\.[a-z]{2,}' gitops/ --include=*.yaml \| grep -v example.com` | no literal domain committed (C-14; NFR-7.6) — a heuristic; `vk-lab-platform/scripts/verify-no-leaks.sh` is the authoritative check on that side |

### PR-A: pointer `AppProject` and `Application`

New files `vk-lab-platform/gitops/templates/apps/vk-llm-on-demand/{appproject,application}.yaml`,
copied from `gitops/templates/apps/vk-ahorro/{appproject,application}.yaml`
with every `ahorro`/`vk-ahorro` replaced by `vk-llm-on-demand`, plus: no
`target` helm parameter (this chart has none, unlike ahorro's); `fqdn`
(from `.Values.envoyGateway.fqdn`), `chartVersion`, `tag` instead of the
four `cognito.*` fields, each still guarded by `{{- if }}`; `destinations`
namespaces `llm`/`argocd` instead of `ahorro`/`argocd`. Waves, `syncPolicy`,
and the `argocd`-namespace `destination` (both apps render further
`Application` objects) are unchanged.

Also: add a `vkllm: {targetRevision: main, chartVersion: "", tag: ""}`
block to `vk-lab-platform/gitops/values.yaml` (sibling to `ahorro:`, lines
206–217); add `AppProject__argocd__vk-llm-on-demand
Application__argocd__vk-llm-on-demand` to `REQUIRED_OBJECTS_CIVO`/`_HETZNER`/`_LOCAL`
in `gitops-render-check.sh` (the lists already carrying the `vk-ahorro`
pair); run `./scripts/gitops-render-check.sh update` to refresh the golden.

### PR-B: secrets

Hand-created Secrets (`kubectl create secret generic`, the three `existingSecrets.{provider,apiKeys,ssh}` objects ADR 0009 §1 names) remain the first-deployment path — ADR 0009 Risks allows verifying the MVP end to end before this PR lands. PR-B exists so the five values behind those three objects survive a cluster rebuild, not for first-deployment correctness: every other Persistent-lifecycle secret here is rebuilt from committed ciphertext on each `make up`, and the Vast.ai API key and SSH host key are not values an operator wants to regenerate and re-register by hand after every teardown.

Five committed ciphertext files, one per ADR 0009 §1 value (`client-keys`, `management-tokens`, `worker-seed` are three keys of the one `apiKeys` Secret, each delivered from its own parameter), following the exact naming the platform already uses for a per-app secret under a shared project (`vk-lab-platform/secrets/vk-hetzner-lab/ahorro-test-user-password.enc`):

```
secrets/<project>/vk-llm-on-demand-vast-api-key.enc
secrets/<project>/vk-llm-on-demand-client-keys.enc
secrets/<project>/vk-llm-on-demand-management-tokens.enc
secrets/<project>/vk-llm-on-demand-worker-seed.enc
secrets/<project>/vk-llm-on-demand-ssh-private-key.enc
```

Created with the platform's documented procedure (`vk-lab-platform/Makefile`
lines 418–427, `vk-lab-platform/CLAUDE.md` "Secrets and authentication"):
`make secret-encrypt NAME=vk-llm-on-demand-<name> VALUE=<value>`, once per
file above (`SCOPE=project`, the default; `PROJECT_NAME` selects the
platform project, e.g. `vk-hetzner-lab`).

To reach SSM, PR-B adds a **new** Terraform module and live unit rather than
extending `terraform/modules/persistent-secrets` (which holds the
platform's own `postgres`/`grafana`/`argocd` secrets): extending it would
add five new *required* variables tied to five new *required* files,
breaking `make persistent-up` for every other project in the account
(`vk-civo-lab`, `vk-hetzner-ci`, `vk-hetzner-lab`) the moment this app's
files didn't exist there too. Instead:

- `terraform/modules/vk-llm-on-demand-secrets/{main,variables}.tf` — same
  shape as `persistent-secrets/{main,variables}.tf`: one `data
  "aws_kms_secrets"` block with five `secret {}` entries (`filebase64` over
  the five `.enc` paths), five `aws_ssm_parameter` resources, `type =
  "SecureString"`, `key_id = "alias/lab-secrets"`, names
  `/${var.project}/apps/vk-llm-on-demand/{vast-api-key,client-keys,management-tokens,worker-seed,ssh-private-key}`
  (ADR 0009 §3's exact path).
- `terraform/live/persistent/vk-llm-on-demand-secrets/terragrunt.hcl` — same
  shape as `terraform/live/persistent/secrets/terragrunt.hcl`, pointing at
  the five files. Applied by hand (`terragrunt apply` in that directory),
  **not** wired into `persistent-up`'s aggregate run, so a project without
  this app is unaffected (open question below).

`terraform/modules/rolesanywhere/main.tf` (lines 123–145, the `eso` policy
document): a new local list builds five ARNs the same way
`postgres_password_arn`/`grafana_password_arn` (lines 13–14) already do;
both the `ssm:GetParameter` statement's `resources` and the `kms:Decrypt`
statement's `StringEquals` condition `values` gain that list via
`concat(...)`:

```hcl
locals {
  vkllm_secret_names = ["vast-api-key", "client-keys", "management-tokens", "worker-seed", "ssh-private-key"]
  vkllm_secret_arns = [for n in local.vkllm_secret_names :
    "arn:aws:ssm:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:parameter/${var.project}/apps/vk-llm-on-demand/${n}"
  ]
}
```

Five explicit ARNs, not a wildcard — the existing `StringEquals` condition
operator (line 141) has no prefix form, and an explicit list is tighter
than a wildcard would be. ADR 0009 §3's "widens... to that prefix" is
satisfied in effect.

### PR-C: Envoy request timeout

ADR 0010 Consequences: the MVP runs `lifecycle.provisioningMode: explicit` only, so no inference request ever waits for a cold start — the Envoy route timeout MUST be disabled or `>= model.generationTimeout + 60 s` (600 s + 60 s = 660 s, `docs/architecture.md` §9), plus a 120 s stream idle timeout. On-demand mode's 1080 s figure (ADR 0007 §4) is future-only and out of this spec's MVP scope. The CRD this needs already runs on the platform —
`vk-lab-platform/gitops/templates/platform/shared/observability/grafana-traffic-policy.yaml`
is a working `gateway.envoyproxy.io/v1alpha1` `BackendTrafficPolicy` on
Envoy Gateway v1.2.1 today — the CRD's presence is verified at
implementation time; Q14's timeout fields stay open until PR-C renders
(research §5 only confirms the chart version). New file
`vk-lab-platform/gitops/templates/apps/vk-llm-on-demand/traffic-policy.yaml`,
same shape:

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
      requestTimeout: 660s    # or disabled; ADR 0010 Consequences (explicit mode only)
      idleTimeout: 120s       # stream idle timeout; exact field name unverified, see Open questions
{{- end }}
```

Sync-wave `6` (one after the pointer `Application`'s wave `5`) is a
best-effort hint, not a guarantee — namespace `llm` and its `HTTPRoute` are
created by a *different* `Application`'s own reconciliation, so waves don't
strictly serialize across the two. `SkipDryRunOnMissingResource=true` (same
reason the grafana file uses it) plus `automated.selfHeal` self-corrects a
first-apply miss within the ~3-minute bound vk-lab-platform ADR 0045 §Consequences records
for a comparable case. The `HTTPRoute` name (`gpu-on-demand`, assumed from
the chart name) is confirmed only once CORE-080 is written.

### Vast.ai prerequisites (operator checklist) and ordering

1. Create a Vast.ai account and fund it — `$0` balance schedules a running
   instance for deletion (research §1.5); generate an API key from the
   console → `vk-llm-on-demand-vast-api-key`.
2. Generate an `ed25519` keypair; register the **public** half in the
   account's "Manage Keys" page (every new instance gets it automatically,
   research §1.3); the **private** half → `vk-llm-on-demand-ssh-private-key`.
3. Generate inference client keys with `make keygen` (ADR 0009 §2, `sk-vkllm-` prefix) → `vk-llm-on-demand-client-keys`. **Create a management token for the CLI and a separate one for GitHub Actions**, same command (ADR 0012 §3–§5) → both lines in `vk-llm-on-demand-management-tokens`. Generate the worker seed (`openssl rand -base64 32`, ADR 0009 §1) → `vk-llm-on-demand-worker-seed`. Run the five `make secret-encrypt` calls and commit the `.enc` files.
4. Merge **PR-B** first — PR-A's pointer would otherwise sync a chart whose
   `ExternalSecret`s never resolve (ADR 0009 Risks allows hand-created
   Secrets as a stopgap meanwhile).
5. Merge **PR-A** next — creates the workload and its `HTTPRoute`.
6. Merge **PR-C** last — its `targetRefs` needs that `HTTPRoute` to exist
   to take effect (not to apply without error).
7. Here: bump `chartVersion`/`tag` only after CORE-120 publishes that
   version to GHCR.
8. The repository secret `VK_LLM_TOKEN` (one `management-tokens` line) and variable `VK_LLM_API_ENDPOINT` are GitHub repository configuration for `gpu-control.yml`, not a platform pull request — CORE-160 owns that setup, not repeated here.

### Rollback

0. `POST /api/v1/gpu/stop` (management token; ADR 0012 §1, superseding `/admin/v1/scale`); wait for `state: STOPPED` and an empty provider instance list for the installation's label prefix — a GPU must not keep billing through a rollback (C-1; FR-4.1 never destroys on shutdown, so this step cannot be skipped).
1. `kubectl -n argocd delete application vk-llm-on-demand` (cascade, via the finalizer) prunes the inner `Application` and every resource it owns — the fast path. Whether cascade also removes namespace `llm` itself is an Argo CD behavior not stated in `docs/research.md` (open question below).
2. The durable rollback is `git revert` of PR-A in `vk-lab-platform`; PR-B's secrets/SSM/IAM and PR-C's traffic policy are left in place (unused, they cost nothing), so re-adding PR-A later re-attaches to the same values with no new setup.

## Requirements

- R1. `gitops/values.yaml` MUST default `externalSecrets.enabled`, `httpRoute.enabled`, and `serviceMonitor.enabled` to `true`; `fqdn`, `chartVersion`, and `tag` MUST default to the empty string. (source: ADR 0009 §3; ADR 0008 §4; ADR 0008 §5)
- R2. `gitops/templates/validate.yaml` MUST fail the Helm render when `fqdn`, `chartVersion`, or `tag` is empty. (source: C-14; NFR-7.6)
- R3. `gitops/templates/application.yaml` MUST render exactly one Argo `Application` with `spec.source.chart: gpu-on-demand`, `spec.source.targetRevision` equal to `.Values.chartVersion`, and MUST NOT render a Helm parameter with an empty `value`. (source: ADR 0008 §5; platform precedent `barman-plugin-application.yaml`)
- R4. That `Application`'s `syncPolicy` MUST set `automated.prune: true`, `automated.selfHeal: true`, `CreateNamespace=true`, and `ServerSideApply=true`, and MUST carry the finalizer `resources-finalizer.argocd.argoproj.io`. (source: ADR 0009 §3; `vk-ahorro` precedent)
- R5. No file committed in this repository's `gitops/` MUST contain a literal domain, `fqdn`, or secret value. (source: C-14; NFR-7.6)
- R6. PR-B's new SSM parameters (all five: `vast-api-key`, `client-keys`, `management-tokens`, `worker-seed`, `ssh-private-key`) MUST use `type = "SecureString"` with `key_id = "alias/lab-secrets"` and MUST NOT be added to the `terraform/modules/persistent-secrets` module every platform project shares. (source: ADR 0009 §1, §3; C-24)
- R7. PR-B's widened `eso` IAM policy document MUST grant `ssm:GetParameter` and the matching `kms:Decrypt` condition on exactly the five ARNs under `/<project>/apps/vk-llm-on-demand/`, and MUST NOT alter the two existing ARNs (`postgres_password_arn`, `grafana_password_arn`). (source: ADR 0009 §3; C-15)
- R8. PR-C's `BackendTrafficPolicy` MUST set `spec.timeout.http.requestTimeout` to `660s` (explicit mode's bound: `model.generationTimeout + 60 s`) or disable it, and MUST set a 120 s stream idle timeout. (source: ADR 0010 Consequences; architecture.md §9)
- R9. This repository MUST hold no platform-side file; PR-A, PR-B, and PR-C are pull requests against `vk-lab-platform`, never a file committed here. (source: C-24)
- R10. Secrets reaching the pod MUST do so only by the three `existingSecrets.{provider,apiKeys,ssh}` references ADR 0009 §1 defines; this spec MUST NOT introduce a fourth secret-delivery path. (source: C-15; ADR 0009 §1)
- R11. `scripts/gitops-check.sh` MUST exit nonzero when `gitops/` is rendered with any of `fqdn`, `chartVersion`, `tag` unset, and MUST exit zero when all three are set to placeholder values. (source: R2)

## Implementation steps

1. Write `gitops/Chart.yaml`/`values.yaml`. Test: `helm lint gitops/ --set fqdn=lab.example.com --set chartVersion=0.1.0 --set tag=deadbee` exits 0.
2. Write `templates/validate.yaml`. Test: `helm template gitops/` with no `--set` exits nonzero, stderr contains `fqdn is required`.
3. Write `templates/application.yaml`. Test: the same `--set` render piped through `yq '.spec.source.chart'` prints `gpu-on-demand`; `yq '.spec.source.helm.parameters[].value'` contains no empty string.
4. Write `scripts/gitops-check.sh` (the five steps in Design). Test: exits 0 on repo HEAD; exits nonzero at the grep step against a copy with a literal domain hard-coded into `values.yaml`.
5. Draft PR-A's three files plus the `gitops-render-check.sh` list addition in a `vk-lab-platform` checkout. Test: `./scripts/gitops-render-check.sh update` then (check mode) exits 0; `git diff --stat` shows only the two new objects and the golden directory.
6. Draft PR-B's Terraform module, live unit, and `rolesanywhere` diff. Test: `terragrunt plan` in the new live unit shows exactly five resources to add; `terraform plan` in `rolesanywhere` shows the `eso` policy gaining exactly five ARNs in both statements, nothing else.
7. Draft PR-C's traffic-policy file. Test: `helm template vk-lab-platform/gitops --set target=hetzner --set envoyGateway.fqdn=lab.example.com ... | yq 'select(.kind=="BackendTrafficPolicy") | .spec.timeout.http.requestTimeout'` prints `660s`.
8. Run the Vast.ai checklist and the five `make secret-encrypt` calls. Test: `secret-decrypt.sh vk-llm-on-demand-vast-api-key` prints a nonempty value, no error.

## Tests / acceptance criteria

- AC1. `curl -sv https://llm.<fqdn>/v1/models` returns HTTP 200 with a body listing `model.alias`, and `curl -sv -H "Authorization: Bearer <management-tokens line>" https://llm.<fqdn>/api/v1/gpu/status` returns HTTP 200 with a status document; both TLS certificate issuer lines in `curl -sv`'s stderr name Let's Encrypt. (NFR-9.4)
- AC2. `kubectl -n llm get externalsecret -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].reason}'` prints `SecretSynced` for all three `ExternalSecret` objects. (R7; R10)
- AC3. `kubectl -n argocd get application vk-llm-on-demand gpu-on-demand -o jsonpath='{.status.sync.status}{" "}{.status.health.status}'` (run once per app) prints `Synced Healthy` for both. (R3; R4)
- AC4. `make gitops-check` (this repository) exits 0 on a clean checkout. (R11)
- AC5. `grep -rlE '[a-z0-9-]+\.(com|net|org|io|dev)' gitops/ --include=*.yaml` (this repository) returns no match outside `lab.example.com`/`<fqdn>`-style placeholders. (R5)
- AC6. Against a GPU already `READY` (started with `vk-llm start`, no cold-start wait in explicit mode — ADR 0010 §2), a streamed request that runs for `model.generationTimeout` (10 min) completes through Envoy with no `504` — proving the 660 s route timeout (or its disablement) and the 120 s stream idle timeout both clear real generation timing. (R8; ADR 0010 Consequences)
- AC7. `terraform plan` in `terraform/live/persistent/vk-llm-on-demand-secrets` and in every other project's `terraform/live/persistent/secrets` both run clean (no forced changes) after PR-B merges, proving the new module didn't touch the shared one. (R6)

## Definition of done

- [ ] AC1–AC7 pass on a real `vk-lab-platform` installation (not `act`, not a dry run — PR-A/B/C are infrastructure that only a real apply proves).
- [ ] PR-A, PR-B, PR-C merged to `vk-lab-platform` in the order in Design.
- [ ] `gitops/`, `scripts/gitops-check.sh` committed here; `make gitops-check` wired into this repository's CI (CORE-010's `ci.yml`).
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE in the same pull request; folder renamed `130-D-platform-integration`.
- [ ] Evidence section filled with the three PR links and the first successful `curl` transcript (`<fqdn>` redacted back to the placeholder — C-14 applies to evidence too).

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Does a 660 s `requestTimeout` (or disabling it) plus a 120 s stream idle timeout on `BackendTrafficPolicy` behave as shown (CRD itself already confirmed live, see Design)? (Q14) | R8, AC6 | PR-C's implementation step, real render + sync | CRD/field present (`grafana-traffic-policy.yaml`); exact values unverified until PR-C runs |
| Exact Envoy Gateway v1.2.1 `BackendTrafficPolicy` field for the 120 s stream idle timeout (`http.idleTimeout` assumed above; could be `tcp.idleTimeout` or absent). | R8, AC6 | PR-C's implementation step, real render | assume `http.idleTimeout`; corrected when PR-C renders against a real install |
| Will the platform accept the SSM prefix and IAM widening in PR-B? (Q15) | R7; ADR 0009 stays Proposed until this merges | PR-B's review | hand-created Secrets until merged (ADR 0009 Risks) |
| Exact name of the `HTTPRoute` CORE-080's chart renders. | PR-C's `targetRefs.name` | CORE-080's spec | assumed `gpu-on-demand`; corrected with CORE-080 if wrong |
| Does `local`/kind ever enable `httpRoute` with a real `fqdn`, or does `validate.yaml` leave the pointer `Application` `Degraded` there? | whether `local` needs a carve-out | first real `target=local` render, or CORE-140 | assume `local` never deploys this pointer with real traffic |
| Wire the new secrets live unit into `persistent-up` eventually? | convenience vs. blast radius (R6) | a later platform decision | no; stays a manual/CI-specific apply |
| Does cascade-deleting the pointer `Application` (Rollback step 1) actually prune namespace `llm`, or only the objects Argo tracks within it? | correctness of Rollback | the first real rollback exercise | assume cascade prunes the namespace too (`CreateNamespace=true` implies Argo owns it) |

## Evidence

None yet.
