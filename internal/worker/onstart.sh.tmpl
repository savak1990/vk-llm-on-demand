#!/usr/bin/env bash
# vkllm worker onstart. Never prints environment values.
set -euo pipefail
set -m
L=/var/log/vkllm-stages.log M=/models/model.gguf U=http://127.0.0.1:8080
st() { local l; l="VKLLM_STAGE $1 $(date +%s%3N)"; echo "$l"; echo "$l" >>"$L"; }
fail() {
  st "$1"
  if [ -n "${bp:-}" ]; then kill -TERM -- -"$bp" 2>/dev/null || true; fi
  kill -TERM 1 2>/dev/null || true
  exit "$2"
}
st start
for v in MODEL_URL MODEL_SHA256 MODEL_ALIAS CTX PARALLEL WORKER_API_KEY DEADMAN_SECONDS REASONING_FORMAT; do
  [ -n "${!v:-}" ] || fail fail_env 1
done

dl() {
  if command -v aria2c >/dev/null; then
    aria2c -q -c -x 16 -s 16 --file-allocation=none -d /models -o model.gguf "$MODEL_URL"
    return
  fi
  local n s i a b f h pids=()
  n=$(curl -sfIL "$MODEL_URL" | tr -d '\r' | awk 'tolower($1)=="content-length:"{v=$2}END{print v}')
  [ -n "$n" ] || return 1
  s=$(((n + 7) / 8))
  for i in 0 1 2 3 4 5 6 7; do
    a=$((i * s)) b=$((i * s + s - 1)) f=$M.$i h=0
    [ "$b" -lt "$n" ] || b=$((n - 1))
    [ -f "$f" ] && h=$(stat -c%s "$f")
    [ $((a + h)) -le "$b" ] || continue
    curl -sfL -r "$((a + h))-$b" "$MODEL_URL" >>"$f" &
    pids+=($!)
  done
  for i in "${pids[@]}"; do wait "$i" || return 1; done
  cat "$M".[0-7] >"$M" && rm -f "$M".[0-7]
}

boot() {
  mkdir -p /models
  if ! command -v aria2c >/dev/null && command -v apt-get >/dev/null; then
    { timeout 120 apt-get update -qq && DEBIAN_FRONTEND=noninteractive timeout 120 apt-get install -y -qq aria2; } >/dev/null 2>&1 || true
  fi
  st download_begin
  dl || dl || dl || fail fail_download 2
  st download_end
  echo "$MODEL_SHA256  $M" | sha256sum -c --status || fail fail_checksum 3
  st verified
  unset LLAMA_ARG_HOST LLAMA_ARG_PORT
  st server_spawn
  exec /app/llama-server --host 127.0.0.1 --port 8080 --model "$M" \
    -c "$CTX" -np "$PARALLEL" -ngl "${NGL:-all}" -fa on --no-mmproj --metrics --jinja \
    --reasoning-format "$REASONING_FORMAT" --api-key "$WORKER_API_KEY" \
    --timeout 600 --alias "$MODEL_ALIAS" >/dev/null 2>&1
}

boot &
bp=$!
SECONDS=0 up=0 last=
while sleep "${POLL_SECONDS:-15}"; do
  if ! kill -0 "$bp" 2>/dev/null; then
    rc=0
    wait "$bp" || rc=$?
    case $rc in 2 | 3) exit "$rc" ;; esac
    fail fail_server 4
  fi
  if [ "$up" = 0 ] && curl -sf -o /dev/null "$U/health"; then up=1 && st server_healthy; fi
  m=$(printf 'header = "Authorization: Bearer %s"\n' "$WORKER_API_KEY" | curl -sf -K - "$U/metrics") || m=
  b=$(awk '$1=="llamacpp:requests_processing"{print $2+0}' <<<"$m")
  c=$(awk '$1=="llamacpp:prompt_tokens_total"||$1=="llamacpp:tokens_predicted_total"{s=s" "$2}END{print s}' <<<"$m")
  if [ "${b:-0}" != 0 ] || [ "$c" != "$last" ]; then SECONDS=0 last=$c; fi
  if [ "$SECONDS" -ge "$DEADMAN_SECONDS" ]; then fail idle_exit 0; fi
done
