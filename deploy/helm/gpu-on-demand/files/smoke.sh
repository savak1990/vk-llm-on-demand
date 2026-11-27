#!/bin/sh
# Read-only checks after a sync. GET only: a chat request would start or keep
# a GPU, and the GPU endpoints rent money.
set -eu

: "${BASE:?}" "${KEYS_DIR:?}"
interval="${SMOKE_INTERVAL:-10}"
max_wait="${SMOKE_MAX_WAIT:-300}"
BASE="${BASE%/}"

client="$(head -n 1 "$KEYS_DIR/client-keys" 2>/dev/null || true)"
mgmt="$(head -n 1 "$KEYS_DIR/management-tokens" 2>/dev/null || true)"
if [ -z "$client" ] || [ -z "$mgmt" ]; then
  echo "smoke: no client key or management token under $KEYS_DIR" >&2
  exit 1
fi

check() {
  path=$1
  key=$2
  deadline=$(( $(date +%s) + max_wait ))
  while :; do
    code="$(printf 'Authorization: Bearer %s\n' "$key" |
      curl -s -o /dev/null --max-time 10 -H @- -w '%{http_code}' "$BASE$path" || true)"
    if [ "$code" = 200 ]; then
      echo "smoke: GET $path 200"
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "smoke: GET $path returned $code for ${max_wait}s" >&2
      return 1
    fi
    sleep "$interval"
  done
}

check /v1/models "$client"
check /api/v1/gpu/status "$mgmt"
check /api/v1/gpu/cost "$mgmt"
