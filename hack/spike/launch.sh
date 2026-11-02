#!/usr/bin/env bash
# Laptop-side driver for the CORE-030 worker-launch spike. --dry-run prints
# the bodies launch.sh would send; --sweep lists, destroys and confirms
# removal of vkllm:spike:* instances; --run drives search/create through models_ok.

if ((BASH_VERSINFO[0] < 5)); then
  echo "LAUNCH: bash >= 5 is required (found ${BASH_VERSION})." >&2
  exit 1
fi

set -euo pipefail
export LC_NUMERIC=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly ONSTART_FILE="$SCRIPT_DIR/onstart.sh"
readonly ONSTART_MAX_CHARS=4048

usage() {
  echo "LAUNCH: usage: launch.sh --dry-run | --sweep | --run" >&2
  exit 1
}

readonly SWEEP_LABEL_PREFIX="vkllm:spike:"
readonly MAX_LIST_PAGES=1000
readonly SEARCH_MAX_DPH_TOTAL="0.80"
readonly WATCHDOG_SECONDS=2400
readonly BOOT_ABORT_SECONDS=1500
readonly SSH_POLL_INTERVAL=5
readonly HEALTH_POLL_INTERVAL=2

# --run state: read by cleanup() when a signal or an early exit fires it,
# so always declared even when empty.
INSTANCE_ID=""
WATCHDOG_PID=""
TUNNEL_PID=""
LABEL=""
V0_BASE=""
V1_BASE=""
WORKER_API_KEY=""
RUN_TMP=""
SSH_HOST=""
SSH_PORT=""
OFFER_ID=""
OFFER_DPH_TOTAL=""
HARD_CAP=""

# Key and body travel only through the -K config stream on stdin, never
# argv or a file on disk (curl refuses --data-binary against a
# process-substitution fifo here). 000 means the call failed; it never kills the script via set -e.
vast_call() {
  local method="$1" url="$2" body="${3:-}" max_time="${4:-30}" key resp status_line
  local config escaped
  key="$(cat -- "$VAST_API_KEY_FILE")"
  config="header = \"Authorization: Bearer $key\""
  if [[ -n "$body" ]]; then
    escaped="${body//\\/\\\\}"
    escaped="${escaped//\"/\\\"}"
    config+=$'\n'"data = \"$escaped\""
  fi
  resp="$(curl -sS --connect-timeout 10 --max-time "$max_time" -X "$method" \
    -K - -w '\n%{http_code}' "$url" <<EOF
$config
EOF
  )" || true
  status_line="${resp##*$'\n'}"
  if [[ "$status_line" =~ ^[0-9]{3}$ ]]; then
    VAST_STATUS="$status_line"
    VAST_BODY="${resp%$'\n'*}"
  else
    VAST_STATUS="000"
    VAST_BODY="$resp"
  fi
}

# Pages GET /api/v1/instances and prints the id of every instance whose
# label starts with the given prefix (an exact label also matches as its
# own prefix). A full page with no next_token is refused: we cannot know it is the end.
list_sweep_matches() {
  local v1_base="$1" prefix="$2" after="" after_enc url next count pages=0
  while true; do
    url="$v1_base/instances/?limit=25"
    if [[ -n "$after" ]]; then
      after_enc="$(jq -rn --arg v "$after" '$v | @uri')"
      url+="&after_token=$after_enc"
    fi
    vast_call GET "$url"
    if [[ "$VAST_STATUS" != 2* ]]; then
      echo "LAUNCH: sweep: list failed, status $VAST_STATUS" >&2
      return 1
    fi
    jq -e '.instances | type == "array"' <<<"$VAST_BODY" >/dev/null || {
      echo "LAUNCH: sweep: list response has no instances array" >&2
      return 1
    }
    next="$(jq -r '.next_token // empty' <<<"$VAST_BODY")"
    count="$(jq '.instances | length' <<<"$VAST_BODY")"
    jq -r --arg p "$prefix" \
      '.instances[]? | select((.label // "") | startswith($p)) | .id' <<<"$VAST_BODY"
    if [[ -z "$next" ]]; then
      if ((count == 25)); then
        echo "LAUNCH: sweep: full page (25) with no next_token, cannot confirm end of list" >&2
        return 1
      fi
      return 0
    fi
    if [[ "$next" == "$after" ]]; then
      echo "LAUNCH: sweep: pagination did not advance, got repeated token" >&2
      return 1
    fi
    pages=$((pages + 1))
    if ((pages >= MAX_LIST_PAGES)); then
      echo "LAUNCH: sweep: exceeded $MAX_LIST_PAGES pages" >&2
      return 1
    fi
    after="$next"
  done
}

# destroy_and_verify deletes one instance and polls GET every 10s, up to 10
# times, until the provider reports it absent (404).
destroy_and_verify() {
  local v0_base="$1" id="$2" attempt
  vast_call DELETE "$v0_base/instances/$id/"
  if [[ "$VAST_STATUS" != 2* && "$VAST_STATUS" != "404" ]]; then
    echo "LAUNCH: sweep: destroy $id failed, status $VAST_STATUS" >&2
    return 1
  fi
  for attempt in $(seq 1 10); do
    sleep 10
    vast_call GET "$v0_base/instances/$id/"
    if [[ "$VAST_STATUS" == "404" ]]; then
      echo "LAUNCH: instance $id confirmed absent (attempt $attempt)"
      return 0
    fi
  done
  echo "LAUNCH: sweep: instance $id not confirmed absent after 10 attempts" >&2
  return 1
}

sweep() {
  : "${VAST_API_KEY_FILE:?LAUNCH: VAST_API_KEY_FILE must be set}"
  [[ -r "$VAST_API_KEY_FILE" ]] || { echo "LAUNCH: cannot read VAST_API_KEY_FILE" >&2; exit 1; }
  command -v jq >/dev/null || { echo "LAUNCH: jq is required for --sweep" >&2; exit 1; }

  local v0_base v1_base matches id
  v0_base="${SPIKE_API_BASE:-https://console.vast.ai/api/v0}"
  v1_base="${v0_base%/api/v0}/api/v1"

  if ! matches="$(list_sweep_matches "$v1_base" "$SWEEP_LABEL_PREFIX")"; then
    exit 1
  fi

  local -a match_ids=()
  if [[ -n "$matches" ]]; then
    while IFS= read -r id; do
      match_ids+=("$id")
    done <<<"$matches"
  fi

  echo "LAUNCH: ${#match_ids[@]} instances with prefix $SWEEP_LABEL_PREFIX"
  for id in "${match_ids[@]}"; do
    echo "LAUNCH: destroying instance $id"
    destroy_and_verify "$v0_base" "$id" || exit 1
  done
}

# filter.go sends no "order" field; provider.Select sorts offers by
# dph_total client-side, so this body omits it too.
print_search_body() {
  cat <<'JSON'
{
  "gpu_name": {"eq": "L40S"},
  "num_gpus": {"eq": 1},
  "cpu_cores": {"gte": 8},
  "cpu_ram": {"gte": 32000},
  "disk_space": {"gte": 60},
  "reliability": {"gte": 0.95},
  "inet_down": {"gte": 500},
  "disk_bw": {"gte": 500},
  "cuda_max_good": {"gte": 12.8},
  "dph_total": {"lte": 0.80},
  "verified": {"eq": true},
  "rentable": {"eq": true},
  "type": "ondemand"
}
JSON
}

print_create_body() {
  local label
  label="vkllm:spike:$(date +%s)"
  cat <<JSON
{
  "image": "${SPIKE_IMAGE:-ghcr.io/ggml-org/llama.cpp:server-cuda}",
  "disk": 60,
  "runtype": "${SPIKE_RUNTYPE:-ssh}",
  "label": "$label",
  "env": {
    "WORKER_API_KEY": "<redacted>",
    "SPIKE_MODEL_SHA256": "${SPIKE_MODEL_SHA256:-<unset>}",
    "SPIKE_SELF_STOP_SECONDS": "${SPIKE_SELF_STOP_SECONDS:-2400}"
  },
  "onstart": "<hack/spike/onstart.sh, $onstart_len bytes>"
}
JSON
}

# Refuses a create whose worst-case 40-minute spend plus the $1 bandwidth
# allowance would exceed the budget left. Called once with the filter's
# own price cap before any network call, then again with the real offer's price.
preflight_budget_check() {
  local dph_total="$1" budget cost
  budget="${SPIKE_BUDGET_LEFT_USD:-5.00}"
  cost="$(awk -v d="$dph_total" 'BEGIN{printf "%.4f", d*40/60+1.00}')"
  if awk -v c="$cost" -v b="$budget" 'BEGIN{exit !(c > b)}'; then
    echo "LAUNCH: pre-flight refused: estimated cost \$$cost (dph_total \$$dph_total/h) exceeds budget \$$budget" >&2
    return 1
  fi
  echo "LAUNCH: pre-flight ok: estimated cost \$$cost within budget \$$budget"
}

require_run_deps() {
  : "${VAST_API_KEY_FILE:?LAUNCH: VAST_API_KEY_FILE must be set}"
  [[ -r "$VAST_API_KEY_FILE" ]] || { echo "LAUNCH: cannot read VAST_API_KEY_FILE" >&2; exit 1; }
  command -v jq >/dev/null || { echo "LAUNCH: jq is required for --run" >&2; exit 1; }
  SPIKE_SSH_KEY="${SPIKE_SSH_KEY:-$HOME/.ssh/vkllm_spike_ed25519}"
  [[ -r "$SPIKE_SSH_KEY" ]] || { echo "LAUNCH: cannot read SPIKE_SSH_KEY ($SPIKE_SSH_KEY)" >&2; exit 1; }
  if [[ -z "${SPIKE_LOCAL_TEST:-}" ]]; then
    : "${SPIKE_MODEL_SHA256:?LAUNCH: SPIKE_MODEL_SHA256 must be set (onstart fails without it)}"
  fi
  local onstart_bytes
  onstart_bytes=$(wc -c <"$ONSTART_FILE" | tr -d ' ')
  if ((onstart_bytes > ONSTART_MAX_CHARS)); then
    echo "LAUNCH: onstart.sh is $onstart_bytes bytes, limit is $ONSTART_MAX_CHARS" >&2
    exit 1
  fi
}

gen_worker_api_key() {
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
}

# Renders the real PUT body: actual label, actual WORKER_API_KEY, the
# verbatim onstart script. Built with jq, not a heredoc, so none of the
# onstart script's own quotes or newlines need manual escaping.
build_run_create_body() {
  local label="$1" api_key="$2"
  jq -nc \
    --arg image "${SPIKE_IMAGE:-ghcr.io/ggml-org/llama.cpp:server-cuda}" \
    --arg runtype "${SPIKE_RUNTYPE:-ssh}" \
    --arg label "$label" \
    --arg api_key "$api_key" \
    --arg sha "${SPIKE_MODEL_SHA256:-}" \
    --arg self_stop "${SPIKE_SELF_STOP_SECONDS:-2400}" \
    --rawfile onstart "$ONSTART_FILE" \
    '{
      image: $image,
      disk: 60,
      runtype: $runtype,
      label: $label,
      env: {
        WORKER_API_KEY: $api_key,
        SPIKE_MODEL_SHA256: $sha,
        SPIKE_SELF_STOP_SECONDS: $self_stop
      },
      onstart: $onstart
    }'
}

# POSTs the production filter and picks the cheapest matching offer. Sets
# OFFER_ID and OFFER_DPH_TOTAL; returns 1 if nothing came back or the
# cheapest offer still violates the filter's own price cap.
search_cheapest_offer() {
  local v0_base="$1" body cheapest
  body="$(print_search_body | jq -c .)"
  echo "LAUNCH: event=search_start t=$EPOCHREALTIME"
  vast_call POST "$v0_base/bundles" "$body" 30
  echo "LAUNCH: event=search_end t=$EPOCHREALTIME status=$VAST_STATUS"
  if [[ "$VAST_STATUS" != 2* ]]; then
    echo "LAUNCH: search failed, status $VAST_STATUS" >&2
    return 1
  fi
  cheapest="$(jq -c '.offers // [] | sort_by(.dph_total) | .[0] // empty' <<<"$VAST_BODY")"
  if [[ -z "$cheapest" ]]; then
    echo "LAUNCH: search returned no offers" >&2
    return 1
  fi
  OFFER_ID="$(jq -r '.id' <<<"$cheapest")"
  OFFER_DPH_TOTAL="$(jq -r '.dph_total' <<<"$cheapest")"
  if awk -v d="$OFFER_DPH_TOTAL" -v m="$SEARCH_MAX_DPH_TOTAL" 'BEGIN{exit !(d > m)}'; then
    echo "LAUNCH: cheapest offer dph_total $OFFER_DPH_TOTAL exceeds filter cap \$$SEARCH_MAX_DPH_TOTAL" >&2
    return 1
  fi
}

# create_instance PUTs the ask and sets INSTANCE_ID (global) from
# new_contract on success. INSTANCE_ID stays empty on any failure so
# cleanup's no-id, sweep-by-label path takes over.
create_instance() {
  local v0_base="$1" offer_id="$2" label="$3" api_key="$4" body ok new_id
  body="$(build_run_create_body "$label" "$api_key")"
  vast_call PUT "$v0_base/asks/$offer_id" "$body" 60
  if [[ "$VAST_STATUS" != 2* ]]; then
    echo "LAUNCH: create failed, status $VAST_STATUS" >&2
    return 1
  fi
  ok="$(jq -r '.success // false' <<<"$VAST_BODY")"
  new_id="$(jq -r '.new_contract // 0' <<<"$VAST_BODY")"
  if [[ "$ok" != "true" || "$new_id" == "0" || -z "$new_id" ]]; then
    echo "LAUNCH: create rejected: success=$ok new_contract=$new_id" >&2
    return 1
  fi
  INSTANCE_ID="$new_id"
  echo "LAUNCH: event=create_end t=$EPOCHREALTIME instance_id=$INSTANCE_ID"
}

# Sends TERM to the script 40 min after create_end, regardless of state.
# main_pid is captured explicitly rather than relying on $$'s own subshell
# semantics.
start_watchdog() {
  local main_pid=$$
  (sleep "$WATCHDOG_SECONDS"
    echo "LAUNCH: watchdog: hard cap reached, sending TERM" >&2
    kill -TERM "$main_pid" 2>/dev/null) &
  WATCHDOG_PID=$!
}

# Polls actual_status every 5s until "running", aborting if the deadline
# passes first. On success also sets SSH_HOST/SSH_PORT from the response,
# which wraps instance fields under "instances".
poll_until_running() {
  local v0_base="$1" id="$2" deadline="$3" status now
  while true; do
    vast_call GET "$v0_base/instances/$id/" "" 15
    if [[ "$VAST_STATUS" == 2* ]]; then
      status="$(jq -r '.instances.actual_status // empty' <<<"$VAST_BODY" 2>/dev/null)" || status=""
      if [[ "$status" == "running" ]]; then
        SSH_HOST="$(jq -r '.instances.ssh_host // empty' <<<"$VAST_BODY" 2>/dev/null)" || SSH_HOST=""
        SSH_PORT="$(jq -r '.instances.ssh_port // empty' <<<"$VAST_BODY" 2>/dev/null)" || SSH_PORT=""
        if [[ -n "$SSH_HOST" && -n "$SSH_PORT" ]]; then
          echo "LAUNCH: event=running t=$EPOCHREALTIME ssh_host=<redacted>"
          return 0
        fi
      fi
    fi
    now="${EPOCHREALTIME%.*}"
    if ((now >= deadline)); then
      echo "LAUNCH: boot abort: not running $BOOT_ABORT_SECONDS s after create_end" >&2
      return 1
    fi
    sleep 5
  done
}

# Retries `ssh ... true` every 5s until it succeeds; only the overall
# watchdog bounds this.
poll_ssh_ready() {
  local host="$1" port="$2" key="$3" known_hosts="$4"
  while true; do
    if ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
      -o UserKnownHostsFile="$known_hosts" -i "$key" -p "$port" "root@$host" true 2>/dev/null; then
      echo "LAUNCH: event=ssh_ok t=$EPOCHREALTIME"
      return 0
    fi
    sleep "$SSH_POLL_INTERVAL"
  done
}

# Forwards the laptop's own loopback 18080 to the instance's own loopback
# 8080 over SSH. llama-server must never bind a public interface, and
# this never binds one locally either.
open_tunnel() {
  local host="$1" port="$2" key="$3" known_hosts="$4"
  ssh -N -o ExitOnForwardFailure=yes -o BatchMode=yes -o ConnectTimeout=10 \
    -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile="$known_hosts" \
    -i "$key" -p "$port" -L 127.0.0.1:18080:127.0.0.1:8080 "root@$host" &
  TUNNEL_PID=$!
  sleep 1
  if ! kill -0 "$TUNNEL_PID" 2>/dev/null; then
    echo "LAUNCH: tunnel exited immediately" >&2
    TUNNEL_PID=""
    return 1
  fi
}

# Issues one GET through the loopback tunnel. with_key sends the worker's
# Authorization header via -K on stdin, same pattern as vast_call, so the
# key never reaches argv.
tunnel_get() {
  local path="$1" with_key="$2" resp status_line config=""
  if [[ "$with_key" == "1" ]]; then
    config="header = \"Authorization: Bearer $WORKER_API_KEY\""
  fi
  resp="$(curl -sS --connect-timeout 5 --max-time 10 -K - -w '\n%{http_code}' \
    "http://127.0.0.1:18080$path" <<EOF
$config
EOF
  )" || true
  status_line="${resp##*$'\n'}"
  if [[ "$status_line" =~ ^[0-9]{3}$ ]]; then
    TUNNEL_STATUS="$status_line"
    TUNNEL_BODY="${resp%$'\n'*}"
  else
    TUNNEL_STATUS="000"
    TUNNEL_BODY="$resp"
  fi
}

# Polls /health every 2s until 200; unauthenticated.
poll_health() {
  while true; do
    tunnel_get "/health" 0
    if [[ "$TUNNEL_STATUS" == "200" ]]; then
      echo "LAUNCH: event=health_200 t=$EPOCHREALTIME"
      return 0
    fi
    sleep "$HEALTH_POLL_INTERVAL"
  done
}

# Polls /v1/models until the model alias is listed.
poll_models() {
  while true; do
    tunnel_get "/v1/models" 1
    if [[ "$TUNNEL_STATUS" == "200" ]] &&
      jq -e '.data // [] | any(.id == "qwen3.6-35b-a3b")' <<<"$TUNNEL_BODY" >/dev/null 2>&1; then
      echo "LAUNCH: event=models_ok t=$EPOCHREALTIME"
      return 0
    fi
    sleep "$HEALTH_POLL_INTERVAL"
  done
}

# Trap handler for EXIT/INT/TERM/HUP, installed right before the create
# call. Disarms itself first so a second signal can't interrupt an
# in-progress destroy-verify, and runs under set +e so one failing step can't skip it.
cleanup() {
  local rc=$?
  trap - EXIT
  trap '' INT TERM HUP
  set +e

  [[ -n "$TUNNEL_PID" ]] && kill "$TUNNEL_PID" 2>/dev/null
  [[ -n "$WATCHDOG_PID" ]] && kill "$WATCHDOG_PID" 2>/dev/null
  [[ -n "$RUN_TMP" ]] && rm -rf "$RUN_TMP"

  # $? at signal-handler time reflects the interrupted command, not
  # whether this run itself succeeded, so a watchdog/TERM abort always
  # reports its own exit code rather than trusting rc.
  if [[ -n "$HARD_CAP" ]]; then
    echo "LAUNCH: cleanup: hard-cap abort" >&2
    rc=3
  fi

  if [[ -n "$INSTANCE_ID" ]]; then
    echo "LAUNCH: cleanup: destroying instance $INSTANCE_ID" >&2
    if ! destroy_and_verify "$V0_BASE" "$INSTANCE_ID"; then
      exit 2
    fi
    exit "$rc"
  fi

  echo "LAUNCH: cleanup: no id, sweeping label $LABEL" >&2
  local matches id
  if matches="$(list_sweep_matches "$V1_BASE" "$LABEL")" && [[ -n "$matches" ]]; then
    while IFS= read -r id; do
      destroy_and_verify "$V0_BASE" "$id"
    done <<<"$matches"
  fi
  # A no-id create can mean the request never reached the provider, or the
  # response was lost after it succeeded there; absence is never confirmed
  # either way, so this path always exits 2 (designed failure, not a bug).
  exit 2
}

run() {
  preflight_budget_check "$SEARCH_MAX_DPH_TOTAL" || exit 1

  require_run_deps

  V0_BASE="${SPIKE_API_BASE:-https://console.vast.ai/api/v0}"
  V1_BASE="${V0_BASE%/api/v0}/api/v1"

  local offer_id
  if [[ -n "${SPIKE_OFFER_ID:-}" ]]; then
    offer_id="$SPIKE_OFFER_ID"
  else
    search_cheapest_offer "$V0_BASE" || exit 1
    offer_id="$OFFER_ID"
    preflight_budget_check "$OFFER_DPH_TOTAL" || exit 1
  fi

  LABEL="vkllm:spike:$(date +%s)"
  WORKER_API_KEY="$(gen_worker_api_key)"
  RUN_TMP="$(mktemp -d)"

  trap cleanup EXIT INT HUP
  trap 'HARD_CAP=1; cleanup' TERM

  create_instance "$V0_BASE" "$offer_id" "$LABEL" "$WORKER_API_KEY" || exit 2

  start_watchdog

  local deadline
  deadline=$(("${EPOCHREALTIME%.*}" + BOOT_ABORT_SECONDS))
  poll_until_running "$V0_BASE" "$INSTANCE_ID" "$deadline" || exit 1

  poll_ssh_ready "$SSH_HOST" "$SSH_PORT" "$SPIKE_SSH_KEY" "$RUN_TMP/known_hosts"
  open_tunnel "$SSH_HOST" "$SSH_PORT" "$SPIKE_SSH_KEY" "$RUN_TMP/known_hosts" || exit 1

  poll_health
  poll_models

  echo "LAUNCH: run reached models_ok on instance $INSTANCE_ID; destroying now"
}

case "${1:-}" in
--dry-run) ;;
--sweep)
  sweep
  exit 0
  ;;
--run)
  run
  exit 0
  ;;
*) usage ;;
esac

onstart_len=$(wc -c <"$ONSTART_FILE" | tr -d ' ')
if ((onstart_len > ONSTART_MAX_CHARS)); then
  echo "LAUNCH: onstart.sh is $onstart_len bytes, limit is $ONSTART_MAX_CHARS." >&2
  exit 1
fi

echo "=== search body ==="
print_search_body
echo "=== create body (env redacted) ==="
print_create_body
echo "=== onstart length ==="
echo "$onstart_len bytes (limit $ONSTART_MAX_CHARS)"
