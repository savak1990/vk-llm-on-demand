---
id: "CORE-080"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-060"]
adrs: ["0004", "0007", "0008", "0009"]
---

# 080 — Helm chart `gpu-on-demand`

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** M — many small templates and a long values schema, no business logic.
**Risk:** medium — a wrong RBAC verb or a wrong `config.*` → env-var mapping breaks every other package on day one, invisibly, because nothing here is unit-testable in Go.
**Estimated effort:** one session (4–6 h)
**Recommended model:** standard — pattern-copy from `vk-ahorro` plus the fixed value list in `docs/architecture.md` §9; the one novel piece is the amended RBAC rule and the NetworkPolicy egress shape.
**Depends on:** [CORE-060](../060-P-gateway-openai-api/spec.md) (the gateway's config surface and admin port this chart wires up; written after this spec but its contract — ports 8080/9090, `/healthz`/`/readyz`, admin endpoints — is already fixed by `docs/architecture.md` and ADR 0007)

## Outcome

`helm install gpu-on-demand oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand` on any Kubernetes cluster, given three pre-existing Secrets, brings up a `Deployment` of one pod that passes its own probes. With no route value set it renders with no `HTTPRoute` and no `Ingress`. Setting `host` plus `httpRoute.enabled` or `ingress.enabled` adds exactly one of the two. `helm uninstall` leaves nothing behind, including the state ConfigMap — but it does not touch a running GPU instance (ADR 0003 §6: the pod's shutdown path never destroys), so an operator drains first with `POST /admin/v1/scale {desired:0}` and waits for `state: Idle`. `values.yaml` contains no hostname, no domain and no secret value anywhere (C-14).

## Scope

- `deploy/helm/gpu-on-demand/`: `Chart.yaml`, `values.yaml`, `values.schema.json`, `templates/{_helpers.tpl, deployment.yaml, service.yaml, serviceaccount.yaml, role.yaml, rolebinding.yaml, configmap.yaml, state-configmap.yaml, httproute.yaml, ingress.yaml, servicemonitor.yaml, networkpolicy.yaml, externalsecrets.yaml, NOTES.txt}`.
- The full `values.yaml` schema (this spec's Design section is the source of truth for every key and its default).
- `make helm-lint`, `make helm-template`, `make helm-package` producing real output for the first time (CORE-010 left the `helm` CI job as a no-op pending this chart).
- A kind-based CI job (testing strategy tier 2): install with the fake provider, a fake worker reached through the `direct` dialer, and three dummy Secrets; wait for Ready; curl `/v1/models` and a streamed `/v1/chat/completions` through a port-forward; `helm upgrade` with a changed `config.*` value; `helm uninstall`.
- `kubeconform -strict` validation of all three route variants, with a CRD schema source for `HTTPRoute`, `ServiceMonitor`, `ExternalSecret`.

Excludes: the `gitops/` app-of-apps chart and the platform pointer `Application` (CORE-130); the platform's External Secrets IAM policy change (CORE-130, ADR 0009 §3); `deploy/docker/gpu-on-demand.Dockerfile`, `cmd/fake-worker` and the publish/release workflows (CORE-010, CORE-120); the fake worker's own request handling (CORE-010, tier 2 test fixture, not chart code); the state document's JSON shape (CORE-040); the gateway's and controller's runtime behaviour, including what each `config.*` value actually changes (CORE-050, CORE-060); CORE-090's dashboard JSON and alert rule expressions, authored there — this chart renders the `ServiceMonitor`, `PrometheusRule` and dashboard `ConfigMap` objects from that content, gated by `serviceMonitor.enabled`; the Envoy-side route timeout policy, a platform object outside this chart (ADR 0007 §4, CORE-130); a `-dev` or `-pr` installation of this chart — `docs/testing-strategy.md` states there is exactly one installation per provider account and no preview workflow, so the kind job is the only automated install this spec's CI ever runs.

## Design and contracts

### Template list

| File | Renders | Always present |
|---|---|---|
| `_helpers.tpl` | `svc.name`, `svc.fullname`, `svc.labels`, `svc.selectorLabels`, `svc.host`, `svc.image`, `svc.pullPolicy`, `svc.configEnv` (pattern copied from `vk-ahorro/deploy/helm/ahorro-api/templates/_helpers.tpl`) | — |
| `deployment.yaml` | one `Deployment`, `replicas: 1`, `strategy.type: Recreate` | yes |
| `service.yaml` | one `Service`, ports `http` and `metrics` | yes |
| `serviceaccount.yaml` | `ServiceAccount` | if `rbac.create` |
| `role.yaml` | `Role` on ConfigMaps (ADR 0004 §3) | if `rbac.create` |
| `rolebinding.yaml` | `RoleBinding` | if `rbac.create` |
| `configmap.yaml` | non-secret `config.*` as `VKLLM_*` keys, plus the fixed `VKLLM_STATE_CONFIGMAP` key | yes |
| `state-configmap.yaml` | an empty shell `ConfigMap` named `{{ .Release.Name }}-state`, no `state.json` key | yes |
| `httproute.yaml` | Gateway API `HTTPRoute`, `metadata.name: {{ include "svc.fullname" . }}` (= `gpu-on-demand` for release `gpu-on-demand`) | if `httpRoute.enabled` |
| `ingress.yaml` | `networking.k8s.io/v1` `Ingress` | if `ingress.enabled` |
| `servicemonitor.yaml` | `monitoring.coreos.com/v1` `ServiceMonitor` | if `serviceMonitor.enabled` |
| `prometheusrule.yaml` | one `PrometheusRule` (`monitoring.coreos.com/v1`) with CORE-090's four alert expressions | if `serviceMonitor.enabled` |
| `dashboard-configmap.yaml` | one `ConfigMap` holding CORE-090's dashboard JSON, labelled for the platform's Grafana sidecar discovery | if `serviceMonitor.enabled` |
| `networkpolicy.yaml` | one `NetworkPolicy`, egress + ingress | if `networkPolicy.enabled` |
| `externalsecrets.yaml` | three `external-secrets.io/v1` `ExternalSecret` objects, one `range` | if `externalSecrets.enabled` |
| `NOTES.txt` | post-install text; never prints `.Values.host`; prints the `installationId` warning and the drain-before-uninstall reminder | yes |

`Chart.yaml`: `apiVersion: v2`, `name: gpu-on-demand`, `type: application`, placeholder `version: "0.1.0"` overridden by `--version` at publish, placeholder `appVersion: "0.1.0"` overridden by `--app-version` to the commit SHA (ADR 0008 §4, copied pattern).

### `values.yaml` — image, secrets, route

| Key | Default | Required | Note |
|---|---|---|---|
| `image.repository` | `ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand` | no | ADR 0008 §3 |
| `image.tag` | `""` | **yes** | full commit SHA or version; `required` guard; never `latest` (C-21) |
| `image.pullPolicy` | `""` | no | empty → `svc.pullPolicy` derives `IfNotPresent` for a 40-hex tag, else `Always` |
| `existingSecrets.provider` | `""` | **yes** | Secret name holding `VAST_API_KEY` (ADR 0009 §1) |
| `existingSecrets.apiKeys` | `""` | **yes** | Secret name holding `client-keys`, `admin-key` |
| `existingSecrets.ssh` | `""` | **yes** | Secret name holding `id_ed25519`, optional `known_hosts` |
| `host` | `""` | **yes, only when** `httpRoute.enabled` or `ingress.enabled` | never a real value in this file (C-14) |
| `installationId` | `""` | no | empty → `svc.installationId` derives `sha256("<namespace>/<release>")[:12]`; set explicitly for a laptop tier-3 kind install (`docs/testing-strategy.md`, for example `e2e-<user>`) so its orphan sweep stays inside its own label prefix (ADR 0004 §6) |

### `values.yaml` — `config.*` (every key, `docs/architecture.md` §9)

Each `config.<path>` value becomes one `VKLLM_<NAME>` entry in the rendered `ConfigMap`, consumed by `envFrom`. The env-var name is fixed by this table — it is the contract CORE-040's `internal/config` loader reads against, not the reverse.

| `values.yaml` key | Default | Env var |
|---|---|---|
| `config.provider.name` | `vastai` | `VKLLM_PROVIDER_NAME` |
| `config.provider.filter.gpuName` | `L40S` | `VKLLM_PROVIDER_FILTER_GPU_NAME` |
| `config.provider.filter.numGpus` | `1` | `VKLLM_PROVIDER_FILTER_NUM_GPUS` |
| `config.provider.filter.minCpuCores` | `8` | `VKLLM_PROVIDER_FILTER_MIN_CPU_CORES` |
| `config.provider.filter.minCpuRamGB` | `32` | `VKLLM_PROVIDER_FILTER_MIN_CPU_RAM_GB` |
| `config.provider.filter.minDiskGB` | `60` | `VKLLM_PROVIDER_FILTER_MIN_DISK_GB` |
| `config.provider.filter.minReliability` | `0.95` | `VKLLM_PROVIDER_FILTER_MIN_RELIABILITY` |
| `config.provider.filter.minInetDownMBps` | `500` | `VKLLM_PROVIDER_FILTER_MIN_INET_DOWN_MBPS` |
| `config.provider.filter.minDiskBwMBps` | `500` | `VKLLM_PROVIDER_FILTER_MIN_DISK_BW_MBPS` |
| `config.provider.filter.minCudaVersion` | `12.8` | `VKLLM_PROVIDER_FILTER_MIN_CUDA_VERSION` |
| `config.provider.filter.geolocation` | `""` | `VKLLM_PROVIDER_FILTER_GEOLOCATION` |
| `config.gpu.maxHourlyPriceUSD` | `0.80` | `VKLLM_GPU_MAX_HOURLY_PRICE_USD` |
| `config.gpu.minInstances` | `0` | `VKLLM_GPU_MIN_INSTANCES` |
| `config.gpu.maxInstances` | `1` | `VKLLM_GPU_MAX_INSTANCES` |
| `config.gpu.idleTimeout` | `20m` | `VKLLM_GPU_IDLE_TIMEOUT` (fixed form, architecture §9) |
| `config.gpu.startupDeadline` | `15m` | `VKLLM_GPU_STARTUP_DEADLINE` |
| `config.gpu.maxLifetime` | `6h` | `VKLLM_GPU_MAX_LIFETIME` |
| `config.gpu.drainTimeout` | `5m` | `VKLLM_GPU_DRAIN_TIMEOUT` |
| `config.gpu.maxProvisionAttemptsPerHour` | `3` | `VKLLM_GPU_MAX_PROVISION_ATTEMPTS_PER_HOUR` |
| `config.budget.monthlyUSD` | `30` | `VKLLM_BUDGET_MONTHLY_USD` |
| `config.budget.warnFraction` | `0.8` | `VKLLM_BUDGET_WARN_FRACTION` |
| `config.controller.reconcileInterval` | `60s` | `VKLLM_CONTROLLER_RECONCILE_INTERVAL` |
| `config.gateway.maxWait` | `480s` | `VKLLM_GATEWAY_MAX_WAIT` |
| `config.gateway.maxPending` | `32` | `VKLLM_GATEWAY_MAX_PENDING` |
| `config.gateway.maxBodyBytes` | `1048576` | `VKLLM_GATEWAY_MAX_BODY_BYTES` |
| `config.gateway.maxTokensCap` | `8192` | `VKLLM_GATEWAY_MAX_TOKENS_CAP` |
| `config.gateway.rateLimitPerKeyRPM` | `60` | `VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM` |
| `config.gateway.heartbeatInterval` | `15s` | `VKLLM_GATEWAY_HEARTBEAT_INTERVAL` |
| `config.tunnel.dialer` | `ssh` | `VKLLM_TUNNEL_DIALER` — `ssh` or `direct`; `direct` only for the kind job with the fake worker (architecture §9) |
| `config.tunnel.directAddr` | `""` | `VKLLM_TUNNEL_DIRECT_ADDR` — host:port of the fake worker when `direct` (architecture §9) |
| `config.model.alias` | `qwen3.6-35b-a3b` | `VKLLM_MODEL_ALIAS` |
| `config.model.url` | `""` (pinned by CORE-070 at implementation) | `VKLLM_MODEL_URL` |
| `config.model.sha256` | `""` (pinned by CORE-070 at implementation) | `VKLLM_MODEL_SHA256` |
| `config.model.contextSize` | `32768` | `VKLLM_MODEL_CONTEXT_SIZE` |
| `config.model.parallel` | `1` | `VKLLM_MODEL_PARALLEL` |
| `config.model.generationTimeout` | `10m` | `VKLLM_MODEL_GENERATION_TIMEOUT` — upstream deadline after the cold-start wait (architecture §9) |
| `config.worker.image` | `""` (pinned by CORE-070, which lands first) | `VKLLM_WORKER_IMAGE` |
| `config.worker.deadManTimeout` | `""` (empty → config derives `idleTimeout + 10m`) | `VKLLM_WORKER_DEAD_MAN_TIMEOUT` |

`configmap.yaml` renders one key per row above, always present (empty string when the default is empty), never conditional — a value absent from the ConfigMap would silently fall back to whatever default `internal/config` compiles in, which this chart cannot see or test. It also renders one fixed key outside the `config.*` table:

| Env var | Value | Why it is not a `config.*` row |
|---|---|---|
| `VKLLM_STATE_CONFIGMAP` | `{{ .Release.Name }}-state` | it names the object `internal/store`'s `configmap.InCluster` reads, the same literal name `role.yaml` and `state-configmap.yaml` use; it is wiring, not a tunable |
| `VKLLM_INSTALLATION_ID` | `include "svc.installationId" .` | fixes the identity in the provider label `vkllm:<installation-id>:<generation>` to the release (ADR 0004 §6, amended) |

### Installation identity

```
{{- define "svc.installationId" -}}
{{- if .Values.installationId -}}
{{- .Values.installationId -}}
{{- else -}}
{{- printf "%s/%s" .Release.Namespace .Release.Name | sha256sum | trunc 12 -}}
{{- end -}}
{{- end -}}
```

`values.schema.json` adds one `pattern` beyond types: `installationId`, when non-empty, matches `^[a-z0-9][a-z0-9-]{0,31}$` — the same pattern CORE-040's `NewDocument` enforces, so a value like `e2e-<user>` (`docs/testing-strategy.md`) passes both and a value CORE-040 would reject never reaches it. CORE-040's `NewDocument` reads `installationId` from configuration rather than generating one at random (ADR 0004 §6, amended); this chart's `svc.installationId` helper is that value's source, fixed ahead of time so it survives a lost-and-recreated `ConfigMap` and so two tier-3 kind installs (one per laptop user) never collide. `NOTES.txt` prints a one-line warning: changing `installationId` on an existing release changes every future instance's label, so the running instance (if any) stops matching it and the next reconcile treats it as an orphan and destroys it (ADR 0003 §4).

### State ConfigMap shell

The store never creates `<release>-state` itself (ADR 0004 §3); the chart renders it so Helm owns its lifetime from `helm install` through `helm uninstall`. `state-configmap.yaml` renders that ConfigMap as an empty shell (metadata and labels only, no `state.json` key). On `helm upgrade`, Helm's three-way merge leaves the `state.json` key the chart never declares untouched, so the controller's live document survives upgrades; only `helm uninstall` (or `helm upgrade --force`, which replaces instead of merging) removes it.

### `values.yaml` — route, service, monitoring, network, secrets delivery, RBAC

| Key | Default | Note |
|---|---|---|
| `httpRoute.enabled` | `false` | |
| `httpRoute.parentRef.name` | `platform-gateway` | configurable, unlike `vk-ahorro`'s hard-coded value, so a non-platform install can point at its own Gateway |
| `httpRoute.parentRef.namespace` | `envoy` | |
| `httpRoute.parentRef.sectionName` | `https` | |
| `ingress.enabled` | `false` | |
| `ingress.className` | `""` | empty → omit `ingressClassName`, cluster default applies |
| `ingress.annotations` | `{}` | |
| `ingress.tls.enabled` | `false` | |
| `ingress.tls.secretName` | `""` | required when `ingress.tls.enabled` |
| `service.type` | `ClusterIP` | |
| `service.port` | `80` | maps to the pod's `http` container port 8080 |
| `serviceMonitor.enabled` | `false` | |
| `serviceMonitor.interval` | `15s` | matches `gateway.heartbeatInterval`'s order of magnitude, not derived from it |
| `serviceMonitor.labels` | `{}` | extra labels; the platform's `kube-prometheus-stack` already selects any namespace (research §5), so this is for a non-platform install with a stricter selector |
| `networkPolicy.enabled` | `false` | |
| `networkPolicy.gatewayNamespace` | `envoy` | namespace label allowed to reach port 8080 |
| `networkPolicy.vastApiCidrs` | `[]` | optional allow-list for the 443 egress rule; empty means unrestricted (Vast publishes no IP range — open questions) |
| `networkPolicy.apiServerCidrs` | `[]` | overrides the `lookup`-derived kube-apiserver `ipBlock`s; required in practice wherever rendering happens without live-cluster access (see NetworkPolicy section) |
| `networkPolicy.sshPorts.from` | `1` | |
| `networkPolicy.sshPorts.to` | `65535` | see NetworkPolicy section for why the full range is needed |
| `externalSecrets.enabled` | `false` | |
| `externalSecrets.storeRef` | `""` | `ClusterSecretStore` name, e.g. `aws-parameter-store` (research §5); required when enabled |
| `externalSecrets.prefix` | `""` | SSM path prefix, e.g. `/<project>/apps/vk-llm-on-demand`; required when enabled |
| `rbac.create` | `true` | creates `ServiceAccount` + `Role` + `RoleBinding` together; C-20 names no case for splitting them |
| `serviceAccount.name` | `""` | empty → `svc.fullname` |
| `resources.requests.cpu` | `50m` | |
| `resources.requests.memory` | `64Mi` | |
| `resources.limits.memory` | `256Mi` | |
| (no `resources.limits.cpu` key exists) | — | a GPU-bound control plane throttled on CPU would slow reconcile and the SSH tunnel; the chart has no key to set one (C-1) |
| `probes.readiness.periodSeconds` | `5` | `/readyz` on port 9090 |
| `probes.liveness.periodSeconds` | `20` | `/healthz` on port 9090 |
| `strategy` | fixed `Recreate` | not a value: with `maxInstances: 1` and one state writer (ADR 0004 §2), a `RollingUpdate` would briefly run two pods racing the same ConfigMap and the same provider label |
| `replicaCount` | `1` | rendering `{{ fail }}`s when not exactly `1` (ADR 0003 §1: one process) |

### RBAC — ADR 0004 §3 as amended

`role.yaml`, rendered only when `rbac.create` is true:

```yaml
kind: Role
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["{{ .Release.Name }}-state"]
    verbs: ["get", "update"]
```

One rule, exactly `get` and `update`, nothing else: no `create`, `patch`, `delete` or `list`. `state-configmap.yaml` (above) renders the object, so the controller never needs to create it; ADR 0004 §3 as amended drops the `create` rule entirely once the chart owns the shell. CORE-040's `TestKindRBAC` already expects `create` to answer 403, matching this single-rule shape.

### Secret mounts

Each `existingSecrets.*` Secret is mounted read-only as files, never as environment variables, so a Secret update reaches the process without a restart (ADR 0009 §1, Consequences):

| Secret (`existingSecrets.*`) | Mount path | Keys expected inside |
|---|---|---|
| `.provider` | `/etc/vkllm/secrets/provider/` | `VAST_API_KEY` |
| `.apiKeys` | `/etc/vkllm/secrets/apiKeys/` | `client-keys`, `admin-key` |
| `.ssh` | `/etc/vkllm/secrets/ssh/` | `id_ed25519`, optional `known_hosts` |

`defaultMode: 0400` on all three volumes. No `env` or `envFrom` ever references a Secret; only the `config.*` ConfigMap uses `envFrom`.

### `ExternalSecret` contract, when `externalSecrets.enabled`

`externalsecrets.yaml` ranges over the three rows below, rendering one `external-secrets.io/v1` `ExternalSecret` per row. `target.name` MUST equal the matching `existingSecrets.*` value — the Secret mounts above are keyed by that name, not by anything `ExternalSecret` invents — so ESO and this chart must agree on the name even though neither renders the other's object.

| Remote key (`externalSecrets.prefix` + suffix, ADR 0009 §3) | `target.name` | `target` Secret keys |
|---|---|---|
| `/vast-api-key` | `.Values.existingSecrets.provider` | `VAST_API_KEY` |
| `/client-keys` | `.Values.existingSecrets.apiKeys` | `client-keys` |
| `/admin-key` | `.Values.existingSecrets.apiKeys` (same Secret, second key) | `admin-key` |
| `/ssh-private-key` | `.Values.existingSecrets.ssh` | `id_ed25519` |

`secretStoreRef.name` is `externalSecrets.storeRef`, kind `ClusterSecretStore`. The `external-secrets.io/v1` API group and the `ClusterSecretStore`/`ExternalSecret` kinds are taken from ADR 0009 §3's prose, not from `research.md`, which has no External Secrets Operator section — recorded in Open questions.

### Pod and container security

Pod-level `securityContext`: `runAsNonRoot: true`, `runAsUser: 65532`, `runAsGroup: 65532`, `seccompProfile.type: RuntimeDefault` (ADR 0008 §4, C-20). Container-level `securityContext`: `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: ["ALL"]`. `automountServiceAccountToken: true` at the pod level — unlike `vk-ahorro`'s `false`, because `internal/store`'s `configmap` client needs the mounted token and CA (ADR 0004 §3, ADR 0008 §4).

### `checksum/config` and rollout

The pod template carries `checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}` (pattern copied from `vk-ahorro/deploy/helm/ahorro-api/templates/deployment.yaml`). Any `config.*` change forces a new `ReplicaSet` under `Recreate`, so the running pod is replaced rather than left running on stale env vars.

### NetworkPolicy — egress and ingress, when `networkPolicy.enabled`

| Rule | Destination | Port | Why |
|---|---|---|---|
| DNS | no `to` (all destinations) | 53 TCP+UDP | the controller resolves `ssh_host` and the Vast API host per request; the cluster DNS Service address is not fixed across platforms, so it cannot be pinned to an `ipBlock` |
| kube-apiserver | `networkPolicy.apiServerCidrs` if set, else `ipBlock`s built from `{{ lookup "v1" "Endpoints" "default" "kubernetes" }}` at install time | that Endpoints object's port | `internal/store` talks to the API server directly (ADR 0004 §3). `lookup` only resolves against a live cluster the Helm client itself can reach at that moment: a direct `helm install`/`upgrade` run from a machine with cluster access gets the narrow rule for free, but `helm template` and Argo CD's repo-server rendering cannot resolve `lookup` at all, since neither renders with live-cluster access. The chart therefore does not fall back to `0.0.0.0/0` silently: when `networkPolicy.enabled` is `true`, `apiServerCidrs` is empty, and `lookup` returns nothing, the template `fail`s naming `networkPolicy.apiServerCidrs`. Every caller that cannot resolve `lookup`, including Argo, must set `apiServerCidrs` explicitly |
| Vast API | `networkPolicy.vastApiCidrs` if set, else `0.0.0.0/0` | 443 TCP | the provider client calls `console.vast.ai` (research §1.1); Vast publishes no IP range to pin by default |
| worker SSH | `0.0.0.0/0` | `networkPolicy.sshPorts.from`–`.to` TCP (default 1–65535) | each instance's `ssh_host`/`ssh_port` is a random external port on a shared, sometimes dynamic public IP assigned only after creation (research §1.4, §1.6); neither the IP nor the port is knowable in advance, so this rule constrains protocol only — recorded honestly as a weak control, not a real restriction, and the only thing it is useful for is a single place to tighten if Vast ever exposes a published range |
| ingress | from namespace labelled `kubernetes.io/metadata.name: {{ .Values.networkPolicy.gatewayNamespace }}` | `http` (8080) | the platform's Envoy Gateway lives in namespace `envoy` (research §5) and is the only caller the gateway ever expects on 8080 |
| ingress | no `from` (all in-cluster sources) | `metrics` (9090) | port 9090 carries no secret and is never routed by Envoy (architecture §6); restricting it to one monitoring namespace would need a namespace name this chart does not know (open questions) |

### CI validation

`kubeconform -strict` needs schemas for four CRDs this chart can render: `HTTPRoute` (Gateway API), `ServiceMonitor` and `PrometheusRule` (`monitoring.coreos.com`), `ExternalSecret` (`external-secrets.io`). The `helm` CI job (CORE-010) adds `-schema-location default -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'` to the existing kubeconform invocation — the same pattern `vk-lab-platform`'s `lifecycle-test.yml` uses (as CORE-130 cites at its own line 176); the exact URL is verified at implementation time, not taken from `research.md`. Three renders are validated per pull request:

1. defaults (`image.tag=test`, the three `existingSecrets.*` names, no route) — no `HTTPRoute`, no `Ingress`.
2. `--set httpRoute.enabled=true --set host=llm.lab.example.com`.
3. `--set ingress.enabled=true --set host=llm.lab.example.com`.

### Make targets (CORE-010 reserved the names; this spec gives them real output)

| Target | Does |
|---|---|
| `helm-lint` | `helm lint deploy/helm/gpu-on-demand` |
| `helm-template` | `helm template deploy/helm/gpu-on-demand --set image.tag=test --set existingSecrets.provider=p --set existingSecrets.apiKeys=k --set existingSecrets.ssh=s` |
| `helm-package` | `helm package deploy/helm/gpu-on-demand --version $(VERSION) -d dist/` |

### kind CI job

1. `kind create cluster`; load the CI-built `gpu-on-demand` and `fake-worker` images into the kind node (`kind load docker-image`); install three dummy Secrets (`VAST_API_KEY=x`, `client-keys`/`admin-key`, a generated `id_ed25519`); apply a plain `Deployment`+`Service` manifest (not a Helm chart) for `cmd/fake-worker` — a small Go SSE server imitating llama-server's `/health`, `/v1/models`, `/v1/chat/completions` and `/metrics` (CORE-010, `docs/testing-strategy.md` tier 2; this chart neither builds nor renders it).
2. `helm install gpu-on-demand deploy/helm/gpu-on-demand --set image.tag=$GITHUB_SHA --set config.provider.name=fake --set config.tunnel.dialer=direct --set config.tunnel.directAddr=fake-worker.default.svc.cluster.local:8080 --set existingSecrets.provider=... --set existingSecrets.apiKeys=... --set existingSecrets.ssh=...`. `provider.name=fake` starts `fake.NewSeeded` (CORE-020): one $0.50/h L40S offer, an instance that reaches `running` on the first `GetInstance`, and a $100 balance, so the cold start and the budget check both pass with no manual seeding. `image.tag` is the full 40-hex commit SHA, so `svc.pullPolicy` derives `IfNotPresent` and the kubelet uses the image already loaded into the kind node instead of trying to pull it.
3. `kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=gpu-on-demand --timeout=60s`.
4. `kubectl port-forward` the Service; `curl /v1/models` returns `200` (FR-1.2); a streamed `POST /v1/chat/completions` returns SSE chunks ending `data: [DONE]`, proxied through the `direct` dialer to the fake worker. Read `installationId` from the `state.json` key the controller has by now written into `gpu-on-demand-state`.
5. `helm upgrade` with `--set config.gpu.idleTimeout=10m`; observe a new `ReplicaSet` (changed `checksum/config`); re-read `installationId` from `gpu-on-demand-state` and assert it is unchanged — the proof that the shell `ConfigMap`'s merge left the live document alone.
6. `curl -X POST .../admin/v1/scale -d '{"desired":0}'` through the port-forward, poll `/admin/v1/status` until `state: Idle` (ADR 0003 §6: the pod's shutdown path never destroys, so this is the only way to stop GPU billing before removing the chart); then `helm uninstall`; `kubectl get configmap gpu-on-demand-state` returns `NotFound`.

## Requirements

- R1. The chart MUST be exactly one chart at `deploy/helm/gpu-on-demand`, published as an OCI artifact at the release version, `appVersion` set to the commit SHA. (source: ADR 0008 §4)
- R2. `image.tag` MUST be a `required` value; `latest` MUST NOT be a usable default. (source: ADR 0008 §4, C-21)
- R3. `existingSecrets.provider`, `.apiKeys`, `.ssh` MUST each be `required`; the chart MUST NOT render any `Secret` object from a plaintext value. (source: ADR 0009 §1, C-14, C-15)
- R4. `host` MUST be `required` exactly when `httpRoute.enabled` or `ingress.enabled` is `true`, and MUST NOT have a default value. (source: ADR 0008 §4)
- R5. The pod MUST run as non-root UID/GID 65532 with a read-only root filesystem, no added capabilities, and `seccompProfile: RuntimeDefault`. (source: ADR 0008 §4, C-20)
- R6. `rbac.create: true` MUST render a `ServiceAccount`, a `Role` and a `RoleBinding`; `automountServiceAccountToken` MUST be `true`. (source: ADR 0008 §4, ADR 0004 §3)
- R7. The `Role` MUST grant exactly `get` and `update` scoped by `resourceNames` on the state `ConfigMap`, and MUST NOT grant `create`, `patch`, `delete` or `list`. (source: ADR 0004 §3, amended)
- R8. Default `resources` MUST be `requests.cpu: 50m`, `requests.memory: 64Mi`, `limits.memory: 256Mi`, with no `limits.cpu` key. (source: NFR-10.4, NFR-8.5)
- R9. `strategy.type` MUST be `Recreate`; the chart MUST fail its render when `replicaCount` is not exactly `1`. (source: ADR 0003 §1)
- R10. Probes MUST target `/healthz` and `/readyz` on container port 9090, matching CORE-010 R6/R7, and MUST NOT depend on a GPU instance existing. (source: CORE-010 R6, R7)
- R11. Each `existingSecrets.*` Secret MUST be mounted as files under `/etc/vkllm/secrets/{provider,apiKeys,ssh}` and MUST NOT be exposed through `env` or `envFrom`. (source: ADR 0009 §1)
- R12. The rendered application `ConfigMap` MUST contain exactly the `VKLLM_*` keys listed in this spec's `config.*` table plus the two fixed keys `VKLLM_STATE_CONFIGMAP` and `VKLLM_INSTALLATION_ID`, with no other key. (source: architecture §9)
- R13. `values.yaml` MUST NOT contain a hostname, a domain, an `fqdn` value or a secret value anywhere in its defaults. (source: C-14, NFR-7.6)
- R14. The chart MUST render without error, and without any `HTTPRoute` or `Ingress` object, when both `httpRoute.enabled` and `ingress.enabled` are `false`, and MUST install on any Kubernetes cluster with no ingress controller present. (source: NFR-8.2, C-23)
- R15. `helm uninstall` MUST leave no `ConfigMap` behind, including the state document's `ConfigMap`; `helm upgrade` MUST leave the state document's content untouched. (source: NFR-8.1, ADR 0004 §1)
- R16. `VKLLM_STATE_CONFIGMAP` MUST equal `{{ .Release.Name }}-state` everywhere it is used: the `configmap.yaml` env var, the `Role`'s `resourceNames`, and `state-configmap.yaml`'s object name. (source: ADR 0004 §1, §3)
- R17. When `networkPolicy.enabled` is `true`, egress MUST be limited to the four rules in the NetworkPolicy table; ingress MUST be limited to the two rules in that table. (source: architecture §6)
- R18. `NOTES.txt` MUST NOT render `.Values.host` or any Secret value. (source: C-14, NFR-7.6)
- R19. `kubeconform -strict` MUST pass for the default render, the `httpRoute` render and the `ingress` render. (source: NFR-8.2)
- R20. The kind CI job MUST install, reach Ready, serve `/v1/models` through a port-forward, show a rollout on a changed `config.*` value, and leave no `ConfigMap` after `helm uninstall`. (source: NFR-8.1, FR-1.2)
- R21. `helm lint` MUST exit 0 with no warnings on the default values. (source: NFR-8.1)
- R22. `VKLLM_INSTALLATION_ID` MUST be deterministic for a given `(namespace, release)` pair across repeated renders and across `helm upgrade`, unless `installationId` is set explicitly. (source: ADR 0004 §6, amended; `docs/testing-strategy.md`)
- R23. `serviceMonitor.enabled: true` MUST also render exactly one `PrometheusRule` carrying CORE-090's four alert expressions and exactly one dashboard `ConfigMap`. (source: NFR-9.5, CORE-090)
- R24. `installationId`, when set, MUST match `^[a-z0-9][a-z0-9-]{0,31}$` in `values.schema.json`, the same pattern CORE-040's `NewDocument` enforces. (source: ADR 0004 §6, amended; `docs/testing-strategy.md`)

## Implementation steps

1. `Chart.yaml`, `values.yaml` with every key and default from this spec, `values.schema.json` enforcing types only (string, integer, boolean, array shapes) — `required` fields are guarded in the templates (`required "message" .Values.x`) so every missing-value error comes from one place with one message. Test: `helm lint deploy/helm/gpu-on-demand` exits 0.
2. `_helpers.tpl` copied and extended from `vk-ahorro`'s (`svc.name`, `svc.fullname`, `svc.labels`, `svc.selectorLabels`, `svc.host`, `svc.image`, `svc.pullPolicy`) plus `svc.configEnv`. Test: `helm template` with minimal `--set` values renders a `Deployment` with a resolved image reference.
3. `deployment.yaml`, `service.yaml`, `configmap.yaml`, `serviceaccount.yaml`. Test: `helm template` output contains the `checksum/config` annotation and exactly the 38 `VKLLM_*` keys from the `config.*` table.
4. `role.yaml`, `rolebinding.yaml`. Test: `helm template --set rbac.create=true` output's `Role` has exactly one rule block matching the RBAC section verbatim; `grep -cE 'create|patch'` on its verbs is `0`.
5. `httproute.yaml`, `ingress.yaml`. Test: three `helm template` invocations (no route / httpRoute / ingress) each produce exactly the expected object set.
6. `servicemonitor.yaml`, `networkpolicy.yaml`, `externalsecrets.yaml`, `prometheusrule.yaml`, `dashboard-configmap.yaml`. Test: enabling each in isolation renders one well-formed object of the right `kind`; the `networkpolicy.yaml` render adds `--set networkPolicy.apiServerCidrs={10.0.0.1/32}`, since `helm template` cannot resolve `lookup` and would otherwise hit the `fail` guard.
7. `NOTES.txt`. Test: `helm template` with `host` set does not print that string anywhere in `NOTES.txt`'s rendered output.
8. Add the `-schema-location` datree catalog flag to the `helm` CI job's kubeconform step. Test: the three-variant kubeconform run (CI validation section) exits 0.
9. Write the kind job. Test: the full six-step sequence in the kind CI job section passes locally against a throwaway kind cluster.
10. Wire `helm-lint`, `helm-template`, `helm-package` to real chart paths in the Makefile. Test: all three targets exit 0.

## Tests / acceptance criteria

- AC1. Setup: default values plus `image.tag`, three `existingSecrets.*`. Action: `helm template`. Expected: no `HTTPRoute`, no `Ingress`, no `NetworkPolicy`, no `ServiceMonitor`, no `ExternalSecret` object in the output. (NFR-8.2)
- AC2. Setup: same, `--set httpRoute.enabled=true --set host=llm.lab.example.com`. Expected: exactly one `HTTPRoute`, `parentRefs[0].sectionName` equals `.Values.httpRoute.parentRef.sectionName`. (NFR-8.2)
- AC3. Setup: same, `--set ingress.enabled=true --set host=llm.lab.example.com`. Expected: exactly one `Ingress`, no `HTTPRoute`. (NFR-8.2)
- AC4. Action: `kubeconform -strict` on the three renders in AC1–AC3, with the datree catalog schema location. Expected: exit code `0` for all three. (NFR-8.2, R19)
- AC5. Action: `helm lint deploy/helm/gpu-on-demand`. Expected: exit code `0`, zero `[WARNING]` lines. (R19)
- AC6. Action: `grep -E 'lab\.example\.com|<fqdn>|sk-vkllm-' deploy/helm/gpu-on-demand/values.yaml`. Expected: no match (the placeholders in this spec's prose are not copied into the file). (R13)
- AC7. Setup: `--set image.tag=""`. Action: `helm template`. Expected: non-zero exit, error text containing `image.tag is required`. (R2)
- AC8. Setup: omit one of the three `existingSecrets.*`. Action: `helm template`. Expected: non-zero exit naming the missing key. (R3)
- AC9. Setup: `--set httpRoute.enabled=true`, no `host`. Action: `helm template`. Expected: non-zero exit, error text containing `host is required`. (R4)
- AC10. Setup: `--set replicaCount=2`. Action: `helm template`. Expected: non-zero exit, error text naming `replicaCount`. (R9)
- AC11. Setup: `--set rbac.create=true`. Action: `helm template`, extract the `Role`. Expected: exactly one rule block; its verbs are exactly `["get","update"]`; no rule contains `create`, `patch`, `delete` or `list`. (R7)
- AC12. Action: kind job steps 1–6 (CI validation / kind CI job section). Expected: step 3 (`Ready`) within 60 s; step 4's curl returns `200` and the streamed chat completion ends `data: [DONE]`; step 5 produces a new `ReplicaSet` name and the same `installationId` as step 4; step 6's `kubectl get configmap` returns `NotFound` for both the application `ConfigMap` and `gpu-on-demand-state`. (R15, R20)
- AC13. Setup: `--set rbac.create=true` with release name `foo`. Action: `helm template`, extract the `Role`'s `resourceNames` and `state-configmap.yaml`'s `metadata.name`. Expected: both equal `foo-state`, not `foo-gpu-on-demand-state`. (R16)
- AC14. Action: render `NOTES.txt` with `--set host=llm.lab.example.com`. Expected: the string `lab.example.com` does not appear in the rendered `NOTES.txt`. (R18)
- AC15. Action: `helm template` twice with the same `--namespace`/release name and no `installationId` set. Expected: `VKLLM_INSTALLATION_ID` is identical both times; a third render with a different release name yields a different value. (R22)
- AC16. Setup: `--set config.tunnel.dialer=direct --set config.tunnel.directAddr=fake-worker.default.svc.cluster.local:8080`. Action: `helm template`. Expected: the `ConfigMap` contains `VKLLM_TUNNEL_DIALER: "direct"` and `VKLLM_TUNNEL_DIRECT_ADDR: "fake-worker.default.svc.cluster.local:8080"`. (R12)
- AC17. Setup: `--set serviceMonitor.enabled=true`. Action: `helm template`. Expected: exactly one `PrometheusRule` with 4 `rules` entries; exactly one dashboard `ConfigMap`. (R23)
- AC18. Setup: `--set installationId=Not_Valid!`. Action: `helm template` or `helm install --dry-run` (schema validation runs first). Expected: non-zero exit, the `values.schema.json` pattern error names `installationId`; `--set installationId=e2e-alice` succeeds. (R24)

## Definition of done

- [ ] AC1–AC18 pass in CI, AC12 in the kind job.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE in the same pull request, folder renamed `080-D-helm-chart`.
- [ ] Evidence section filled, including the measured kind job wall-clock time.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Vast publishes no IP range for its API or its SSH hosts (research §1.1, §1.6) | the egress NetworkPolicy for 443 and for SSH cannot be narrowed below `0.0.0.0/0` today | re-check research.md if Vast publishes one later | `networkPolicy.vastApiCidrs` empty; SSH rule stays protocol-only |
| The platform's monitoring namespace name is not recorded in research §5 | the 9090 ingress rule cannot be scoped to it | CORE-130, when the platform integration spec reads the real namespace | 9090 ingress allowed from all in-cluster sources (not sensitive data) |
| `config.worker.image`'s exact digest is CORE-070's to pin (ADR 0005, model and image choice) | this chart must not invent a digest | CORE-070 | pinned by CORE-070 (lands first) |
| Whether `lookup`-based kube-apiserver `ipBlock`s resolve correctly across k3s and kind, and what CIDR CORE-130 should pass as `apiServerCidrs` for the platform's Argo-rendered install | a wrong `ipBlock` either over-permits (defeats the policy) or blocks the store entirely; Argo's client-side render means the platform install always needs `apiServerCidrs` set, never `lookup` | kind job AC12 for the `lookup` path; CORE-130 supplies the platform's `apiServerCidrs` | `lookup` for a direct `helm install`/`upgrade`; `fail` (not a silent `0.0.0.0/0`) when `networkPolicy.enabled` and neither resolves |
| ADR 0007 §4's required Envoy route timeout (disabled or ≥ 1080 s, plus a 120 s stream idle timeout) has no object this chart renders | a platform install without it risks Envoy cutting a slow cold start or a long generation short | CORE-130 (a platform-side `BackendTrafficPolicy` or equivalent, out of this chart) | ≥ 1080 s or disabled, 120 s idle (ADR 0007 §4) |

## Evidence

None yet.
