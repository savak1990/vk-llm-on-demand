---
id: "CORE-080"
status: "DONE"
updated: "2026-11-06"
depends_on: ["CORE-060"]
adrs: ["0004", "0008", "0009", "0010", "0011", "0012", "0013"]
---

# 080 — Helm chart `gpu-on-demand`

**Status note:** DONE as of 2026-11-06. Amended against the `internal/config` env-var contract shipped in PRs #15/#16; the chart, its kind CI job and the Makefile targets are all built and validated (see Evidence).

**Complexity:** M — many small templates and a long values schema, no business logic.
**Risk:** medium — a wrong RBAC verb, a wrong `config.*` → env-var mapping, or a schema guard that does not actually block `idleShutdown.enabled: false` breaks safety invisibly, because nothing here is unit-testable in Go.
**Estimated effort:** one session (4–6 h)
**Recommended model:** standard — pattern-copy from `vk-ahorro` plus the fixed value list in `docs/architecture.md` §9; the novel pieces are the amended RBAC rule, the NetworkPolicy egress shape, and the `values.schema.json` `if`/`then` guard tying `idleShutdown.enabled` to `maxLifetime`.
**Depends on:** [CORE-060](../060-D-gateway-openai-api/spec.md) (the gateway's config surface and admin port this chart wires up; written after this spec but its contract — ports 8080/9090, `/healthz`/`/readyz`, management and inference endpoints — is already fixed by `docs/architecture.md` and ADR 0012)

## Outcome

`helm install gpu-on-demand oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand` on any Kubernetes cluster, given three pre-existing Secrets, brings up a `Deployment` of one pod that passes its own probes with no GPU running. With no route value set it renders with no `HTTPRoute` and no `Ingress`. Setting `host` plus `httpRoute.enabled` or `ingress.enabled` adds exactly one of the two, and that one route exposes both the inference API (`/v1`) and the management API (`/api/v1`) on the same host. `helm uninstall` leaves nothing behind, including the state ConfigMap — but it does not touch a running GPU instance (ADR 0003 §6: the pod's shutdown path never destroys), so an operator drains first with `POST /api/v1/gpu/stop` (management token) and waits for `state: STOPPED` (ADR 0011 §3: stop drains and destroys immediately, no 30-minute wait). `values.yaml` contains no hostname, no domain and no secret value anywhere (C-14).

## Scope

- `deploy/helm/gpu-on-demand/`: `Chart.yaml`, `values.yaml`, `values.schema.json`, `templates/{_helpers.tpl, deployment.yaml, service.yaml, serviceaccount.yaml, role.yaml, rolebinding.yaml, configmap.yaml, state-configmap.yaml, httproute.yaml, ingress.yaml, servicemonitor.yaml, networkpolicy.yaml, externalsecrets.yaml, NOTES.txt}`.
- The full `values.yaml` schema (this spec's Design section is the source of truth for every key and its default).
- `make helm-lint`, `make helm-template`, `make helm-package` producing real output for the first time (CORE-010 left the `helm` CI job as a no-op pending this chart).
- A kind-based CI job (testing strategy tier 2): install with the fake provider, a fake worker reached through the `direct` dialer, and three dummy Secrets; wait for Ready with no GPU; drive start/stop and status checks with `curl` against the management API; curl the inference API; `helm upgrade` with a changed `config.*` value; `helm uninstall`.
- `kubeconform -strict` validation of all three route variants, with a CRD schema source for `HTTPRoute`, `ServiceMonitor`, `ExternalSecret`.

Excludes: the `gitops/` app-of-apps chart and the platform pointer `Application` (CORE-130); the platform's External Secrets IAM policy change (CORE-130, ADR 0009 §3); `deploy/docker/gpu-on-demand.Dockerfile`, `cmd/fake-worker` and the publish/release workflows (CORE-010, CORE-120); the fake worker's own request handling (CORE-010, tier 2 test fixture, not chart code); the state document's JSON shape (CORE-040); the gateway's, management API's and controller's runtime behaviour, including what each `config.*` value actually changes (CORE-050, CORE-055, CORE-060); CORE-090's dashboard JSON and alert rule expressions, and the `PrometheusRule`/dashboard `ConfigMap` templates that render them — this chart renders only the `ServiceMonitor`, gated by `serviceMonitor.enabled`; CORE-090 adds its own templates to this chart's `templates/` directory when it is implemented; the Envoy-side route timeout policy, a platform object outside this chart (660s in explicit mode, `model.generationTimeout + 60s`; 1080s reserved for a future on-demand install — ADR 0010 Consequences, CORE-130); a `-dev` or `-pr` installation of this chart — `docs/testing-strategy.md` states there is exactly one installation per provider account and no preview workflow, so the kind job is the only automated install this spec's CI ever runs.

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
| `configmap.yaml` | non-secret `config.*` as `VKLLM_*` keys, plus the fixed `VKLLM_STATE_CONFIGMAP` and `VKLLM_INSTALLATION_ID` keys | yes |
| `state-configmap.yaml` | an empty shell `ConfigMap` named `{{ .Release.Name }}-state`, no `state.json` key | yes |
| `httproute.yaml` | Gateway API `HTTPRoute`, `metadata.name: {{ include "svc.fullname" . }}`, two rules (`/v1`, `/api/v1`), same backend | if `httpRoute.enabled` |
| `ingress.yaml` | `networking.k8s.io/v1` `Ingress`, one `/` prefix rule to the same `Service` | if `ingress.enabled` |
| `servicemonitor.yaml` | `monitoring.coreos.com/v1` `ServiceMonitor` only (`PrometheusRule` and the dashboard `ConfigMap` are CORE-090's own templates, added when that spec is implemented) | if `serviceMonitor.enabled` |
| `networkpolicy.yaml` | one `NetworkPolicy`, egress + ingress | if `networkPolicy.enabled` |
| `externalsecrets.yaml` | five `external-secrets.io/v1` `ExternalSecret` objects, one `range` | if `externalSecrets.enabled` |
| `NOTES.txt` | post-install text; never prints `.Values.host` or a Secret value; prints the three-command workflow (`curl .../api/v1/gpu/start` / client curl / `curl .../api/v1/gpu/stop`) with placeholders, an OpenCode config pointer, the `installationId` warning, and the drain-before-uninstall reminder | yes |

`Chart.yaml`: `apiVersion: v2`, `name: gpu-on-demand`, `type: application`, placeholder `version: "0.1.0"` overridden by `--version` at publish, placeholder `appVersion: "0.1.0"` overridden by `--app-version` to the commit SHA (ADR 0008 §4, copied pattern).

### `values.yaml` — image, secrets, route

| Key | Default | Required | Note |
|---|---|---|---|
| `image.repository` | `ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand` | no | ADR 0008 §3 |
| `image.tag` | `""` | **yes** | full commit SHA or version; `required` guard; never `latest` (C-21) |
| `image.pullPolicy` | `""` | no | empty → `svc.pullPolicy` derives `IfNotPresent` for a 40-hex tag, else `Always` |
| `existingSecrets.provider` | `""` | **yes** | Secret name holding `VAST_API_KEY` (ADR 0009 §1) |
| `existingSecrets.apiKeys` | `""` | **yes** | Secret name holding `client-keys`, `management-tokens`, `worker-seed` (ADR 0012 §3) |
| `existingSecrets.ssh` | `""` | **yes** | Secret name holding `id_ed25519`, optional `known_hosts` |
| `host` | `""` | **yes, only when** `httpRoute.enabled` or `ingress.enabled` | never a real value in this file (C-14) |
| `installationId` | `""` | no | empty → `svc.installationId` derives `sha256("<namespace>/<release>")[:12]`; set explicitly for a laptop tier-3 kind install (`docs/testing-strategy.md`, for example `e2e-<user>`) so its orphan sweep stays inside its own label prefix (ADR 0004 §6) |

### `values.yaml` — `config.*` (every key, `docs/architecture.md` §9)

Each `config.<path>` value becomes one `VKLLM_<NAME>` entry in the rendered `ConfigMap`, consumed by `envFrom`. The env-var name is fixed by this table — it is the contract CORE-040's `internal/config` loader reads against, not the reverse. `config.gpu.*` from the previous revision of this chart is gone; every lifecycle value now lives under `config.lifecycle.*` (ADR 0011, R-I).

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
| `config.provider.apiKeyFile` | `/etc/vkllm/secrets/provider/VAST_API_KEY` | `VKLLM_PROVIDER_API_KEY_FILE` |
| `config.lifecycle.provisioningMode` | `explicit` | `VKLLM_LIFECYCLE_PROVISIONING_MODE` — schema-restricted to `explicit` (ADR 0010 §1) |
| `config.lifecycle.idleShutdown.enabled` | `true` | `VKLLM_LIFECYCLE_IDLE_SHUTDOWN_ENABLED` — `false` refused unless `maxLifetime` is set (ADR 0011 §5) |
| `config.lifecycle.idleShutdown.timeout` | `30m` | `VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT` (fixed form, architecture §9) |
| `config.lifecycle.shutdown.drainTimeout` | `60s` | `VKLLM_LIFECYCLE_SHUTDOWN_DRAIN_TIMEOUT` — manual stop, lifetime and budget drains alike |
| `config.lifecycle.shutdown.allowForce` | `false` | `VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE` — gates the future `force` stop (ADR 0011 §3) |
| `config.lifecycle.startupTimeout` | `15m` | `VKLLM_LIFECYCLE_STARTUP_TIMEOUT` (was `gpu.startupDeadline`) |
| `config.lifecycle.safety.maxHourlyPriceUSD` | `0.80` | `VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD` |
| `config.lifecycle.safety.maxLifetime` | `12h` | `VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME` (was `6h`; a deliberate change, ADR 0011 §5) |
| `config.lifecycle.safety.maxInstanceSpendUSD` | `0` | `VKLLM_LIFECYCLE_SAFETY_MAX_INSTANCE_SPEND_USD` — `0` = off; otherwise bounds one instance's estimated spend |
| `config.lifecycle.safety.maxProvisionAttemptsPerHour` | `3` | `VKLLM_LIFECYCLE_SAFETY_MAX_PROVISION_ATTEMPTS_PER_HOUR` |
| `config.budget.monthlyUSD` | `30` | `VKLLM_BUDGET_MONTH_USD` |
| `config.budget.warnFraction` | `0.8` | `VKLLM_BUDGET_WARN_FRACTION` |
| `config.controller.reconcileInterval` | `60s` | `VKLLM_CONTROLLER_RECONCILE_INTERVAL` |
| `config.gateway.maxBodyBytes` | `1048576` | `VKLLM_GATEWAY_MAX_BODY_BYTES` |
| `config.gateway.maxTokensCap` | `8192` | `VKLLM_GATEWAY_MAX_TOKENS_CAP` |
| `config.gateway.rateLimitPerKeyRPM` | `60` | `VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM` — inference keys and management tokens alike |
| `config.tunnel.dialer` | `ssh` | `VKLLM_TUNNEL_DIALER` — `ssh` or `direct`; `direct` only for the kind job with the fake worker (architecture §9) |
| `config.tunnel.directAddr` | `""` | `VKLLM_TUNNEL_DIRECT_ADDR` — host:port of the fake worker when `direct` (architecture §9) |
| `config.tunnel.sshKeyFile` | `/etc/vkllm/secrets/ssh/id_ed25519` | `VKLLM_TUNNEL_SSH_KEY_FILE` |
| `config.auth.clientKeysFile` | `/etc/vkllm/api-keys/client-keys` | `VKLLM_AUTH_CLIENT_KEYS_FILE` |
| `config.auth.managementTokensFile` | `/etc/vkllm/api-keys/management-tokens` | `VKLLM_AUTH_MANAGEMENT_TOKENS_FILE` |
| `config.auth.workerSeedFile` | `/etc/vkllm/api-keys/worker-seed` | `VKLLM_AUTH_WORKER_SEED_FILE` |
| `config.model.alias` | `qwen3.6-35b-a3b` | `VKLLM_MODEL_ALIAS` |
| `config.model.url` | no default, required | `VKLLM_MODEL_URL` |
| `config.model.sha256` | no default, required | `VKLLM_MODEL_SHA256` |
| `config.model.contextSize` | `32768` | `VKLLM_MODEL_CONTEXT_SIZE` |
| `config.model.parallel` | `1` | `VKLLM_MODEL_PARALLEL` |
| `config.model.generationTimeout` | `10m` | `VKLLM_MODEL_GENERATION_TIMEOUT` — upstream deadline per request; sizes the Envoy route timeout to 660s (architecture §9, ADR 0010 Consequences) |
| `config.worker.image` | no default, required | `VKLLM_WORKER_IMAGE` |
| `config.worker.deadManTimeout` | `""` (empty → derives `lifecycle.idleShutdown.timeout + 10m`) | `VKLLM_WORKER_DEAD_MAN_TIMEOUT` |
| `config.mgmt.rateLimitPerKeyRPM` | `60` | `VKLLM_MGMT_RATE_LIMIT_RPM` |

`config.model.url`, `config.model.sha256` and `config.worker.image` have no default: no production value exists anywhere in this repository yet; each is set at install time, the same category as `image.tag`.

`config.mgmt.budgetMonthUSD`/`VKLLM_MGMT_BUDGET_MONTH_USD` is deliberately not a chart value — `cmd/gpu-on-demand/wire.go` always overrides it from `config.budget.monthlyUSD`, so rendering it would let an operator set a value with no effect.

43 `config.*` rows above, each always rendered (empty string when the default is empty), never conditional — a value absent from the ConfigMap would silently fall back to whatever default `internal/config` compiles in, which this chart cannot see or test. Two fixed keys outside the `config.*` table:

| Env var | Value | Why it is not a `config.*` row |
|---|---|---|
| `VKLLM_STATE_CONFIGMAP` | `{{ .Release.Name }}-state` | it names the object `internal/store`'s `configmap.InCluster` reads, the same literal name `role.yaml` and `state-configmap.yaml` use; it is wiring, not a tunable |
| `VKLLM_INSTALLATION_ID` | `include "svc.installationId" .` | fixes the identity in the provider label `vkllm:<installation-id>:<generation>` to the release (ADR 0004 §6, amended) |

### Future: on-demand mode and P2 cache are not rendered

`onDemand.{maxWait,maxPending,heartbeatInterval}` (ADR 0007, ADR 0010 §4) and `storage.*` (P2 persistent volume cache, ADR 0014) are not `values.yaml` keys in this chart. Rendering them now, before any controller code consumes them, would let an operator believe setting them does something in the MVP. They are documented here, not in `values.yaml`, as the place a future chart revision adds them.

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

### `values.schema.json` — provisioning mode and the idle/lifetime guard

Two checks beyond plain types, both load-bearing for safety (R-I):

```json
{
  "properties": {
    "config": { "properties": { "lifecycle": {
      "properties": {
        "provisioningMode": { "type": "string", "enum": ["explicit"] },
        "idleShutdown": { "properties": { "enabled": { "type": "boolean" } } },
        "safety": { "properties": { "maxLifetime": { "type": "string" } } }
      },
      "if": { "properties": { "idleShutdown": { "properties": { "enabled": { "const": false } } } } },
      "then": {
        "properties": {
          "safety": {
            "properties": { "maxLifetime": { "minLength": 1, "pattern": ".*[1-9].*" } },
            "required": ["maxLifetime"]
          }
        },
        "required": ["safety"]
      }
    } } }
  }
}
```

1. `config.lifecycle.provisioningMode` accepts only `explicit` in the MVP; `on-demand` fails schema validation naming the field (ADR 0010 §1 — `on-demand` is parsed by the Go binary but this chart never ships a value for it).
2. `config.lifecycle.idleShutdown.enabled: false` is refused unless `config.lifecycle.safety.maxLifetime` is a non-empty string, so no installation can rent a GPU with neither stop condition active (ADR 0011 §5, FR-3.12). With the default `maxLifetime: 12h` already set, disabling `idleShutdown` alone never trips this guard; only an installation that also clears `maxLifetime` does. The `pattern` requires at least one digit `1`-`9` somewhere in the string: a duration string with no such digit (`"0s"`, `"0h0m"`) always evaluates to zero, which is the same as having no lifetime bound at all.

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
| `service.port` | `80` | maps to the pod's `http` container port 8080, carrying both `/v1` and `/api/v1` |
| `serviceMonitor.enabled` | `false` | |
| `serviceMonitor.interval` | `15s` | a fixed scrape cadence, not derived from any config value |
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
| `probes.readiness.periodSeconds` | `5` | `/readyz` on port 9090; never depends on a GPU existing (FR-1.7) |
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
| `.apiKeys` | `/etc/vkllm/api-keys/` | `client-keys`, `management-tokens`, `worker-seed` (ADR 0012 §3) |
| `.ssh` | `/etc/vkllm/secrets/ssh/` | `id_ed25519`, optional `known_hosts` |

The `.apiKeys` mount path matches `internal/config/auth.go`'s real shipped defaults (`VKLLM_AUTH_CLIENT_KEYS_FILE` et al.) rather than inventing a new path. `defaultMode: 0400` on all three volumes. No `env` or `envFrom` ever references a Secret; only the `config.*` ConfigMap uses `envFrom`.

### `ExternalSecret` contract, when `externalSecrets.enabled`

`externalsecrets.yaml` ranges over the five rows below, rendering one `external-secrets.io/v1` `ExternalSecret` per row. `target.name` MUST equal the matching `existingSecrets.*` value — the Secret mounts above are keyed by that name, not by anything `ExternalSecret` invents — so ESO and this chart must agree on the name even though neither renders the other's object.

| Remote key (`externalSecrets.prefix` + suffix, ADR 0009 §3) | `target.name` | `target` Secret key |
|---|---|---|
| `/vast-api-key` | `.Values.existingSecrets.provider` | `VAST_API_KEY` |
| `/client-keys` | `.Values.existingSecrets.apiKeys` | `client-keys` |
| `/management-tokens` | `.Values.existingSecrets.apiKeys` (same Secret, second key) | `management-tokens` |
| `/worker-seed` | `.Values.existingSecrets.apiKeys` (same Secret, third key) | `worker-seed` |
| `/ssh-private-key` | `.Values.existingSecrets.ssh` | `id_ed25519` |

`secretStoreRef.name` is `externalSecrets.storeRef`, kind `ClusterSecretStore`. The `external-secrets.io/v1` API group and the `ClusterSecretStore`/`ExternalSecret` kinds are taken from ADR 0009 §3's prose, not from `research.md`, which has no External Secrets Operator section — recorded in Open questions.

### Pod and container security

Pod-level `securityContext`: `runAsNonRoot: true`, `runAsUser: 65532`, `runAsGroup: 65532`, `seccompProfile.type: RuntimeDefault` (ADR 0008 §4, C-20). Container-level `securityContext`: `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: ["ALL"]`. `automountServiceAccountToken: true` at the pod level — unlike `vk-ahorro`'s `false`, because `internal/store`'s `configmap` client needs the mounted token and CA (ADR 0004 §3, ADR 0008 §4).

### `checksum/config` and rollout

The pod template carries `checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}` (pattern copied from `vk-ahorro/deploy/helm/ahorro-api/templates/deployment.yaml`). Any `config.*` change forces a new `ReplicaSet` under `Recreate`, so the running pod is replaced rather than left running on stale env vars.

### HTTPRoute and Ingress both reach one Service on two paths

One `Service`, one port, two public paths. `httproute.yaml` renders one `HTTPRoute` with two rules, each matching a `PathPrefix` (`/v1`, `/api/v1`) and both pointing at the same `backendRef` (the chart's `Service`, port `http`) — a single route object, a single host, because both the inference API and the management API are served by the same process on the same port (ADR 0012 §1; the previous revision of this chart routed `/v1` and the now-removed `/admin/v1`). `ingress.yaml` renders one `Ingress` with a single `/` prefix rule to the same `Service`, which already covers both paths without needing two rules — the simpler shape an `Ingress` allows that `HTTPRoute` does not require explicitly.

### NetworkPolicy — egress and ingress, when `networkPolicy.enabled`

| Rule | Destination | Port | Why |
|---|---|---|---|
| DNS | no `to` (all destinations) | 53 TCP+UDP | the controller resolves `ssh_host` and the Vast API host per request; the cluster DNS Service address is not fixed across platforms, so it cannot be pinned to an `ipBlock` |
| kube-apiserver | `networkPolicy.apiServerCidrs` if set, else `ipBlock`s built from `{{ lookup "v1" "Endpoints" "default" "kubernetes" }}` at install time | that Endpoints object's port | `internal/store` talks to the API server directly (ADR 0004 §3). `lookup` only resolves against a live cluster the Helm client itself can reach at that moment: a direct `helm install`/`upgrade` run from a machine with cluster access gets the narrow rule for free, but `helm template` and Argo CD's repo-server rendering cannot resolve `lookup` at all, since neither renders with live-cluster access. The chart therefore does not fall back to `0.0.0.0/0` silently: when `networkPolicy.enabled` is `true`, `apiServerCidrs` is empty, and `lookup` returns nothing, the template `fail`s naming `networkPolicy.apiServerCidrs`. Every caller that cannot resolve `lookup`, including Argo, must set `apiServerCidrs` explicitly |
| Vast API | `networkPolicy.vastApiCidrs` if set, else `0.0.0.0/0` | 443 TCP | the provider client calls `console.vast.ai` (research §1.1); Vast publishes no IP range to pin by default |
| worker SSH | `0.0.0.0/0` | `networkPolicy.sshPorts.from`–`.to` TCP (default 1–65535) | each instance's `ssh_host`/`ssh_port` is a random external port on a shared, sometimes dynamic public IP assigned only after creation (research §1.4, §1.6); neither the IP nor the port is knowable in advance, so this rule constrains protocol only — recorded honestly as a weak control, not a real restriction, and the only thing it is useful for is a single place to tighten if Vast ever exposes a published range |
| ingress | from namespace labelled `kubernetes.io/metadata.name: {{ .Values.networkPolicy.gatewayNamespace }}` | `http` (8080) | the platform's Envoy Gateway lives in namespace `envoy` (research §5) and is the only caller the gateway and management API ever expect on 8080 |
| ingress | no `from` (all in-cluster sources) | `metrics` (9090) | port 9090 carries no secret and is never routed by Envoy (architecture §6); restricting it to one monitoring namespace would need a namespace name this chart does not know (open questions) |

### CI validation

`kubeconform -strict` needs schemas for three CRDs this chart can render: `HTTPRoute` (Gateway API), `ServiceMonitor` (`monitoring.coreos.com`), `ExternalSecret` (`external-secrets.io`). The `helm` CI job (CORE-010) adds `-schema-location default -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'` to the existing kubeconform invocation — the same pattern `vk-lab-platform`'s `lifecycle-test.yml` uses (as CORE-130 cites at its own line 176); the exact URL is verified at implementation time, not taken from `research.md`. Three renders are validated per pull request:

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

1. `kind create cluster`; load the CI-built `gpu-on-demand` and `fake-worker` images into the kind node (`kind load docker-image`); install three dummy Secrets: provider (`VAST_API_KEY=x`), apiKeys (`client-keys`, `management-tokens`, `worker-seed`, each a short fixture value), ssh (a generated `id_ed25519`); apply a plain `Deployment`+`Service` manifest (not a Helm chart) for `cmd/fake-worker` — a small Go SSE server imitating llama-server's `/health`, `/v1/models`, `/v1/chat/completions` and `/metrics` (CORE-010, `docs/testing-strategy.md` tier 2; this chart neither builds nor renders it).
2. `helm install gpu-on-demand deploy/helm/gpu-on-demand --set image.tag=$GITHUB_SHA --set config.provider.name=fake --set config.tunnel.dialer=direct --set config.tunnel.directAddr=fake-worker.default.svc.cluster.local:8080 --set existingSecrets.provider=... --set existingSecrets.apiKeys=... --set existingSecrets.ssh=...`. `provider.name=fake` starts `fake.NewSeeded` (CORE-020): one $0.50/h L40S offer, an instance that reaches `running` on the first `GetInstance`, and a $100 balance. `config.lifecycle.provisioningMode` stays at its default `explicit`, so no GPU is provisioned by this step. `image.tag` is the full 40-hex commit SHA, so `svc.pullPolicy` derives `IfNotPresent` and the kubelet uses the image already loaded into the kind node instead of trying to pull it.
3. `kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=gpu-on-demand --timeout=60s` — the pod is Ready with no GPU running (FR-1.7).
4. `kubectl port-forward` the Service; `curl -s -H "Authorization: Bearer <management token>" .../api/v1/gpu/status | jq -r .state` reports `STOPPED`.
5. `curl -X POST .../api/v1/gpu/start -H "Authorization: Bearer <management token>"` returns `202`; poll with `curl -s -H "Authorization: Bearer <management token>" .../api/v1/gpu/status | jq -r .state` until it reports `READY` (the fake provider's instant offer and instance make this fast, no manual seeding beyond step 2).
6. `curl -H "Authorization: Bearer <client key>" .../v1/models` returns `200` (FR-1.2); a streamed `curl -N -X POST .../v1/chat/completions` returns SSE chunks ending `data: [DONE]`, proxied through the `direct` dialer to the fake worker.
7. `curl -X POST .../api/v1/gpu/stop -H "Authorization: Bearer <management token>"` returns `202`; poll `curl -s -H "Authorization: Bearer <management token>" .../api/v1/gpu/status | jq -r .state` until it reports `STOPPED`.
8. `helm upgrade` with `--set config.lifecycle.idleShutdown.timeout=10m`; observe a new `ReplicaSet` (changed `checksum/config`); re-run `curl -s -H "Authorization: Bearer <management token>" .../api/v1/gpu/status | jq -r .installationId` and assert it is unchanged — the proof that the shell `ConfigMap`'s merge left the live document alone.
9. `helm uninstall`; `kubectl get configmap gpu-on-demand-state` and the application `ConfigMap` both return `NotFound`.

## Requirements

- R1. The chart MUST be exactly one chart at `deploy/helm/gpu-on-demand`, published as an OCI artifact at the release version, `appVersion` set to the commit SHA. (source: ADR 0008 §4)
- R2. `image.tag` MUST be a `required` value; `latest` MUST NOT be a usable default. (source: ADR 0008 §4, C-21)
- R3. `existingSecrets.provider`, `.apiKeys`, `.ssh` MUST each be `required`; the `.apiKeys` Secret MUST carry `client-keys`, `management-tokens` and `worker-seed`; the chart MUST NOT render any `Secret` object from a plaintext value. (source: ADR 0009 §1, ADR 0012 §3, C-14, C-15)
- R4. `host` MUST be `required` exactly when `httpRoute.enabled` or `ingress.enabled` is `true`, and MUST NOT have a default value. (source: ADR 0008 §4)
- R5. When `httpRoute.enabled`, the rendered `HTTPRoute` MUST route both `/v1` and `/api/v1` to the same `Service` on the same host, in one `HTTPRoute` object. (source: ADR 0012 §1)
- R6. The pod MUST run as non-root UID/GID 65532 with a read-only root filesystem, no added capabilities, and `seccompProfile: RuntimeDefault`. (source: ADR 0008 §4, C-20)
- R7. `rbac.create: true` MUST render a `ServiceAccount`, a `Role` and a `RoleBinding`; `automountServiceAccountToken` MUST be `true`. (source: ADR 0008 §4, ADR 0004 §3)
- R8. The `Role` MUST grant exactly `get` and `update` scoped by `resourceNames` on the state `ConfigMap`, and MUST NOT grant `create`, `patch`, `delete` or `list`. (source: ADR 0004 §3, amended)
- R9. Default `resources` MUST be `requests.cpu: 50m`, `requests.memory: 64Mi`, `limits.memory: 256Mi`, with no `limits.cpu` key. (source: NFR-9.5)
- R10. `strategy.type` MUST be `Recreate`; the chart MUST fail its render when `replicaCount` is not exactly `1`. (source: ADR 0003 §1)
- R11. Probes MUST target `/healthz` and `/readyz` on container port 9090, matching CORE-010 R6/R7, and MUST NOT depend on a GPU instance existing. (source: CORE-010 R6, R7, FR-1.7)
- R12. Each `existingSecrets.*` Secret MUST be mounted as files — `provider` and `ssh` under `/etc/vkllm/secrets/{provider,ssh}`, `apiKeys` under `/etc/vkllm/api-keys/` — and MUST NOT be exposed through `env` or `envFrom`. (source: ADR 0009 §1)
- R13. The rendered application `ConfigMap` MUST contain exactly the 43 `VKLLM_*` keys listed in this spec's `config.*` table plus the two fixed keys `VKLLM_STATE_CONFIGMAP` and `VKLLM_INSTALLATION_ID`, 45 total, with no other key. (source: architecture §9)
- R14. `values.yaml` MUST NOT contain a hostname, a domain, an `fqdn` value or a secret value anywhere in its defaults. (source: C-14, NFR-8.6)
- R15. The chart MUST render without error, and without any `HTTPRoute` or `Ingress` object, when both `httpRoute.enabled` and `ingress.enabled` are `false`, and MUST install on any Kubernetes cluster with no ingress controller present. (source: NFR-9.2, C-23)
- R16. `helm uninstall` MUST leave no `ConfigMap` behind, including the state document's `ConfigMap`; `helm upgrade` MUST leave the state document's content untouched. (source: NFR-9.1, ADR 0004 §1)
- R17. `VKLLM_STATE_CONFIGMAP` MUST equal `{{ .Release.Name }}-state` everywhere it is used: the `configmap.yaml` env var, the `Role`'s `resourceNames`, and `state-configmap.yaml`'s object name. (source: ADR 0004 §1, §3)
- R18. When `networkPolicy.enabled` is `true`, egress MUST be limited to the four rules in the NetworkPolicy table; ingress MUST be limited to the two rules in that table. (source: architecture §6)
- R19. `NOTES.txt` MUST NOT render `.Values.host` or any Secret value. (source: C-14, NFR-8.6)
- R20. `kubeconform -strict` MUST pass for the default render, the `httpRoute` render and the `ingress` render. (source: NFR-9.2)
- R21. The kind CI job MUST install, reach Ready with no GPU running, serve `/v1/models`, drive start and stop through the management API, confirm status with `curl` against `/api/v1/gpu/status`, show a rollout on a changed `config.*` value, and leave no `ConfigMap` after `helm uninstall`. (source: NFR-9.1, FR-1.2, FR-7.4)
- R22. `helm lint` MUST exit 0 with no warnings on the default values. (source: NFR-9.1)
- R23. `VKLLM_INSTALLATION_ID` MUST be deterministic for a given `(namespace, release)` pair across repeated renders and across `helm upgrade`, unless `installationId` is set explicitly. (source: ADR 0004 §6, amended; `docs/testing-strategy.md`)
- R24. `serviceMonitor.enabled: true` MUST render exactly one `ServiceMonitor`. (CORE-090 adds the `PrometheusRule` and dashboard `ConfigMap` templates to this chart's `templates/` directory when it is implemented; this spec does not build them.) (source: NFR-10.6, CORE-090)
- R25. `installationId`, when set, MUST match `^[a-z0-9][a-z0-9-]{0,31}$` in `values.schema.json`, the same pattern CORE-040's `NewDocument` enforces. (source: ADR 0004 §6, amended; `docs/testing-strategy.md`)
- R26. `values.schema.json` MUST restrict `config.lifecycle.provisioningMode` to `explicit`; any other value MUST fail schema validation naming the field. (source: ADR 0010 §1)
- R27. `values.schema.json` MUST refuse `config.lifecycle.idleShutdown.enabled: false` unless `config.lifecycle.safety.maxLifetime` is a non-empty string. (source: ADR 0011 §5, FR-3.12)

## Implementation steps

1. `Chart.yaml`, `values.yaml` with every key and default from this spec, `values.schema.json` enforcing types, the `provisioningMode` enum, and the `idleShutdown`/`maxLifetime` `if`/`then` guard — other required fields are guarded in the templates (`required "message" .Values.x`) so every missing-value error comes from one place with one message. Test: `helm lint deploy/helm/gpu-on-demand` exits 0.
2. `_helpers.tpl` copied and extended from `vk-ahorro`'s (`svc.name`, `svc.fullname`, `svc.labels`, `svc.selectorLabels`, `svc.host`, `svc.image`, `svc.pullPolicy`) plus `svc.configEnv`. Test: `helm template` with minimal `--set` values renders a `Deployment` with a resolved image reference.
3. `deployment.yaml`, `service.yaml`, `configmap.yaml`, `serviceaccount.yaml`. Test: `helm template` output contains the `checksum/config` annotation and exactly the 43 `VKLLM_*` keys from the `config.*` table, plus the 2 fixed keys.
4. `role.yaml`, `rolebinding.yaml`. Test: `helm template --set rbac.create=true` output's `Role` has exactly one rule block matching the RBAC section verbatim; `grep -cE 'create|patch'` on its verbs is `0`.
5. `httproute.yaml`, `ingress.yaml`. Test: three `helm template` invocations (no route / httpRoute / ingress) each produce exactly the expected object set; the `httpRoute` render's `HTTPRoute` has exactly 2 rules, paths `/v1` and `/api/v1`.
6. `servicemonitor.yaml`, `networkpolicy.yaml`, `externalsecrets.yaml`. Test: enabling each in isolation renders the right object set — `externalsecrets.yaml` renders exactly 5 `ExternalSecret` objects; the `networkpolicy.yaml` render adds `--set networkPolicy.apiServerCidrs={10.0.0.1/32}`, since `helm template` cannot resolve `lookup` and would otherwise hit the `fail` guard.
7. `NOTES.txt`. Test: `helm template` with `host` set does not print that string anywhere in `NOTES.txt`'s rendered output.
8. Add the `-schema-location` datree catalog flag to the `helm` CI job's kubeconform step. Test: the three-variant kubeconform run (CI validation section) exits 0.
9. Write the kind job. Test: the full nine-step sequence in the kind CI job section passes locally against a throwaway kind cluster.
10. Wire `helm-lint`, `helm-template`, `helm-package` to real chart paths in the Makefile. Test: all three targets exit 0.

## Tests / acceptance criteria

- AC1. Setup: default values plus `image.tag`, three `existingSecrets.*`. Action: `helm template`. Expected: no `HTTPRoute`, no `Ingress`, no `NetworkPolicy`, no `ServiceMonitor`, no `ExternalSecret` object in the output. (NFR-9.2)
- AC2. Setup: same, `--set httpRoute.enabled=true --set host=llm.lab.example.com`. Expected: exactly one `HTTPRoute` with exactly 2 rules, matching `PathPrefix` `/v1` and `/api/v1`, both `backendRefs` naming the same `Service`. (R5)
- AC3. Setup: same, `--set ingress.enabled=true --set host=llm.lab.example.com`. Expected: exactly one `Ingress`, no `HTTPRoute`. (NFR-9.2)
- AC4. Action: `kubeconform -strict` on the three renders in AC1–AC3, with the datree catalog schema location. Expected: exit code `0` for all three. (NFR-9.2, R20)
- AC5. Action: `helm lint deploy/helm/gpu-on-demand`. Expected: exit code `0`, zero `[WARNING]` lines. (R22)
- AC6. Action: `grep -E 'lab\.example\.com|<fqdn>|sk-vkllm-' deploy/helm/gpu-on-demand/values.yaml`. Expected: no match (the placeholders in this spec's prose are not copied into the file). (R14)
- AC7. Setup: `--set image.tag=""`. Action: `helm template`. Expected: non-zero exit, error text containing `image.tag is required`. (R2)
- AC8. Setup: omit one of the three `existingSecrets.*`. Action: `helm template`. Expected: non-zero exit naming the missing key. (R3)
- AC9. Setup: `--set httpRoute.enabled=true`, no `host`. Action: `helm template`. Expected: non-zero exit, error text containing `host is required`. (R4)
- AC10. Setup: `--set replicaCount=2`. Action: `helm template`. Expected: non-zero exit, error text naming `replicaCount`. (R10)
- AC11. Setup: `--set rbac.create=true`. Action: `helm template`, extract the `Role`. Expected: exactly one rule block; its verbs are exactly `["get","update"]`; no rule contains `create`, `patch`, `delete` or `list`. (R8)
- AC12. Action: kind job steps 1–9 (CI validation / kind CI job section). Expected: step 3 (`Ready`) within 60 s; step 4's status curl reports `STOPPED`; step 5's start reaches `READY`; step 6's curl returns `200` and the streamed chat completion ends `data: [DONE]`; step 7's stop reaches `STOPPED`; step 8 produces a new `ReplicaSet` name and the same `installationId` as step 5; step 9's `kubectl get configmap` returns `NotFound` for both the application `ConfigMap` and `gpu-on-demand-state`. (R16, R21, FR-1.2)
- AC13. Setup: `--set rbac.create=true` with release name `foo`. Action: `helm template`, extract the `Role`'s `resourceNames` and `state-configmap.yaml`'s `metadata.name`. Expected: both equal `foo-state`, not `foo-gpu-on-demand-state`. (R17)
- AC14. Action: render `NOTES.txt` with `--set host=llm.lab.example.com`. Expected: the string `lab.example.com` does not appear in the rendered `NOTES.txt`. (R19)
- AC15. Action: `helm template` twice with the same `--namespace`/release name and no `installationId` set. Expected: `VKLLM_INSTALLATION_ID` is identical both times; a third render with a different release name yields a different value. (R23)
- AC16. Setup: `--set config.tunnel.dialer=direct --set config.tunnel.directAddr=fake-worker.default.svc.cluster.local:8080`. Action: `helm template`. Expected: the `ConfigMap` contains `VKLLM_TUNNEL_DIALER: "direct"` and `VKLLM_TUNNEL_DIRECT_ADDR: "fake-worker.default.svc.cluster.local:8080"`. (R13)
- AC17. Setup: `--set serviceMonitor.enabled=true`. Action: `helm template`. Expected: exactly one `ServiceMonitor`. (R24)
- AC18. Setup: `--set installationId=Not_Valid!`. Action: `helm template` or `helm install --dry-run` (schema validation runs first). Expected: non-zero exit, the `values.schema.json` pattern error names `installationId`; `--set installationId=e2e-alice` succeeds. (R25)
- AC19. Setup: `--set config.lifecycle.provisioningMode=on-demand`. Action: `helm template` or `helm install --dry-run`. Expected: non-zero exit, the schema error names `provisioningMode`. (R26)
- AC20. Setup: `--set config.lifecycle.idleShutdown.enabled=false --set config.lifecycle.safety.maxLifetime=""`. Action: `helm template` or `helm install --dry-run`. Expected: non-zero exit, the schema error names `maxLifetime`. A second run with the same `idleShutdown.enabled=false` and the default `maxLifetime: 12h` left in place succeeds. (R27, FR-3.12)
- AC21. Action: `helm template` with defaults, extract the `Deployment`'s `apiKeys` volume. Expected: the Secret named by `existingSecrets.apiKeys` is mounted read-only at `/etc/vkllm/api-keys/`, `defaultMode: 0400`, with no `env`/`envFrom` reference to that Secret anywhere in the output. (R3, R12)

## Definition of done

- [x] AC1–AC21 pass in CI, AC12 in the kind job. (local reproduction of the CI commands; first Actions run is Task 12's PR)
- [x] `make specs-check` passes.
- [x] Status flipped to DONE in the same pull request, folder renamed `080-D-helm-chart`.
- [ ] Evidence section filled, including the measured kind job wall-clock time — filled below, but the kind job's wall-clock time is not yet a real measured number (see Evidence): it was run three times for real locally with no timer captured, and this branch has not yet gone through a GitHub Actions run. The first real number comes from Task 12's PR.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Vast publishes no IP range for its API or its SSH hosts (research §1.1, §1.6) | the egress NetworkPolicy for 443 and for SSH cannot be narrowed below `0.0.0.0/0` today | re-check research.md if Vast publishes one later | `networkPolicy.vastApiCidrs` empty; SSH rule stays protocol-only |
| The platform's monitoring namespace name is not recorded in research §5 | the 9090 ingress rule cannot be scoped to it | CORE-130, when the platform integration spec reads the real namespace | 9090 ingress allowed from all in-cluster sources (not sensitive data) |
| Whether `lookup`-based kube-apiserver `ipBlock`s resolve correctly across k3s and kind, and what CIDR CORE-130 should pass as `apiServerCidrs` for the platform's Argo-rendered install | a wrong `ipBlock` either over-permits (defeats the policy) or blocks the store entirely; Argo's client-side render means the platform install always needs `apiServerCidrs` set, never `lookup` | kind job AC12 for the `lookup` path; CORE-130 supplies the platform's `apiServerCidrs` | `lookup` for a direct `helm install`/`upgrade`; `fail` (not a silent `0.0.0.0/0`) when `networkPolicy.enabled` and neither resolves |
| The Envoy route timeout object (660s in explicit mode, or disabled; 1080s reserved for a future on-demand install) has no object this chart renders | a platform install without it risks Envoy cutting a slow cold start or a long generation short | CORE-130 (a platform-side `BackendTrafficPolicy` or equivalent, out of this chart) | 660s (`model.generationTimeout + 60s`) or disabled (ADR 0010 Consequences) |
| No production `model.url`/`model.sha256`/`worker.image` value exists anywhere in this repository — settled by whoever first runs a real install | this chart must not invent a value | whoever first runs a real install | `required`, no chart default, matching `image.tag`'s own category |

## Evidence

Built across Tasks 1–10 of `docs/superpowers/plans/2026-11-06-core-080-helm-chart.md`, each with an independent reviewer pass; this section cites the real findings from that record (`.superpowers/sdd/2026-11-06-core-080-helm-chart/progress.md`), not a generic pass/fail summary.

**`values.schema.json` idle/lifetime guard — a real cost-safety bug, found and fixed (Task 2).** The brief this spec was built from required only `"required": ["safety"]` at the `config.lifecycle` level. The implementer found that `--set config.lifecycle.safety.maxLifetime=null` deletes the `maxLifetime` key entirely while still satisfying that guard (the `safety` object is still present), bypassing R27/FR-3.12 — exactly the class of invisible safety bug this spec's own Risk line warns about. Fixed by nesting `"required": ["maxLifetime"]` inside the `safety` schema (shown in the `then` block above, which now matches `deploy/helm/gpu-on-demand/values.schema.json` exactly — this spec's own prose snippet had drifted from the shipped schema until this write-back corrected it). The reviewer independently reproduced the bypass from a second direction too (`--set config.lifecycle.safety=null`) and confirmed both now fail.

**`VKLLM_GATEWAY_MAX_BODY_BYTES` scientific-notation bug, found and fixed (Task 3).** The default `1048576` rendered as `"1.048576e+06"` through a bare `| quote` — Helm's YAML-via-JSON values loader boxes that specific round integer as `float64`, and `quote`'s `%v` formatting switches to scientific notation for it. `internal/config`'s `strconv`-based integer parser would have failed on every default install. Fixed by changing exactly the 11 integer-typed `config.*` fields to `| int64 | quote` (float-typed fields such as `minReliability` or `maxHourlyPriceUSD` were left on bare `| quote`, since `int64`-casting a float would truncate decimals).

**`fsGroup: 65532`, a deliberate addition not in the original brief (Task 4).** Without it, kubelet mounts the three `existingSecrets.*` volumes `root:root` with `defaultMode: 0400`; the non-root `65532` container gets `EACCES` on every one of them and crash-loops on first real install — invisible to `helm lint`/`helm template`, real-cluster-only. The reviewer independently confirmed the mechanism against kubelet's `SetVolumeOwnership` behavior (fsGroup both chowns to the GID and widens read-only secret volumes to `0440`) before accepting it. Confirmed again, live, by Task 10's kind runs (the pod reaches Ready with all three secrets mounted).

**Plan's own verification regex undercounted by one (Task 4).** `VKLLM_[A-Z_]+:` misses `VKLLM_MODEL_SHA256` (digits excluded from the character class), undercounting 45 keys to 44. Fixed to `VKLLM_[A-Z0-9_]+:` in the plan's own verification section.

**Kind CI job — 7 real defects found and fixed by actually running the job three times against two separate local kind clusters, not by writing YAML and trusting it (Task 10):** a 63-character `model.sha256` fixture (one hex digit short of the schema's `^[0-9a-f]{64}$`); an 18-byte `worker-seed` fixture against `internal/gateway/workerseed.go`'s hard 32-byte requirement (this one actually broke the job — the pod went `Running` but never `Ready`, `/readyz` returned 503 until the fixture was widened); `helm/kind-action`'s real default cluster name (`chart-testing`, not `kind`), which needed pinning on both the action and every `kind load docker-image` call; a missing explicit understanding of whether a `kind create cluster` step was needed (resolved by letting the action create the pinned-name cluster itself, matching its actual default behavior read from the pinned action's source); a `kubectl port-forward` that does not survive the `Recreate`-strategy pod replacement during `helm upgrade` (reproduced directly: `curl: (52) Empty reply from server`), fixed by restarting the forward after `kubectl rollout status` confirms the new pod; an `helm upgrade` assertion that only checked `installationId` was non-empty rather than literally unchanged across the upgrade — the real R23 cost-safety invariant — now captures before/after and asserts equality (confirmed `70ac92c091ea` both times in a real run); and a `kubectl wait -l` line in the upgrade step that could transiently match both the new Ready pod and the old Terminating one under `Recreate`, removed as redundant with `kubectl rollout status`, which already blocks on `availableReplicas`.

**Fresh validation run for this write-back (2026-11-06, this task):**

```
$ make helm-lint CHART=gpu-on-demand
==> Linting deploy/helm/gpu-on-demand
[INFO] Chart.yaml: icon is recommended
1 chart(s) linted, 0 chart(s) failed

$ make -s helm-template CHART=gpu-on-demand | grep -c '^kind:'
7

$ make -s helm-template CHART=gpu-on-demand | grep -oE 'VKLLM_[A-Z0-9_]+:' | sort -u | wc -l
45
```

45 distinct `VKLLM_*` keys confirmed (43 `config.*` rows plus the 2 fixed keys, R13).

**Three-route-variant `kubeconform -strict` pass, re-run locally against the exact command the `helm` CI job uses** (`-schema-location default -schema-location '.../datreeio/CRDs-catalog/...'`), `kubeconform v0.8.0`:

```
defaults:   Summary: 7 resources found in 1 file - Valid: 7, Invalid: 0, Errors: 0, Skipped: 0
httpRoute:  Summary: 8 resources found in 1 file - Valid: 8, Invalid: 0, Errors: 0, Skipped: 0
ingress:    Summary: 8 resources found in 1 file - Valid: 8, Invalid: 0, Errors: 0, Skipped: 0
```

(`httpRoute`/`ingress` each add one object over the 7-object default render — the `HTTPRoute` or `Ingress` itself.)

**Kind job wall-clock time — a known gap, not fabricated.** Task 10 ran the full nine-step job for real against local kind clusters three times (`citest-local`, then `citest` twice more after fix rounds), every run passing end to end, but did not capture an elapsed-time figure in its own report. This branch has not yet gone through a GitHub Actions run (`gh run list` shows none for this branch) — there is no PR open yet. The first real wall-clock number is Task 12's (`timeout-minutes: 15` is the budget `store-kind`-style jobs in this repo are held to; `helm`'s own job budget is `timeout-minutes: 10`). Recorded honestly as outstanding rather than invented.

**IP-literal leak check** (`grep -rEn '([0-9]{1,3}\.){3}[0-9]{1,3}' specs/core/080-D-helm-chart/spec.md`): 6 matches, all expected and not a leak — `0.0.0.0/0` appears 3 times in the NetworkPolicy table and Open questions (the deliberately unrestricted Vast-API and worker-SSH egress rules, both documented as intentionally wide, not narrowable today) and once more in the `lookup`/`apiServerCidrs` open question; `10.0.0.1/32` appears once, in Implementation step 6, as the example CIDR a `helm template` invocation needs to pass `networkPolicy.apiServerCidrs` since `helm template` cannot resolve `lookup`. None is a real host, domain or credential (C-14).

**Host/secret leak check** (`grep -n 'lab\.example\.com\|sk-vkllm-' deploy/helm/gpu-on-demand/values.yaml`): no match (exit 1). `values.yaml` carries no hostname, domain or secret value; every placeholder of that shape lives only in this spec's prose and in CI `--set` flags (R14, C-14).

**Residuals, named explicitly, not silently dropped:**

- `PrometheusRule` and the dashboard `ConfigMap` are not rendered by this chart; `servicemonitor.yaml` renders only the `ServiceMonitor` (AC17, R24). CORE-090 adds its own templates to this chart's `templates/` directory when implemented.
- The worker-SSH egress `NetworkPolicy` rule has no `to:` (0.0.0.0/0, full port range) — close to allow-all-TCP-egress once it exists, since Vast publishes no IP range and assigns a random external port per instance after creation. Recorded as a weak, protocol-only control (Task 7 review; Q32, closed above).
- `networkPolicy.apiServerCidrs` is hard-mandatory (the template `fail`s naming it) under `helm template` and under Argo CD's repo-server rendering, since neither resolves `lookup` against a live cluster. Flagged for whoever writes `vk-lab-platform`'s values file (CORE-130).
- `httpRoute.enabled` and `ingress.enabled` can both be set `true` simultaneously, rendering both objects — nothing in the chart enforces mutual exclusivity; R15 only requires zero-of-each when both are `false` (Task 6 review).
- `make image-build`'s command has no `--provenance=false` (unlike `image-push`, which does); this is unverified against the real `ubuntu-latest` buildx/kind interaction specifically, left unchanged because `specs/core/010-D-repo-skeleton-and-ci/spec.md` pins that command's text verbatim and Task 10 scoped to `.github/workflows/ci.yml` only (Task 10 Concerns).
- `GET /api/v1/gpu/status` reports `"provider":"vastai"` even when the fake provider is genuinely in use (`internal/controller/status.go:23` hardcodes the field) — a pre-existing display bug, out of this chart's scope, found incidentally by Task 10's kind runs.
- No production `model.url`/`model.sha256`/`worker.image` value exists anywhere in this repository yet (Open questions); each stays `required` with no chart default, the same category as `image.tag`.
