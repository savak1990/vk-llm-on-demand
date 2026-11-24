# ADR 0020: A dev installation in `llm-dev` follows `main`, deployed by the pipeline

## Status

Accepted (2026-11-23). Supersedes the rejected alternative "A dev namespace
deployed by CI" in [ADR 0008](0008-release-pipeline-and-chart.md) and the
"one installation per provider account" rule in `docs/testing-strategy.md`.
Implemented by CORE-280.

## Context

Production (`llm`) moves only when a reviewed PR pins a new chart version in
`gitops/values.yaml`. Every merge to `main` publishes `0.x.y-main.<sha7>` and
deploys it nowhere, so the first place a merged change runs is production.

ADR 0008 rejected a CI-deployed dev namespace for the MVP because it "would
need a second set of real secrets and a second GPU budget". Both objections
are now answered by choice: dev reuses production's secrets, and each
installation's budget counts only its own instances.

vk-ahorro solved the same problem in its ADRs 0009 and 0010: `ahorro-dev`
tracks `main` through a pipeline `helm upgrade` with a scoped deploy
credential, and Argo CD owns only the released namespace.

## Decision

1. **A second installation, `gpu-on-demand` in namespace `llm-dev`, at
   `llm-dev.<fqdn>`, follows every code merge to `main`.** Production stays
   a manual gitops change and pins clean `0.x.y` releases only.
2. **The pipeline pushes; Argo CD does not pull.** A `deploy-dev` job in
   `deploy.yml` runs `helm upgrade --install --rollback-on-failure` inside
   the `gpu-control` Environment through `vk-llm-ci-role` and OIDC, with a
   service-account token the platform publishes to SSM. Helm alone owns the
   chart objects in `llm-dev`; Argo CD self-heals only the platform objects
   there (namespace, RBAC, ExternalSecrets, traffic policy).
3. **Dev is production's configuration plus newer code.** Its values are
   rendered from the same `gitops/` chart as production's, with only the
   namespace, host prefix, version and image tag changed, and alerts and
   dashboard off. Budget, price cap, idle timeout and explicit mode are
   production's.
4. **Dev shares production's Vast.ai account and all four application
   secrets** (client key, management token, worker seed, SSH key). Instance
   labels carry each installation's own `installationId`, so neither
   installation lists, adopts or destroys the other's instances.
5. **After each deploy the job calls three read-only endpoints**
   (`/v1/models`, `/api/v1/gpu/status`, `/api/v1/gpu/cost`) and fails on any
   non-200. It never calls an endpoint that can rent, stop or reconfigure a
   GPU, and never sends inference.

## Consequences

- The monthly spend ceiling on the shared account is $60 ($30 + $30), and
  both installations draw on one balance; the low-balance gate in each sees
  the other's spend.
- A dev management token is a production management token. Anyone who can
  start the dev GPU can start the production GPU.
- Production's alerts and dashboard do not filter by namespace, so they also
  cover dev; the dashboard mixes both until a namespace selector is added.
- A pin change in `gitops/values.yaml` alone does not redeploy dev; dev picks
  it up on the next code merge.
- After a cluster rebuild, dev returns when the latest `deploy.yml` run is
  re-run.
- The `llm-deploy` identity has full verbs on Secrets in `llm-dev`, since
  Helm's own release storage needs them; those Secrets include the Vast.ai
  API key and the SSH key. Anyone who can run a workflow in the
  `gpu-control` Environment can therefore reach the Vast.ai key. Ahorro's
  deploy credential has the same shape.
- After a cluster rebuild, a dev GPU that was already running is not reaped
  until `deploy.yml` is re-run or the worker's own idle timer fires;
  production's GPU returns through Argo CD instead.

## Alternatives considered

- **Argo CD pulls the newest `main` chart by semver range.** `-main.<sha7>`
  does not sort by time, and no range matches a prerelease. Rejected.
- **A bot commits the dev version to a gitops file.** vk-ahorro tried and
  superseded this (its ADR 0007). Rejected.
- **Dev gets its own four secrets.** Keeps a dev token away from production,
  at the cost of four more ciphertext files and SSM parameters. Rejected by
  the user for simplicity.
- **Dev on a smaller model or lower budget.** Tests code paths more cheaply
  but not production's configuration. Rejected.
- **A smoke test that starts a real GPU on every merge.** About $0.30–0.50
  and 10–15 minutes per merge. Rejected.
