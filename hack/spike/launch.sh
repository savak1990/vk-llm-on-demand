#!/usr/bin/env bash
# Laptop-side driver for the CORE-030 worker-launch spike. --dry-run prints
# the bodies launch.sh would send; --sweep lists, destroys and confirms
# removal of vkllm:spike:* instances. --run lands in a later task.

if ((BASH_VERSINFO[0] < 5)); then
  echo "LAUNCH: bash >= 5 is required (found ${BASH_VERSION})." >&2
  exit 1
fi

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly ONSTART_FILE="$SCRIPT_DIR/onstart.sh"
readonly ONSTART_MAX_CHARS=4048

usage() {
  echo "LAUNCH: usage: launch.sh --dry-run | --sweep" >&2
  exit 1
}

readonly SWEEP_LABEL_PREFIX="vkllm:spike:"
readonly MAX_LIST_PAGES=1000

# vast_call issues one authenticated request and sets VAST_STATUS/VAST_BODY.
# The key is read fresh from VAST_API_KEY_FILE and handed to curl via -K -
# on stdin, never argv, so it never shows up in `ps` or a log line.
vast_call() {
  local method="$1" url="$2" key resp
  key="$(cat -- "$VAST_API_KEY_FILE")"
  resp="$(curl -sS -X "$method" -K - -w '\n%{http_code}' "$url" <<EOF
header = "Authorization: Bearer $key"
EOF
  )"
  VAST_STATUS="${resp##*$'\n'}"
  VAST_BODY="${resp%$'\n'*}"
}

# Pages GET /api/v1/instances (after_token/next_token) and prints the id of
# every instance whose label starts with SWEEP_LABEL_PREFIX, one per line.
# A full page with no next_token is refused: we cannot know it is the end.
list_sweep_matches() {
  local v1_base="$1" after="" after_enc url next count pages=0
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
    jq -r --arg p "$SWEEP_LABEL_PREFIX" \
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

  if ! matches="$(list_sweep_matches "$v1_base")"; then
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

case "${1:-}" in
--dry-run) ;;
--sweep)
  sweep
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
