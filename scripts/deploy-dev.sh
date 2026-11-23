#!/usr/bin/env bash
# Usage: deploy-dev.sh <version> <sha>
# Exit 2: the cluster is not reachable (a normal lab state); any other non-zero is a real failure.
set -euo pipefail

REGION=eu-west-1
NAMESPACE=llm-dev
CHART=oci://ghcr.io/savak1990/vk-llm-on-demand/charts/gpu-on-demand
: "${PROJECT_NAME:?}"

if [ $# -ne 2 ]; then
  echo "usage: deploy-dev.sh <version> <sha>" >&2
  exit 1
fi
VERSION=$1
SHA=$2
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"

export KUBECONFIG="$TMP/llm-deploy.kubeconfig"
status=0
"$REPO_ROOT/scripts/kubeconfig.sh" "$KUBECONFIG" || status=$?
[ "$status" -eq 0 ] || exit "$status"

ssm() {
  local value
  if ! value="$(aws ssm get-parameter --region "$REGION" --with-decryption \
    --name "/$PROJECT_NAME/$1" --query Parameter.Value --output text)"; then
    echo "deploy-dev: could not read /$PROJECT_NAME/$1" >&2
    exit 1
  fi
  printf '%s' "${value%%$'\n'*}"
}

fqdn="$(ssm cluster/llm-deploy/fqdn)"
client_key="$(ssm persistent/llm/api_token)"
mgmt_token="$(ssm persistent/llm/management_tokens)"
if [ -n "${GITHUB_ACTIONS:-}" ]; then
  echo "::add-mask::$fqdn"
  echo "::add-mask::$client_key"
  echo "::add-mask::$mgmt_token"
fi

values="$TMP/llm-dev-values.yaml"
"$REPO_ROOT/scripts/dev-values.sh" "$VERSION" "$SHA" "$fqdn" > "$values"

echo "deploy-dev: upgrading $NAMESPACE to $VERSION"
helm upgrade --install gpu-on-demand "$CHART" \
  --version "$VERSION" --namespace "$NAMESPACE" \
  --rollback-on-failure --timeout 5m -f "$values"

ENDPOINT="https://llm-dev.$fqdn" VK_LLM_TOKEN="$mgmt_token" VK_LLM_CLIENT_KEY="$client_key" \
  "$REPO_ROOT/scripts/dev-smoke.sh"

echo "deploy-dev: $NAMESPACE is at $VERSION"
