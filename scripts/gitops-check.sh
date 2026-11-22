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

appns="$(yq 'select(.kind == "Application") | .metadata.namespace' "$tmp/outer.yaml")"
[ "$appns" = "llm" ] || {
  echo "gitops-check: child Application must live in namespace llm, not '$appns' (an Application in argocd may name any project)" >&2
  exit 1
}

echo "gitops-check: ok (chart $version)"
