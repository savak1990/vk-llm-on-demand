#!/usr/bin/env bash
# Read-only checks after a dev deploy. GET only: a chat request reaching a
# running dev GPU would reset its idle timer, and the GPU endpoints rent money.
set -euo pipefail

: "${ENDPOINT:?}" "${VK_LLM_TOKEN:?}" "${VK_LLM_CLIENT_KEY:?}"
: "${SMOKE_INTERVAL:=10}"
: "${SMOKE_MAX_WAIT:=300}"

if [[ "$ENDPOINT" != https://* ]]; then
  echo "dev-smoke: ENDPOINT must start with https://" >&2
  exit 1
fi
ENDPOINT=${ENDPOINT%/}

http_code() {
  printf 'Authorization: Bearer %s\n' "$2" |
    curl -s -o /dev/null --max-time 10 -H @- -w '%{http_code}' "$ENDPOINT$1" || true
}

check() {
  local path=$1 token=$2 code deadline=$((SECONDS + SMOKE_MAX_WAIT))
  while :; do
    code=$(http_code "$path" "$token")
    if [[ "$code" == 200 ]]; then
      echo "dev-smoke: GET $path 200"
      return 0
    fi
    if ((SECONDS >= deadline)); then
      echo "dev-smoke: GET $path returned $code for ${SMOKE_MAX_WAIT}s" >&2
      return 1
    fi
    sleep "$SMOKE_INTERVAL"
  done
}

check /v1/models "$VK_LLM_CLIENT_KEY"
check /api/v1/gpu/status "$VK_LLM_TOKEN"
check /api/v1/gpu/cost "$VK_LLM_TOKEN"
