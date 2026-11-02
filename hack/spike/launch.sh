#!/usr/bin/env bash
# Laptop-side driver for the CORE-030 worker-launch spike. --dry-run prints
# the bodies launch.sh would send, without touching the network; --run and
# --sweep (search/create/poll/destroy/sweep against the Vast.ai API) land in
# later tasks.

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
  echo "LAUNCH: usage: launch.sh --dry-run" >&2
  exit 1
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

[[ "${1:-}" == "--dry-run" ]] || usage

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
