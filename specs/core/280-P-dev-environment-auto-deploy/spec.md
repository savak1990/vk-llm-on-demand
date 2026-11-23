---
id: "CORE-280"
status: "DRAFT"
updated: "2026-11-23"
depends_on: ["CORE-120", "CORE-130", "CORE-160"]
adrs: ["0008", "0009", "0012"]
---

# 280 — A dev environment that follows `main`

**Status note:** DRAFT, problem and requirements, with the scope agreed
2026-11-23: dev auto-deploy only, production stays a manual gitops change,
no per-PR previews; one Vast.ai account for both. No full design yet.

## Problem

The only installation is production (namespace `llm`). It moves only when
someone pins a new chart version in `gitops/values.yaml`. Every merge to
`main` publishes a chart and image (`0.x.y-main.<sha>`) but deploys them
nowhere, so the first place a merged change runs is production.

## Agreed scope

| Environment | Namespace | How it changes |
|---|---|---|
| **dev** | `llm-dev` | automatically: every merge to `main` that changes code installs that commit's chart and image |
| **production** | `llm` | only by hand: a reviewed PR that edits `chartVersion` and `tag` in `gitops/values.yaml`; Argo CD syncs it after the merge |
| PR previews | — | out of scope (see below) |

## What exists today (verified 2026-11-23)

- `deploy.yml` runs on every push to `main`, ignoring `docs/`, `specs/` and
  `gitops/`; it computes `0.x.y-main.<sha7>` and calls `publish.yml`, which
  pushes the image (`:<sha>` and `:<version>`) and the OCI chart. It writes
  nothing to any cluster.
- vk-ahorro already does exactly this for its own `ahorro-dev` namespace:
  a `deploy-dev` job runs `helm upgrade --install` with the just-published
  version, using a deploy credential the platform publishes to SSM
  (`/<project>/cluster/ahorro-deploy/{endpoint,ca,token,fqdn}`), a service
  account with Roles in its own namespaces only, and a CI role reached by
  GitHub OIDC. A cluster that does not answer makes the job end green with a
  warning, so a rebuilt or parked cluster does not turn every merge red.
  All of these platform pieces are hard-coded to Ahorro.
- This repository's CI role, `vk-llm-ci-role`, trusts only jobs that run in
  the GitHub Environment `gpu-control`; that Environment holds no secrets or
  variables and allows only protected branches (`main`). A job on a push to
  `main` can run there; a PR branch cannot.
- Production's route has a `BackendTrafficPolicy` with a 660 s request
  timeout; it targets only the `gpu-on-demand` route in `llm`.
- Every installation is a full controller that can rent its own GPU. The
  default `installationId` is derived from namespace and release, so an
  install in `llm-dev` gets its own id and its orphan sweep touches only its
  own instances.
- In explicit mode an installation costs nothing until someone starts its
  GPU.

## Decision: one Vast.ai account for dev and production (2026-11-23)

Dev and production use the same Vast.ai account and API key (the existing
`/<project>/persistent/llm/vast_ai_api_key`). Verified consequences:

- **Cleanup stays separate.** Each controller lists only instances whose
  label starts with its own `vkllm:<installationId>:` prefix
  (`internal/controller/reconcile.go`, `internal/provider/vastai/client.go`),
  and the two namespaces give two different ids. Production never destroys
  dev's GPU, nor the reverse.
- **Spend limits stay separate; the balance does not.** Each installation's
  monthly budget counts only its own instances, so the combined monthly
  ceiling is production's budget plus dev's ($30 + $5 by default). The
  account balance is shared: the "balance low" gate
  (`balance < 2 × maxHourlyPriceUSD`, `internal/budget/checker.go`) sees
  the same balance from both, and a running dev GPU drains the money
  production also needs.
- A second account can be introduced later by giving dev its own
  `vast_ai_api_key` parameter; nothing else changes.

## Requirements

- R1. Every merge to `main` that publishes a new version MUST install that
  exact chart version and image into namespace `llm-dev`, with no manual
  step.
- R2. A merge that changes only `gitops/`, `docs/` or `specs/` MUST NOT
  redeploy dev (today's `paths-ignore` already gives this).
- R3. Production (`llm`) MUST change only through a merged PR that edits
  `gitops/values.yaml`; no workflow MUST write to namespace `llm` or edit
  that file. Argo CD MUST sync such a merge without a manual refresh, within
  its normal polling interval.
- R4. The dev deploy job MUST run inside the GitHub Environment
  `gpu-control` and reach the cluster only through `vk-llm-ci-role` and
  OIDC; no long-lived cluster credential MUST be stored in GitHub.
- R5. The credential the job uses MUST be able to write only in `llm-dev`
  and MUST NOT be able to read or write namespace `llm`. Its permissions
  MUST cover every kind the chart renders there (including NetworkPolicy,
  Role/RoleBinding, ServiceMonitor, PrometheusRule and ExternalSecret if
  enabled).
- R6. Dev MUST have its own client key, management token, worker seed and
  SSH key, distinct from production's, so a dev credential can never start,
  stop or reconfigure the production GPU.
- R7. Dev MUST have its own monthly budget, lower than production's
  (default $5), and MUST run in explicit mode.
- R8. Dev MUST be reachable at `llm-dev.<fqdn>` with the existing wildcard
  certificate and external-dns, and its route MUST get the same request
  timeout policy as production's.
- R9. If the cluster does not answer, the deploy job MUST end with a warning,
  not a failure (the vk-ahorro convention).
- R10. A failed install MUST roll back to the previous dev release
  (`helm upgrade --rollback-on-failure`) and MUST fail the job.
- R11. After a cluster rebuild, dev MUST be restorable by re-running the
  latest `deploy.yml` run, with no other step.
- R12. Platform pieces MUST be parameterised by application where practical
  (namespace list, SSM prefix, role ARNs), so vk-ahorro and this repository
  do not keep two hand-copied variants.
- R13. Dev MUST use production's Vast.ai API key (one account), and MUST
  label every instance with its own installation id so neither installation
  can see or destroy the other's instances.

## Facts the design needs

- Platform side: namespace `llm-dev` and a deploy service account with
  Roles in it (like `shared/rbac/ahorro-deploy.yaml`); the credential
  publisher (like `scripts/lib/ahorro-deploy.sh`, called from both
  `argo-up.sh` paths and cleaned up by `argo-down.sh`); `vk-llm-ci-role`
  gaining `ssm:GetParameter` on the new `cluster/llm-deploy/*` parameters and
  `kms:Decrypt` limited to its token; ExternalSecrets in `llm-dev` for the four dev
  secrets plus the shared Vast.ai key; render-check lists and the aws golden.
- Repository side: a `deploy-dev` job in `deploy.yml`, a `kubeconfig.sh`
  and a `deploy-dev.sh` mirroring vk-ahorro's, setting `host`,
  `existingSecrets.*`, model/worker pins and the dev budget.
- The model, GPU and context pins for dev come from the same
  `gitops/values.yaml` as production, so dev runs what production would run
  after a bump, plus the new code.

## Open questions

| Question | Why it matters |
|---|---|
| Does the deploy service account need `bind`/`escalate` to create the chart's Role? | Kubernetes refuses to grant permissions the creator does not hold |
| Should production pin clean releases (`release.yml`, `0.x.y`) instead of `main` prereleases? | Today it pins `0.1.1-main.0ab826b` |
| Should the `gpu-control` workflow and the `vk-llm` CLI get a dev target (`--endpoint`)? | Starting the dev GPU from Actions |

## Out of scope

Per-PR preview environments. If wanted later, the safe shape found on
2026-11-23 is a preview workflow dispatched from `main` with a PR number:
an unprivileged job builds the PR's image, and only `main`'s workflow code
deploys it, inside `gpu-control`, into a separate namespace with its own
small budget and keys. Running a preview from the PR's own branch would
need the branch rule removed from `gpu-control`, which would let any pushed
branch assume the role.

## Evidence

None. Problem and requirements only.
