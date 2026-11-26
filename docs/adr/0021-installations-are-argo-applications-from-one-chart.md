# ADR 0021: Every installation is an Argo CD Application of the one `gitops/` chart; dev tracks the newest build

## Status

Accepted (2026-11-27). Supersedes the rejected alternative "A dev namespace
deployed by CI" in [ADR 0008](0008-release-pipeline-and-chart.md) and the
"one installation per provider account" rule in `docs/testing-strategy.md`.
Implemented by CORE-280.

## Context

Production (`llm`) moves only when a reviewed PR pins a new chart version in
`gitops/values.yaml`. Every merge to `main` publishes a prerelease and
deploys it nowhere, so the first place a merged change runs is production.

A first design (2026-11-23) deployed dev from CI with `helm upgrade` and a
cluster credential in SSM, as vk-ahorro does. It was implemented and then
withdrawn before merge: it put a credential in GitHub that could read every
Secret in `llm-dev`, including the Vast.ai API key, and it made dev a second,
different delivery path instead of a second instance of the same one.

A spike on 2026-11-24 (Argo CD v3.5.1, the lab's version) showed that an
Application with `targetRevision: ">=0.0.0-0"` on an OCI Helm repository
lists the tags, includes prereleases, compares numeric identifiers
numerically, prefers a release over its own prereleases, and follows a new
push on refresh. Today's `-main.<sha7>` versions sort by sha text, not by
build order.

## Decision

1. **One chart, many installations.** Each installation is a platform
   pointer Application that renders vk-llm-on-demand's `gitops/` chart with
   a list of value files. Production uses `values.yaml`; dev uses
   `values.yaml` plus `values-dev.yaml`. All configuration lives in this
   repository; the platform only lists which installations exist.
2. **Dev tracks the newest build.** `values-dev.yaml` sets
   `chartVersion: ">=0.0.0-0"`. Production pins an exact released version.
3. **Versions sort by build.** `main` builds are
   `<next patch>-main.<run number>.g<sha7>`, where the run number is
   `deploy.yml`'s `github.run_number`. The `g` prefix keeps the identifier
   alphanumeric, so an all-digit sha never makes an invalid version.
4. **One pin.** When `image.tag` is empty the chart uses
   `.Chart.AppVersion`, which the pipeline sets to the version; every
   published build pushes the image under that tag before the chart.
   Production pins only `chartVersion`.
5. **Each installation has its own AppProject** that allows only its own
   namespace, so a wrong namespace in a value file cannot reach another
   installation.
6. **Dev proves itself after every sync** with a PostSync hook Job that
   calls `GET /v1/models`, `/api/v1/gpu/status` and `/api/v1/gpu/cost` on
   the in-cluster Service. A failure is a failed sync in Argo CD.
7. **Dev is production's configuration plus newer code**, in the same
   on-demand mode, budget and idle timeout, with the model alias prefixed
   `dev-`, alerts and dashboard off, and production's secrets and Vast.ai
   account. Open WebUI lists the dev model as a second model.

## Consequences

- No cluster credential leaves the cluster; nothing new is granted to
  `vk-llm-ci-role`.
- Dev returns by itself after a cluster rebuild.
- A merge whose build fails to deploy stays green in GitHub; the signal is
  the dev Application's health and sync status in Argo CD.
- Dev runs the newest chart against the shared `values.yaml`. A chart that
  needs a new value breaks dev until the value is added; `values-dev.yaml`
  can carry it ahead of production.
- Both installations draw on one Vast.ai balance; the monthly ceiling is the
  sum of their budgets ($60 by default). A message to the `dev-` model in
  Open WebUI starts a dev GPU.
- A dev token is a production token. Anyone who can start the dev GPU can
  start the production GPU.
- A new installation is one platform list entry plus one value file here.
  Only one installation may enable the alert rules, which do not filter by
  namespace.

## Alternatives considered

- **CI runs `helm upgrade` into `llm-dev` (vk-ahorro's pattern).** Built and
  withdrawn: a cluster credential in GitHub that reaches the Vast.ai key, and
  a second delivery path. Rejected.
- **Argo CD Image Updater.** Tracks images only; the chart changes with the
  code here. Rejected.
- **CI commits the dev version to a gitops file.** Bot commits on `main`;
  vk-ahorro tried and superseded it. Rejected.
- **Kargo.** A promotion orchestrator for multi-stage teams; too much for
  two installations. Rejected.
- **A CI job waits for dev to report the new version.** Turns a failed dev
  deploy red, but needs a version field in the API and tokens in CI.
  Deferred.
