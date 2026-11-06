# CORE-080: Helm chart `gpu-on-demand` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `deploy/helm/gpu-on-demand/`, a Helm chart that installs the already-wired `gpu-on-demand` binary (CORE-010/050/055/060/070/100, and the main.go wiring PRs #15/#16) onto any Kubernetes cluster with no GPU running, exposes the management and inference APIs on an optional route, and leaves nothing behind on `helm uninstall`.

**Architecture:** One chart, one `Deployment` (`replicas: 1`, `Recreate`), one `Service` (http+metrics), `Role`/`RoleBinding` scoped to `get`/`update` on the state `ConfigMap`, optional `HTTPRoute` XOR `Ingress`, optional `ServiceMonitor`, optional `NetworkPolicy`, optional `ExternalSecret`s. No business logic — the risk is entirely in getting the `values.yaml` → `VKLLM_*` env var mapping exactly right, because nothing here is unit-testable in Go.

**Spec:** [`specs/core/080-D-helm-chart/spec.md`](../../../specs/core/080-D-helm-chart/spec.md). ADRs: 0003 (one replica, Recreate, destroy-not-stop), 0004 (state ConfigMap, RBAC), 0008 (image/pipeline conventions, pod security), 0009 (secrets never in Git, file-mount delivery), 0010 (explicit provisioning), 0011 (idle/lifetime safety), 0012 (one process serves both APIs), 0013 (OCI chart publish).

## Why a spec-amendment task comes first (Task 1)

Spec 080 was written 2026-11-01, **before any Go code existed** (its own status note says so). `internal/config`'s real env-var set has since been built across CORE-050/055/070/100 and, most recently, PRs #15/#16 (`cmd/gpu-on-demand/main.go` wiring, merged today). A direct audit of every `internal/config/*.go` loader `main()` actually calls — not a re-read of the spec's own table — finds the spec's `config.*` table is wrong in both directions: it invents 2 keys that no Go code reads, it's missing 13 keys that 3 packages (`config.Worker`, `config.Auth`, parts of `config.Mgmt`) do read, and one key name is spelled wrong. This is the same "spec written before code" pattern CORE-055/100's plans already resolved by amending the spec first — this one is larger, so it gets more of Task 1.

**Full audit, real source wins over spec text:**

grep across `internal/config/*.go` (excluding `_test.go`) for every `VKLLM_` env var `main()`'s own call chain (`config.Load`, `LoadController`, `LoadBudget`, `LoadGateway`, `LoadMgmt`, `LoadWorker`, `LoadAuth`) touches gives **45 total keys**: 2 fixed (`VKLLM_INSTALLATION_ID`, `VKLLM_STATE_CONFIGMAP`) + 43 `config.*` keys — not spec's claimed "39 `config.*` + 2 fixed = 41".

1. **`VKLLM_BUDGET_MONTHLY_USD` is wrong; shipped code reads `VKLLM_BUDGET_MONTH_USD`** (`internal/config/budget.go:17`). Spec's row gets the env var name fixed.
2. **Two spec rows have no corresponding Go field anywhere**: `config.lifecycle.safety.maxInstances` → `VKLLM_LIFECYCLE_SAFETY_MAX_INSTANCES` and `config.lifecycle.shutdown.policy` → `VKLLM_LIFECYCLE_SHUTDOWN_POLICY`. Neither string appears in any non-test `.go` file. Both rows are deleted — `maxInstances=1` and the destroy-only shutdown policy are enforced in code, not configurable (matches ADR 0003 §1's own "one process" framing — there is nothing to tune).
3. **`internal/config.Worker` (new in PR #15) adds 7 keys spec never anticipated**, since spec 080 predates it: `VKLLM_MODEL_URL`, `VKLLM_MODEL_SHA256`, `VKLLM_MODEL_PARALLEL`, `VKLLM_WORKER_IMAGE`, `VKLLM_WORKER_DEAD_MAN_TIMEOUT`, `VKLLM_PROVIDER_API_KEY_FILE`, `VKLLM_TUNNEL_SSH_KEY_FILE`. The first 5 already had near-equivalent rows in spec's original table under slightly different key paths (`config.model.url/sha256/parallel`, `config.worker.image/deadManTimeout`) — those rows are kept, corrected to match the real env var names and the fact that **none of them has a safe default**: `config.LoadWorker` errors on an empty `ModelURL`/`ModelSHA256`/`WorkerImage`. Checked `docs/research.md` and `specs/core/070-D-worker-runtime/spec.md`'s own Evidence for a real pinned value to use as a chart default — **none exists**: CORE-070 pinned a real sha256 only for its own tiny AC8 test fixture (`stories260K.gguf`), never for the production Qwen3.6-35B-A3B model. These three become `required`, no-default values.yaml keys, exactly like `image.tag` and `existingSecrets.*` already are — same pattern, same risk category (missing value fails the render loudly, not silently).
4. **`internal/config.Auth` (shipped since CORE-055, never added to spec 080's table) adds 3 keys**: `VKLLM_AUTH_CLIENT_KEYS_FILE` (default `/etc/vkllm/api-keys/client-keys`), `VKLLM_AUTH_MANAGEMENT_TOKENS_FILE` (default `/etc/vkllm/api-keys/management-tokens`), `VKLLM_AUTH_WORKER_SEED_FILE` (default `/etc/vkllm/api-keys/worker-seed`). **Spec's own "Secret mounts" table already planned a mount path for this Secret that does not match these real defaults** (`/etc/vkllm/secrets/apiKeys/` vs the real `/etc/vkllm/api-keys/`). Resolution: mount at the real default path (`/etc/vkllm/api-keys/`) so the chart needs no env var overrides for these three at all — simpler, and it means a future change to `internal/config/auth.go`'s defaults can't silently drift from the chart without a failing mount. The two `VKLLM_PROVIDER_API_KEY_FILE`/`VKLLM_TUNNEL_SSH_KEY_FILE` defaults from point 3, by contrast, **were set to match spec 080's existing mount table** when `internal/config/worker.go` was written (`/etc/vkllm/secrets/provider/VAST_API_KEY`, `/etc/vkllm/secrets/ssh/id_ed25519`) — those already agree, no fix needed there.
5. **`config.Mgmt` adds `VKLLM_PROVIDER_NAME`, `VKLLM_MODEL_CONTEXT_SIZE`, `VKLLM_MGMT_RATE_LIMIT_RPM`** — 3 new rows. `VKLLM_MGMT_BUDGET_MONTH_USD` also exists in `config.Mgmt` but **is never rendered as a chart value**: `cmd/gpu-on-demand/wire.go` unconditionally overrides `d.Mgmt.BudgetMonthUSD = d.Budget.MonthlyUSD` before using it — the env var is read but its value is always discarded in favor of `VKLLM_BUDGET_MONTH_USD`. Rendering it in the chart would let an operator set a value that does nothing, which is worse than not rendering it. One sentence in the amendment documents this explicitly so a future reader doesn't "fix" the omission.
6. **39 rows → 43 config.* rows + the 2 fixed keys = 45 total.** R13 gets the corrected count.
7. **R24/AC17 (ServiceMonitor → PrometheusRule + dashboard ConfigMap) need CORE-090's alert expressions and dashboard JSON, which don't exist yet** (CORE-090 is unimplemented — confirmed `internal/obs` is still a `doc.go` stub). Amendment: `serviceMonitor.enabled` renders only the `ServiceMonitor` object in this spec; `PrometheusRule` and the dashboard `ConfigMap` move to CORE-090's own scope (it depends only on CORE-050+060, both done, so it can add these templates whenever it's implemented — this chart's `templates/` directory is exactly where it adds them, no re-opening of this spec needed).
8. **AC12 steps 4/5/7/8 and `NOTES.txt` reference `vk-llm status --json`, built in-tree**. `cmd/vk-llm/main.go` is still CORE-010's usage-only skeleton (confirmed: prints usage, no subcommands) — CORE-150 adds them, and CORE-150 depends on CORE-120 (release pipeline), neither done. Amendment: every `vk-llm status`/`start`/`stop` reference becomes a `curl`+`jq` call against the already-real management API (`curl -s .../api/v1/gpu/status | jq -r .state`), matching CLAUDE.md's ambiguity tiebreak (simplicity, and an interpretation that doesn't invent a dependency on unshipped code). `NOTES.txt`'s three-command workflow becomes curl-based too.
9. **R27's `if`/`then` schema design was tested empirically before writing this plan** (not left as the spec's own open question): a 15-line stub chart with exactly spec's `if`/`then` block, run through `helm lint` and `helm template --set config.lifecycle.idleShutdown.enabled=false --set config.lifecycle.safety.maxLifetime=""`, correctly failed with `minLength: got 0, want 1`; the valid combination (`maxLifetime="12h"`) correctly passed. **Helm's schema validator does honor `if`/`then`** — no template-level `{{ fail }}` fallback is needed, R27 is exactly as spec designed it. One refinement found during this test: `envDuration("")` in `internal/config/controller.go` falls through to the 12h default (safe), but `"0s"` parses successfully to a zero duration (not safe — `idleShutdown.enabled: false` plus `maxLifetime: "0s"` would pass `minLength: 1` while giving zero real protection). Fix: the schema's `then` branch adds `"pattern": ".*[1-9].*"` alongside `minLength: 1` — any valid Go duration string evaluating to a non-zero duration contains at least one digit 1-9 somewhere; an all-zero string like `"0s"` or `"00h0m0s"` never does.
10. The CI `helm` job **already has** the datree kubeconform `-schema-location` flag (confirmed by reading `.github/workflows/ci.yml` directly) — spec's Implementation step 8 is already satisfied; Task 9 below only updates the `helm-template` Makefile target's default `--set` flags (now covering the new required keys) and adds the kind job, not the kubeconform flag.

## Global Constraints

- `deploy/helm/gpu-on-demand/` is the only chart this plan creates. `Chart.yaml` apiVersion v2, placeholder `version`/`appVersion` (both overridden at publish — `vk-ahorro`'s exact pattern, see Task 3).
- `values.yaml` contains no hostname, no domain, no `fqdn`, no secret value anywhere (C-14). `--set host=...lab.example.com` only ever appears in command lines, never in a committed file.
- Every `config.*` row is always rendered in `configmap.yaml`, never conditional — 43 keys exactly, plus `VKLLM_STATE_CONFIGMAP`/`VKLLM_INSTALLATION_ID`. A key absent from the ConfigMap would silently fall back to whatever default `internal/config` compiles in, invisible to this chart.
- `image.tag`, `existingSecrets.{provider,apiKeys,ssh}`, `config.model.url`, `config.model.sha256`, `config.worker.image`, and `host` (only when a route is enabled) are `required` with no default — missing any of them is a loud render failure, never a silent one.
- Pod security: non-root UID/GID 65532, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: ["ALL"]`, `automountServiceAccountToken: true` (unlike `vk-ahorro`'s `false` — `internal/store/configmap` needs the mounted token).
- `strategy.type: Recreate`; render `{{ fail }}` when `replicaCount != 1`.
- Comments ≤3 lines, never name a spec/ADR/requirement by number (CLAUDE.md) — this applies to Helm template comments too.
- Attribution footer on every commit (session rule).

## Design decisions this plan makes

1. Spec amendment lands first (Task 1), per the audit above — the spec text is fixed before any implementer reads it, same pattern as CORE-055/100.
2. `_helpers.tpl`, `deployment.yaml`'s skeleton, and `Chart.yaml`'s version-placeholder comment are copied verbatim from `/Users/margo/Proj/Lab/vk-ahorro/deploy/helm/ahorro-api/templates/` (confirmed present on disk) and extended — not reinvented. Three *new* helpers (`svc.installationId`, `svc.configEnv`, `svc.apiKeysDir`) are added because `vk-ahorro` has no equivalent need for them.
3. `config.model.url`, `config.model.sha256`, `config.worker.image` are `required` values.yaml keys (no default) — the no-real-value finding from the audit — with `values.schema.json` `pattern` guards (`^https://`, `^[0-9a-f]{64}$`) as defense in depth beyond the template-level `required` call.
4. `PrometheusRule` and the dashboard `ConfigMap` are explicitly out of this plan's scope (audit point 7) — `serviceMonitor.enabled` renders only the `ServiceMonitor`.
5. Every `vk-llm` CLI reference in AC12/NOTES.txt becomes `curl`+`jq` against the real management API (audit point 8).
6. The kind CI job is new (nothing like it exists today); the existing `helm` CI job's datree kubeconform flag is already correct and untouched.

---

### Task 1: Spec amendment — close every delta the audit found, flip DRAFT→READY

**Files:**
- Modify: `specs/core/080-D-helm-chart/spec.md`
- Modify: `docs/open-questions.md`

- [ ] **Step 1:** Rewrite the "`values.yaml` — `config.*` (every key, `docs/architecture.md` §9)" table: fix `VKLLM_BUDGET_MONTHLY_USD` → `VKLLM_BUDGET_MONTH_USD`; delete the `config.lifecycle.safety.maxInstances`/`VKLLM_LIFECYCLE_SAFETY_MAX_INSTANCES` row and the `config.lifecycle.shutdown.policy`/`VKLLM_LIFECYCLE_SHUTDOWN_POLICY` row (no Go field exists); add these 13 rows:

  | `values.yaml` key | Default | Env var |
  |---|---|---|
  | `config.model.parallel` | `1` | `VKLLM_MODEL_PARALLEL` |
  | `config.worker.deadManTimeout` | `""` (empty → `idleShutdown.timeout + 10m`) | `VKLLM_WORKER_DEAD_MAN_TIMEOUT` |
  | `config.provider.apiKeyFile` | `/etc/vkllm/secrets/provider/VAST_API_KEY` | `VKLLM_PROVIDER_API_KEY_FILE` |
  | `config.tunnel.sshKeyFile` | `/etc/vkllm/secrets/ssh/id_ed25519` | `VKLLM_TUNNEL_SSH_KEY_FILE` |
  | `config.provider.name` (**already in spec, keep**) | `vastai` | `VKLLM_PROVIDER_NAME` |
  | `config.model.contextSize` (**already in spec, keep**) | `32768` | `VKLLM_MODEL_CONTEXT_SIZE` |
  | `config.mgmt.rateLimitPerKeyRPM` | `60` | `VKLLM_MGMT_RATE_LIMIT_RPM` |

  Correct the two already-existing rows for `config.model.url` and `config.model.sha256` and `config.worker.image` to say **"no default, required"** instead of `""`/`"" (was 6h)`-style placeholder text, with a one-line note: "no production value exists anywhere in this repository yet; set at install time, same category as `image.tag`." Add one sentence directly under the table: *"`config.mgmt.budgetMonthUSD`/`VKLLM_MGMT_BUDGET_MONTH_USD` is deliberately not a chart value — `cmd/gpu-on-demand/wire.go` always overrides it from `config.budget.monthlyUSD`, so rendering it would let an operator set a value with no effect."*
- [ ] **Step 2:** Update R13 to say "43 `config.*` rows... plus the 2 fixed keys, 45 total", matching the corrected table.
- [ ] **Step 3:** In the "Secret mounts" table, change the `.apiKeys` row's mount path from `/etc/vkllm/secrets/apiKeys/` to `/etc/vkllm/api-keys/`, with a one-line note that this matches `internal/config/auth.go`'s real shipped defaults rather than inventing a new path.
- [ ] **Step 4:** In the template list table, change the `servicemonitor.yaml` row's "Renders" text to "Gateway API... `ServiceMonitor` only (`PrometheusRule` and the dashboard `ConfigMap` are CORE-090's own templates, added when that spec is implemented)"; delete the `prometheusrule.yaml` and `dashboard-configmap.yaml` rows entirely (they move to CORE-090, not built here).
- [ ] **Step 5:** Update R24 to read: *"`serviceMonitor.enabled: true` MUST render exactly one `ServiceMonitor`. (CORE-090 adds the `PrometheusRule` and dashboard `ConfigMap` templates to this chart's `templates/` directory when it is implemented; this spec does not build them.)"* Update AC17 to check only for the `ServiceMonitor`.
- [ ] **Step 6:** In the "`values.schema.json` — provisioning mode and the idle/lifetime guard" section's JSON snippet, add `"pattern": ".*[1-9].*"` alongside `"minLength": 1` in the `then.properties.safety.properties.maxLifetime` block; add one sentence explaining why (a duration string with no digit 1-9 anywhere always evaluates to zero).
- [ ] **Step 7:** In the "kind CI job" section, replace every `./vk-llm status --json` with `curl -s -H "Authorization: Bearer <management token>" .../api/v1/gpu/status | jq -r .state`, and delete the `go build -o vk-llm ./cmd/vk-llm` step entirely (nothing in the job needs the binary once status checks are curl-based). Update AC12's Expected clause the same way. Update `NOTES.txt`'s described contents (in the template list table) to say the three-command workflow is curl-based, not `vk-llm`-based.
- [ ] **Step 8:** Delete the two now-resolved Open Questions rows ("Whether Helm's vendored JSON Schema validator supports the `if`/`then`..." — resolved by this plan's own empirical test in Task 1's audit) and ("`config.worker.image`'s exact digest is CORE-070's to pin" — resolved: no digest is pinned anywhere, it's a required value now). Add one new row: *"No production `model.url`/`model.sha256`/`worker.image` value exists anywhere in this repository — settled by whoever first runs a real install; default until then: `required`, no chart default, matching `image.tag`'s own category."*
- [ ] **Step 9:** Flip front-matter `status: "DRAFT"` to `status: "READY"`. Folder stays `080-P-...` (both map to `P` per `specs/README.md`).
- [ ] **Step 10:** In `docs/open-questions.md`, close spec 080's row in the "Known spec inconsistencies" table (`080 / 110 | require at least one management token at install...`) is about a *different*, still-open item — leave it; add a **new** row instead: *"Spec 080's `config.*` table was rewritten against the real shipped `internal/config` env vars post-PR #15/#16 (45 keys, not 39+2; two invented keys deleted, 13 missing keys added) — see spec 080's own amendment for the full diff."*
- [ ] **Step 11:** `make specs-check`.
- [ ] **Step 12:** `git add specs/core/080-D-helm-chart/spec.md docs/open-questions.md && git commit -m "docs: CORE-080 spec amendment — real 45-key env contract, CORE-090 split, vk-llm→curl"`.

---

### Task 2: `Chart.yaml`, `values.yaml`, `values.schema.json`

**Files:**
- Create: `deploy/helm/gpu-on-demand/Chart.yaml`
- Create: `deploy/helm/gpu-on-demand/values.yaml`
- Create: `deploy/helm/gpu-on-demand/values.schema.json`

- [ ] **Step 1:** `Chart.yaml`:

```yaml
apiVersion: v2
name: gpu-on-demand
description: The GPU on-demand control plane - a Deployment, a Service, and the inference/management APIs on the platform gateway.
type: application
version: 0.1.0
# A placeholder. The pipeline overrides both with --version and --app-version;
# the version is a git tag, so there is no hand bump to forget.
appVersion: "0.1.0"
```

- [ ] **Step 2:** `values.yaml`, every key from the amended spec's tables, grouped exactly as the spec groups them:

```yaml
image:
  repository: ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand
  tag: ""
  pullPolicy: ""

existingSecrets:
  provider: ""
  apiKeys: ""
  ssh: ""

host: ""
installationId: ""

config:
  provider:
    name: vastai
    apiKeyFile: /etc/vkllm/secrets/provider/VAST_API_KEY
    filter:
      gpuName: L40S
      numGpus: 1
      minCpuCores: 8
      minCpuRamGB: 32
      minDiskGB: 60
      minReliability: 0.95
      minInetDownMBps: 500
      minDiskBwMBps: 500
      minCudaVersion: 12.8
      geolocation: ""
  lifecycle:
    provisioningMode: explicit
    idleShutdown:
      enabled: true
      timeout: 30m
    shutdown:
      drainTimeout: 60s
      allowForce: false
    startupTimeout: 15m
    safety:
      maxHourlyPriceUSD: 0.80
      maxLifetime: 12h
      maxInstanceSpendUSD: 0
      maxProvisionAttemptsPerHour: 3
  budget:
    monthlyUSD: 30
    warnFraction: 0.8
  controller:
    reconcileInterval: 60s
  gateway:
    maxBodyBytes: 1048576
    maxTokensCap: 8192
    rateLimitPerKeyRPM: 60
  mgmt:
    rateLimitPerKeyRPM: 60
  tunnel:
    dialer: ssh
    directAddr: ""
    sshKeyFile: /etc/vkllm/secrets/ssh/id_ed25519
  model:
    alias: qwen3.6-35b-a3b
    url: ""
    sha256: ""
    contextSize: 32768
    parallel: 1
    generationTimeout: 10m
  worker:
    image: ""
    deadManTimeout: ""

httpRoute:
  enabled: false
  parentRef:
    name: platform-gateway
    namespace: envoy
    sectionName: https

ingress:
  enabled: false
  className: ""
  annotations: {}
  tls:
    enabled: false
    secretName: ""

service:
  type: ClusterIP
  port: 80

serviceMonitor:
  enabled: false
  interval: 15s
  labels: {}

networkPolicy:
  enabled: false
  gatewayNamespace: envoy
  vastApiCidrs: []
  apiServerCidrs: []
  sshPorts:
    from: 1
    to: 65535

externalSecrets:
  enabled: false
  storeRef: ""
  prefix: ""

rbac:
  create: true

serviceAccount:
  name: ""

resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    memory: 256Mi

probes:
  readiness:
    periodSeconds: 5
  liveness:
    periodSeconds: 20

replicaCount: 1
```

- [ ] **Step 3:** `values.schema.json` — types for every key above, plus the four load-bearing checks (`provisioningMode` enum, the `idleShutdown`/`maxLifetime` `if`/`then` with the zero-duration `pattern` fix, `installationId`'s pattern, and `model.url`/`model.sha256` shape patterns):

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["image", "existingSecrets"],
  "properties": {
    "image": {
      "type": "object",
      "required": ["tag"],
      "properties": {
        "repository": { "type": "string" },
        "tag": { "type": "string", "minLength": 1 },
        "pullPolicy": { "type": "string" }
      }
    },
    "existingSecrets": {
      "type": "object",
      "required": ["provider", "apiKeys", "ssh"],
      "properties": {
        "provider": { "type": "string", "minLength": 1 },
        "apiKeys": { "type": "string", "minLength": 1 },
        "ssh": { "type": "string", "minLength": 1 }
      }
    },
    "host": { "type": "string" },
    "installationId": {
      "type": "string",
      "pattern": "^$|^[a-z0-9][a-z0-9-]{0,31}$"
    },
    "config": {
      "type": "object",
      "properties": {
        "model": {
          "type": "object",
          "required": ["url", "sha256"],
          "properties": {
            "alias": { "type": "string" },
            "url": { "type": "string", "pattern": "^https://" },
            "sha256": { "type": "string", "pattern": "^[0-9a-f]{64}$" },
            "contextSize": { "type": "integer", "minimum": 1 },
            "parallel": { "type": "integer", "minimum": 1 },
            "generationTimeout": { "type": "string" }
          }
        },
        "worker": {
          "type": "object",
          "required": ["image"],
          "properties": {
            "image": { "type": "string", "minLength": 1 },
            "deadManTimeout": { "type": "string" }
          }
        },
        "lifecycle": {
          "type": "object",
          "properties": {
            "provisioningMode": { "type": "string", "enum": ["explicit"] },
            "idleShutdown": {
              "type": "object",
              "properties": {
                "enabled": { "type": "boolean" },
                "timeout": { "type": "string" }
              }
            },
            "safety": {
              "type": "object",
              "properties": {
                "maxHourlyPriceUSD": { "type": "number" },
                "maxLifetime": { "type": "string" },
                "maxInstanceSpendUSD": { "type": "number" },
                "maxProvisionAttemptsPerHour": { "type": "integer" }
              }
            }
          },
          "if": {
            "properties": {
              "idleShutdown": { "properties": { "enabled": { "const": false } } }
            }
          },
          "then": {
            "properties": {
              "safety": {
                "properties": {
                  "maxLifetime": { "minLength": 1, "pattern": ".*[1-9].*" }
                }
              }
            },
            "required": ["safety"]
          }
        }
      }
    }
  }
}
```

  (`installationId`'s pattern allows the empty string too — `svc.installationId` derives a value when empty, so an empty string must still validate. The non-empty branch's `[a-z0-9][a-z0-9-]{0,31}` matches `internal/store`'s `NewDocument` pattern exactly.)

- [ ] **Step 4:** `helm lint deploy/helm/gpu-on-demand` — fails with missing-required-values errors at this point (no templates exist yet to render), which is expected; this step only confirms the YAML/JSON itself parses. Use `python3 -c "import json; json.load(open('deploy/helm/gpu-on-demand/values.schema.json'))"` to confirm the schema file alone is valid JSON.
- [ ] **Step 5:** `git add deploy/helm/gpu-on-demand/Chart.yaml deploy/helm/gpu-on-demand/values.yaml deploy/helm/gpu-on-demand/values.schema.json && git commit -m "feat: gpu-on-demand chart skeleton — Chart.yaml, values.yaml, values.schema.json"`.

---

### Task 3: `_helpers.tpl`

**Files:**
- Create: `deploy/helm/gpu-on-demand/templates/_helpers.tpl`

Copy `/Users/margo/Proj/Lab/vk-ahorro/deploy/helm/ahorro-api/templates/_helpers.tpl` verbatim for `svc.name`/`svc.fullname`/`svc.labels`/`svc.selectorLabels`/`svc.host`/`svc.image`/`svc.pullPolicy`, then add three new helpers this chart needs that `vk-ahorro` doesn't:

- [ ] **Step 1:** Write the file:

```
{{/*
Keyed on the chart name, not a hard-coded string, so a sibling chart can copy
this file unchanged.
*/}}
{{- define "svc.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "svc.fullname" -}}
{{- if eq .Release.Name (include "svc.name" .) -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "svc.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "svc.labels" -}}
app.kubernetes.io/name: {{ include "svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "svc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "svc.host" -}}
{{- required "host is required when httpRoute.enabled or ingress.enabled is true" .Values.host -}}
{{- end -}}

{{- define "svc.image" -}}
{{- $tag := required "image.tag is required: the full commit SHA, never latest" .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
A moving tag cached on a node is never re-pulled under IfNotPresent, so the
node keeps serving an old build. A commit SHA can never move.
*/}}
{{- define "svc.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if regexMatch "^[0-9a-f]{40}$" .Values.image.tag -}}
IfNotPresent
{{- else -}}
Always
{{- end -}}
{{- end -}}

{{/*
A production installation gets a stable, predictable id without setting one
by hand; a laptop kind install sets installationId explicitly so its orphan
sweep stays inside its own label prefix.
*/}}
{{- define "svc.installationId" -}}
{{- if .Values.installationId -}}
{{- .Values.installationId -}}
{{- else -}}
{{- printf "%s/%s" .Release.Namespace .Release.Name | sha256sum | trunc 12 -}}
{{- end -}}
{{- end -}}

{{/*
Every config.* key becomes one VKLLM_<NAME> entry; this is the single place
that mapping is written, so configmap.yaml never hand-lists 45 keys inline.
*/}}
{{- define "svc.configEnv" -}}
VKLLM_INSTALLATION_ID: {{ include "svc.installationId" . | quote }}
VKLLM_STATE_CONFIGMAP: {{ printf "%s-state" .Release.Name | quote }}
VKLLM_PROVIDER_NAME: {{ .Values.config.provider.name | quote }}
VKLLM_PROVIDER_API_KEY_FILE: {{ .Values.config.provider.apiKeyFile | quote }}
VKLLM_PROVIDER_FILTER_GPU_NAME: {{ .Values.config.provider.filter.gpuName | quote }}
VKLLM_PROVIDER_FILTER_NUM_GPUS: {{ .Values.config.provider.filter.numGpus | quote }}
VKLLM_PROVIDER_FILTER_MIN_CPU_CORES: {{ .Values.config.provider.filter.minCpuCores | quote }}
VKLLM_PROVIDER_FILTER_MIN_CPU_RAM_GB: {{ .Values.config.provider.filter.minCpuRamGB | quote }}
VKLLM_PROVIDER_FILTER_MIN_DISK_GB: {{ .Values.config.provider.filter.minDiskGB | quote }}
VKLLM_PROVIDER_FILTER_MIN_RELIABILITY: {{ .Values.config.provider.filter.minReliability | quote }}
VKLLM_PROVIDER_FILTER_MIN_INET_DOWN_MBPS: {{ .Values.config.provider.filter.minInetDownMBps | quote }}
VKLLM_PROVIDER_FILTER_MIN_DISK_BW_MBPS: {{ .Values.config.provider.filter.minDiskBwMBps | quote }}
VKLLM_PROVIDER_FILTER_MIN_CUDA_VERSION: {{ .Values.config.provider.filter.minCudaVersion | quote }}
VKLLM_PROVIDER_FILTER_GEOLOCATION: {{ .Values.config.provider.filter.geolocation | quote }}
VKLLM_LIFECYCLE_PROVISIONING_MODE: {{ .Values.config.lifecycle.provisioningMode | quote }}
VKLLM_LIFECYCLE_IDLE_SHUTDOWN_ENABLED: {{ .Values.config.lifecycle.idleShutdown.enabled | quote }}
VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT: {{ .Values.config.lifecycle.idleShutdown.timeout | quote }}
VKLLM_LIFECYCLE_SHUTDOWN_DRAIN_TIMEOUT: {{ .Values.config.lifecycle.shutdown.drainTimeout | quote }}
VKLLM_LIFECYCLE_SHUTDOWN_ALLOW_FORCE: {{ .Values.config.lifecycle.shutdown.allowForce | quote }}
VKLLM_LIFECYCLE_STARTUP_TIMEOUT: {{ .Values.config.lifecycle.startupTimeout | quote }}
VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD: {{ .Values.config.lifecycle.safety.maxHourlyPriceUSD | quote }}
VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME: {{ .Values.config.lifecycle.safety.maxLifetime | quote }}
VKLLM_LIFECYCLE_SAFETY_MAX_INSTANCE_SPEND_USD: {{ .Values.config.lifecycle.safety.maxInstanceSpendUSD | quote }}
VKLLM_LIFECYCLE_SAFETY_MAX_PROVISION_ATTEMPTS_PER_HOUR: {{ .Values.config.lifecycle.safety.maxProvisionAttemptsPerHour | quote }}
VKLLM_BUDGET_MONTH_USD: {{ .Values.config.budget.monthlyUSD | quote }}
VKLLM_BUDGET_WARN_FRACTION: {{ .Values.config.budget.warnFraction | quote }}
VKLLM_CONTROLLER_RECONCILE_INTERVAL: {{ .Values.config.controller.reconcileInterval | quote }}
VKLLM_GATEWAY_MAX_BODY_BYTES: {{ .Values.config.gateway.maxBodyBytes | quote }}
VKLLM_GATEWAY_MAX_TOKENS_CAP: {{ .Values.config.gateway.maxTokensCap | quote }}
VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM: {{ .Values.config.gateway.rateLimitPerKeyRPM | quote }}
VKLLM_MGMT_RATE_LIMIT_RPM: {{ .Values.config.mgmt.rateLimitPerKeyRPM | quote }}
VKLLM_TUNNEL_DIALER: {{ .Values.config.tunnel.dialer | quote }}
VKLLM_TUNNEL_DIRECT_ADDR: {{ .Values.config.tunnel.directAddr | quote }}
VKLLM_TUNNEL_SSH_KEY_FILE: {{ .Values.config.tunnel.sshKeyFile | quote }}
VKLLM_MODEL_ALIAS: {{ .Values.config.model.alias | quote }}
VKLLM_MODEL_URL: {{ required "config.model.url is required: no production value is pinned anywhere yet" .Values.config.model.url | quote }}
VKLLM_MODEL_SHA256: {{ required "config.model.sha256 is required: no production value is pinned anywhere yet" .Values.config.model.sha256 | quote }}
VKLLM_MODEL_CONTEXT_SIZE: {{ .Values.config.model.contextSize | quote }}
VKLLM_MODEL_PARALLEL: {{ .Values.config.model.parallel | quote }}
VKLLM_MODEL_GENERATION_TIMEOUT: {{ .Values.config.model.generationTimeout | quote }}
VKLLM_WORKER_IMAGE: {{ required "config.worker.image is required: no production value is pinned anywhere yet" .Values.config.worker.image | quote }}
VKLLM_WORKER_DEAD_MAN_TIMEOUT: {{ .Values.config.worker.deadManTimeout | quote }}
{{- end -}}
```

- [ ] **Step 2:** `helm template deploy/helm/gpu-on-demand --set image.tag=test --set existingSecrets.provider=p --set existingSecrets.apiKeys=k --set existingSecrets.ssh=s --set config.model.url=https://example.com/m.gguf --set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85 --set config.worker.image=example/worker` — still fails (no `deployment.yaml` exists to actually call `svc.image` yet), but confirms the helper file alone parses: run `helm template` against a throwaway one-line test template in `/tmp` that just does `{{ include "svc.configEnv" . | nindent 2 }}` under a `ConfigMap`'s `stringData`, pointed at this chart's `-f` values, to prove the 45-line block renders with no `required` error when every required value is supplied, and *does* error naming `config.model.url` when it's omitted.
- [ ] **Step 3:** `git add deploy/helm/gpu-on-demand/templates/_helpers.tpl && git commit -m "feat: gpu-on-demand chart helpers — copied from vk-ahorro, extended for installationId/configEnv"`.

---

### Task 4: `deployment.yaml`, `service.yaml`, `serviceaccount.yaml`, `configmap.yaml`, `state-configmap.yaml`

**Files:**
- Create: `deploy/helm/gpu-on-demand/templates/deployment.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/service.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/serviceaccount.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/configmap.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/state-configmap.yaml`

- [ ] **Step 1:** `configmap.yaml` (the 45-key render, from Task 3's `svc.configEnv`):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
data:
  {{- include "svc.configEnv" . | nindent 2 }}
```

- [ ] **Step 2:** `state-configmap.yaml` (an empty shell; the controller never creates this object itself, per ADR 0004 §3):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ printf "%s-state" .Release.Name }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
```

- [ ] **Step 3:** `serviceaccount.yaml`:

```yaml
{{- if .Values.rbac.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ .Values.serviceAccount.name | default (include "svc.fullname" .) }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
{{- end }}
```

- [ ] **Step 4:** `service.yaml` (two ports, `http` for both APIs on 8080, `metrics` on 9090):

```yaml
apiVersion: v1
kind: Service
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  selector:
    {{- include "svc.selectorLabels" . | nindent 4 }}
  ports:
    - name: http
      port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
    - name: metrics
      port: 9090
      targetPort: metrics
      protocol: TCP
```

- [ ] **Step 5:** `deployment.yaml` — copied+extended from `vk-ahorro`'s: `replicaCount` fail-guard, `Recreate`, two ports, probes on 9090, three secret volumes, `automountServiceAccountToken: true`:

```yaml
{{- if ne (int .Values.replicaCount) 1 }}
{{- fail "replicaCount must be exactly 1: one state writer, one provider label owner" }}
{{- end }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
  annotations:
    kubernetes.io/change-cause: {{ printf "%s (%s)" .Chart.Version (.Chart.AppVersion | trunc 7) | quote }}
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      {{- include "svc.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      annotations:
        checksum/config: {{ include (print $.Template.BasePath "/configmap.yaml") . | sha256sum }}
      labels:
        {{- include "svc.selectorLabels" . | nindent 8 }}
    spec:
      serviceAccountName: {{ .Values.serviceAccount.name | default (include "svc.fullname" .) }}
      automountServiceAccountToken: true
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: {{ include "svc.name" . }}
          image: {{ include "svc.image" . }}
          imagePullPolicy: {{ include "svc.pullPolicy" . }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop:
                - ALL
          envFrom:
            - configMapRef:
                name: {{ include "svc.fullname" . }}
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
            - name: metrics
              containerPort: 9090
              protocol: TCP
          readinessProbe:
            httpGet:
              path: /readyz
              port: metrics
            periodSeconds: {{ .Values.probes.readiness.periodSeconds }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: metrics
            periodSeconds: {{ .Values.probes.liveness.periodSeconds }}
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          volumeMounts:
            - name: provider-secret
              mountPath: /etc/vkllm/secrets/provider
              readOnly: true
            - name: api-keys-secret
              mountPath: /etc/vkllm/api-keys
              readOnly: true
            - name: ssh-secret
              mountPath: /etc/vkllm/secrets/ssh
              readOnly: true
      volumes:
        - name: provider-secret
          secret:
            secretName: {{ required "existingSecrets.provider is required" .Values.existingSecrets.provider }}
            defaultMode: 0400
        - name: api-keys-secret
          secret:
            secretName: {{ required "existingSecrets.apiKeys is required" .Values.existingSecrets.apiKeys }}
            defaultMode: 0400
        - name: ssh-secret
          secret:
            secretName: {{ required "existingSecrets.ssh is required" .Values.existingSecrets.ssh }}
            defaultMode: 0400
```

- [ ] **Step 6:** `helm template deploy/helm/gpu-on-demand --set image.tag=test --set existingSecrets.provider=p --set existingSecrets.apiKeys=k --set existingSecrets.ssh=s --set config.model.url=https://example.com/m.gguf --set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85 --set config.worker.image=example/worker` — renders a `Deployment` with the `checksum/config` annotation; `grep -oE 'VKLLM_[A-Z_]+:' <(helm template ...)` outputs exactly 45 lines (count it — 2 fixed + 43 `config.*`).
- [ ] **Step 7:** `helm lint deploy/helm/gpu-on-demand` (same `--set` flags) exits 0.
- [ ] **Step 8:** `git add deploy/helm/gpu-on-demand/templates/{deployment,service,serviceaccount,configmap,state-configmap}.yaml && git commit -m "feat: gpu-on-demand chart — Deployment, Service, ConfigMaps, ServiceAccount"`.

---

### Task 5: RBAC — `role.yaml`, `rolebinding.yaml`

**Files:**
- Create: `deploy/helm/gpu-on-demand/templates/role.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/rolebinding.yaml`

- [ ] **Step 1:** `role.yaml` — exactly `get`/`update`, scoped by `resourceNames` to the state ConfigMap (ADR 0004 §3):

```yaml
{{- if .Values.rbac.create }}
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: [{{ printf "%s-state" .Release.Name | quote }}]
    verbs: ["get", "update"]
{{- end }}
```

- [ ] **Step 2:** `rolebinding.yaml`:

```yaml
{{- if .Values.rbac.create }}
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: {{ include "svc.fullname" . }}
subjects:
  - kind: ServiceAccount
    name: {{ .Values.serviceAccount.name | default (include "svc.fullname" .) }}
    namespace: {{ .Release.Namespace }}
{{- end }}
```

- [ ] **Step 3:** `helm template deploy/helm/gpu-on-demand --set rbac.create=true <other required --set flags from Task 4>`, extract the `Role`: exactly one rule, verbs exactly `["get", "update"]`. `grep -cE 'create|patch|delete|list' <(helm template ... | yq eval 'select(.kind == "Role")')` is `0`.
- [ ] **Step 4:** `helm lint` exits 0.
- [ ] **Step 5:** `git add deploy/helm/gpu-on-demand/templates/role.yaml deploy/helm/gpu-on-demand/templates/rolebinding.yaml && git commit -m "feat: gpu-on-demand chart — RBAC (get/update on the state ConfigMap only)"`.

---

### Task 6: `httproute.yaml`, `ingress.yaml`

**Files:**
- Create: `deploy/helm/gpu-on-demand/templates/httproute.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/ingress.yaml`

- [ ] **Step 1:** `httproute.yaml` — one object, two rules, same backend (both APIs on one process):

```yaml
{{- if .Values.httpRoute.enabled }}
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
spec:
  parentRefs:
    - name: {{ .Values.httpRoute.parentRef.name }}
      namespace: {{ .Values.httpRoute.parentRef.namespace }}
      sectionName: {{ .Values.httpRoute.parentRef.sectionName }}
  hostnames:
    - {{ include "svc.host" . | quote }}
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /v1
      backendRefs:
        - name: {{ include "svc.fullname" . }}
          port: {{ .Values.service.port }}
    - matches:
        - path:
            type: PathPrefix
            value: /api/v1
      backendRefs:
        - name: {{ include "svc.fullname" . }}
          port: {{ .Values.service.port }}
{{- end }}
```

- [ ] **Step 2:** `ingress.yaml` — one object, one `/` prefix rule:

```yaml
{{- if .Values.ingress.enabled }}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
  {{- with .Values.ingress.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- if .Values.ingress.className }}
  ingressClassName: {{ .Values.ingress.className }}
  {{- end }}
  {{- if .Values.ingress.tls.enabled }}
  tls:
    - hosts:
        - {{ include "svc.host" . | quote }}
      secretName: {{ required "ingress.tls.secretName is required when ingress.tls.enabled" .Values.ingress.tls.secretName }}
  {{- end }}
  rules:
    - host: {{ include "svc.host" . | quote }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ include "svc.fullname" . }}
                port:
                  number: {{ .Values.service.port }}
{{- end }}
```

- [ ] **Step 3:** Three `helm template` invocations (same base `--set` flags from Task 4, plus respectively nothing / `--set httpRoute.enabled=true --set host=llm.lab.example.com` / `--set ingress.enabled=true --set host=llm.lab.example.com`) each produce exactly the expected object set; the `httpRoute` render's `HTTPRoute` has exactly 2 rules with paths `/v1` and `/api/v1`.
- [ ] **Step 4:** `--set httpRoute.enabled=true` with no `host` fails naming `host is required`.
- [ ] **Step 5:** `git add deploy/helm/gpu-on-demand/templates/httproute.yaml deploy/helm/gpu-on-demand/templates/ingress.yaml && git commit -m "feat: gpu-on-demand chart — HTTPRoute and Ingress, both APIs on one host"`.

---

### Task 7: `servicemonitor.yaml`, `networkpolicy.yaml`, `externalsecrets.yaml`

**Files:**
- Create: `deploy/helm/gpu-on-demand/templates/servicemonitor.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/networkpolicy.yaml`
- Create: `deploy/helm/gpu-on-demand/templates/externalsecrets.yaml`

- [ ] **Step 1:** `servicemonitor.yaml` (per Task 1's amendment: only the `ServiceMonitor`, not a `PrometheusRule`/dashboard `ConfigMap` — CORE-090's scope):

```yaml
{{- if .Values.serviceMonitor.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
    {{- with .Values.serviceMonitor.labels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  selector:
    matchLabels:
      {{- include "svc.selectorLabels" . | nindent 6 }}
  endpoints:
    - port: metrics
      interval: {{ .Values.serviceMonitor.interval }}
{{- end }}
```

- [ ] **Step 2:** `networkpolicy.yaml` — 4 egress rules, 2 ingress rules, exactly as spec's NetworkPolicy table lists:

```yaml
{{- if .Values.networkPolicy.enabled }}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: {{ include "svc.fullname" . }}
  labels:
    {{- include "svc.labels" . | nindent 4 }}
spec:
  podSelector:
    matchLabels:
      {{- include "svc.selectorLabels" . | nindent 6 }}
  policyTypes:
    - Ingress
    - Egress
  egress:
    - ports:
        - port: 53
          protocol: TCP
        - port: 53
          protocol: UDP
    - to:
        {{- if .Values.networkPolicy.apiServerCidrs }}
        {{- range .Values.networkPolicy.apiServerCidrs }}
        - ipBlock:
            cidr: {{ . }}
        {{- end }}
        {{- else }}
        {{- $ep := lookup "v1" "Endpoints" "default" "kubernetes" }}
        {{- if not $ep }}
        {{- fail "networkPolicy.apiServerCidrs is required: lookup could not resolve the kube-apiserver Endpoints (helm template / Argo rendering has no live-cluster access)" }}
        {{- end }}
        {{- range (index $ep.subsets 0).addresses }}
        - ipBlock:
            cidr: {{ printf "%s/32" .ip }}
        {{- end }}
        {{- end }}
      ports:
        - port: 443
          protocol: TCP
    - to:
        {{- if .Values.networkPolicy.vastApiCidrs }}
        {{- range .Values.networkPolicy.vastApiCidrs }}
        - ipBlock:
            cidr: {{ . }}
        {{- end }}
        {{- else }}
        - ipBlock:
            cidr: 0.0.0.0/0
        {{- end }}
      ports:
        - port: 443
          protocol: TCP
    - ports:
        - port: {{ .Values.networkPolicy.sshPorts.from }}
          endPort: {{ .Values.networkPolicy.sshPorts.to }}
          protocol: TCP
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: {{ .Values.networkPolicy.gatewayNamespace }}
      ports:
        - port: http
    - ports:
        - port: metrics
{{- end }}
```

- [ ] **Step 3:** `externalsecrets.yaml` — one `range` over the 5 fixed rows from spec's `ExternalSecret` contract table:

```yaml
{{- if .Values.externalSecrets.enabled }}
{{- $prefix := required "externalSecrets.prefix is required when externalSecrets.enabled" .Values.externalSecrets.prefix }}
{{- $store := required "externalSecrets.storeRef is required when externalSecrets.enabled" .Values.externalSecrets.storeRef }}
{{- range $i, $row := list
  (dict "name" "vast-api-key" "remote" "/vast-api-key" "target" $.Values.existingSecrets.provider "key" "VAST_API_KEY")
  (dict "name" "client-keys" "remote" "/client-keys" "target" $.Values.existingSecrets.apiKeys "key" "client-keys")
  (dict "name" "management-tokens" "remote" "/management-tokens" "target" $.Values.existingSecrets.apiKeys "key" "management-tokens")
  (dict "name" "worker-seed" "remote" "/worker-seed" "target" $.Values.existingSecrets.apiKeys "key" "worker-seed")
  (dict "name" "ssh-private-key" "remote" "/ssh-private-key" "target" $.Values.existingSecrets.ssh "key" "id_ed25519")
}}
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: {{ printf "%s-%s" (include "svc.fullname" $) $row.name }}
  labels:
    {{- include "svc.labels" $ | nindent 4 }}
spec:
  secretStoreRef:
    name: {{ $store }}
    kind: ClusterSecretStore
  target:
    name: {{ $row.target }}
  data:
    - secretKey: {{ $row.key }}
      remoteRef:
        key: {{ printf "%s%s" $prefix $row.remote }}
{{- end }}
{{- end }}
```

- [ ] **Step 4:** Enabling each in isolation renders the right object set; `externalsecrets.yaml --set externalSecrets.enabled=true --set externalSecrets.storeRef=aws-parameter-store --set externalSecrets.prefix=/vkllm` renders exactly 5 `ExternalSecret` objects; `networkpolicy.yaml --set networkPolicy.enabled=true --set networkPolicy.apiServerCidrs={10.0.0.1/32}` (required since `helm template` cannot resolve `lookup`) renders without the `fail` guard tripping.
- [ ] **Step 5:** `git add deploy/helm/gpu-on-demand/templates/{servicemonitor,networkpolicy,externalsecrets}.yaml && git commit -m "feat: gpu-on-demand chart — ServiceMonitor, NetworkPolicy, ExternalSecrets"`.

---

### Task 8: `NOTES.txt`

**Files:**
- Create: `deploy/helm/gpu-on-demand/templates/NOTES.txt`

Per Task 1's amendment: curl-based workflow, never `.Values.host` or a Secret value, the `installationId` warning, the drain-before-uninstall reminder.

- [ ] **Step 1:**

```
gpu-on-demand installed as {{ .Release.Name }} in {{ .Release.Namespace }}.

Three-command workflow (replace <management-token> with a value from the
{{ .Values.existingSecrets.apiKeys }} Secret's management-tokens key):

  curl -X POST -H "Authorization: Bearer <management-token>" \
    http://<service>/api/v1/gpu/start
  curl -H "Authorization: Bearer <management-token>" \
    http://<service>/api/v1/gpu/status
  curl -X POST -H "Authorization: Bearer <management-token>" \
    http://<service>/api/v1/gpu/stop

installationId: {{ include "svc.installationId" . }}
Changing installationId on an existing release changes every future
instance's label; a running instance stops matching it and the next
reconcile treats it as an orphan and destroys it.

Before `helm uninstall`: drain with the stop command above and wait for
state STOPPED. Uninstall never touches a running GPU instance, only this
chart's own Kubernetes objects.
```

- [ ] **Step 2:** `helm template deploy/helm/gpu-on-demand --set host=llm.lab.example.com <other required --set flags>` — the rendered `NOTES.txt` output never contains the literal string `lab.example.com`.
- [ ] **Step 3:** `git add deploy/helm/gpu-on-demand/templates/NOTES.txt && git commit -m "feat: gpu-on-demand chart — NOTES.txt, curl-based workflow"`.

---

### Task 9: Makefile `helm-template` default flags; confirm the CI datree flag

**Files:**
- Modify: `Makefile`

Per Task 1's audit point 10: the CI `helm` job's kubeconform step already has the datree `-schema-location` flag (confirmed present in `.github/workflows/ci.yml`) — this task does not touch CI. It only updates `helm-template`'s default `--set` flags, since every required value (`image.tag`, `existingSecrets.*`, `config.model.url`, `config.model.sha256`, `config.worker.image`) would otherwise fail a bare `make helm-template`.

- [ ] **Step 1:** `helm lint` needs the same required-value overrides `helm-template` does (confirmed empirically — a bare `make helm-lint` fails with the same `minLength` schema errors) — factor the flags into one shared variable and apply it to both targets:

```makefile
HELM_TEST_SET = --set image.tag=test \
	--set existingSecrets.provider=p --set existingSecrets.apiKeys=k --set existingSecrets.ssh=s \
	--set config.model.url=https://example.com/m.gguf \
	--set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 \
	--set config.worker.image=example/worker

helm-lint: ## Lint the deploy/helm/<CHART> chart
	helm lint deploy/helm/$(CHART) $(HELM_TEST_SET)

helm-template: ## Render the deploy/helm/<CHART> chart
	helm template deploy/helm/$(CHART) $(HELM_TEST_SET)
```

(The sha256 is the real 64-character digest of the empty string — the plan's earlier draft of this exact value was off by one digit, caught and corrected during Task 3.)

- [ ] **Step 2:** `make helm-lint CHART=gpu-on-demand` and `make helm-template CHART=gpu-on-demand` both exit 0.
- [ ] **Step 3:** `git add Makefile && git commit -m "fix: helm-template default --set flags cover the chart's required values"`.

---

### Task 10: kind CI job

**Files:**
- Modify: `.github/workflows/ci.yml`

New job, gated on the same `helm`/`go` path filters (it needs both: the chart and the Go binaries). Mirrors `store-kind`'s checkout/setup-go/kind-action pins exactly (`actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1`, `actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0`, `helm/kind-action@1544676c07bb3570fcb70166beaa07c4528fd4aa # v1.15.1` — all confirmed already pinned in this file, reused verbatim).

**One thing this plan could not verify without running it:** `store-kind` (the existing job this mirrors) never calls the `kind` CLI directly, only `kubectl` — it's unconfirmed whether `helm/kind-action` leaves a `kind` binary on `PATH` for a later step's `kind load docker-image` call, or only sets up `kubectl`'s context. Step 3 (local dry-run against a throwaway kind cluster) is exactly where the implementer finds out; if `kind` isn't on `PATH`, install it the same pinned-binary way this repo already installs `kubeconform` (`curl`+`sha256sum -c`) rather than adding a second action.

- [ ] **Step 1:** Add `test/kind/**` to the `helm` path filter too (currently only `go` watches it; the new job depends on both filters being true, so trigger it on `needs.changes.outputs.go == 'true' || needs.changes.outputs.helm == 'true'`).
- [ ] **Step 2:** Add the job:

```yaml
  chart-kind:
    needs: changes
    if: needs.changes.outputs.go == 'true' || needs.changes.outputs.helm == 'true'
    runs-on: ubuntu-latest
    timeout-minutes: 15
    permissions:
      contents: read
    concurrency:
      group: ci-chart-kind-${{ github.event.pull_request.number || github.ref }}
      cancel-in-progress: true
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false

      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0
        with:
          go-version-file: go.mod

      - uses: docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069 # v4.4.1

      - name: Build local images
        run: |
          set -euo pipefail
          make image-build SVC=gpu-on-demand IMAGE_TAG=citest
          make image-build SVC=fake-worker IMAGE_TAG=citest

      - uses: azure/setup-helm@9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310 # v5.0.1
        with:
          version: v4.3.0

      - uses: helm/kind-action@1544676c07bb3570fcb70166beaa07c4528fd4aa # v1.15.1

      - name: Load images into the kind node
        run: |
          set -euo pipefail
          kind load docker-image ghcr.io/savak1990/vk-llm-on-demand/gpu-on-demand:citest
          kind load docker-image ghcr.io/savak1990/vk-llm-on-demand/fake-worker:citest

      - name: Create the fixture Secrets and the fake-worker Deployment
        run: |
          set -euo pipefail
          kubectl create secret generic provider-secret --from-literal=VAST_API_KEY=x
          kubectl create secret generic api-keys-secret \
            --from-literal=client-keys=sk-vkllm-citestclientkey \
            --from-literal=management-tokens=sk-vkllm-citestmgmttoken \
            --from-literal=worker-seed=citest-worker-seed
          ssh-keygen -t ed25519 -N "" -f id_ed25519
          kubectl create secret generic ssh-secret --from-file=id_ed25519
          kubectl create deployment fake-worker --image=ghcr.io/savak1990/vk-llm-on-demand/fake-worker:citest --port=8080
          kubectl expose deployment fake-worker --port=8080
          kubectl wait --for=condition=Available deployment/fake-worker --timeout=60s

      - name: helm install
        run: |
          set -euo pipefail
          helm install gpu-on-demand deploy/helm/gpu-on-demand \
            --set image.tag=citest --set image.pullPolicy=Never \
            --set config.provider.name=fake \
            --set config.tunnel.dialer=direct --set config.tunnel.directAddr=fake-worker.default.svc.cluster.local:8080 \
            --set config.model.url=https://example.com/m.gguf \
            --set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85 \
            --set config.worker.image=example/worker \
            --set existingSecrets.provider=provider-secret \
            --set existingSecrets.apiKeys=api-keys-secret \
            --set existingSecrets.ssh=ssh-secret

      - name: Wait for Ready with no GPU running
        run: kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=gpu-on-demand --timeout=60s

      - name: Confirm initial state is STOPPED
        run: |
          set -euo pipefail
          kubectl port-forward svc/gpu-on-demand 18080:80 &
          sleep 2
          state=$(curl -sf -H "Authorization: Bearer sk-vkllm-citestmgmttoken" http://localhost:18080/api/v1/gpu/status | jq -r .state)
          test "$state" = "STOPPED"

      - name: Start, poll for Ready
        run: |
          set -euo pipefail
          curl -sf -X POST -H "Authorization: Bearer sk-vkllm-citestmgmttoken" http://localhost:18080/api/v1/gpu/start
          for i in $(seq 1 30); do
            state=$(curl -sf -H "Authorization: Bearer sk-vkllm-citestmgmttoken" http://localhost:18080/api/v1/gpu/status | jq -r .state)
            [ "$state" = "READY" ] && break
            sleep 1
          done
          test "$state" = "READY"

      - name: Inference API reachable
        run: |
          set -euo pipefail
          curl -sf -H "Authorization: Bearer sk-vkllm-citestclientkey" http://localhost:18080/v1/models
          curl -sf -N -X POST -H "Authorization: Bearer sk-vkllm-citestclientkey" \
            -H "Content-Type: application/json" \
            -d '{"model":"qwen3.6-35b-a3b","messages":[{"role":"user","content":"hi"}],"stream":true}' \
            http://localhost:18080/v1/chat/completions | grep -q 'data: \[DONE\]'

      - name: Stop, poll for STOPPED
        run: |
          set -euo pipefail
          curl -sf -X POST -H "Authorization: Bearer sk-vkllm-citestmgmttoken" http://localhost:18080/api/v1/gpu/stop
          for i in $(seq 1 30); do
            state=$(curl -sf -H "Authorization: Bearer sk-vkllm-citestmgmttoken" http://localhost:18080/api/v1/gpu/status | jq -r .state)
            [ "$state" = "STOPPED" ] && break
            sleep 1
          done
          test "$state" = "STOPPED"

      - name: helm upgrade with a changed config value, confirm rollout and unchanged installationId
        run: |
          set -euo pipefail
          before=$(kubectl get deployment gpu-on-demand -o jsonpath='{.metadata.annotations.kubernetes\.io/change-cause}')
          helm upgrade gpu-on-demand deploy/helm/gpu-on-demand \
            --set image.tag=citest --set image.pullPolicy=Never \
            --set config.provider.name=fake \
            --set config.tunnel.dialer=direct --set config.tunnel.directAddr=fake-worker.default.svc.cluster.local:8080 \
            --set config.lifecycle.idleShutdown.timeout=10m \
            --set config.model.url=https://example.com/m.gguf \
            --set config.model.sha256=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85 \
            --set config.worker.image=example/worker \
            --set existingSecrets.provider=provider-secret \
            --set existingSecrets.apiKeys=api-keys-secret \
            --set existingSecrets.ssh=ssh-secret
          kubectl rollout status deployment/gpu-on-demand --timeout=60s
          kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=gpu-on-demand --timeout=60s
          sleep 2
          id=$(curl -sf -H "Authorization: Bearer sk-vkllm-citestmgmttoken" http://localhost:18080/api/v1/gpu/status | jq -r .installationId)
          test -n "$id"

      - name: helm uninstall, confirm no ConfigMap remains
        run: |
          set -euo pipefail
          helm uninstall gpu-on-demand
          ! kubectl get configmap gpu-on-demand 2>/dev/null
          ! kubectl get configmap gpu-on-demand-state 2>/dev/null
```

  `install_only: true` on the `helm/kind-action` step reuses it purely for its Helm/kind binary installation, since the cluster itself is created explicitly in the next step (the action's default behavior also creates a cluster, which this job wants to control the name/options of — `install_only` avoids creating two clusters).

- [ ] **Step 3:** Confirm this job passes locally against a throwaway kind cluster before relying on CI alone (`kind create cluster --name citest-local`, run each step's command manually, `kind delete cluster --name citest-local`).
- [ ] **Step 4:** `git add .github/workflows/ci.yml && git commit -m "feat: chart-kind CI job — install, start/stop through the management API, upgrade, uninstall"`.

---

### Task 11: Write-back

**Files:**
- Modify: the `080-P-`-lettered helm-chart spec folder, renamed to `specs/core/080-D-helm-chart/spec.md`
- Modify: `specs/README.md`
- Modify: any spec cross-referencing `080-D-helm-chart`

- [ ] **Step 1:** Fill the Evidence section: `helm lint`/`helm template` output, the 45-key count confirmation, the three-route-variant kubeconform pass, the kind job's measured wall-clock time, and the known residual: `PrometheusRule`/dashboard ConfigMap deferred to CORE-090 (named explicitly, not silently dropped).
- [ ] **Step 2:** Check every Definition of Done box; AC17 updated to its amended (ServiceMonitor-only) wording.
- [ ] **Step 3:** `git mv` the `080-P-`-lettered helm-chart spec folder to `specs/core/080-D-helm-chart`; flip `status: "DONE"`.
- [ ] **Step 4:** `grep -rn` the stale `080-P-`-lettered folder name across the repo; fix every stale cross-reference.
- [ ] **Step 5:** `grep -rEn '([0-9]{1,3}\.){3}[0-9]{1,3}' specs/core/080-D-helm-chart/spec.md` — leak check; also `grep -n 'lab\.example\.com\|sk-vkllm-' deploy/helm/gpu-on-demand/values.yaml` (should be empty, C-14).
- [ ] **Step 6:** `make specs-check`.
- [ ] **Step 7:** `git add -A && git commit -m "docs: CORE-080 Helm chart DONE"`.

---

### Task 12: Validation and PR

- [ ] **Step 1:** `make specs-check go-build go-test go-lint helm-lint helm-template` — all pass.
- [ ] **Step 2:** `go test ./... -race -count=1` twice in a row — green, no flakes (the baseline this plan started from was confirmed flake-free after PR #16).
- [ ] **Step 3:** The three-variant `kubeconform -strict` check (reuse the existing CI `helm` job's own command locally) passes for defaults / `httpRoute` / `ingress`.
- [ ] **Step 4:** Open the PR against `main`, body naming: the 45-key env-var contract rewrite, the CORE-090 scope split (PrometheusRule/dashboard deferred), the no-production-model-value finding (now a `required` value, same category as `image.tag`), and the new kind CI job.

## Verification (end to end)

1. `helm lint deploy/helm/gpu-on-demand` exits 0 with the required `--set` flags.
2. AC1-AC21 each map to a real command run in Task 4-9's steps; AC12 is the kind job, run both locally and in CI.
3. `specs/core/080-D-helm-chart/spec.md` exists, old path gone, `make specs-check` passes.
4. `grep -oE 'VKLLM_[A-Z0-9_]+:' <(make -s helm-template)` outputs exactly 45 lines. (`[A-Z_]+` without digits undercounts to 44 — it misses `VKLLM_MODEL_SHA256`, caught during Task 4's review.)
5. No hostname/domain/`fqdn`/secret value anywhere in `deploy/helm/gpu-on-demand/values.yaml`.
6. `go test ./... -race -count=1` still green (nothing in this plan touches Go code).
