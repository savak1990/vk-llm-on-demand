#!/usr/bin/env bash
set -euo pipefail

: "${ACTION:?}" "${VK_LLM_TOKEN:?}" "${ENDPOINT:?}"
: "${POLL_INTERVAL:=20}"
: "${START_MAX_WAIT:=1200}"
: "${STOP_MAX_WAIT:=300}"
: "${GITHUB_STEP_SUMMARY:=/dev/null}"

echo "::add-mask::${ENDPOINT#https://}"

req() {
  local method=$1 path=$2
  printf 'Authorization: Bearer %s\n' "$VK_LLM_TOKEN" |
    curl -s --max-time 30 --fail-with-body -X "$method" -H @- \
         "$ENDPOINT/api/v1/gpu/$path"
}

fail_from_body() {
  local body=$1
  if [[ -z "$body" ]]; then
    echo "gpu-control: request failed (no response)" >&2
    exit 1
  fi
  local code reason
  code=$(jq -r '.error.code // "unknown"' <<<"$body" 2>/dev/null || echo unknown)
  reason=$(jq -r '.error.reason // "unknown"' <<<"$body" 2>/dev/null || echo unknown)
  echo "gpu-control: $code: $reason" >&2
  exit 1
}

print_status_row() {
  local body=$1
  local idle
  idle=$(jq -r '.secondsUntilIdleShutdown // "n/a"' <<<"$body")
  jq -r --arg idle "$idle" '
    "provider=\(.provider) state=\(.state) gpu=\(.instance.gpu // "n/a") " +
    "model=\(.model.alias // "n/a") ready=\(.ready) " +
    "priceUSDPerHour=\(.instance.priceUSDPerHour // "n/a") " +
    "uptimeSeconds=\(.uptimeSeconds // 0) " +
    "estimatedInstanceUSD=\(.cost.estimatedInstanceUSD // "n/a") " +
    "estimatedMonthUSD=\(.cost.estimatedMonthUSD // "n/a") " +
    "secondsUntilIdleShutdown=\($idle)"
  ' <<<"$body"
}

summarize() {
  local state=$1 idle=$2
  echo "state=$state secondsUntilIdleShutdown=$idle" >> "$GITHUB_STEP_SUMMARY"
}

do_status() {
  local body
  body=$(req GET status) || fail_from_body "$body"
  print_status_row "$body"
  local state idle
  state=$(jq -r '.state' <<<"$body")
  idle=$(jq -r '.secondsUntilIdleShutdown // "n/a"' <<<"$body")
  summarize "$state" "$idle"
}

do_start() {
  echo "gpu-control: not implemented" >&2
  exit 1
}

do_stop() {
  echo "gpu-control: not implemented" >&2
  exit 1
}

case "$ACTION" in
  status) do_status ;;
  start) do_start ;;
  stop) do_stop ;;
  *) echo "gpu-control: unknown action '$ACTION'" >&2; exit 1 ;;
esac
