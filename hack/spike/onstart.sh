#!/usr/bin/env bash
# Shipped verbatim in the Vast.ai `onstart` field (CORE-030); no functions
# exported, nothing here is sourced by launch.sh.

if ((BASH_VERSINFO[0] < 5)); then
  echo "ONSTART: bash >= 5 is required (found ${BASH_VERSION})." >&2
  exit 1
fi

log() { printf '%s %s\n' "$EPOCHREALTIME" "$*" >>/root/spike-events.log; }

# Fires 40 min after onstart begins even if the laptop watchdog is gone;
# first action so a later failure never leaves the host billing forever.
# -x (not -f): never matches a wrapper whose own argv also says "llama-server".
( sleep "${SPIKE_SELF_STOP_SECONDS:-2400}"; log self_stop_fired; pkill -x llama-server; kill -TERM 1 ) &

set -euo pipefail
: "${WORKER_API_KEY:?}"

readonly SPIKE_MODEL_URL="https://huggingface.co/bartowski/Qwen_Qwen3.6-35B-A3B-GGUF/resolve/main/Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf"
readonly SPIKE_MODEL_BYTES_EXPECTED=25913185920

if [[ -z "${SPIKE_LOCAL_TEST:-}" ]]; then
  : "${SPIKE_MODEL_SHA256:?}"

  log apt_start
  if timeout 60 sh -c 'apt-get update -qq && apt-get install -y aria2'; then
    log apt_ok
  else
    log apt_failed
  fi

  content_length="$(curl -sfIL "$SPIKE_MODEL_URL" | tr -d '\r' | awk 'tolower($1) == "content-length:" {v=$2} END {print v}' || true)"
  log content_length "${content_length:-unknown}" expected "$SPIKE_MODEL_BYTES_EXPECTED"

  mkdir -p /models
  log download_start
  if command -v aria2c >/dev/null 2>&1; then
    fetcher=aria2c
    aria2c -x 16 -s 16 -o model.gguf -d /models "$SPIKE_MODEL_URL"
  else
    fetcher=curl
    curl -sfL -o /models/model.gguf "$SPIKE_MODEL_URL"
  fi
  log download_end "$fetcher"

  actual_sha256="$(sha256sum /models/model.gguf | awk '{print $1}')"
  log sha_end "$actual_sha256"
  if [[ "$actual_sha256" != "$SPIKE_MODEL_SHA256" ]]; then
    log sha_mismatch
    exit 1
  fi
fi

# The image sets LLAMA_ARG_HOST=0.0.0.0; llama-server must stay loopback-only.
unset LLAMA_ARG_HOST

if [[ -n "${SPIKE_LOCAL_TEST:-}" ]]; then
  log server_start local_test
  exec sleep infinity
fi

log server_start
exec /app/llama-server --host 127.0.0.1 --port 8080 --model /models/model.gguf \
  -c 32768 -np 1 -ngl all -fa on --no-mmproj --metrics --jinja \
  --reasoning-format deepseek --api-key "$WORKER_API_KEY" \
  --timeout 600 --alias qwen3.6-35b-a3b >>/root/llama.log 2>&1
