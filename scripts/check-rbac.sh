#!/usr/bin/env bash
# Asserts the rendered Role never widens past get/update on the state ConfigMap.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v yq >/dev/null || { echo "check-rbac: yq is not installed." >&2; exit 1; }

# yq ships preinstalled on the GitHub-hosted ubuntu-latest runner.
rendered=$(make -s helm-template)

role=$(echo "$rendered" | yq e 'select(.kind == "Role")' -)
rule_count=$(echo "$role" | yq e '.rules | length' -)
verbs=$(echo "$role" | yq e '.rules[0].verbs | sort | join(",")' -)
resources=$(echo "$role" | yq e '.rules[0].resources[0]' -)
resource_names=$(echo "$role" | yq e '.rules[0].resourceNames | length' -)

if [[ "$rule_count" != "1" ]]; then
  echo "check-rbac: Role has $rule_count rules, want 1" >&2
  exit 1
fi
if [[ "$verbs" != "get,update" ]]; then
  echo "check-rbac: Role verbs = [$verbs], want [get,update]" >&2
  exit 1
fi
if [[ "$resources" != "configmaps" ]]; then
  echo "check-rbac: Role resource = [$resources], want [configmaps]" >&2
  exit 1
fi
if [[ "$resource_names" != "1" ]]; then
  echo "check-rbac: Role rule has $resource_names resourceNames, want 1 (the state ConfigMap only)" >&2
  exit 1
fi

echo "check-rbac: OK (verbs=$verbs, resource=$resources, scoped to $resource_names resourceName)"
