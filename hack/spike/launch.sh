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
readonly SEARCH_MAX_DPH_TOTAL="1.00"
readonly WATCHDOG_SECONDS=2400
readonly BOOT_ABORT_SECONDS=1500
readonly SSH_POLL_INTERVAL=5
readonly HEALTH_POLL_INTERVAL=2
readonly SKIP_AHEAD_SECONDS=1920
readonly M10_STEP_TIMEOUT_SECONDS=120
readonly M10_STATUS_POLL_INTERVAL=5
readonly M10_WATCHDOG_EXTENSION_SECONDS=3300
readonly M10_IDLE_SECONDS=60
readonly RESTART_HEALTH_TIMEOUT_SECONDS="${SPIKE_RESTART_HEALTH_TIMEOUT_SECONDS:-180}"
readonly M10_RESERVE_SECONDS=300

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
SSH_TARGET=""
declare -a SSH_OPTS
OFFER_ID=""
OFFER_DPH_TOTAL=""
HARD_CAP=""
JSONL_FILE=""
CREATE_END_EPOCH=""
CHANNEL_DROPS=0
CHARS_PER_TOKEN="4"
INSTANCE_PUBLIC_IP=""
INSTANCE_PORTS_JSON=""
INSTANCE_DIRECT_PORT_START=""
INSTANCE_DIRECT_PORT_END=""
INSTANCE_DPH_TOTAL=""
SSH_LINK_MODE=""
BALANCE_START=""
SUMMARY_WRITTEN=""
declare -A EVENT_T
declare -A HOST_EVENT_T
declare -A RESULTS

# Appends one JSONL event line (run/event/t/host_t/detail) and remembers the
# laptop timestamp in EVENT_T for the stage table. detail must be a jq-valid
# JSON literal (use jq -cn to build one), or empty for null.
record_event() {
  local event="$1" host_t="${2:-}" detail="${3:-}" t="${4:-$EPOCHREALTIME}"
  EVENT_T["$event"]="$t"
  [[ -n "$host_t" ]] && HOST_EVENT_T["$event"]="$host_t"
  [[ -n "$JSONL_FILE" ]] || return 0
  jq -nc --arg run "$LABEL" --arg event "$event" --argjson t "$t" \
    --argjson host_t "${host_t:-null}" --argjson detail "${detail:-null}" \
    '{run: $run, event: $event, t: $t, host_t: $host_t, detail: $detail}' \
    >>"$JSONL_FILE" 2>/dev/null || true
}

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
    # curl's own default content-type for -K "data" is form-urlencoded,
    # not JSON; the real API 400s on a JSON body without this header.
    config+=$'\n'"header = \"Content-Type: application/json\""$'\n'"data = \"$escaped\""
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
  cat <<JSON
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
  "dph_total": {"lte": $SEARCH_MAX_DPH_TOTAL},
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
  : "${SPIKE_BUDGET_LEFT_USD:?LAUNCH: SPIKE_BUDGET_LEFT_USD must be set}"
  budget="$SPIKE_BUDGET_LEFT_USD"
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
  command -v nmap >/dev/null || echo "LAUNCH: nmap not found; M1's external port scan will be skipped" >&2
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
  record_event search_start
  vast_call POST "$v0_base/bundles" "$body" 30
  echo "LAUNCH: event=search_end t=$EPOCHREALTIME status=$VAST_STATUS"
  record_event search_end "" "$(jq -cn --arg s "$VAST_STATUS" '{status: $s}')"
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
  CREATE_END_EPOCH="${EPOCHREALTIME%.*}"
  record_event create_end
}

# Sends TERM to the script 40 min after create_end, regardless of state.
# main_pid is captured explicitly rather than relying on $$'s own subshell
# semantics.
start_watchdog() {
  local main_pid=$$
  (sleep "$WATCHDOG_SECONDS"
    echo "LAUNCH: watchdog: hard cap reached, sending TERM" >&2
    # TERM to the main script must be sent first: with errexit active, a
    # child killed before the main process gets its own signal makes the
    # script exit on the child's nonzero status, skipping the TERM trap.
    kill -TERM "$main_pid" 2>/dev/null
    pkill -TERM -P "$main_pid" 2>/dev/null) &
  WATCHDOG_PID=$!
}

# Polls actual_status every 5s until "running", aborting if the deadline
# passes first. On success also sets SSH_HOST/SSH_PORT from the response,
# which wraps instance fields under "instances".
poll_until_running() {
  local v0_base="$1" id="$2" deadline="$3" status now msg last_msg=""
  local -a status_msgs=()
  while true; do
    vast_call GET "$v0_base/instances/$id/" "" 15
    if [[ "$VAST_STATUS" == 2* ]]; then
      status="$(jq -r '.instances.actual_status // empty' <<<"$VAST_BODY" 2>/dev/null)" || status=""
      msg="$(jq -r '.instances.status_msg // empty' <<<"$VAST_BODY" 2>/dev/null)" || msg=""
      if [[ -n "$msg" && "$msg" != "$last_msg" ]]; then
        status_msgs+=("$msg")
        last_msg="$msg"
      fi
      if [[ "$status" == "running" ]]; then
        SSH_HOST="$(jq -r '.instances.ssh_host // empty' <<<"$VAST_BODY" 2>/dev/null)" || SSH_HOST=""
        SSH_PORT="$(jq -r '.instances.ssh_port // empty' <<<"$VAST_BODY" 2>/dev/null)" || SSH_PORT=""
        if [[ -n "$SSH_HOST" && -n "$SSH_PORT" ]]; then
          echo "LAUNCH: event=running t=$EPOCHREALTIME ssh_host=<redacted>"
          record_event running "" "$(printf '%s\n' "${status_msgs[@]:-}" | jq -Rnc '[inputs | select(length > 0)]')"
          return 0
        fi
      fi
    fi
    now="${EPOCHREALTIME%.*}"
    if ((now >= deadline)); then
      echo "LAUNCH: boot abort: not running $BOOT_ABORT_SECONDS s after create_end" >&2
      record_event boot_abort
      return 1
    fi
    sleep 5
  done
}

# Common options for every ssh call: batch mode, a short connect timeout,
# host-key pinning, and a keepalive so a silently dead connection is
# noticed in ~30s instead of blocking on OS-level TCP keepalive (hours).
set_ssh_opts() {
  local host="$1" port="$2" key="$3" known_hosts="$4"
  SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
    -o ServerAliveInterval=10 -o ServerAliveCountMax=3 \
    -o UserKnownHostsFile="$known_hosts" -i "$key" -p "$port")
  SSH_TARGET="root@$host"
}

# Retries `ssh ... true` every 5s until it succeeds; only the overall
# watchdog bounds this.
poll_ssh_ready() {
  local host="$1" port="$2" key="$3" known_hosts="$4"
  set_ssh_opts "$host" "$port" "$key" "$known_hosts"
  while true; do
    if ssh "${SSH_OPTS[@]}" "$SSH_TARGET" true 2>/dev/null; then
      echo "LAUNCH: event=ssh_ok t=$EPOCHREALTIME"
      record_event ssh_ok
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
  set_ssh_opts "$host" "$port" "$key" "$known_hosts"
  ssh -N -o ExitOnForwardFailure=yes "${SSH_OPTS[@]}" \
    -L 127.0.0.1:18080:127.0.0.1:8080 "$SSH_TARGET" 2>/dev/null &
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

# Issues one POST through the loopback tunnel, same auth/escaping pattern as
# tunnel_get and vast_call so the key never reaches argv.
tunnel_post() {
  local path="$1" body="$2" with_key="$3" max_time="${4:-60}" resp status_line config escaped
  config=""
  if [[ "$with_key" == "1" ]]; then
    config="header = \"Authorization: Bearer $WORKER_API_KEY\""$'\n'
  fi
  escaped="${body//\\/\\\\}"
  escaped="${escaped//\"/\\\"}"
  config+="data = \"$escaped\""
  resp="$(curl -sS --connect-timeout 5 --max-time "$max_time" -X POST -K - -w '\n%{http_code}' \
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
      record_event health_200
      return 0
    fi
    sleep "$HEALTH_POLL_INTERVAL"
  done
}

# Bounded variant for post-restart waits (M7), so a bad config (OOM, crash)
# cannot loop forever and strand the rest of the run on the watchdog.
poll_health_bounded() {
  local deadline="$1"
  while true; do
    ensure_tunnel
    tunnel_get "/health" 0
    [[ "$TUNNEL_STATUS" == "200" ]] && return 0
    (("${EPOCHREALTIME%.*}" >= deadline)) && return 1
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
      record_event models_ok
      return 0
    fi
    sleep "$HEALTH_POLL_INTERVAL"
  done
}

poll_models_bounded() {
  local deadline="$1"
  while true; do
    tunnel_get "/v1/models" 1
    if [[ "$TUNNEL_STATUS" == "200" ]] &&
      jq -e '.data // [] | any(.id == "qwen3.6-35b-a3b")' <<<"$TUNNEL_BODY" >/dev/null 2>&1; then
      return 0
    fi
    (("${EPOCHREALTIME%.*}" >= deadline)) && return 1
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

  # Covers every abnormal exit (watchdog TERM, boot abort, an M-function
  # that somehow still escaped its own guard): the summary line must exist
  # even when the run never reached the end of run_measurements.
  write_summary

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

# ---- Measurement helpers (M1-M10) ----------------------------------------

# Runs one command on the host; never raises errexit on the caller (callers
# decide what a failure means), never leaks stderr (it can contain the host
# name on a connection error).
ssh_exec() {
  local cmd="$1"
  ssh -n "${SSH_OPTS[@]}" "$SSH_TARGET" "$cmd" 2>/dev/null
}

# Copies a local file's bytes to a path on the host via an exec'd `cat`,
# avoiding a dependency on scp being installed.
upload_to_host() {
  local local_path="$1" remote_path="$2"
  ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "cat > $remote_path" <"$local_path" 2>/dev/null
}

# True once 32 min have passed since create_end; M5-M9 are skipped past this
# point so M10 (always last) still gets to run within the hard cap.
past_skip_deadline() {
  [[ -n "$CREATE_END_EPOCH" ]] || return 1
  (("${EPOCHREALTIME%.*}" - CREATE_END_EPOCH >= SKIP_AHEAD_SECONDS))
}

# Seconds left before the 40-minute hard-cap watchdog fires, from now.
seconds_until_hard_cap() {
  awk -v c="${CREATE_END_EPOCH:-0}" -v w="$WATCHDOG_SECONDS" -v now="${EPOCHREALTIME%.*}" \
    'BEGIN{print (c + w) - now}'
}

# The 32-minute skip-ahead only gates the START of M5-M9, so a step
# already running can still overrun it; this checks whether starting a
# new $1-second step would leave M10 its own reserve before the hard cap.
too_close_to_hard_cap() {
  local needed="$1" left
  left="$(seconds_until_hard_cap)"
  awk -v l="$left" -v n="$needed" -v r="$M10_RESERVE_SECONDS" 'BEGIN{exit !(l < n + r)}'
}

# Runs $1 unless the 32-minute deadline has passed, in which case it records
# a skip event under the $2 label instead.
run_if_time_remains() {
  local fn="$1" mname="$2"
  if past_skip_deadline; then
    echo "LAUNCH: $mname skipped, 32 min skip-ahead reached" >&2
    record_event "${mname}_skipped" "" "$(jq -cn '"32min skip-ahead reached"')"
    return
  fi
  # "$fn" is not the final command here, so its failure cannot raise
  # errexit: a bad config (OOM, a crashed restart) must not cost the rest
  # of the run or M10's self-exit.
  "$fn" || record_event "${mname}_error"
}

# Re-arms the hard-cap watchdog to create_end+55min. Only called once M10
# confirms the instance is `exited`: billing has already stopped by then,
# so the extra wall-clock time to read the balance twice costs nothing.
extend_watchdog_for_m10() {
  local main_pid=$$ remaining
  remaining=$((CREATE_END_EPOCH + M10_WATCHDOG_EXTENSION_SECONDS - ${EPOCHREALTIME%.*}))
  ((remaining < 0)) && remaining=0
  [[ -n "$WATCHDOG_PID" ]] && kill "$WATCHDOG_PID" 2>/dev/null
  (sleep "$remaining"
    echo "LAUNCH: watchdog: M10 extension hard cap (55 min) reached, sending TERM" >&2
    kill -TERM "$main_pid" 2>/dev/null
    pkill -TERM -P "$main_pid" 2>/dev/null) &
  WATCHDOG_PID=$!
}

# One GET /api/v0/users/current/, returns the spendable balance or empty
# on failure. Reads .credit first: on a real account .balance stayed
# stuck at 0 while .credit held the funds GPU billing actually draws down.
read_balance() {
  local v0_base="$1" balance
  vast_call GET "$v0_base/users/current/" "" 15
  [[ "$VAST_STATUS" == 2* ]] || { echo ""; return; }
  balance="$(jq -r '.credit // .balance // empty' <<<"$VAST_BODY" 2>/dev/null)" || balance=""
  echo "$balance"
}

# One GET /api/v0/instances/{id}/, filling the instance-shaped globals used
# by M1 (ports, public ip) and the summary (dph_total). Never logs the IP.
fetch_instance_details() {
  vast_call GET "$V0_BASE/instances/$INSTANCE_ID/" "" 15
  if [[ "$VAST_STATUS" != 2* ]]; then
    echo "LAUNCH: fetch_instance_details failed, status $VAST_STATUS" >&2
    return 1
  fi
  INSTANCE_PUBLIC_IP="$(jq -r '.instances.public_ipaddr // empty' <<<"$VAST_BODY" 2>/dev/null)" || INSTANCE_PUBLIC_IP=""
  INSTANCE_PORTS_JSON="$(jq -c '.instances.ports // {}' <<<"$VAST_BODY" 2>/dev/null)" || INSTANCE_PORTS_JSON="{}"
  INSTANCE_DIRECT_PORT_START="$(jq -r '.instances.direct_port_start // empty' <<<"$VAST_BODY" 2>/dev/null)" || INSTANCE_DIRECT_PORT_START=""
  INSTANCE_DIRECT_PORT_END="$(jq -r '.instances.direct_port_end // empty' <<<"$VAST_BODY" 2>/dev/null)" || INSTANCE_DIRECT_PORT_END=""
  INSTANCE_DPH_TOTAL="$(jq -r '.instances.dph_total // empty' <<<"$VAST_BODY" 2>/dev/null)" || INSTANCE_DPH_TOTAL=""
  if [[ -n "$INSTANCE_PUBLIC_IP" && "$INSTANCE_PUBLIC_IP" == "$SSH_HOST" ]]; then
    SSH_LINK_MODE="direct"
  else
    SSH_LINK_MODE="proxy"
  fi
  return 0
}

# Scrapes one Prometheus gauge/counter value from /metrics through the
# tunnel. Returns empty (not an error) when the line is absent.
scrape_metric() {
  local name="$1" line
  tunnel_get "/metrics" 1
  line="$(grep -E "^llamacpp:${name}[[:space:]]" <<<"$TUNNEL_BODY" | tail -1)" || line=""
  awk '{print $2}' <<<"$line"
}

# nvidia-smi memory.used/memory.total in MiB, space-separated. Empty fields
# on failure rather than aborting the caller.
nvidia_smi_mem() {
  local out
  out="$(ssh_exec "nvidia-smi --query-gpu=memory.used,memory.total --format=csv,noheader,nounits" 2>/dev/null)" || out=""
  out="$(tr -d ' \r' <<<"$out" | head -1)"
  echo "${out//,/ }"
}

# Deterministic filler text of roughly $1 tokens, using the chars-per-token
# ratio calibrated from M4's first real response (CHARS_PER_TOKEN).
gen_filler_prompt() {
  local tokens="$1" chars
  # CHARS_PER_TOKEN can be a decimal once M4 calibrates it, so the
  # multiplication goes through awk, not bash's integer-only $(( )).
  chars="$(awk -v t="$tokens" -v c="$CHARS_PER_TOKEN" 'BEGIN{printf "%d", t*c}')"
  # yes exits 141 (SIGPIPE) once head stops reading; under pipefail that
  # would otherwise read as this function's own failure.
  { yes 'token filler word sequence number text block ' 2>/dev/null | tr -d '\n' | head -c "$chars"; } || true
}

# -x (exact process-name match), not -f: a wrapper shell whose own argv
# happens to contain the text "llama-server" would also match -f, but
# never matches the exact process name -x requires.
restart_llama_server() {
  local ctx="$1" parallel="$2" deadline still_running
  ssh_exec "pkill -x llama-server" >/dev/null 2>&1 || true

  # A ~25GB model occupies VRAM and port 8080 until the old process is
  # actually gone; wait for that to be true instead of guessing with a
  # fixed sleep, so the new server can't collide with a still-dying old one.
  deadline=$(("${EPOCHREALTIME%.*}" + 30))
  while true; do
    still_running="$(ssh_exec "pgrep -x llama-server")" || still_running=""
    [[ -z "$still_running" ]] && break
    (("${EPOCHREALTIME%.*}" >= deadline)) && break
    sleep 2
  done

  ssh_exec "rm -f /root/llama.log; nohup /app/llama-server --host 127.0.0.1 --port 8080 \
    --model /models/model.gguf -c $ctx -np $parallel -ngl all -fa on --no-mmproj --metrics \
    --jinja --reasoning-format deepseek --api-key-file /root/spike_api_key.txt --timeout 600 \
    --alias qwen3.6-35b-a3b >/root/llama.log 2>&1 </dev/null & disown; echo restarted" >/dev/null 2>&1 || true
}

# Confirms a "healthy" restart is really the new config, not an old
# process that never died still answering with its old context size.
# Returns "confirmed", "mismatch: ...", or "unknown" if /props lacks n_ctx.
confirm_restart_config() {
  local expected_ctx="$1" props actual_ctx
  tunnel_get "/props" 1
  props="$TUNNEL_BODY"
  actual_ctx="$(jq -r '.default_generation_settings.n_ctx // .n_ctx // empty' <<<"$props" 2>/dev/null)" || actual_ctx=""
  if [[ -z "$actual_ctx" ]]; then
    echo "unknown"
  elif [[ "$actual_ctx" == "$expected_ctx" ]]; then
    echo "confirmed"
  else
    echo "mismatch: got $actual_ctx, expected $expected_ctx"
  fi
}

# One streamed chat-completion sample, timed on whichever clock runs it:
# identical source run locally or over ssh, so a tunnel-vs-host comparison
# is never skewed by ssh's own channel overhead.
readonly SSE_SAMPLE_SCRIPT='
export LC_NUMERIC=C
set -uo pipefail
start=$EPOCHREALTIME
first="-1"
chunks=0
has_reasoning=0
done_seen=0
finish_reason="none"
prompt_tokens="-1"
completion_tokens="-1"
timings_prompt_ms="-1"
while IFS= read -r line; do
  case "$line" in
    "data: [DONE]") done_seen=1 ;;
    data:*)
      chunks=$((chunks + 1))
      if [[ "$first" == "-1" ]]; then
        if [[ "$line" == *'"'"'"content":"'"'"'* && "$line" != *'"'"'"content":""'"'"'* ]]; then
          first=$EPOCHREALTIME
        elif [[ "$line" == *'"'"'"reasoning_content":"'"'"'* && "$line" != *'"'"'"reasoning_content":""'"'"'* ]]; then
          first=$EPOCHREALTIME
        fi
      fi
      if [[ "$line" == *'"'"'"reasoning_content":"'"'"'* && "$line" != *'"'"'"reasoning_content":""'"'"'* ]]; then
        has_reasoning=1
      fi
      if [[ "$line" == *'"'"'"finish_reason":"'"'"'* ]]; then
        fr="${line#*\"finish_reason\":\"}"
        finish_reason="${fr%%\"*}"
      fi
      if [[ "$line" == *'"'"'"prompt_tokens":'"'"'* ]]; then
        pt="${line#*\"prompt_tokens\":}"
        prompt_tokens="${pt%%[,\}]*}"
      fi
      if [[ "$line" == *'"'"'"completion_tokens":'"'"'* ]]; then
        ct="${line#*\"completion_tokens\":}"
        completion_tokens="${ct%%[,\}]*}"
      fi
      if [[ "$line" == *'"'"'"prompt_ms":'"'"'* ]]; then
        pm="${line#*\"prompt_ms\":}"
        timings_prompt_ms="${pm%%[,\}]*}"
      fi
      ;;
  esac
done < <(curl -sS -N --max-time "${SAMPLE_TIMEOUT:-120}" -X POST "$SAMPLE_URL" \
  -H "Content-Type: application/json" -K "$SAMPLE_HEADERS" -d @"$SAMPLE_BODY" 2>/dev/null)
end=$EPOCHREALTIME
awk -v s="$start" -v e="$end" -v f="$first" -v c="$chunks" -v d="$done_seen" \
  -v r="$has_reasoning" -v fr="$finish_reason" -v pt="$prompt_tokens" \
  -v ct="$completion_tokens" -v pm="$timings_prompt_ms" "BEGIN{
    ttft_str = (f == \"-1\") ? \"-1\" : sprintf(\"%.3f\", f - s)
    printf \"start=%.6f ttft=%s total=%.3f chunks=%d done=%d has_reasoning=%d finish_reason=%s prompt_tokens=%s completion_tokens=%s timings_prompt_ms=%s\n\", s, ttft_str, e-s, c, d, r, fr, pt, ct, pm
  }"
'

# Runs SSE_SAMPLE_SCRIPT either "local" (laptop clock, through the tunnel)
# or "remote" (host clock, straight to loopback) and returns its one
# key=value result line, or an empty string on any failure.
run_sse_sample() {
  local where="$1" url="$2" headers="$3" body="$4" timeout="$5" out
  if [[ "$where" == "local" ]]; then
    out="$(SAMPLE_URL="$url" SAMPLE_HEADERS="$headers" SAMPLE_BODY="$body" \
      SAMPLE_TIMEOUT="$timeout" bash -c "$SSE_SAMPLE_SCRIPT" 2>/dev/null)" || out=""
  else
    out="$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "SAMPLE_URL='$url' SAMPLE_HEADERS='$headers' SAMPLE_BODY='$body' \
      SAMPLE_TIMEOUT='$timeout' bash -s" <<<"$SSE_SAMPLE_SCRIPT" 2>/dev/null)" || out=""
  fi
  echo "$out"
}

# Parses one run_sse_sample result line into the SAMPLE_* globals used by
# the M-functions. Unset/garbled fields default to empty, never abort.
parse_sse_result() {
  local line="$1" kv k v
  SAMPLE_START="" SAMPLE_TTFT="" SAMPLE_TOTAL="" SAMPLE_CHUNKS="" SAMPLE_DONE="" SAMPLE_HAS_REASONING=""
  SAMPLE_FINISH_REASON="" SAMPLE_PROMPT_TOKENS="" SAMPLE_COMPLETION_TOKENS="" SAMPLE_TIMINGS_PROMPT_MS=""
  for kv in $line; do
    k="${kv%%=*}"
    v="${kv#*=}"
    case "$k" in
      start) SAMPLE_START="$v" ;;
      ttft) SAMPLE_TTFT="$v" ;;
      total) SAMPLE_TOTAL="$v" ;;
      chunks) SAMPLE_CHUNKS="$v" ;;
      done) SAMPLE_DONE="$v" ;;
      has_reasoning) SAMPLE_HAS_REASONING="$v" ;;
      finish_reason) SAMPLE_FINISH_REASON="$v" ;;
      prompt_tokens) SAMPLE_PROMPT_TOKENS="$v" ;;
      completion_tokens) SAMPLE_COMPLETION_TOKENS="$v" ;;
      timings_prompt_ms) SAMPLE_TIMINGS_PROMPT_MS="$v" ;;
    esac
  done
}

# M1: listening sockets from /proc/net/tcp[6] over ssh, plus an external
# nmap scan of only this instance's own mapped ports. Never logs the
# public IP or nmap's raw output; only the derived port/state pairs.
m1_port_scan() {
  local tcp_out line hexaddr hexport ip port state listening_json mapped_ports open_ports verdict
  local -a listen_entries=() mapped_list=() open_list=()

  tcp_out="$(ssh_exec 'cat /proc/net/tcp /proc/net/tcp6 2>/dev/null; true')" || tcp_out=""
  while IFS= read -r line; do
    state="$(awk '{print $4}' <<<"$line")"
    [[ "$state" == "0A" ]] || continue
    hexaddr="$(awk '{print $2}' <<<"$line")"
    hexport="${hexaddr#*:}"
    hexaddr="${hexaddr%:*}"
    port=$((16#$hexport))
    if [[ ${#hexaddr} -eq 8 ]]; then
      ip="$(printf '%d.%d.%d.%d' "0x${hexaddr:6:2}" "0x${hexaddr:4:2}" "0x${hexaddr:2:2}" "0x${hexaddr:0:2}")"
    else
      ip="ipv6"
    fi
    listen_entries+=("$(jq -cn --arg ip "$ip" --argjson port "$port" '{ip:$ip, port:$port}')")
  done <<<"$(grep -E '^\s*[0-9]+:' <<<"$tcp_out" || true)"
  listening_json="$(printf '%s\n' "${listen_entries[@]:-}" | jq -cn 'reduce inputs as $i ([]; . + [$i])' 2>/dev/null)" || listening_json="[]"

  mapped_list=()
  if [[ -n "$INSTANCE_PORTS_JSON" ]]; then
    while IFS= read -r port; do
      [[ -n "$port" ]] && mapped_list+=("$port")
    done <<<"$(jq -r '[.[]?[]?.HostPort // empty] | .[]' <<<"$INSTANCE_PORTS_JSON" 2>/dev/null || true)"
  fi
  if [[ -n "$INSTANCE_DIRECT_PORT_START" && -n "$INSTANCE_DIRECT_PORT_END" ]] &&
    [[ "$INSTANCE_DIRECT_PORT_START" =~ ^[0-9]+$ && "$INSTANCE_DIRECT_PORT_END" =~ ^[0-9]+$ ]] &&
    ((INSTANCE_DIRECT_PORT_END - INSTANCE_DIRECT_PORT_START < 128)); then
    local p
    for ((p = INSTANCE_DIRECT_PORT_START; p <= INSTANCE_DIRECT_PORT_END; p++)); do
      mapped_list+=("$p")
    done
  fi
  mapped_ports="$(printf '%s\n' "${mapped_list[@]:-}" | sort -un | paste -sd, -)"

  local nmap_attempted=0
  open_list=()
  if [[ -n "$INSTANCE_PUBLIC_IP" && -n "$mapped_ports" ]] && command -v nmap >/dev/null; then
    local nmap_out
    nmap_out="$(nmap -Pn --host-timeout 120s -p "$mapped_ports" -oG - "$INSTANCE_PUBLIC_IP" 2>/dev/null)" || nmap_out=""
    # "ran" means it actually produced a result line, not just that the
    # command was invoked: an error or a host-timeout leaves nmap_out
    # empty, which must not be read as "found nothing open".
    grep -qE 'Ports:|Ignored State:' <<<"$nmap_out" && nmap_attempted=1
    while IFS= read -r port; do
      [[ -n "$port" ]] && open_list+=("$port")
    done <<<"$(grep -oE '[0-9]+/open' <<<"$nmap_out" | cut -d/ -f1 || true)"
  fi
  open_ports="$(printf '%s\n' "${open_list[@]:-}" | jq -Rnc '[inputs | select(length > 0) | tonumber]')"

  # A wrong "pass" here costs real money (it skips a safer fallback), so
  # it requires positive evidence both ways, not just an absent finding:
  # a confirmed loopback-only 8080 and a scan that actually ran.
  local has_8080_loopback=0 has_8080_exposed=0
  jq -e '.[] | select(.ip == "127.0.0.1" and .port == 8080)' <<<"$listening_json" >/dev/null 2>&1 && has_8080_loopback=1
  jq -e '.[] | select(.ip != "127.0.0.1" and .port == 8080)' <<<"$listening_json" >/dev/null 2>&1 && has_8080_exposed=1

  # The SSH port is expected open: the mapped-port heuristic from the
  # ports map, plus (in direct mode only, where the scan target is the
  # same endpoint we connected through) the exact port we connected on.
  local ssh_mapped_port expected_ssh_ports
  ssh_mapped_port="$(jq -r '.["22/tcp"][0].HostPort // empty' <<<"$INSTANCE_PORTS_JSON" 2>/dev/null)" || ssh_mapped_port=""
  expected_ssh_ports="$ssh_mapped_port"
  [[ "$SSH_LINK_MODE" == "direct" ]] && expected_ssh_ports="$expected_ssh_ports $SSH_PORT"

  if ((has_8080_exposed == 1)); then
    verdict="fail: port 8080 bound beyond loopback"
  elif ((nmap_attempted == 0)); then
    verdict="inconclusive: nmap did not produce output"
  elif ((has_8080_loopback == 0)); then
    verdict="inconclusive: no confirmed evidence port 8080 is bound to loopback"
  else
    local extra
    extra="$(jq --arg ssh "$expected_ssh_ports" \
      '[.[] | select((tostring) as $p | ($ssh | split(" ")) as $list | ($list | index($p)) == null)] | length' \
      <<<"$open_ports" 2>/dev/null)" || extra=""
    if [[ -z "$extra" ]]; then
      verdict="inconclusive: could not evaluate nmap results"
    elif [[ "$extra" != "0" ]]; then
      verdict="fail: open mapped port besides SSH"
    else
      verdict="pass"
    fi
  fi

  record_event m1_listening_sockets "" "$listening_json"
  record_event m1_nmap_scan "" "$(jq -cn --argjson p "$open_ports" --arg mode "$SSH_LINK_MODE" '{open_ports: $p, link_mode: $mode}')"
  RESULTS[m1]="$(jq -cn --argjson listening "$listening_json" --argjson open "$open_ports" \
    --arg mode "$SSH_LINK_MODE" --arg verdict "$verdict" \
    '{listening_sockets: $listening, scanned_open_ports: $open, link_mode: $mode, verdict: $verdict}')"
  echo "LAUNCH: M1 verdict: $verdict" >&2
}

# M2: /health without a key, /v1/models with and without the worker key,
# all through the tunnel. Status codes only, no body logged.
m2_health_models() {
  ensure_tunnel
  tunnel_get "/health" 0
  local health_status="$TUNNEL_STATUS"
  tunnel_get "/v1/models" 1
  local models_with_key="$TUNNEL_STATUS"
  tunnel_get "/v1/models" 0
  local models_without_key="$TUNNEL_STATUS"

  RESULTS[m2]="$(jq -cn --arg h "$health_status" --arg mk "$models_with_key" --arg mnk "$models_without_key" \
    '{health_status: $h, models_with_key_status: $mk, models_without_key_status: $mnk}')"
  record_event m2_health_models "" "${RESULTS[m2]}"
  echo "LAUNCH: M2 health=$health_status models_with_key=$models_with_key models_without_key=$models_without_key" >&2
}

# Builds a /v1/chat/completions body; thinking is left at the model
# default unless explicitly disabled.
build_chat_body() {
  local prompt="$1" max_tokens="$2" enable_thinking="$3" stream="$4"
  if [[ "$enable_thinking" == "false" ]]; then
    jq -cn --arg p "$prompt" --argjson mt "$max_tokens" --argjson stream "$stream" \
      '{model: "qwen3.6-35b-a3b", messages: [{role: "user", content: $p}], max_tokens: $mt,
        stream: $stream, stream_options: {include_usage: true},
        chat_template_kwargs: {enable_thinking: false}}'
  else
    jq -cn --arg p "$prompt" --argjson mt "$max_tokens" --argjson stream "$stream" \
      '{model: "qwen3.6-35b-a3b", messages: [{role: "user", content: $p}], max_tokens: $mt,
        stream: $stream, stream_options: {include_usage: true}}'
  fi
}

# The median of its arguments, or empty on an empty list.
median_of() {
  printf '%s\n' "$@" | grep -E '^-?[0-9.]+$' | sort -n | awk '
    {a[NR] = $1}
    END {
      if (NR == 0) { print ""; exit }
      if (NR % 2 == 1) { print a[(NR + 1) / 2] } else { print (a[NR / 2] + a[NR / 2 + 1]) / 2 }
    }'
}

# M3: one streamed completion with thinking left on (model default), ~1000
# tokens, through the tunnel. Confirms chunking, the terminal [DONE] and
# reasoning_content; also supplies the stage table's time-to-first-token.
m3_streamed_chat() {
  ensure_tunnel
  local prompt body_file result
  prompt="$(gen_filler_prompt 1000)"
  body_file="$RUN_TMP/m3_body.json"
  build_chat_body "$prompt" 512 "" true >"$body_file"

  result="$(run_sse_sample local "http://127.0.0.1:18080/v1/chat/completions" \
    "$CURL_HEADERS_FILE" "$body_file" 120)"
  parse_sse_result "$result"
  record_event m3_raw_result "" "$(jq -cn --arg r "$result" '$r')"

  local first_token_t=""
  if [[ -n "${SAMPLE_START:-}" && -n "$SAMPLE_TTFT" && "$SAMPLE_TTFT" != "-1" ]]; then
    first_token_t="$(awk -v s="$SAMPLE_START" -v f="$SAMPLE_TTFT" 'BEGIN{printf "%.6f", s+f}')"
    record_event first_token "" "" "$first_token_t"
  fi

  RESULTS[m3]="$(jq -cn --arg chunks "${SAMPLE_CHUNKS:-}" --arg term_done "${SAMPLE_DONE:-}" \
    --arg has_reasoning "${SAMPLE_HAS_REASONING:-}" --arg ttft "${SAMPLE_TTFT:-}" \
    --arg finish_reason "${SAMPLE_FINISH_REASON:-}" --arg start "${SAMPLE_START:-}" \
    --arg first_token_t "$first_token_t" \
    '{chunks: $chunks, terminal_done: $term_done, has_reasoning_content: $has_reasoning,
      ttft_seconds: $ttft, finish_reason: $finish_reason, start_t: $start,
      first_token_t: $first_token_t}')"
  record_event m3_streamed_chat "" "${RESULTS[m3]}"
  echo "LAUNCH: M3 chunks=${SAMPLE_CHUNKS:-} done=${SAMPLE_DONE:-} reasoning=${SAMPLE_HAS_REASONING:-}" >&2
}

# M4: 5 streamed ~1000-token requests through the tunnel, the same 5 on
# the host via ssh+curl to loopback, thinking off, to compare tunnel vs.
# on-host latency; also calibrates CHARS_PER_TOKEN for later filler prompts.
m4_latency_and_metrics() {
  ensure_tunnel
  local prompt body_file result i
  local -a tunnel_ttft=() tunnel_tps=() host_ttft=() host_tps=()

  prompt="$(gen_filler_prompt 1000)"
  body_file="$RUN_TMP/m4_body.json"
  build_chat_body "$prompt" 512 false true >"$body_file"

  for i in 1 2 3 4 5; do
    result="$(run_sse_sample local "http://127.0.0.1:18080/v1/chat/completions" \
      "$CURL_HEADERS_FILE" "$body_file" 120)"
    parse_sse_result "$result"
    record_event "m4_tunnel_sample_$i" "" "$(jq -cn --arg r "$result" '$r')"
    [[ -n "${SAMPLE_TTFT:-}" && "$SAMPLE_TTFT" != "-1" ]] && tunnel_ttft+=("$SAMPLE_TTFT")
    # M3 normally supplies the first-token timestamp; if M3 failed, the
    # first sample here is the earliest real inference token we have.
    if [[ -z "${EVENT_T[first_token]:-}" && -n "${SAMPLE_START:-}" &&
      -n "${SAMPLE_TTFT:-}" && "$SAMPLE_TTFT" != "-1" ]]; then
      record_event first_token "" "" "$(awk -v s="$SAMPLE_START" -v f="$SAMPLE_TTFT" 'BEGIN{printf "%.6f", s+f}')"
    fi
    if [[ -n "${SAMPLE_COMPLETION_TOKENS:-}" && "$SAMPLE_COMPLETION_TOKENS" != "-1" &&
      -n "${SAMPLE_TOTAL:-}" && -n "${SAMPLE_TTFT:-}" && "$SAMPLE_TTFT" != "-1" ]]; then
      # Decode tokens/s excludes prefill: completion_tokens over the
      # post-first-token duration, not the whole request.
      tunnel_tps+=("$(awk -v c="$SAMPLE_COMPLETION_TOKENS" -v t="$SAMPLE_TOTAL" -v f="$SAMPLE_TTFT" \
        'BEGIN{d=t-f; if(d>0) print c/d}')")
    fi
    if [[ $i -eq 1 && -n "${SAMPLE_PROMPT_TOKENS:-}" && "$SAMPLE_PROMPT_TOKENS" != "-1" ]]; then
      CHARS_PER_TOKEN="$(awk -v chars="${#prompt}" -v tok="$SAMPLE_PROMPT_TOKENS" 'BEGIN{if(tok>0) printf "%.2f", chars/tok; else print 4}')"
    fi
  done

  upload_to_host "$body_file" "/root/spike_m4_body.json"
  for i in 1 2 3 4 5; do
    result="$(run_sse_sample remote "http://127.0.0.1:8080/v1/chat/completions" \
      "/root/spike_curl_headers.cfg" "/root/spike_m4_body.json" 120)"
    parse_sse_result "$result"
    record_event "m4_host_sample_$i" "" "$(jq -cn --arg r "$result" '$r')"
    [[ -n "${SAMPLE_TTFT:-}" && "$SAMPLE_TTFT" != "-1" ]] && host_ttft+=("$SAMPLE_TTFT")
    if [[ -n "${SAMPLE_COMPLETION_TOKENS:-}" && "$SAMPLE_COMPLETION_TOKENS" != "-1" &&
      -n "${SAMPLE_TOTAL:-}" && -n "${SAMPLE_TTFT:-}" && "$SAMPLE_TTFT" != "-1" ]]; then
      host_tps+=("$(awk -v c="$SAMPLE_COMPLETION_TOKENS" -v t="$SAMPLE_TOTAL" -v f="$SAMPLE_TTFT" \
        'BEGIN{d=t-f; if(d>0) print c/d}')")
    fi
  done

  local predicted_tokens_seconds
  predicted_tokens_seconds="$(scrape_metric predicted_tokens_seconds)"

  RESULTS[m4]="$(jq -cn \
    --arg tunnel_ttft_median "$(median_of "${tunnel_ttft[@]:-}")" \
    --arg host_ttft_median "$(median_of "${host_ttft[@]:-}")" \
    --arg tunnel_tps_median "$(median_of "${tunnel_tps[@]:-}")" \
    --arg host_tps_median "$(median_of "${host_tps[@]:-}")" \
    --arg metrics_predicted_tokens_seconds "$predicted_tokens_seconds" \
    --arg chars_per_token "$CHARS_PER_TOKEN" \
    '{tunnel_ttft_median_s: $tunnel_ttft_median, host_ttft_median_s: $host_ttft_median,
      tunnel_tokens_per_s_median: $tunnel_tps_median, host_tokens_per_s_median: $host_tps_median,
      metrics_predicted_tokens_seconds: $metrics_predicted_tokens_seconds,
      chars_per_token: $chars_per_token}')"
  record_event m4_latency_and_metrics "" "${RESULTS[m4]}"
  echo "LAUNCH: M4 tunnel_ttft_median=$(median_of "${tunnel_ttft[@]:-}") host_ttft_median=$(median_of "${host_ttft[@]:-}")" >&2
}

# M5: 5 two-leg tool-call round trips through the tunnel. Leg 1 must
# return finish_reason=tool_calls naming get_weather with a city argument;
# leg 2 feeds back a canned result and must return finish_reason=stop.
m5_tool_calls() {
  ensure_tunnel
  local cities=(Berlin Tokyo Lima Nairobi Oslo)
  local city leg1_body leg2_body pass_count=0 i
  for i in "${!cities[@]}"; do
    city="${cities[$i]}"
    leg1_body="$(jq -cn --arg city "$city" '{
      model: "qwen3.6-35b-a3b",
      messages: [{role: "user", content: ("What is the weather in " + $city + "?")}],
      tools: [{type: "function", function: {name: "get_weather",
        description: "Get current weather for a city",
        parameters: {type: "object", properties: {city: {type: "string"}}, required: ["city"]}}}],
      tool_choice: "auto", max_tokens: 1024
    }')"
    # Thinking is left on (model default) here: a reasoning preamble can
    # run past a short timeout and read as a model failure, not a timeout.
    tunnel_post "/v1/chat/completions" "$leg1_body" 1 180
    local leg1_resp="$TUNNEL_BODY"
    local finish1 tool_name tool_id tool_args_city
    finish1="$(jq -r '.choices[0].finish_reason // empty' <<<"$leg1_resp" 2>/dev/null)" || finish1=""
    tool_name="$(jq -r '.choices[0].message.tool_calls[0].function.name // empty' <<<"$leg1_resp" 2>/dev/null)" || tool_name=""
    tool_id="$(jq -r '.choices[0].message.tool_calls[0].id // empty' <<<"$leg1_resp" 2>/dev/null)" || tool_id=""
    tool_args_city="$(jq -r '.choices[0].message.tool_calls[0].function.arguments // empty' <<<"$leg1_resp" 2>/dev/null |
      jq -r '.city // empty' 2>/dev/null)" || tool_args_city=""

    local leg1_pass=0 leg2_pass=0 finish2="" content2=""
    if [[ "$finish1" == "tool_calls" && "$tool_name" == "get_weather" && -n "$tool_id" && -n "$tool_args_city" ]]; then
      leg1_pass=1
      local assistant_msg leg2_resp
      assistant_msg="$(jq -c '.choices[0].message' <<<"$leg1_resp")"
      leg2_body="$(jq -cn --arg city "$city" --argjson assistant "$assistant_msg" --arg tcid "$tool_id" '{
        model: "qwen3.6-35b-a3b",
        messages: [{role: "user", content: ("What is the weather in " + $city + "?")}, $assistant,
          {role: "tool", tool_call_id: $tcid, content: "{\"temperature_c\":21,\"condition\":\"clear\"}"}],
        max_tokens: 1024
      }')"
      tunnel_post "/v1/chat/completions" "$leg2_body" 1 180
      leg2_resp="$TUNNEL_BODY"
      finish2="$(jq -r '.choices[0].finish_reason // empty' <<<"$leg2_resp" 2>/dev/null)" || finish2=""
      content2="$(jq -r '.choices[0].message.content // empty' <<<"$leg2_resp" 2>/dev/null)" || content2=""
      [[ "$finish2" == "stop" && -n "$content2" ]] && leg2_pass=1
    fi

    ((leg1_pass == 1 && leg2_pass == 1)) && pass_count=$((pass_count + 1))
    record_event "m5_round_trip_$((i + 1))" "" "$(jq -cn --arg city "$city" --arg f1 "$finish1" \
      --arg tool "$tool_name" --arg f2 "$finish2" \
      --argjson pass "$( ((leg1_pass == 1 && leg2_pass == 1)) && echo true || echo false)" \
      '{city: $city, leg1_finish_reason: $f1, tool_name: $tool, leg2_finish_reason: $f2, pass: $pass}')"
  done

  RESULTS[m5]="$(jq -cn --argjson pass "$pass_count" '{pass_count: $pass, of: 5}')"
  record_event m5_tool_calls "" "${RESULTS[m5]}"
  echo "LAUNCH: M5 pass_count=$pass_count/5" >&2
}

# M6: whether the image was built with SSL support.
m6_help_flag() {
  local help_out has_ssl="no"
  help_out="$(ssh_exec '/app/llama-server --help 2>&1')" || help_out=""
  grep -q -- '--ssl-key-file' <<<"$help_out" && has_ssl="yes"
  RESULTS[m6]="$(jq -cn --arg v "$has_ssl" '{ssl_key_file_flag: $v}')"
  record_event m6_help_flag "" "${RESULTS[m6]}"
  echo "LAUNCH: M6 --ssl-key-file present: $has_ssl" >&2
}

# M7: restarts llama-server with four context/parallelism configs,
# recording VRAM before/after the largest prompt per config. V1 also
# probes a context overflow; V2 adds a needle-in-haystack recall check.
m7_qwen_matrix() {
  ensure_tunnel
  local mem_after_load mem_after_prompt reload_start reload_seconds
  local -a v_results=()
  # Tracks whether a restart actually moved the server off the V1/baseline
  # config (32768/1), so the end-of-matrix restore can be skipped when
  # V2-V4 never ran and there's nothing to restore.
  local baseline_dirty=0

  # V1: 32768/1, ~30K prompt, then an ~40K prompt expected to overflow.
  if too_close_to_hard_cap 660; then
    local vcfg
    for vcfg in V1 V2 V3 V4; do
      v_results+=("$(jq -cn --arg c "$vcfg" '{config: $c, skipped: "too close to the 40min hard cap"}')")
      record_event "m7_${vcfg,,}" "" "${v_results[-1]}"
    done
    RESULTS[m7]="$(printf '%s\n' "${v_results[@]}" | jq -sc .)"
    return
  fi
  reload_start="$EPOCHREALTIME"
  restart_llama_server 32768 1
  if poll_health_bounded "$(("${EPOCHREALTIME%.*}" + RESTART_HEALTH_TIMEOUT_SECONDS))"; then
    reload_seconds="$(awk -v s="$reload_start" -v e="$EPOCHREALTIME" 'BEGIN{print e-s}')"
    mem_after_load="$(nvidia_smi_mem)"
    local config_confirmed
    config_confirmed="$(confirm_restart_config 32768)"
    local body_file="$RUN_TMP/v1_body.json" result overflow_status prompt_status
    build_chat_body "$(gen_filler_prompt 30000)" 16 false false >"$body_file"
    tunnel_post "/v1/chat/completions" "$(cat "$body_file")" 1 240
    prompt_status="$TUNNEL_STATUS"
    mem_after_prompt="$(nvidia_smi_mem)"
    build_chat_body "$(gen_filler_prompt 40000)" 16 false false >"$body_file"
    tunnel_post "/v1/chat/completions" "$(cat "$body_file")" 1 240
    overflow_status="$TUNNEL_STATUS"
    v_results+=("$(jq -cn --arg mem_load "$mem_after_load" --arg mem_prompt "$mem_after_prompt" \
      --arg reload "$reload_seconds" --arg prompt_status "$prompt_status" --arg overflow "$overflow_status" \
      --arg confirmed "$config_confirmed" \
      '{config: "V1", mem_mib_after_load: $mem_load, mem_mib_after_prompt: $mem_prompt,
        reload_seconds: $reload, prompt_status: $prompt_status, overflow_status: $overflow,
        config_confirmed: $confirmed}')")
  else
    v_results+=('{"config": "V1", "error": "did not become healthy after restart"}')
  fi
  record_event m7_v1 "" "${v_results[-1]}"

  # V2: 65536/1, 58K-64K prompt with a needle sentence, prefill via /metrics delta.
  if past_skip_deadline || too_close_to_hard_cap 780; then
    v_results+=('{"config": "V2", "skipped": "32min skip-ahead or too close to the 40min hard cap"}')
  else
    baseline_dirty=1
    reload_start="$EPOCHREALTIME"
    restart_llama_server 65536 1
    if poll_health_bounded "$(("${EPOCHREALTIME%.*}" + RESTART_HEALTH_TIMEOUT_SECONDS))"; then
      reload_seconds="$(awk -v s="$reload_start" -v e="$EPOCHREALTIME" 'BEGIN{print e-s}')"
      mem_after_load="$(nvidia_smi_mem)"
      local config_confirmed
      config_confirmed="$(confirm_restart_config 65536)"
      local needle="XK472-SPIKE" prompt prompt_seconds_before prompt_seconds_after resp prompt_status
      prompt="IMPORTANT: remember this secret code: ${needle}. $(gen_filler_prompt 60000) What is the secret code mentioned at the start of this message? Respond with only the code."
      prompt_seconds_before="$(scrape_metric prompt_seconds_total)"
      tunnel_post "/v1/chat/completions" "$(build_chat_body "$prompt" 32 false false)" 1 300
      resp="$TUNNEL_BODY"
      prompt_status="$TUNNEL_STATUS"
      prompt_seconds_after="$(scrape_metric prompt_seconds_total)"
      mem_after_prompt="$(nvidia_smi_mem)"
      local prefill_seconds needle_answered prompt_tokens content
      if [[ -n "$prompt_seconds_before" && -n "$prompt_seconds_after" ]]; then
        prefill_seconds="$(awk -v a="$prompt_seconds_before" -v b="$prompt_seconds_after" 'BEGIN{print b-a}')"
      else
        prefill_seconds=""
      fi
      prompt_tokens="$(jq -r '.usage.prompt_tokens // empty' <<<"$resp" 2>/dev/null)" || prompt_tokens=""
      content="$(jq -r '.choices[0].message.content // empty' <<<"$resp" 2>/dev/null)" || content=""
      # A 1K-token calibration extrapolated 60x can land outside the
      # intended 58K-64K prompt_tokens band, so correct once if it does.
      if [[ -n "$prompt_tokens" ]] && ((prompt_tokens < 58000 || prompt_tokens > 64000)); then
        CHARS_PER_TOKEN="$(awk -v chars="${#prompt}" -v tok="$prompt_tokens" 'BEGIN{if(tok>0) printf "%.2f", chars/tok; else print 4}')"
        prompt="IMPORTANT: remember this secret code: ${needle}. $(gen_filler_prompt 60000) What is the secret code mentioned at the start of this message? Respond with only the code."
        prompt_seconds_before="$(scrape_metric prompt_seconds_total)"
        tunnel_post "/v1/chat/completions" "$(build_chat_body "$prompt" 32 false false)" 1 300
        resp="$TUNNEL_BODY"
        prompt_status="$TUNNEL_STATUS"
        prompt_seconds_after="$(scrape_metric prompt_seconds_total)"
        mem_after_prompt="$(nvidia_smi_mem)"
        if [[ -n "$prompt_seconds_before" && -n "$prompt_seconds_after" ]]; then
          prefill_seconds="$(awk -v a="$prompt_seconds_before" -v b="$prompt_seconds_after" 'BEGIN{print b-a}')"
        else
          prefill_seconds=""
        fi
        prompt_tokens="$(jq -r '.usage.prompt_tokens // empty' <<<"$resp" 2>/dev/null)" || prompt_tokens=""
        content="$(jq -r '.choices[0].message.content // empty' <<<"$resp" 2>/dev/null)" || content=""
      fi
      [[ "$content" == *"$needle"* ]] && needle_answered="yes" || needle_answered="no"
      v_results+=("$(jq -cn --arg mem_load "$mem_after_load" --arg mem_prompt "$mem_after_prompt" \
        --arg prompt_tokens "$prompt_tokens" --arg prefill "$prefill_seconds" \
        --arg needle "$needle_answered" --arg reload "$reload_seconds" --arg prompt_status "$prompt_status" \
        --arg confirmed "$config_confirmed" \
        '{config: "V2", mem_mib_after_load: $mem_load, mem_mib_after_prompt: $mem_prompt,
          prompt_tokens: $prompt_tokens, prefill_seconds_metrics: $prefill,
          needle_answered: $needle, reload_seconds: $reload, prompt_status: $prompt_status,
          config_confirmed: $confirmed}')")
    else
      v_results+=('{"config": "V2", "error": "did not become healthy after restart"}')
    fi
  fi
  record_event m7_v2 "" "${v_results[-1]}"

  # V3: 65536/2, two concurrent ~30K prompts.
  if past_skip_deadline || too_close_to_hard_cap 480; then
    v_results+=('{"config": "V3", "skipped": "32min skip-ahead or too close to the 40min hard cap"}')
  else
    baseline_dirty=1
    restart_llama_server 65536 2
    if poll_health_bounded "$(("${EPOCHREALTIME%.*}" + RESTART_HEALTH_TIMEOUT_SECONDS))"; then
      mem_after_load="$(nvidia_smi_mem)"
      local config_confirmed
      config_confirmed="$(confirm_restart_config 65536)"
      local b1="$RUN_TMP/v3_body1.json" b2="$RUN_TMP/v3_body2.json"
      local status_f1="$RUN_TMP/v3_status1" status_f2="$RUN_TMP/v3_status2"
      build_chat_body "$(gen_filler_prompt 30000)" 16 false false >"$b1"
      build_chat_body "$(gen_filler_prompt 30000)" 16 false false >"$b2"
      # Each leg runs in its own subshell, so TUNNEL_STATUS set there never
      # reaches this shell; write it to a file instead so a failed leg's
      # memory reading isn't silently misattributed to a successful one.
      ( tunnel_post "/v1/chat/completions" "$(cat "$b1")" 1 240; echo "$TUNNEL_STATUS" >"$status_f1" ) &
      local p1=$!
      ( tunnel_post "/v1/chat/completions" "$(cat "$b2")" 1 240; echo "$TUNNEL_STATUS" >"$status_f2" ) &
      local p2=$!
      wait "$p1" "$p2" 2>/dev/null || true
      mem_after_prompt="$(nvidia_smi_mem)"
      local status1 status2
      status1="$(cat "$status_f1" 2>/dev/null)" || status1=""
      status2="$(cat "$status_f2" 2>/dev/null)" || status2=""
      v_results+=("$(jq -cn --arg mem_load "$mem_after_load" --arg mem_prompt "$mem_after_prompt" \
        --arg s1 "$status1" --arg s2 "$status2" --arg confirmed "$config_confirmed" \
        '{config: "V3", mem_mib_after_load: $mem_load, mem_mib_after_prompt: $mem_prompt,
          prompt1_status: $s1, prompt2_status: $s2, config_confirmed: $confirmed}')")
    else
      v_results+=('{"config": "V3", "error": "did not become healthy after restart"}')
    fi
  fi
  record_event m7_v3 "" "${v_results[-1]}"

  # V4: 131072/2, two concurrent ~60K prompts.
  if past_skip_deadline || too_close_to_hard_cap 480; then
    v_results+=('{"config": "V4", "skipped": "32min skip-ahead or too close to the 40min hard cap"}')
  else
    baseline_dirty=1
    restart_llama_server 131072 2
    if poll_health_bounded "$(("${EPOCHREALTIME%.*}" + RESTART_HEALTH_TIMEOUT_SECONDS))"; then
      mem_after_load="$(nvidia_smi_mem)"
      local config_confirmed
      config_confirmed="$(confirm_restart_config 131072)"
      local b1="$RUN_TMP/v4_body1.json" b2="$RUN_TMP/v4_body2.json"
      local status_f1="$RUN_TMP/v4_status1" status_f2="$RUN_TMP/v4_status2"
      build_chat_body "$(gen_filler_prompt 60000)" 16 false false >"$b1"
      build_chat_body "$(gen_filler_prompt 60000)" 16 false false >"$b2"
      ( tunnel_post "/v1/chat/completions" "$(cat "$b1")" 1 300; echo "$TUNNEL_STATUS" >"$status_f1" ) &
      local p1=$!
      ( tunnel_post "/v1/chat/completions" "$(cat "$b2")" 1 300; echo "$TUNNEL_STATUS" >"$status_f2" ) &
      local p2=$!
      wait "$p1" "$p2" 2>/dev/null || true
      mem_after_prompt="$(nvidia_smi_mem)"
      local status1 status2
      status1="$(cat "$status_f1" 2>/dev/null)" || status1=""
      status2="$(cat "$status_f2" 2>/dev/null)" || status2=""
      v_results+=("$(jq -cn --arg mem_load "$mem_after_load" --arg mem_prompt "$mem_after_prompt" \
        --arg s1 "$status1" --arg s2 "$status2" --arg confirmed "$config_confirmed" \
        '{config: "V4", mem_mib_after_load: $mem_load, mem_mib_after_prompt: $mem_prompt,
          prompt1_status: $s1, prompt2_status: $s2, config_confirmed: $confirmed}')")
    else
      v_results+=('{"config": "V4", "error": "did not become healthy after restart"}')
    fi
  fi
  record_event m7_v4 "" "${v_results[-1]}"

  RESULTS[m7]="$(printf '%s\n' "${v_results[@]}" | jq -sc .)"

  # Restore the production config so M8-M10 don't run against whatever
  # the matrix left active. Skipped when V2-V4 never ran (V1 already left
  # the server at this exact config) or there's no runway for another restart.
  if ((baseline_dirty == 0)); then
    echo "LAUNCH: M7 matrix done, V2-V4 did not run, baseline already current" >&2
  elif too_close_to_hard_cap "$RESTART_HEALTH_TIMEOUT_SECONDS"; then
    echo "LAUNCH: M7 matrix done, skipping baseline restore: too close to the 40min hard cap" >&2
    record_event m7_restore_skipped
  else
    restart_llama_server 32768 1
    poll_health_bounded "$(("${EPOCHREALTIME%.*}" + RESTART_HEALTH_TIMEOUT_SECONDS))" || true
    echo "LAUNCH: M7 matrix done, baseline restored" >&2
  fi
}

# Re-checks the long-lived tunnel and reopens it if it died, counting the
# drop. Called at the top of each tunnel-using measurement so the drop
# count reflects the whole run, not just the moments we happened to look.
ensure_tunnel() {
  if [[ -n "$TUNNEL_PID" ]] && kill -0 "$TUNNEL_PID" 2>/dev/null; then
    return 0
  fi
  CHANNEL_DROPS=$((CHANNEL_DROPS + 1))
  echo "LAUNCH: tunnel channel drop detected (#$CHANNEL_DROPS), reopening" >&2
  record_event tunnel_channel_drop "" "$(jq -cn --argjson n "$CHANNEL_DROPS" '{count: $n}')"
  open_tunnel "$SSH_HOST" "$SSH_PORT" "$SPIKE_SSH_KEY" "$RUN_TMP/known_hosts" || true
}

# M8: one max_tokens:4096 stream through the tunnel, a 1 GiB bulk
# transfer over a plain ssh exec channel (not the tunnel) for MB/s, and
# the channel-drop count accumulated since ssh_ok.
m8_bulk_and_stream() {
  ensure_tunnel
  local mb_s="" bulk_skipped=""

  if too_close_to_hard_cap 300; then
    record_event m8_stream_4096_skipped
    echo "LAUNCH: M8 stream skipped: too close to the 40min hard cap" >&2
  else
    local body_file="$RUN_TMP/m8_body.json" result
    build_chat_body "$(gen_filler_prompt 200)" 4096 false true >"$body_file"
    result="$(run_sse_sample local "http://127.0.0.1:18080/v1/chat/completions" \
      "$CURL_HEADERS_FILE" "$body_file" 300)"
    parse_sse_result "$result"
    record_event m8_stream_4096 "" "$(jq -cn --arg r "$result" '$r')"
  fi

  # Checked again here, separately from the stream above: the stream can
  # itself eat several minutes, and the bulk transfer is the single
  # longest remaining step before M10 needs its turn.
  if too_close_to_hard_cap 240; then
    bulk_skipped="too close to the 40min hard cap"
    record_event m8_bulk_transfer_skipped
    echo "LAUNCH: M8 bulk transfer skipped: too close to the 40min hard cap" >&2
  else
    local start end bytes
    start="$EPOCHREALTIME"
    # timeout always exits non-zero on a link too slow to move 1GiB in 240s
    # (exactly the slow/proxied case this measures); "; true" keeps that
    # expected exit from discarding the partial byte count wc -c already saw.
    bytes="$(ssh "${SSH_OPTS[@]}" "$SSH_TARGET" \
      "timeout 240 head -c 1073741824 /dev/zero; true" 2>/dev/null | wc -c | tr -d ' ')" || bytes=0
    end="$EPOCHREALTIME"
    mb_s="$(awk -v b="${bytes:-0}" -v s="$start" -v e="$end" 'BEGIN{if(e>s) print b/(e-s)/1000000; else print 0}')"
  fi

  RESULTS[m8]="$(jq -cn --arg chunks "${SAMPLE_CHUNKS:-}" --arg total "${SAMPLE_TOTAL:-}" \
    --arg prompt_ms "${SAMPLE_TIMINGS_PROMPT_MS:-}" --arg bulk_mb_s "$mb_s" --arg bulk_skipped "$bulk_skipped" \
    --argjson drops "$CHANNEL_DROPS" \
    '{stream_4096_chunks: $chunks, stream_4096_total_s: $total, timings_prompt_ms: $prompt_ms,
      bulk_transfer_mb_s: $bulk_mb_s, bulk_transfer_skipped: $bulk_skipped, channel_drops_through_m8: $drops}')"
  record_event m8_bulk_and_stream "" "${RESULTS[m8]}"
  echo "LAUNCH: M8 bulk_mb_s=$mb_s channel_drops=$CHANNEL_DROPS" >&2
}

# M9: 60s single-stream download of the pinned model URL, on the host.
# The URL is read from onstart.sh rather than duplicated here.
m9_download_speed() {
  local model_url speed_bps speed_mb_s
  if too_close_to_hard_cap 90; then
    RESULTS[m9]='{"skipped": "too close to the 40min hard cap"}'
    record_event m9_download_speed "" "${RESULTS[m9]}"
    return
  fi
  model_url="$(grep -oE 'https://huggingface\.co/[^"]+' "$ONSTART_FILE" | head -1)" || model_url=""
  if [[ -z "$model_url" ]]; then
    RESULTS[m9]='{"error": "model URL not found in onstart.sh"}'
    record_event m9_download_speed "" "${RESULTS[m9]}"
    return
  fi
  # curl --max-time 60 against a multi-GB file always exits 28 (timeout)
  # by design, after already printing -w's output; "; true" keeps that
  # expected nonzero exit from discarding the speed curl already measured.
  speed_bps="$(ssh_exec "curl -o /dev/null -sS --max-time 60 -w '%{speed_download}' '$model_url'; true")" || speed_bps=""
  speed_mb_s="$(awk -v b="${speed_bps:-0}" 'BEGIN{printf "%.2f", b/1000000}')"
  RESULTS[m9]="$(jq -cn --arg mb_s "$speed_mb_s" '{download_speed_mb_s: $mb_s}')"
  record_event m9_download_speed "" "${RESULTS[m9]}"
  echo "LAUNCH: M9 download_speed_mb_s=$speed_mb_s" >&2
}

# M10: self-exit, always last. pkill and kill -TERM 1 are each their own
# standalone ssh call; see restart_llama_server's comment on why pkill
# uses -x rather than -f.
m10_self_exit() {
  local last_request_t idle_target
  last_request_t="${EVENT_T[m8_bulk_and_stream]:-${EVENT_T[m8_stream_4096]:-$EPOCHREALTIME}}"
  idle_target="$(awk -v t="$last_request_t" -v i="$M10_IDLE_SECONDS" 'BEGIN{printf "%.6f", t+i}')"
  while true; do
    awk -v now="$EPOCHREALTIME" -v t="$idle_target" 'BEGIN{exit (now>=t)}' || break
    sleep 2
  done

  local step="" exited=0 poll_start now status
  local -a observed_statuses=()

  ssh_exec "pkill -x llama-server" >/dev/null 2>&1 || true
  poll_start="${EPOCHREALTIME%.*}"
  while true; do
    vast_call GET "$V0_BASE/instances/$INSTANCE_ID/" "" 15
    status="unknown"
    if [[ "$VAST_STATUS" == 2* ]]; then
      status="$(jq -r '.instances.actual_status // "unknown"' <<<"$VAST_BODY" 2>/dev/null)" || status="unknown"
    fi
    observed_statuses+=("$status")
    record_event m10_poll_a "" "$(jq -cn --arg s "$status" '{actual_status: $s}')"
    if [[ "$status" == "exited" ]]; then
      step="a"
      exited=1
      break
    fi
    now="${EPOCHREALTIME%.*}"
    if ((now - poll_start >= M10_STEP_TIMEOUT_SECONDS)); then
      break
    fi
    sleep "$M10_STATUS_POLL_INTERVAL"
  done

  if ((exited == 0)); then
    ssh_exec "kill -TERM 1" >/dev/null 2>&1 || true
    poll_start="${EPOCHREALTIME%.*}"
    while true; do
      vast_call GET "$V0_BASE/instances/$INSTANCE_ID/" "" 15
      status="unknown"
      if [[ "$VAST_STATUS" == 2* ]]; then
        status="$(jq -r '.instances.actual_status // "unknown"' <<<"$VAST_BODY" 2>/dev/null)" || status="unknown"
      fi
      observed_statuses+=("$status")
      record_event m10_poll_b "" "$(jq -cn --arg s "$status" '{actual_status: $s}')"
      if [[ "$status" == "exited" ]]; then
        step="b"
        exited=1
        break
      fi
      now="${EPOCHREALTIME%.*}"
      if ((now - poll_start >= M10_STEP_TIMEOUT_SECONDS)); then
        break
      fi
      sleep "$M10_STATUS_POLL_INTERVAL"
    done
  fi

  local distinct_statuses
  distinct_statuses="$(printf '%s\n' "${observed_statuses[@]:-}" | sort -u |
    jq -Rnc '[inputs | select(length > 0)]')"

  if ((exited == 0)); then
    RESULTS[m10]="$(jq -cn --argjson statuses "$distinct_statuses" \
      '{exited: false, note: "neither pkill nor kill -TERM 1 reached exited within 120s each",
        observed_statuses: $statuses}')"
    record_event m10_self_exit "" "${RESULTS[m10]}"
    echo "LAUNCH: M10: instance did not reach exited" >&2
    return
  fi

  local exited_t="$EPOCHREALTIME"
  extend_watchdog_for_m10

  # target2 is capped below the re-armed watchdog's own deadline so the
  # second balance read can never race the hard-cap TERM that would kill
  # the script before it gets there.
  local target1 target2 cap
  target1="$(awk -v t="$exited_t" 'BEGIN{printf "%.6f", t+300}')"
  cap="$(awk -v c="$CREATE_END_EPOCH" 'BEGIN{print c+3300-30}')"
  target2="$(awk -v t="$exited_t" -v c="$cap" 'BEGIN{v=t+1200; printf "%.6f", (v<c)?v:c}')"

  while true; do
    awk -v now="$EPOCHREALTIME" -v t="$target1" 'BEGIN{exit (now>=t)}' || break
    sleep 15
  done
  local balance1 t1
  balance1="$(read_balance "$V0_BASE")"
  t1="$EPOCHREALTIME"
  record_event m10_balance_1 "" "$(jq -cn --arg b "$balance1" '{balance: $b}')"

  local still_exited=1
  while true; do
    awk -v now="$EPOCHREALTIME" -v t="$target2" 'BEGIN{exit (now>=t)}' || break
    sleep 30
    vast_call GET "$V0_BASE/instances/$INSTANCE_ID/" "" 15
    status="unknown"
    if [[ "$VAST_STATUS" == 2* ]]; then
      status="$(jq -r '.instances.actual_status // "unknown"' <<<"$VAST_BODY" 2>/dev/null)" || status="unknown"
    fi
    if [[ "$status" != "exited" ]]; then
      still_exited=0
      break
    fi
  done

  local balance2 t2 window_h delta threshold verdict
  balance2="$(read_balance "$V0_BASE")"
  t2="$EPOCHREALTIME"
  record_event m10_balance_2 "" "$(jq -cn --arg b "$balance2" '{balance: $b}')"
  window_h="$(awk -v a="$t1" -v b="$t2" 'BEGIN{print (b-a)/3600}')"
  if [[ -n "$balance1" && -n "$balance2" ]]; then
    delta="$(awk -v a="$balance1" -v b="$balance2" 'BEGIN{print a-b}')"
    threshold="$(awk -v d="${INSTANCE_DPH_TOTAL:-0}" -v h="$window_h" 'BEGIN{print d*h*0.25}')"
    if awk -v x="$delta" -v y="$threshold" 'BEGIN{exit !(x<y)}'; then
      verdict="stopped"
    else
      verdict="not_stopped"
    fi
  else
    delta=""
    threshold=""
    verdict="unknown: balance read failed"
  fi

  RESULTS[m10]="$(jq -cn --arg step "$step" --argjson statuses "$distinct_statuses" \
    --arg balance1 "${balance1:-}" --arg balance2 "${balance2:-}" --arg window_h "$window_h" \
    --arg delta "$delta" --arg threshold "$threshold" --arg verdict "$verdict" \
    --argjson still_exited "$([[ $still_exited == 1 ]] && echo true || echo false)" \
    '{exited: true, exit_step: $step, observed_statuses: $statuses,
      balance_before_window: $balance1, balance_after_window: $balance2,
      window_hours: $window_h, delta_usd: $delta, threshold_usd: $threshold,
      stayed_exited_through_window: $still_exited, gpu_billing_verdict: $verdict}')"
  record_event m10_self_exit "" "${RESULTS[m10]}"
  echo "LAUNCH: M10 exit_step=$step verdict=$verdict" >&2
}

# Reads the onstart log over SSH and records the download/checksum/server
# events with their own host clock, never the laptop's: the two clocks
# can drift, so a duration must be computed from one clock or the other.
collect_host_events() {
  local log_out line t rest ev args
  log_out="$(ssh_exec 'cat /root/spike-events.log 2>/dev/null; true')" || log_out=""
  if [[ -z "$log_out" ]]; then
    echo "LAUNCH: /root/spike-events.log not found or empty" >&2
    return 1
  fi
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    t="${line%% *}"
    rest="${line#* }"
    ev="${rest%% *}"
    if [[ "$ev" == "$rest" ]]; then
      args=""
    else
      args="${rest#* }"
    fi
    case "$ev" in
      apt_start | apt_ok | apt_failed | download_start | sha_mismatch | server_start | self_stop_fired)
        record_event "$ev" "$t"
        ;;
      download_end | sha_end)
        if [[ -n "$args" ]]; then
          record_event "$ev" "$t" "$(jq -cn --arg r "$args" '$r')"
        else
          record_event "$ev" "$t"
        fi
        ;;
      content_length)
        record_event content_length "$t" "$(jq -cn --arg r "$args" '$r')"
        ;;
    esac
  done <<<"$log_out"
  return 0
}

run_measurements() {
  CURL_HEADERS_FILE="$RUN_TMP/headers_auth.cfg"
  printf 'header = "Authorization: Bearer %s"\n' "$WORKER_API_KEY" >"$CURL_HEADERS_FILE"

  # M3 runs before the port scan and M2 (a deliberate reorder) so the
  # cold-start timing it supplies isn't inflated by unrelated measurements.
  if ! m3_streamed_chat; then
    record_event m3_error
    echo "LAUNCH: M3 failed, continuing" >&2
  fi

  fetch_instance_details || true
  upload_to_host <(printf '%s' "$WORKER_API_KEY") /root/spike_api_key.txt || true
  ssh_exec "chmod 600 /root/spike_api_key.txt" >/dev/null 2>&1 || true
  upload_to_host "$CURL_HEADERS_FILE" /root/spike_curl_headers.cfg || true
  ssh_exec "chmod 600 /root/spike_curl_headers.cfg" >/dev/null 2>&1 || true

  collect_host_events || true

  if ! m1_port_scan; then
    record_event m1_error
    echo "LAUNCH: M1 failed, continuing" >&2
  fi
  if ! m2_health_models; then
    record_event m2_error
    echo "LAUNCH: M2 failed, continuing" >&2
  fi
  if ! m4_latency_and_metrics; then
    record_event m4_error
    echo "LAUNCH: M4 failed, continuing" >&2
  fi

  run_if_time_remains m5_tool_calls M5
  run_if_time_remains m6_help_flag M6
  run_if_time_remains m7_qwen_matrix M7
  run_if_time_remains m8_bulk_and_stream M8
  run_if_time_remains m9_download_speed M9

  if ! m10_self_exit; then
    record_event m10_error
    echo "LAUNCH: M10 failed" >&2
  fi

  write_summary
}

# Tolerant of a partial run: every field falls back to null rather than
# aborting, since this is also called from cleanup() on every abnormal
# exit (watchdog TERM, boot abort, an escaped M-function failure).
write_summary() {
  [[ -n "$SUMMARY_WRITTEN" ]] && return 0
  [[ -n "$JSONL_FILE" ]] || return 0
  SUMMARY_WRITTEN=1

  local stage_json measurements_json lifetime_seconds dph estimated_cost now

  stage_json="$(jq -cn \
    --arg e0 "${EVENT_T[search_start]:-}" --arg e1 "${EVENT_T[search_end]:-}" \
    --arg e2 "${EVENT_T[create_end]:-}" --arg e3 "${EVENT_T[running]:-}" \
    --arg e4 "${EVENT_T[ssh_ok]:-}" \
    --arg h5 "${HOST_EVENT_T[download_start]:-}" --arg h6 "${HOST_EVENT_T[sha_end]:-}" \
    --arg h7 "${HOST_EVENT_T[server_start]:-}" \
    --arg e8 "${EVENT_T[health_200]:-}" --arg e9 "${EVENT_T[models_ok]:-}" \
    --arg e10 "${EVENT_T[first_token]:-}" '
    def d($a; $b): if ($a == "" or $b == "") then null else (($b | tonumber) - ($a | tonumber)) end;
    {
      search: d($e0; $e1),
      create: d($e1; $e2),
      boot: d($e2; $e3),
      link: d($e3; $e4),
      download: d($h5; $h6),
      load: d($h7; $e8),
      ready: d($e8; $e9),
      first_token: d($e9; $e10),
      cold_start_total: d($e0; $e10)
    }')"

  measurements_json="$(jq -cn \
    --argjson m1 "${RESULTS[m1]:-null}" --argjson m2 "${RESULTS[m2]:-null}" \
    --argjson m3 "${RESULTS[m3]:-null}" --argjson m4 "${RESULTS[m4]:-null}" \
    --argjson m5 "${RESULTS[m5]:-null}" --argjson m6 "${RESULTS[m6]:-null}" \
    --argjson m7 "${RESULTS[m7]:-null}" --argjson m8 "${RESULTS[m8]:-null}" \
    --argjson m9 "${RESULTS[m9]:-null}" --argjson m10 "${RESULTS[m10]:-null}" \
    '{m1: $m1, m2: $m2, m3: $m3, m4: $m4, m5: $m5, m6: $m6, m7: $m7, m8: $m8, m9: $m9, m10: $m10}')"

  now="$EPOCHREALTIME"
  dph="${INSTANCE_DPH_TOTAL:-${OFFER_DPH_TOTAL:-}}"
  if [[ -n "$CREATE_END_EPOCH" ]]; then
    lifetime_seconds="$(awk -v s="$CREATE_END_EPOCH" -v e="$now" 'BEGIN{print e-s}')"
  else
    lifetime_seconds=""
  fi
  if [[ -n "$dph" && -n "$lifetime_seconds" ]]; then
    estimated_cost="$(awk -v d="$dph" -v s="$lifetime_seconds" 'BEGIN{printf "%.4f", d*s/3600}')"
  else
    estimated_cost=""
  fi

  jq -nc --arg run "${LABEL:-}" --argjson t "$EPOCHREALTIME" \
    --argjson stages "$stage_json" --argjson measurements "$measurements_json" \
    --arg dph "${dph:-}" --arg lifetime "${lifetime_seconds:-}" --arg cost "${estimated_cost:-}" \
    --arg balance_start "${BALANCE_START:-}" --arg link_mode "${SSH_LINK_MODE:-}" \
    --argjson channel_drops "$CHANNEL_DROPS" \
    '{run: $run, event: "summary", t: $t, host_t: null, detail: {
      stages: $stages, measurements: $measurements, dph_total: $dph,
      lifetime_seconds_since_create_end: $lifetime, estimated_cost_usd: $cost,
      balance_start: $balance_start, ssh_link_mode: $link_mode, channel_drops_total: $channel_drops
    }}' >>"$JSONL_FILE" 2>/dev/null || true

  echo "LAUNCH: summary written to $JSONL_FILE" >&2
}

run() {
  preflight_budget_check "$SEARCH_MAX_DPH_TOTAL" || exit 1

  require_run_deps

  V0_BASE="${SPIKE_API_BASE:-https://console.vast.ai/api/v0}"
  V1_BASE="${V0_BASE%/api/v0}/api/v1"
  LABEL="vkllm:spike:$(date +%s)"
  JSONL_FILE="${TMPDIR:-/tmp}/vkllm-spike-$(date +%s).jsonl"
  : >"$JSONL_FILE"

  vast_call GET "$V0_BASE/users/current/" "" 15
  # .credit first, same reason as read_balance: .balance is stuck at 0
  # on a real account, .credit holds the funds that actually move.
  [[ "$VAST_STATUS" == 2* ]] && BALANCE_START="$(jq -r '.credit // .balance // empty' <<<"$VAST_BODY" 2>/dev/null)" || true

  local offer_id
  if [[ -n "${SPIKE_OFFER_ID:-}" ]]; then
    offer_id="$SPIKE_OFFER_ID"
  else
    search_cheapest_offer "$V0_BASE" || exit 1
    offer_id="$OFFER_ID"
    preflight_budget_check "$OFFER_DPH_TOTAL" || exit 1
  fi

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

  echo "LAUNCH: run reached models_ok on instance $INSTANCE_ID; starting measurements"
  run_measurements
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
