# ADR 0020: The chat front end is a second Argo app from this repository

## Status

Accepted (2026-11-23).

## Context

CORE-230 adds Open WebUI and SearXNG. The first implementation put the whole
app in `vk-lab-platform`, like Grafana. That duplicated this repository's
model alias in the platform: a model change (as in #38, #40) would need two
pull requests, and a missed one breaks the chat silently. It also made every
chat change a platform change, unlike `gpu-on-demand`, whose repository owns
everything below the platform's pointer (ADR 0008).

## Decision

1. `chat/` in this repository is an unpublished Helm chart: a vendored
   `open-webui` dependency (`chat/charts/*.tgz`, so Argo downloads nothing at
   sync time), SearXNG, the two HTTPRoutes and a ConfigMap for `WEBUI_URL`,
   which depends on `fqdn`. The model alias is in the pod spec, so a model
   change rolls the pod; Open WebUI reads it only at start.
2. The platform renders it with its own pointer Application `vk-llm-chat`
   and AppProject `vk-llm-chat`, limited to namespace `chat`. No child
   Application: the pointer renders the chart directly, so Argo needs no new
   application namespace.
3. The platform keeps shared services: the `chat` namespace and its
   ExternalSecret, the `openwebui` database, Open WebUI's key in
   `client-keys`, and the Envoy traffic policies.
4. `make gitops-check` fails when the rendered `DEFAULT_MODELS` or
   `TASK_MODEL_EXTERNAL` of Deployment `open-webui` differs from
   `gitops/values.yaml` `model.alias`.

## Consequences

A chat change is a pull request here; the platform changes only for shared
services. The chart is not versioned by the repository tag: the pointer
tracks `main`, like `gitops/`. Upgrading Open WebUI means bumping
`chat/Chart.yaml` and re-running `helm dependency update chat`. The
constitution's chart rules (version from a git tag, installs on any
cluster) apply to the published `gpu-on-demand` chart; `chat/` is a
platform-only, unversioned chart rendered from Git.
