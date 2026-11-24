#!/usr/bin/env bash
# Usage: dev-values.sh <chart-version> <image-sha> <fqdn>
# Prints production's chart values from gitops/ with only namespace, host, version and tag
# changed, plus provisioningMode forced to explicit (dev stays explicit regardless of production's mode).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ $# -ne 3 ]; then
  echo "usage: dev-values.sh <chart-version> <image-sha> <fqdn>" >&2
  exit 1
fi

helm template gpu-on-demand gitops \
  --set namespace=llm-dev --set hostPrefix=llm-dev \
  --set-string chartVersion="$1" --set-string tag="$2" --set-string fqdn="$3" |
  yq 'select(.kind == "Application") | .spec.source.helm.valuesObject
      | .prometheusRule.enabled = false | .dashboard.enabled = false
      | .config.lifecycle.provisioningMode = "explicit"'
