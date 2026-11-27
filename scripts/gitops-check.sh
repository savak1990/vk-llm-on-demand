#!/usr/bin/env bash
# Lint, render and validate gitops/, then render the pinned inner chart with
# the exact values the Application passes, so a chart/values mismatch fails
# here instead of on the platform's first sync.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

SET=(--set fqdn=lab.example.com)
KC=(kubeconform -summary -strict
    -schema-location default
    -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json')

helm lint gitops "${SET[@]}"
helm template gpu-on-demand gitops "${SET[@]}" > "$tmp/outer.yaml"
"${KC[@]}" "$tmp/outer.yaml"

if helm template gpu-on-demand gitops >/dev/null 2>&1; then
  echo "gitops-check: a render without fqdn must fail" >&2
  exit 1
fi
grep -q '^fqdn: ""$' gitops/values.yaml || {
  echo "gitops-check: gitops/values.yaml must keep fqdn empty" >&2
  exit 1
}
if grep -q '^ *value: ""$' "$tmp/outer.yaml"; then
  echo "gitops-check: an empty helm parameter value wedges Argo (omit it)" >&2
  exit 1
fi

version="$(yq '.spec.source.targetRevision' "$tmp/outer.yaml")"
yq '.spec.source.helm.valuesObject' "$tmp/outer.yaml" > "$tmp/values.yaml"
helm template gpu-on-demand oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand \
  --version "$version" --namespace llm -f "$tmp/values.yaml" > "$tmp/inner.yaml"
"${KC[@]}" "$tmp/inner.yaml"

route="$(yq 'select(.kind == "HTTPRoute") | .metadata.name' "$tmp/inner.yaml")"
[ "$route" = "gpu-on-demand" ] || {
  echo "gitops-check: HTTPRoute is '$route', the platform traffic policy targets 'gpu-on-demand'" >&2
  exit 1
}

cm='select(.kind == "ConfigMap" and .metadata.name == "gpu-on-demand") | .data'
[ "$(yq "$cm | .VKLLM_GATEWAY_MAX_TOKENS_CAP" "$tmp/inner.yaml")" = "16384" ] &&
  [ "$(yq "$cm | .VKLLM_GATEWAY_RATE_LIMIT_PER_KEY_RPM" "$tmp/inner.yaml")" = "100" ] || {
  echo "gitops-check: gateway must render maxTokensCap 16384 and rateLimitPerKeyRPM 100 for Open WebUI" >&2
  exit 1
}

appns="$(yq 'select(.kind == "Application") | .metadata.namespace' "$tmp/outer.yaml")"
[ "$appns" = "llm" ] || {
  echo "gitops-check: child Application must live in namespace llm, not '$appns' (an Application in argocd may name any project)" >&2
  exit 1
}

helm template gpu-on-demand gitops "${SET[@]}" -f gitops/values.yaml -f gitops/values-dev.yaml > "$tmp/outer-dev.yaml"
"${KC[@]}" "$tmp/outer-dev.yaml"
app='select(.kind == "Application")'
[ "$(yq "$app | .metadata.namespace" "$tmp/outer-dev.yaml")" = "llm-dev" ] &&
  [ "$(yq "$app | .spec.destination.namespace" "$tmp/outer-dev.yaml")" = "llm-dev" ] &&
  [ "$(yq "$app | .spec.project" "$tmp/outer-dev.yaml")" = "vk-llm-on-demand-dev" ] &&
  [ "$(yq "$app | .spec.source.targetRevision" "$tmp/outer-dev.yaml")" = ">=0.0.0-0" ] || {
  echo "gitops-check: dev Application must live in llm-dev, project vk-llm-on-demand-dev, targetRevision >=0.0.0-0" >&2
  exit 1
}
[ "$(yq "$app | .spec.project" "$tmp/outer.yaml")" = "vk-llm-on-demand" ] || {
  echo "gitops-check: production Application must stay in project vk-llm-on-demand" >&2
  exit 1
}
yq "$app | .spec.source.helm.valuesObject" "$tmp/outer-dev.yaml" > "$tmp/values-dev.yaml"
alias="$(yq '.model.alias' gitops/values.yaml)"
[ "$(yq '.config.model.alias' "$tmp/values-dev.yaml")" = "dev-$alias" ] &&
  [ "$(yq '.host' "$tmp/values-dev.yaml")" = "llm-dev.lab.example.com" ] &&
  [ "$(yq '.smokeTest.enabled' "$tmp/values-dev.yaml")" = "true" ] &&
  [ "$(yq '.prometheusRule.enabled' "$tmp/values-dev.yaml")" = "false" ] &&
  [ "$(yq '.dashboard.enabled' "$tmp/values-dev.yaml")" = "false" ] &&
  [ "$(yq '.image.tag // ""' "$tmp/values-dev.yaml")" = "" ] || {
  echo "gitops-check: dev values must set alias dev-$alias, host llm-dev.<fqdn>, smoke test on, alerts and dashboard off, no image tag" >&2
  exit 1
}
strip='del(.host, .image, .smokeTest, .prometheusRule, .dashboard, .config.model.alias)'
diff <(yq -P "$strip" "$tmp/values.yaml") <(yq -P "$strip" "$tmp/values-dev.yaml") || {
  echo "gitops-check: dev may differ from production only in host, image, alias, smoke test, alerts and dashboard" >&2
  exit 1
}
helm template gpu-on-demand deploy/helm/gpu-on-demand --namespace llm-dev -f "$tmp/values-dev.yaml" > "$tmp/inner-dev.yaml"
"${KC[@]}" "$tmp/inner-dev.yaml"
kinds="$(yq -N '.kind' "$tmp/inner-dev.yaml" | sort -u | tr '\n' ' ')"
[ "$kinds" = "ConfigMap Deployment HTTPRoute Job Role RoleBinding Service ServiceAccount ServiceMonitor " ] || {
  echo "gitops-check: dev renders kinds '$kinds'" >&2
  exit 1
}

helm lint chat "${SET[@]}"
helm template vk-llm-chat chat --namespace chat "${SET[@]}" > "$tmp/chat.yaml"
"${KC[@]}" "$tmp/chat.yaml"
if helm template vk-llm-chat chat --namespace chat >/dev/null 2>&1; then
  echo "gitops-check: a chat render without fqdn must fail" >&2
  exit 1
fi
env_of() {
  yq "select(.kind == \"Deployment\" and .metadata.name == \"open-webui\") | .spec.template.spec.containers[0].env[] | select(.name == \"$1\") | .value" "$tmp/chat.yaml"
}
alias="$(yq '.model.alias' gitops/values.yaml)"
for v in DEFAULT_MODELS TASK_MODEL_EXTERNAL; do
  [ "$(env_of "$v")" = "$alias" ] || {
    echo "gitops-check: chat must render $v=$alias (gitops/values.yaml model.alias)" >&2
    exit 1
  }
done
[ "$(env_of OPENAI_API_BASE_URLS)" = "http://gpu-on-demand.llm.svc/v1;http://gpu-on-demand.llm-dev.svc/v1" ] || {
  echo "gitops-check: Open WebUI must list production then dev (the platform's key list has the same order)" >&2
  exit 1
}
[ "$(yq 'select(.kind == "Deployment" and .metadata.name == "open-webui") | .spec.template.spec.containers[0].env[] | select(.name == "OPENAI_API_KEYS") | .valueFrom.secretKeyRef.key' "$tmp/chat.yaml")" = "OPENAI_API_KEYS" ] || {
  echo "gitops-check: OPENAI_API_KEYS must come from open-webui-env key OPENAI_API_KEYS" >&2
  exit 1
}
for r in open-webui open-webui-auth; do
  [ "$(yq "select(.kind == \"HTTPRoute\" and .metadata.name == \"$r\") | .spec.rules[0].backendRefs[0].name" "$tmp/chat.yaml")" = "open-webui" ] || {
    echo "gitops-check: HTTPRoute $r must target Service open-webui (the platform traffic policies name these routes)" >&2
    exit 1
  }
done
[ "$(yq 'select(.kind == "Service" and .metadata.name == "open-webui") | .spec.ports[0].port' "$tmp/chat.yaml")" = "80" ] || {
  echo "gitops-check: chat must render Service open-webui on port 80" >&2
  exit 1
}
if yq -e 'select(.kind == "PersistentVolumeClaim" or .kind == "StatefulSet")' "$tmp/chat.yaml" >/dev/null 2>&1; then
  echo "gitops-check: chat must not render a PVC or StatefulSet (no Redis, no volume)" >&2
  exit 1
fi
if yq 'select(.kind == "Deployment" and .metadata.name == "open-webui") | .spec.template.spec.initContainers[].command[]' "$tmp/chat.yaml" | grep -qF 'data/*'; then
  echo "gitops-check: copy-app-data must not glob /app/backend/data/*: the slim image ships it empty and the init container fails" >&2
  exit 1
fi

for pair in ENABLE_OAUTH_SIGNUP=true DEFAULT_USER_ROLE=pending OAUTH_MERGE_ACCOUNTS_BY_EMAIL=true ENABLE_LOGIN_FORM=true ENABLE_SIGNUP=false BYPASS_MODEL_ACCESS_CONTROL=true; do
  [ "$(env_of "${pair%%=*}")" = "${pair#*=}" ] || {
    echo "gitops-check: chat must render ${pair} (pending sign-ups, a kept admin password login, every model visible to activated users)" >&2
    exit 1
  }
done
[ -z "$(env_of ENABLE_PASSWORD_AUTH)" ] || [ "$(env_of ENABLE_PASSWORD_AUTH)" = "true" ] || {
  echo "gitops-check: ENABLE_PASSWORD_AUTH must stay true: it is the admin's break-glass login" >&2
  exit 1
}
[ "$(yq 'select(.kind == "ConfigMap" and .metadata.name == "open-webui-config") | .data.GOOGLE_REDIRECT_URI' "$tmp/chat.yaml")" = "https://chat.lab.example.com/oauth/google/login/callback" ] || {
  echo "gitops-check: open-webui-config must render GOOGLE_REDIRECT_URI https://chat.<fqdn>/oauth/google/login/callback" >&2
  exit 1
}

echo "gitops-check: ok (chart $version)"
