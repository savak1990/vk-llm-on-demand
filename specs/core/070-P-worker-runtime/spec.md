---
id: "CORE-070"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-020", "CORE-030"]
adrs: ["0002", "0003", "0005", "0006", "0009", "0011"]
---

# 070 — Worker runtime

**Status note:** DRAFT, written 2026-11-01 before any code exists. Written
against bundle A of ADR 0006; the B delta is one section below. CORE-030
results may change values here.

**Complexity:** M
**Risk:** medium — the script runs on hosts we cannot debug, and two Vast behaviours it relies on are unverified
**Estimated effort:** two sessions (6–8 h)
**Recommended model:** strong — shell, process groups and a security boundary in one small file
**Depends on:** [CORE-020](../020-D-vastai-provider-client/spec.md), [CORE-030](../030-D-worker-launch-spike/spec.md)

## Outcome

The controller can build one create request that turns a bare Vast.ai
instance of the official `server-cuda` image into a llama-server bound to
`127.0.0.1:8080`, serving the pinned GGUF under the configured alias with a
per-instance API key. The script verifies the model checksum before it
starts the server, writes `VKLLM_STAGE` lines, and stops the worker by
itself when no tokens move for `DEADMAN_SECONDS`. Over the tunnel the
controller can tell "downloading", "loading" and "ready" apart, and
Prometheus sees llama-server's counters as `vkllm_inference_*`.

## Scope

- `internal/worker`: the embedded `onstart` template with `Render` and the
  size guard, `WorkerKey`, `BuildCreate`, `Client.Probe`, `Client.Metrics`
  with an `Exporter` collector, and `ParseStages`.
- Tests: golden render, size, shellcheck, stub and real-image container runs.

Excludes: the SSH tunnel and host-key pinning (ADR 0006, `internal/tunnel`);
offer search and the HTTP shape of create (CORE-020); state transitions,
deadlines and the 15 s pull loop that calls `Metrics` (CORE-050); pinning
the image digest and model sha256 in chart values (CORE-080); the real-GPU
run (CORE-030, CORE-140); a custom image (bundle B only).

## Design and contracts

### Environment contract

All values travel in the create request's `env` map (research §1.3: the CLI
sends `env` as a JSON object). Nothing is rendered into the script text.

| Variable | Source | Example | Secret |
|---|---|---|---|
| `MODEL_URL` | `model.url` | HF URL of `Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf` (research §3) | no |
| `MODEL_SHA256` | `model.sha256` | 64 hex chars, pinned in chart | no |
| `MODEL_ALIAS` | `model.alias` | `qwen3.6-35b-a3b` | no |
| `CTX` | `model.contextSize` | `32768` | no |
| `PARALLEL` | `model.parallel` | `1` | no |
| `WORKER_API_KEY` | `WorkerKey(...)` (ADR 0006 §4) | 43 chars base64url | **yes** |
| `DEADMAN_SECONDS` | `lifecycle.idleShutdown.timeout + 10m` in seconds (ADR 0011 §5) | `2400` | no |
| `REASONING_FORMAT` | constant `deepseek` (ADR 0002 §3) | `deepseek` | no |
| `NGL`, `POLL_SECONDS` | tests only, never set by `BuildCreate` | `0`, `1` | no |

### Worker key

```go
// base64url(HMAC-SHA256(workerSeed, "worker:"+installationID+":"+generation)), no padding.
func WorkerKey(workerSeed []byte, installationID string, generation int64) string
```

The key is derived, never stored (ADR 0006 §4). Raw base64url of 32 bytes is
43 characters with no comma, which matters because `--api-key` takes a
comma-separated list (research §2).

### Create request

```go
type CreateParams struct {
    Image          string        // worker.image, tag@digest
    DiskGB         int           // provider.filter.minDiskGB (60, ADR 0005 §3)
    Label          string        // vkllm:<installation-id>:<generation>
    ModelURL, ModelSHA256, Alias string
    Ctx, Parallel  int
    DeadMan        time.Duration // lifecycle.idleShutdown.timeout + 10m
    WorkerKey      string
}
func BuildCreate(p CreateParams) (provider.CreateRequest, error)
```

| Field (research §1.3) | Value |
|---|---|
| `image` | `p.Image` |
| `disk` | `p.DiskGB` (fixed after creation) |
| `runtype` | `ssh` |
| `label` | `p.Label` |
| `onstart` | `Render()` output (field is `onstart`, not `onstart_cmd`) |
| `env` | the eight variables above; no key starting with `-p` |

Field names inside `provider.CreateRequest` belong to CORE-020. `BuildCreate`
returns an error when `ModelSHA256` is not 64 lowercase hex characters,
`ModelURL` is not `https://`, `Alias` is empty, `Ctx < 1`, `Parallel < 1`, or
`DeadMan < 60s`.

### Onstart script

Rendered with `text/template` and no fields in bundle A; the template exists
so the B delta can swap the launch line. It ships **unwrapped** while the
rendered size is under the 4,048 limit (research §1.3); at 4,048 or more,
`Render` returns `echo <base64 of gzip -9> | base64 -d | gunzip | bash`
instead. Prototype sizes and lint results are in Evidence.

```bash
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
```

Notes on the script:

- SSH launch mode replaces the image entrypoint (research §1.3), so the
  script calls `/app/llama-server` itself (entrypoint path from research §2).
- The image sets `LLAMA_ARG_HOST=0.0.0.0` (research §2). The script unsets it
  and passes `--host 127.0.0.1 --port 8080` explicitly; the binary's own
  default port is 9931 (research §2).
- `curl` is present because the image's `HEALTHCHECK` uses it (research §2).
  `apt-get` and aria2 are optional; the curl path downloads eight ranges in
  parallel and resumes each part from its current size.
- The API key reaches curl through `printf` (a builtin) on stdin, never argv.
  It reaches llama-server as an argument, as ADR 0002 §3 fixes.
- llama-server output goes to `/dev/null` because nothing proves its default
  log level omits prompts (C-16; open question below).
- Every exit (0–4) goes through `fail()`: stage line, `TERM` to the boot
  process group (`set -m`), `TERM` to PID 1, exit code. A failed worker stops
  like an idle one instead of billing until the startup deadline (FR-5.5).

### Exit codes and stages

| Exit | Meaning | Last stage line |
|---|---|---|
| 0 | dead-man: no token movement for `DEADMAN_SECONDS` | `idle_exit` |
| 1 | a required variable is empty | `fail_env` |
| 2 | download failed three times | `fail_download` |
| 3 | sha256 mismatch | `fail_checksum` |
| 4 | llama-server exited | `fail_server` |

Stage lines go to stdout and `/var/log/vkllm-stages.log`, one per line:
`VKLLM_STAGE <name> <unix_ms>`. Names in order: `start`, `download_begin`,
`download_end`, `verified`, `server_spawn`, `server_healthy`, then one
terminal name from the table.

```go
func ParseStages(r io.Reader) (map[string]time.Time, error) // ignores non-VKLLM lines; error on a malformed VKLLM line
```

Reading the file needs an SSH exec channel that `Dialer` does not offer
(ADR 0006 §6). In bundle A the controller does not read it; it infers stages
from `Probe` (next section). The file serves CORE-030, CORE-140 and humans.

### Dead-man rule

Every `POLL_SECONDS` (15) the loop reads `/metrics`. The idle clock resets
when `llamacpp:requests_processing` is non-zero or when
`llamacpp:prompt_tokens_total` or `llamacpp:tokens_predicted_total` changed
(research §2). Health probes and metric pulls from the controller move none of
these, so they do not keep the worker alive. The clock starts at script start,
so a worker whose control plane vanished during the download also stops.
On expiry `fail idle_exit 0` runs. An `exited` instance stops GPU billing;
storage bills until the reconcile loop destroys it (research §1.5, ADR 0003 §7). **CORE-030 M10's
real result (research §7.4, both real runs): `actual_status` stayed
`running` for 120 s after `pkill -x llama-server` and 120 s after a
subsequent `kill -TERM 1`, whose result was discarded — whether PID 1
itself exited was never checked.** This spec's assumption that an
in-container action reaches `exited` (and therefore stops billing) is
unconfirmed by that data, not disproved by it; ADR 0003 §7 is flagged for
its own review as a result, and this spec's own dead-man/billing design
should be read with that caveat until a future run (CORE-140, with
CORE-030's diagnostics, starting from confirming what PID 1 is under
Vast's SSH launch mode) says otherwise. The dead-man ends an instance
after
`lifecycle.idleShutdown.timeout + 10m` with no inference, regardless of
controller state (CORE-050).

### Readiness client

```go
type Readiness int
const (
    Unreachable Readiness = iota // dial ok, nothing on 127.0.0.1:8080 → stage "download"
    Loading                      // /health 503 → stage "load"
    Ready                        // /health 200 and alias in /v1/models → stage "ready"
)
type Client struct {
    Transport http.RoundTripper // from tunnel.Dialer
    Key       string
    Alias     string
}
func (c Client) Probe(ctx context.Context) (Readiness, error)
func (c Client) Metrics(ctx context.Context) (Snapshot, error)
```

| Step | Request | Outcome |
|---|---|---|
| 1 | `GET http://127.0.0.1:8080/health`, no key (research §2) | conn refused/reset → `Unreachable`; 503 → `Loading`; 200 → step 2; other → error |
| 2 | `GET /v1/models` with `Authorization: Bearer <key>` | 200 and `data[].id` contains `Alias` → `Ready`; 200 without alias → `Loading` plus error `alias missing`; 401/403 → error `worker key rejected` |

`Probe` adds a 5 s timeout to the caller's context. Only the literal
statuses `exited`, `offline`, `unknown` are terminal (ADR 0003 §4); a stuck
`Unreachable` is handled by the startup deadline, not by a status string.

### Metrics re-export

`Metrics` sends `GET /metrics` with the key and parses the text format with
`github.com/prometheus/common/expfmt` (already a dependency of
`client_golang`). An `Exporter` collector serves the last snapshot.

| llama-server (research §2) | Re-exported | Type |
|---|---|---|
| `llamacpp:prompt_tokens_total` | `vkllm_inference_prompt_tokens_total` | counter |
| `llamacpp:prompt_seconds_total` | `vkllm_inference_prompt_seconds_total` | counter |
| `llamacpp:tokens_predicted_total` | `vkllm_inference_generated_tokens_total` | counter |
| `llamacpp:tokens_predicted_seconds_total` | `vkllm_inference_generation_seconds_total` | counter |
| `llamacpp:prompt_tokens_seconds` | `vkllm_inference_prompt_tokens_per_second` | gauge |
| `llamacpp:predicted_tokens_seconds` | `vkllm_inference_generated_tokens_per_second` | gauge |
| `llamacpp:requests_processing` | `vkllm_inference_requests_processing` | gauge |
| `llamacpp:requests_deferred` | `vkllm_inference_requests_deferred` | gauge |
| (pull result) | `vkllm_inference_up` | gauge, 1 after a successful pull, 0 otherwise |
| (pull error) | `vkllm_inference_scrape_errors_total` | counter, registered by `internal/obs`; CORE-050's pull loop calls `obs.IncInferenceScrapeErrors()` when `Metrics` returns an error |

No labels (C-34). Counters reset with each new instance; `rate()` handles it.
Spec-decode counters are not re-exported. Without a Ready worker the eight
series are absent and `vkllm_inference_up` is 0.

### Logs

`slog` JSON, fields `instance_gen`, `stage`, `readiness`, `status_code`,
`duration_ms`. Never the worker key, the worker seed, `env` values or bodies.

### Security

R3, R7, R8 and R9 carry the rules: loopback bind, no echo of `env`, a
per-generation HMAC key, and no port mapping in bundle A.

## Requirements

- R1. The rendered `onstart` MUST be shorter than 4,048 characters or be the gzip+base64 wrapper. (source: research §1.3)
- R2. llama-server MUST be started with exactly the ADR 0002 §3 arguments, with `-c`, `-np`, `--alias`, `--reasoning-format` and `--api-key` taken from the environment contract. (source: ADR 0002 §3)
- R3. llama-server MUST NOT listen on any address other than `127.0.0.1`. (source: C-17, ADR 0006 §1, NFR-7.3)
- R4. The script MUST verify `MODEL_SHA256` before starting llama-server and MUST exit 3 on mismatch. (source: ADR 0005 §1, FR-5.5)
- R5. The download MUST use `aria2c -x 16 -s 16` when available, else ranged parallel `curl`, and MUST resume a partial file. (source: ADR 0005 §2)
- R6. The script MUST exit 0 after `DEADMAN_SECONDS` without a change in token counters or a processing request. (source: C-8, ADR 0003 §7, FR-4.5)
- R7. The script MUST NOT write the API key or any `env` value to stdout, stderr or disk. (source: C-16, ADR 0009 §5)
- R8. `WORKER_API_KEY` MUST be `WorkerKey(workerSeed, installationId, generation)`. (source: ADR 0006 §4)
- R9. `BuildCreate` MUST set `runtype` `ssh` and MUST NOT add a port-mapping key to `env`. (source: ADR 0006 §1, C-17)
- R10. `Probe` MUST return `Ready` only after `/health` 200 and `/v1/models` listing the alias. (source: ADR 0002 §4, FR-3.1)
- R11. The eight `llamacpp:*` metrics MUST be re-exported under the names in the mapping table with no labels. (source: NFR-9.3, C-34)
- R12. Every external fact the code relies on MUST trace to research.md; unverified ones stay behind the open questions below. (source: C-32)
- R13. The worker MUST NOT call the provider's stop endpoint; it only exits. (source: C-5)
- R14. No value in this package, its tests or fixtures MAY be a real key, domain or token. (source: C-14)

## Implementation steps

1. Add `onstart.sh.tmpl`, `Render`, the wrapper fallback. Tests: `TestRenderGolden`, `TestRenderSize`.
2. Add `make lint-worker-script`: render to a temp file, run `shellcheck -s bash`. Wire into the CI lint job.
3. Add `WorkerKey`. Test: `TestWorkerKey` (fixed vector, length 43, no `,`, generation changes the key).
4. Add `BuildCreate`. Test: `TestBuildCreate` (fields, eight env keys, validation errors).
5. Add `ParseStages`. Test: `TestParseStages` (fixture with noise lines; one malformed line → error).
6. Add `Client.Probe` with `httptest`. Test: `TestProbe` (table over refused, 503, 200+alias, 200 without alias, 401).
7. Add `Client.Metrics` and `Exporter`. Test: `TestMetricsReexport` with a recorded `/metrics` fixture and `testutil.CollectAndCompare`.
8. Add `make test-worker-script`: the stub-server container test (AC6, AC7).
9. Add `make test-worker-image`: the real-image container test (AC8, AC9).

## Tests / acceptance criteria

- AC1. Golden: `Render()` equals `testdata/onstart.golden.sh` byte for byte. (R1, R2)
- AC2. Size: `len(Render()) < 4048`. If the wrapper path is taken, decoding it reproduces the plain script. (R1)
- AC3. Lint: `shellcheck -s bash` on the rendered script exits 0 with no findings. (R7)
- AC4. Key: `WorkerKey([]byte("k"), "i", 7)` equals the vector recorded in the test, is 43 characters, contains no `,`, and differs for generation 8. (R8)
- AC5. Create: `BuildCreate` with valid params yields `runtype=ssh`, `disk=60`, exactly the eight env keys, no key starting with `-p`; a 63-character sha256 returns an error. (R9, NFR-7.3)
- AC6. Checksum (stub container, `ubuntu:24.04`, `/app/llama-server` stub, `MODEL_URL=file:///src.bin`, `MODEL_SHA256=bad`): exit code 3 within 10 s, last stage `fail_checksum`, no `server_spawn` line; a `TERM` trap in the container's PID 1 fires. (R4, FR-5.5)
- AC7. Dead-man (same container, correct sha256, `DEADMAN_SECONDS=3`, `POLL_SECONDS=1`): exit 0 within 6 s of `server_spawn`; stub process gone; `/models/model.gguf` sha256 equals the source; combined stdout, stage log and `/models` listing contain zero matches of the key value. (R5, R6, R7, FR-4.5)
- AC8. Real image (gated, network, `make test-worker-image`): pinned `server-cuda` digest, tiny GGUF, `NGL=0`, `DEADMAN_SECONDS=20`, `POLL_SECONDS=2`: stage `server_healthy` within 120 s; `Probe` returns `Ready` through a loopback transport; a probe from a second container to `<container-ip>:8080` gets connection refused. (R2, R3, R10, FR-3.1, NFR-7.3)
- AC9. Same run: one completion sent at t+10 s after `server_healthy` delays `idle_exit` to at least 20 s after that request; then exit 0. (R6)
- AC10. Probe table: refused → `Unreachable`; 503 → `Loading`; 200 + alias → `Ready`; 200 without alias → `Loading` + error; 401 → error. (R10, FR-3.1)
- AC11. Metrics: fixture with the eight metrics yields the eight `vkllm_inference_*` values and `vkllm_inference_up 1`; a 500 yields `vkllm_inference_up 0` and no inference series. (R11, NFR-9.3)
- AC12. Stages: a fixture of eight stage lines plus 20 noise lines parses to eight timestamps; `VKLLM_STAGE x notanumber` returns an error. (FR-2.4 input)
- AC13. Real host: `exited` behaviour from CORE-030 M10; checksum and dead-man on a real host from CORE-140: altered sha256 → onstart exits 3 within the download time; control plane stopped → instance reaches `exited` within `DEADMAN_SECONDS + 60 s`; external port scan shows only SSH. (FR-5.5, FR-4.5, NFR-7.3)

AC6–AC9 touch Docker and AC8–AC9 the network, so they run as separate make targets, not in the unit gate (C-29).

## Definition of done

- [ ] AC1–AC12 pass in CI (`go test ./internal/worker/...`, `make lint-worker-script`, `make test-worker-script`, `make test-worker-image`).
- [ ] AC13 recorded: `exited` by CORE-030 M10, the rest by CORE-140.
- [ ] `make specs-check` passes.
- [ ] Model sha256 and image digest recorded in Evidence; CORE-080 copies them into chart defaults (research §3).
- [ ] Status flipped to DONE in the same PR, folder renamed.
- [ ] Evidence section filled.

## Bundle B delta

Applies only if CORE-030 fails bundle A (ADR 0006 alternative B).

| Item | Bundle A | Bundle B |
|---|---|---|
| `runtype` | `ssh` | `args`; the image entrypoint is kept (research §1.3) |
| Image | official `server-cuda` | custom image with Caddy and llama-server (ADR 0006 alt B) |
| Start | `onstart` script | same script as entrypoint, if `onstart` does not run in `args` mode (research §1.3, U) |
| llama-server bind | `127.0.0.1:8080` | unchanged |
| Exposure | none | Caddy on `0.0.0.0:8443`, TLS, proxy to `127.0.0.1:8080`; `env` gains `"-p 8443:8443": "1"` (research §1.3, §1.6) |
| Certificate | none | self-signed per instance; script prints `VKLLM_CERT_SHA256 <hex>` |
| Dial target | SSH `ssh_host:ssh_port` | `public_ipaddr` + `ports["8443/tcp"][0].HostPort` (research §1.4) |
| Auth | worker key | same worker key as bearer, plus the pinned fingerprint |
| Size | under 4,048 | recheck R1 |

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Which action turns the container `exited` in SSH mode: script exit, `kill -TERM 1`, or neither? | FR-4.5; otherwise the dead-man frees no GPU billing | CORE-030 M10 measured: `actual_status` stayed `running` 120 s after each action in 2/2 real runs; whether the kill itself failed or Vast doesn't report in-container death was not distinguished (research §7.4) | do both, as the script does, but treat GPU-billing-stops-on-`exited` as unconfirmed until CORE-140 retries with CORE-030's diagnostics, confirming what PID 1 is under Vast's SSH launch mode first |
| Does `onstart` run as root, under bash, with the `env` map exported? | `/models`, `apt-get`, `${!v}` | CORE-030 (Q1) | yes |
| `--model` and `--alias` are in ADR 0002 but not in the research §2 flag table | R2 | AC8 | ADR 0002 line as written |
| Does `/metrics` require the API key? | dead-man and `Metrics` | AC8 | send the key always |
| Is `server-cuda` usable with `-ngl 0` on a host without a GPU driver? | AC8 runs on CI without a GPU | AC8 | if not, AC8 moves to CORE-030 |
| Which tiny public GGUF does AC8 use, and its sha256? | AC8 must be reproducible | this spec's implementation | the smallest public GGUF found then, pinned by URL and sha256 in `testdata` |
| Does llama-server's default log level print prompt text? | C-16 vs debuggability | AC8 (grep the log for a marker prompt) | discard output |
| Does `GET /api/v0/instances/{id}` return `env` values, exposing the worker key to Vast account readers? | blast radius of the key | settled by CORE-030 | accept; key is per generation and HMAC-derived |
| Worker seed rotation changes every derived worker key; a running instance cannot be re-adopted after it | ADR 0009 §4 rotation | CORE-110 runbook | rotate the worker seed only while STOPPED |
| Should the controller read the stage log through an SSH exec channel? | per-stage precision of `download` vs `load` | CORE-030 | no; `Probe` inference only |
| `onstart` in `args` mode (Q2) and SSL in the image (Q11) | bundle B only | CORE-030 | B uses its own entrypoint |
| A download that stalls without failing keeps the script in `download` until the dead-man fires | billing while stuck, if the control plane is also down | CORE-030 timings | dead-man covers it: the idle clock starts at script start |

## Evidence

Prototype, 2026-11-01 (not code yet): the script above renders to 2,777
characters (1,965 gzip+base64), `shellcheck -s bash` clean. In `ubuntu:24.04`
with a stub `/app/llama-server` and a `file://` model, a bad sha256 exited 3
and PID 1's `TERM` trap fired; `DEADMAN_SECONDS=3` exited 0, PID 1 got
`TERM`, the stub was gone and the reassembled file matched the source.
