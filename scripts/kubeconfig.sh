#!/usr/bin/env bash
# Usage: kubeconfig.sh [path]
# Builds a kubeconfig from the deploy credential the platform republishes on every bring-up.
# Exit 2: the credential is absent or the cluster does not answer; any other non-zero is a real failure.
set -euo pipefail

REGION=eu-west-1
: "${PROJECT_NAME:?}"
PREFIX="/$PROJECT_NAME/cluster/llm-deploy"
UNREACHABLE=2
TARGET="${1:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/llm-deploy.kubeconfig}"

param() {
  local value err
  if value="$(aws ssm get-parameter --region "$REGION" --with-decryption \
    --name "$PREFIX/$1" --query Parameter.Value --output text 2>/dev/null)"; then
    printf '%s' "$value"
    return 0
  fi
  err="$(aws ssm get-parameter --region "$REGION" --with-decryption \
    --name "$PREFIX/$1" --query Parameter.Value --output text 2>&1 >/dev/null || true)"
  case "$err" in
    *AccessDenied*|*not\ authorized*)
      echo "kubeconfig: not allowed to read $PREFIX/$1. Grant vk-llm-ci-role ssm:GetParameter and kms:Decrypt in vk-lab-platform, then 'make account-up'." >&2
      exit 1
      ;;
    *ParameterNotFound*)
      echo "kubeconfig: no SSM parameter $PREFIX/$1; the cluster is gone. Run 'make up' in vk-lab-platform, then retry." >&2
      exit "$UNREACHABLE"
      ;;
    *)
      echo "kubeconfig: could not read $PREFIX/$1: ${err%%$'\n'*}" >&2
      exit 1
      ;;
  esac
}

# One at a time: an exit inside $( ) kills only the subshell, and set -e then stops here.
endpoint="$(param endpoint)"
ca="$(param ca)"
token="$(param token)"

if [ -n "${GITHUB_ACTIONS:-}" ]; then
  echo "::add-mask::$token"
fi

mkdir -p "$(dirname "$TARGET")"
rm -f "$TARGET"
umask 077
cat > "$TARGET" <<YAML
apiVersion: v1
kind: Config
clusters:
  - name: lab
    cluster:
      server: ${endpoint}
      certificate-authority-data: ${ca}
users:
  - name: llm-deploy
    user:
      token: ${token}
contexts:
  - name: llm-deploy
    context:
      cluster: lab
      user: llm-deploy
current-context: llm-deploy
YAML

command -v kubectl >/dev/null || { echo "kubeconfig: kubectl not found" >&2; exit 1; }

# kubectl's own stderr may name the endpoint, so it is matched, never printed.
if ! kube_err="$(KUBECONFIG="$TARGET" kubectl version --request-timeout=10s 2>&1 >/dev/null)"; then
  case "$kube_err" in
    *Unauthorized*|*Forbidden*|*x509*|*certificate*)
      echo "kubeconfig: the cluster rejected the credential. Run 'make up' in vk-lab-platform, then retry." >&2
      rm -f "$TARGET"
      exit 1
      ;;
    *)
      echo "kubeconfig: the cluster does not answer within 10s. Run 'make up' in vk-lab-platform, then retry." >&2
      rm -f "$TARGET"
      exit "$UNREACHABLE"
      ;;
  esac
fi
