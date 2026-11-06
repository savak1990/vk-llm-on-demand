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
VKLLM_PROVIDER_FILTER_NUM_GPUS: {{ .Values.config.provider.filter.numGpus | int64 | quote }}
VKLLM_PROVIDER_FILTER_MIN_CPU_CORES: {{ .Values.config.provider.filter.minCpuCores | int64 | quote }}
VKLLM_PROVIDER_FILTER_MIN_CPU_RAM_GB: {{ .Values.config.provider.filter.minCpuRamGB | int64 | quote }}
VKLLM_PROVIDER_FILTER_MIN_DISK_GB: {{ .Values.config.provider.filter.minDiskGB | int64 | quote }}
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
VKLLM_LIFECYCLE_SAFETY_MAX_PROVISION_ATTEMPTS_PER_HOUR: {{ .Values.config.lifecycle.safety.maxProvisionAttemptsPerHour | int64 | quote }}
VKLLM_BUDGET_MONTH_USD: {{ .Values.config.budget.monthlyUSD | quote }}
VKLLM_BUDGET_WARN_FRACTION: {{ .Values.config.budget.warnFraction | quote }}
VKLLM_CONTROLLER_RECONCILE_INTERVAL: {{ .Values.config.controller.reconcileInterval | quote }}
VKLLM_GATEWAY_MAX_BODY_BYTES: {{ .Values.config.gateway.maxBodyBytes | int64 | quote }}
VKLLM_GATEWAY_MAX_TOKENS_CAP: {{ .Values.config.gateway.maxTokensCap | int64 | quote }}
VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM: {{ .Values.config.gateway.rateLimitPerKeyRPM | int64 | quote }}
VKLLM_MGMT_RATE_LIMIT_RPM: {{ .Values.config.mgmt.rateLimitPerKeyRPM | int64 | quote }}
VKLLM_TUNNEL_DIALER: {{ .Values.config.tunnel.dialer | quote }}
VKLLM_TUNNEL_DIRECT_ADDR: {{ .Values.config.tunnel.directAddr | quote }}
VKLLM_TUNNEL_SSH_KEY_FILE: {{ .Values.config.tunnel.sshKeyFile | quote }}
VKLLM_AUTH_CLIENT_KEYS_FILE: {{ .Values.config.auth.clientKeysFile | quote }}
VKLLM_AUTH_MANAGEMENT_TOKENS_FILE: {{ .Values.config.auth.managementTokensFile | quote }}
VKLLM_AUTH_WORKER_SEED_FILE: {{ .Values.config.auth.workerSeedFile | quote }}
VKLLM_MODEL_ALIAS: {{ .Values.config.model.alias | quote }}
VKLLM_MODEL_URL: {{ required "config.model.url is required: no production value is pinned anywhere yet" .Values.config.model.url | quote }}
VKLLM_MODEL_SHA256: {{ required "config.model.sha256 is required: no production value is pinned anywhere yet" .Values.config.model.sha256 | quote }}
VKLLM_MODEL_CONTEXT_SIZE: {{ .Values.config.model.contextSize | int64 | quote }}
VKLLM_MODEL_PARALLEL: {{ .Values.config.model.parallel | int64 | quote }}
VKLLM_MODEL_GENERATION_TIMEOUT: {{ .Values.config.model.generationTimeout | quote }}
VKLLM_WORKER_IMAGE: {{ required "config.worker.image is required: no production value is pinned anywhere yet" .Values.config.worker.image | quote }}
VKLLM_WORKER_DEAD_MAN_TIMEOUT: {{ .Values.config.worker.deadManTimeout | quote }}
{{- end -}}
