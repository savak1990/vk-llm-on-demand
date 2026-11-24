---
id: "CORE-280"
status: "IN_PROGRESS"
updated: "2026-11-24"
depends_on: ["CORE-120", "CORE-130", "CORE-160"]
adrs: ["0021", "0008", "0009", "0012", "0016", "0017"]
---

# 280 — A dev environment that follows `main`

**Status note:** IN_PROGRESS (2026-11-24). Implementation is in on both
pull requests; AC1, AC2, AC4 and AC7 are proven now (see Evidence). AC3,
AC5 and AC6 need a live deploy and can only run after both pull requests
merge, so this spec carries DONE and folder letter `D` in a follow-up
pull request once that run is recorded. Decisions are recorded in
[ADR 0021](../../../docs/adr/0021-dev-installation-follows-main.md).
Part of the work lands in `vk-lab-platform` as its own pull request.

**Complexity:** M — one new CI job and two scripts here; a generic deploy
credential, one namespace and IAM rights in the platform.
**Risk:** medium — a second controller rents GPUs on the same account; a
wrong RBAC rule could reach production's namespace.
**Estimated effort:** one to two sessions (5–8 h), across two repositories.
**Recommended model:** strong — two repositories, RBAC and IAM.
**Depends on:** [CORE-120](../120-D-release-pipeline/spec.md),
[CORE-130](../130-D-platform-integration/spec.md),
[CORE-160](../160-D-github-actions-remote-control/spec.md).

## Outcome

Every merge to `main` that changes code installs that commit's chart and
image as release `gpu-on-demand` in namespace `llm-dev`, reachable at
`llm-dev.<fqdn>`, with production's configuration, and proves it answers on
three read-only endpoints. Production (`llm`) still changes only through a
reviewed gitops PR. `gpu-control.yml` can start, stop and query either.

## Scope

Includes: the `deploy-dev` job and its scripts in this repository; a
`target` input on `gpu-control.yml`; doc updates; the platform pull request
(generic deploy credential, `llm-dev` namespace and secrets, traffic policy,
IAM).

Excludes: per-PR preview environments; a dashboard namespace selector;
separate dev secrets or a separate Vast.ai account; any Argo CD Application
for `llm-dev`; changes to the controller or the chart.

## Design and contracts

### Environments

| Environment | Namespace | Host | Owner of chart objects | How it changes |
|---|---|---|---|---|
| dev | `llm-dev` | `llm-dev.<fqdn>` | Helm, through `deploy.yml` | every code merge to `main` |
| production | `llm` | `llm.<fqdn>` | Argo CD | a merged PR that pins a clean `0.x.y` in `gitops/values.yaml` |

The release name is `gpu-on-demand` in both, so the HTTPRoute, the state
ConfigMap and the traffic-policy target have the same names. The default
`installationId` is derived from namespace and release, so the two differ.

### Dev values

`scripts/deploy-dev.sh` renders `gitops/` with `helm template`, overriding
`namespace=llm-dev`, `hostPrefix=llm-dev`, `chartVersion=<version>`,
`tag=<full sha>` and `fqdn=<from SSM>`, and takes
`.spec.source.helm.valuesObject` from the rendered Application with `yq`.
It then sets `prometheusRule.enabled=false` and `dashboard.enabled=false`.
Every other value — model, GPU, worker image, gateway caps, secret names —
is production's, so there is one place that pins them. Budget ($30/month),
price cap ($0.80/h), idle timeout (30 min) and explicit mode are the chart
defaults production also uses.

### Deploy job

`deploy.yml` gains `deploy-dev`, needing `[version, publish]`, with
`environment: gpu-control`, `permissions: {contents: read, id-token: write}`
and a 15-minute timeout. It assumes `vars.AWS_ROLE_ARN` and calls
`scripts/deploy-dev.sh <version> <sha>` directly (not through make, which
turns every failure into exit 2).

`scripts/kubeconfig.sh` (from vk-ahorro) reads
`/<project>/cluster/llm-deploy/{endpoint,ca,token}`, masks the token, writes
the kubeconfig to `$RUNNER_TEMP`, and preflights with
`kubectl version --request-timeout=10s`.

Exit codes of `deploy-dev.sh`:

| Code | Meaning | Job result |
|---|---|---|
| 0 | deployed and smoke-checked | green |
| 2 | a credential parameter is missing or the cluster does not answer | green, with a `::warning::` and a step-summary note |
| other | AccessDenied, helm failure (rolled back), smoke failure | red |

Helm: `helm upgrade --install gpu-on-demand oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand --version <version> --namespace llm-dev --rollback-on-failure --timeout 5m -f <values>`.

### Smoke check

After helm succeeds, the script reads the first line of
`/<project>/persistent/llm/api_token` and `.../management_tokens`, masks
both, and calls, against `https://llm-dev.<fqdn>`:

| Call | Token | Expect |
|---|---|---|
| `GET /v1/models` | client key | 200 |
| `GET /api/v1/gpu/status` | management token | 200 |
| `GET /api/v1/gpu/cost` | management token | 200 |

Each call retries every 10 s for up to 5 min (DNS for a new host). The
script never calls `start`, `stop`, `budget` or `chat/completions`: a chat
request reaching a running dev GPU would reset its idle timer. A smoke
failure fails the job and does not roll back; the failures only it finds
(route, DNS, certificate, token) are not fixed by a code rollback.

### `gpu-control.yml`

A `target` choice input (`prod`, `dev`; default `prod`) selects the host
prefix `llm` or `llm-dev`. The concurrency group becomes
`gpu-control-<target>`, and `run-name` shows the target. The token is the
same for both. `scripts/gpu-control.sh` does not change.

### Platform (`vk-lab-platform`)

1. **A generic deploy credential.** One template renders, for each app in a
   values list, a ServiceAccount `<app>-deploy`, a Role and RoleBinding per
   listed namespace with that app's own rules, and the namespace-read
   ClusterRole. Ahorro's entry reproduces today's objects (same names,
   namespaces and rules); only their comments change in the golden render.
   `scripts/lib/ahorro-deploy.sh` becomes one publisher and one cleanup
   function taking the app name, writing
   `/<project>/cluster/<app>-deploy/{endpoint,ca,token,fqdn}`; `argo-up.sh`
   and `argo-down.sh` call them for each app.
2. **The `llm` entry:** namespace `llm-dev`; rules for exactly the kinds the
   dev values render — ConfigMap, Secret (Helm's release storage), Service,
   ServiceAccount, Deployment, Role, RoleBinding, HTTPRoute, ServiceMonitor
   with full verbs; ReplicaSet, Pod and Event read; `pods/log` get. No
   `bind`, `escalate` or `impersonate`: the chart's own Role grants only
   ConfigMap get/update, which the deploy identity already holds.
3. **Namespace `llm-dev`** with ExternalSecrets `vast-ai-api-key`,
   `vk-llm-api-token` and `vk-llm-ssh-key` reading production's SSM
   parameters, and a `BackendTrafficPolicy` on HTTPRoute `gpu-on-demand`
   with the same request timeout as production's policy, rendered from
   the same template. The root Application self-heals them.
4. **`vk-llm-ci-role`** gains `ssm:GetParameter` on
   `/*/cluster/llm-deploy/*` and `/*/persistent/llm/api_token`, and
   `kms:Decrypt` limited by `kms:EncryptionContext:PARAMETER_ARN` to those
   two SecureStrings plus the existing `management_tokens`.

### Failure and cost rules

- Dev rents nothing unless someone starts it; explicit mode, the $30 budget,
  the $0.80 cap and the 30-minute idle timeout apply as in production.
- The two installations cannot touch each other's instances: each lists only
  labels prefixed `vkllm:<installationId>:`.
- The deploy identity cannot read or write namespace `llm`: it has no
  RoleBinding there and no ClusterRole beyond namespace read.

## Requirements

- R1. Every merge to `main` that publishes a new version MUST install that
  exact chart version and image into `llm-dev`, with no manual step
  (ADR 0021 §1).
- R2. A merge that changes only `gitops/`, `docs/` or `specs/` MUST NOT
  redeploy dev (today's `paths-ignore`).
- R3. Production MUST change only through a merged PR that edits
  `gitops/values.yaml`, and MUST pin a clean `0.x.y` release; no workflow
  MUST write to namespace `llm` or edit that file (ADR 0021 §1).
- R4. The deploy job MUST run in the GitHub Environment `gpu-control` and
  reach the cluster only through `vk-llm-ci-role` and OIDC; no long-lived
  cluster credential MUST be stored in GitHub (ADR 0021 §2).
- R5. The deploy credential MUST be able to write only in `llm-dev` and
  MUST NOT read or write namespace `llm`; its rules MUST cover every kind
  the dev values render and no other.
- R6. Dev MUST use production's client key, management token, worker seed,
  SSH key and Vast.ai API key. A dev token is a production token; this is
  accepted (ADR 0021 §4).
- R7. Dev MUST run in explicit mode with production's budget, price cap and
  idle timeout (ADR 0021 §3).
- R8. Dev MUST be reachable at `llm-dev.<fqdn>` with the existing wildcard
  certificate and external-dns, with the same request timeout as
  production's policy, rendered from the same template.
- R9. If a credential parameter is missing or the cluster does not answer,
  the job MUST end green with a warning (the vk-ahorro convention).
- R10. A failed install MUST roll back to the previous dev release
  (`--rollback-on-failure`) and MUST fail the job.
- R11. After a cluster rebuild, dev MUST be restorable by re-running the
  latest `deploy.yml` run, with no other step.
- R12. The platform's deploy credential MUST be one generic template and one
  publisher parameterised by app, with Ahorro's objects and SSM paths
  unchanged.
- R13. Every dev instance MUST carry dev's own `installationId` in its
  label, so neither installation can see or destroy the other's instances.
- R14. After helm succeeds, the job MUST call the three read-only endpoints
  and MUST fail on any non-200 after 5 minutes of retries; it MUST NOT call
  any endpoint that starts, stops or reconfigures a GPU, nor send inference
  (ADR 0021 §5).
- R15. Dev MUST enable the ServiceMonitor and MUST NOT install a
  PrometheusRule or dashboard; production's cover both namespaces.
- R16. `gpu-control.yml` MUST take a `target` input (`prod` default, `dev`)
  that selects the host, and MUST NOT run two actions on one target at once.

## Implementation steps

1. Platform PR: generic deploy credential (template, publisher, callers),
   Ahorro golden unchanged.
2. Platform PR: `llm` entry, namespace `llm-dev`, ExternalSecrets, traffic
   policy, `vk-llm-ci-role` permissions, render-check lists.
3. This repo: `scripts/kubeconfig.sh`, `scripts/deploy-dev.sh` (values
   render, helm, smoke), with a local dry run against `helm template`.
4. This repo: `deploy-dev` job in `deploy.yml`.
5. This repo: `target` input on `gpu-control.yml`.
6. Docs: `docs/testing-strategy.md`, ADR 0008 status line, README index,
   architecture if it names one installation.
7. After both merge: one real merge to `main` deploys dev; one
   `gpu-control` `status` run with `target=dev`.

## Tests / acceptance criteria

- AC1. The rendered dev values differ from production's only in `host`, the
  image tag, the chart version, `prometheusRule` and `dashboard` (a diff in
  the dry run of step 3).
- AC2. With the credential parameters absent, `deploy-dev.sh` exits 2 and
  the job is green with a warning.
- AC3. A merge to `main` produces a `deploy-dev` job that is green within
  10 minutes, and `helm -n llm-dev history gpu-on-demand` shows that
  version.
- AC4. `kubectl auth can-i --as=system:serviceaccount:llm-dev:llm-deploy`
  answers `no` for every verb in namespace `llm` and `yes` for
  `create deployments` in `llm-dev`.
- AC5. `GET https://llm-dev.<fqdn>/api/v1/gpu/status` returns 200 with
  state "no GPU", and the Vast.ai instance list is unchanged by the deploy.
- AC6. `gpu-control` with `target=dev, action=status` succeeds; with
  `target=prod` it still reports production.
- AC7. After the generic credential change, the platform's golden diff for
  Ahorro's deploy objects shows comment lines only: names, namespaces and
  rules are unchanged.

## Definition of done

Both pull requests merged; AC1–AC7 recorded under Evidence; ADR 0021
Accepted; `docs/testing-strategy.md` no longer says "one installation";
status DONE and folder letter `D` in the same pull request.

## Open questions

None. Closed on 2026-11-23:

| Question | Answer |
|---|---|
| Does the deploy identity need `bind`/`escalate`? | No; the chart's Role grants only what the identity already holds (Platform §2) |
| Should production pin clean releases? | Yes, clean `0.x.y` only, as vk-ahorro (R3) |
| Should `gpu-control` get a dev target? | Yes, a `target` input (R16); the CLI already takes `--endpoint` |

## Evidence

Provable now, without a live deploy:

- **AC1.** `make gitops-check` renders production's values and dev's values
  from the pinned OCI chart and diffs them with `host`, `prometheusRule`
  and `dashboard` stripped — they match. It then additionally renders dev's
  values against the main-branch chart in `deploy/helm/gpu-on-demand`
  (not the pinned OCI chart, which is what `deploy-dev.sh` actually
  installs) and asserts the same kind list:
  `ConfigMap Deployment HTTPRoute Role RoleBinding Service ServiceAccount
  ServiceMonitor`. Proven to bite: adding a throwaway `PodDisruptionBudget`
  template under `deploy/helm/gpu-on-demand/templates/` makes
  `make gitops-check` fail with `dev renders kinds '... PodDisruptionBudget
  ...'`; removing it passes again (`gitops-check: ok (chart 0.2.0)`).
- **AC2.** `go test ./test/deploydev/... -run TestDeployDevPassesUnreachableThrough -v`
  stubs `aws` to return `ParameterNotFound` and asserts `deploy-dev.sh`
  exits 2: `--- PASS: TestDeployDevPassesUnreachableThrough`.
- **AC4 (partly).** The platform's golden render
  (`./scripts/gitops-render-check.sh update`, SHARED-220) shows the
  `llm-deploy` Role and RoleBinding exist only in namespace `llm-dev`,
  scoped to the kinds R5 lists; no Role or RoleBinding for `llm-deploy`
  renders in namespace `llm`. The live `kubectl auth can-i` probe (AC4's
  full text) needs a real cluster and is deferred to the post-merge PR.
- **AC7.** `git diff origin/main...HEAD -- tests/golden/gitops-aws` in
  `vk-lab-platform` shows 18 changed Ahorro golden files (comment rewording
  only). One glob diff over all 18 at once —
  `git diff origin/main...HEAD -- 'tests/golden/gitops-aws/*/*ahorro*' |
  grep '^[+-][^+-]' | grep -v '^[+-]#'` — is empty, so every changed
  `+`/`-` line across all of them starts with `#`. Names, namespaces and
  rules are unchanged. The replicasets fix in this same pull request
  (finding 10) only re-ran `gitops-render-check.sh update` on top of that,
  which changed exactly the three `Role__llm-dev__llm-deploy.yaml` variants
  and touched no Ahorro file.

Deferred to a post-merge pull request, once both pull requests have merged
and one real `deploy.yml` run and one `gpu-control status` run have
happened: AC3, AC5, AC6, and the status/folder-letter move to DONE/`D`.
