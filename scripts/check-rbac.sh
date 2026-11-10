#!/usr/bin/env bash
# Asserts the rendered Role matches the exact expected rule shape and that
# no ClusterRole/ClusterRoleBinding is rendered anywhere in the chart.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v yq >/dev/null || { echo "check-rbac: yq is not installed." >&2; exit 1; }
command -v jq >/dev/null || { echo "check-rbac: jq is not installed." >&2; exit 1; }

if [[ -p /dev/stdin ]]; then
  rendered=$(cat)
else
  rendered=$(make -s helm-template)
fi

expected='[{"apiGroups":[""],"resources":["configmaps"],"resourceNames":["release-name-state"],"verbs":["get","update"]}]'

role_rules=$(echo "$rendered" | yq e 'select(.kind == "Role") | .rules' -o=json - | jq -c 'map(.verbs |= sort)')

if [[ "$role_rules" != "$expected" ]]; then
  echo "check-rbac: Role rules = $role_rules, want $expected" >&2
  exit 1
fi

widened_kinds=$(echo "$rendered" | yq e 'select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding") | .kind' -)
if [[ -n "$widened_kinds" ]]; then
  echo "check-rbac: chart renders cluster-scoped RBAC objects: $widened_kinds" >&2
  exit 1
fi

echo "check-rbac: OK (rules=$role_rules, no ClusterRole/ClusterRoleBinding)"
