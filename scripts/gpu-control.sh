#!/usr/bin/env bash
set -euo pipefail

: "${ACTION:?}" "${VK_LLM_TOKEN:?}" "${ENDPOINT:?}"
: "${POLL_INTERVAL:=20}"
: "${START_MAX_WAIT:=1200}"
: "${STOP_MAX_WAIT:=300}"
: "${GITHUB_STEP_SUMMARY:=/dev/null}"

echo "::add-mask::${ENDPOINT#https://}" >&2

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

poll_until() {
  # Exits early on STOPPED+unavailable so a denied start doesn't poll
  # uselessly for the rest of max_wait. Sets LAST_BODY for the caller.
  local want_field=$1 want_value=$2 max_wait=$3
  local elapsed=0 last_state=""
  while true; do
    LAST_BODY=$(req GET status) || fail_from_body "$LAST_BODY"
    local state
    state=$(jq -r '.state' <<<"$LAST_BODY")
    if [[ "$state" != "$last_state" ]]; then
      echo "$state"
      last_state=$state
    fi
    local got
    got=$(jq -r ".$want_field" <<<"$LAST_BODY")
    if [[ "$got" == "$want_value" ]]; then
      return 0
    fi
    if [[ "$state" == "STOPPED" ]]; then
      local unavailable
      unavailable=$(jq -r '.unavailable // ""' <<<"$LAST_BODY")
      if [[ -n "$unavailable" ]]; then
        echo "gpu-control: start failed: $unavailable" >&2
        return 1
      fi
    fi
    if [[ "$elapsed" -ge "$max_wait" ]]; then
      return 1
    fi
    sleep "$POLL_INTERVAL"
    elapsed=$(( elapsed + POLL_INTERVAL ))
  done
}

do_start() {
  local body
  body=$(req POST start) || fail_from_body "$body"
  local idle
  if poll_until ready true "$START_MAX_WAIT"; then
    idle=$(jq -r '.secondsUntilIdleShutdown // "n/a"' <<<"$LAST_BODY")
    summarize "$(jq -r '.state' <<<"$LAST_BODY")" "$idle"
  else
    summarize "$(jq -r '.state' <<<"$LAST_BODY")" "n/a"
    exit 1
  fi
}

do_stop() {
  local body
  body=$(req POST stop) || fail_from_body "$body"
  if poll_until state STOPPED "$STOP_MAX_WAIT"; then
    summarize STOPPED n/a
  else
    summarize "$(jq -r '.state' <<<"$LAST_BODY")" "n/a"
    exit 1
  fi
}

case "$ACTION" in
  status) do_status ;;
  start) do_start ;;
  stop) do_stop ;;
  *) echo "gpu-control: unknown action '$ACTION'" >&2; exit 1 ;;
esac
