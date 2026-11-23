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

helm lint chat "${SET[@]}"
helm template vk-llm-chat chat --namespace chat "${SET[@]}" > "$tmp/chat.yaml"
"${KC[@]}" "$tmp/chat.yaml"
if helm template vk-llm-chat chat --namespace chat >/dev/null 2>&1; then
  echo "gitops-check: a chat render without fqdn must fail" >&2
  exit 1
fi
alias="$(yq '.model.alias' gitops/values.yaml)"
for v in DEFAULT_MODELS TASK_MODEL_EXTERNAL; do
  [ "$(yq "select(.kind == \"Deployment\" and .metadata.name == \"open-webui\") | .spec.template.spec.containers[0].env[] | select(.name == \"$v\") | .value" "$tmp/chat.yaml")" = "$alias" ] || {
    echo "gitops-check: chat must render $v=$alias (gitops/values.yaml model.alias)" >&2
    exit 1
  }
done
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

tag="$(yq '.tag' gitops/values.yaml)"
./scripts/dev-values.sh "$version" "$tag" lab.example.com > "$tmp/dev-values.yaml"
[ "$(yq '.host' "$tmp/dev-values.yaml")" = "llm-dev.lab.example.com" ] || {
  echo "gitops-check: dev host must be llm-dev.<fqdn>" >&2
  exit 1
}
[ "$(yq '.dashboard.enabled' "$tmp/dev-values.yaml")" = "false" ] &&
  [ "$(yq '.prometheusRule.enabled' "$tmp/dev-values.yaml")" = "false" ] || {
  echo "gitops-check: dev values must disable prometheusRule and dashboard" >&2
  exit 1
}
strip='del(.host, .prometheusRule, .dashboard)'
diff <(yq -P "$strip" "$tmp/values.yaml") <(yq -P "$strip" "$tmp/dev-values.yaml") || {
  echo "gitops-check: dev values may differ from production only in host, prometheusRule and dashboard" >&2
  exit 1
}
helm template gpu-on-demand oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand \
  --version "$version" --namespace llm-dev -f "$tmp/dev-values.yaml" > "$tmp/dev.yaml"
kinds="$(yq -N '.kind' "$tmp/dev.yaml" | sort -u | tr '\n' ' ')"
[ "$kinds" = "ConfigMap Deployment HTTPRoute Role RoleBinding Service ServiceAccount ServiceMonitor " ] || {
  echo "gitops-check: dev renders kinds '$kinds'; update the platform's llm-deploy Role before adding one" >&2
  exit 1
}

echo "gitops-check: ok (chart $version)"
