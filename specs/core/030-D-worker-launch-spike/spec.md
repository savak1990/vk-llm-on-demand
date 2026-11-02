---
id: "CORE-030"
status: "DONE"
updated: "2026-11-02"
depends_on: ["CORE-020"]
adrs: ["0006", "0002", "0005", "0003", "0007"]
---

# 030 — Worker launch spike

**Status note:** DONE, 2026-11-02. Two real runs (A1, A2) against one
1×L40S Vast.ai instance each answered Q1–Q3 and Q7–Q12, Q9 excepted (still
open; see Evidence); ADR 0006 is Accepted (bundle A) with an amendment to
its G5 latency threshold; ADR 0005 is amended with the observed download
range. `hack/spike/` is deleted; `docs/research.md` §7 and
`docs/open-questions.md` carry the durable record.

**Complexity:** M
**Risk:** medium — real GPU spend on a rented host, bounded by a hard cap, a trap and a prepaid balance
**Estimated effort:** two sessions (≤ 8 h), ≤ $5 GPU spend
**Recommended model:** strong — live results must be judged against ADR gates and written back into ADRs
**Depends on:** [CORE-020](../020-D-vastai-provider-client/spec.md)

## Outcome

A time-boxed, scripted run on one real 1×L40S Vast.ai instance answers the
open questions that block ADR 0006 and size the cold-start budget. When this
spec is done, `docs/research.md` has a new §7 "Spike results" with a
measured timestamp for each of the eight cold-start stages, the SSH launch
mode verdict, tunnel latency and throughput, a Qwen3.6 tool-call round trip,
Hugging Face download speed, VRAM at 32K and 64K context and with `-np 2`,
decode tokens/s and warm TTFT. ADR 0006 is Accepted (bundle A) or amended
to fallback B. Rows Q1–Q3 and Q7–Q12 in `docs/open-questions.md` are closed.
No instance with label prefix `vkllm:spike:` exists afterwards, and the
provider balance dropped by at most $5.

## Scope

- One bash script `hack/spike/launch.sh` and its payload `hack/spike/onstart.sh`.
- Modes of `launch.sh`: `--dry-run` (no network), `--run`, `--sweep`.
- Run A1: runtype `ssh`, official image, onstart per ADR 0002 and ADR 0005.
- Run A2 (conditional): runtype `ssh_direct`, only if A1 is proxied and fails the latency gate.
- Run B (conditional): runtype `args` with a throwaway custom image adding Caddy TLS, only if A fails.
- Write-back: research §7, ADR 0006 status, ADR 0005 amendment if triggered, open-questions rows, this spec's Evidence.
- Recommended values for `provider.filter.minInetDownMBps`, `lifecycle.startupTimeout` (and `onDemand.maxWait` for the future mode).
- A host-side self-stop timer in onstart, and M10: which in-container action turns a real SSH-mode instance `exited` and whether GPU billing then stops (FR-4.7, C-8; closes the open question spec 070 assigns here).

Excludes: any Go code in `internal/`; the production onstart script and
image pin (spec 070); the checksum-mismatch check (FR-5.5, spec 070) and
the production dead-man timer logic (spec 070); repeated acceptance runs and `-np 2` adoption (spec 140);
availability over time (Q13); changing an Accepted ADR. The script is
**deleted** in the pull request that records the evidence; git history
keeps it, and spec 140 is the repeatable real-provider run (C-29).

## Design and contracts

### Questions, method, closing evidence

| Q | Question | Method in this spike | Closes with |
|---|---|---|---|
| Q1 | SSH mode works with `server-cuda` | A1: SSH command over `ssh_host:ssh_port` with the account key; onstart log present | yes/no, `link` seconds |
| Q2 | `onstart` runs in `args` mode | B only: onstart writes a marker that Caddy serves | yes/no, or "moot: A accepted" |
| Q3 | SSH endpoint direct or proxied; channel throttling | compare `ssh_host` with `public_ipaddr`; tunnel vs on-host TTFT and tokens/s; bulk MB/s; channel drops | direct/proxy, deltas, drop count |
| Q7 | Qwen3.6 tool-call round trip | 5 two-leg tool-call exchanges through the tunnel | pass count of 5 |
| Q8 | HF download throughput | onstart timestamps around the fetch; 60 s single-stream sample | MB/s, fetcher, advertised `inet_down` |
| Q9 | VRAM at 32K, 64K, `-np 2` | `nvidia-smi` after load and after the largest prompt per config | MiB used / total per config |
| Q10 | Decode tokens/s, warm TTFT | 5 streamed requests, 1K-token prompt, thinking off | median TTFT, median tokens/s |
| Q11 | Image built with SSL | `/app/llama-server --help` over SSH, grep `--ssl-key-file` | yes/no |
| Q12 | Image pull under 2 min | `boot` stage and `status_msg` sequence while not running | seconds, one sample; 140 adds more |

### Inputs (environment of `launch.sh`)

| Variable | Meaning | Handling |
|---|---|---|
| `VAST_API_KEY_FILE` | path to the Vast API key | read into curl via `-K -` on stdin; never in argv, log or evidence |
| `SPIKE_SSH_KEY` | private key whose public half is registered at account level (research §1.3) | used with `ssh -i`; never copied |
| `SPIKE_IMAGE` | `ghcr.io/ggml-org/llama.cpp:server-cuda@sha256:…`, a build ≥ b12032 (ADR 0002 §1) | recorded in evidence |
| `SPIKE_MODEL_SHA256` | sha256 of `Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf` copied by the operator | compared in onstart |
| `SPIKE_RUNTYPE` | `ssh` (default), `ssh_direct`, `args` | research §1.3 lists all three |
| `SPIKE_BUDGET_LEFT_USD` | budget not yet spent | pre-flight refuses a run that could exceed it |
| `SPIKE_SELF_STOP_SECONDS` | host self-stop delay, passed in `env` | default 2400; 60 in the local timer test |
| `SPIKE_LOCAL_TEST` | skip `apt-get`, the download and the checksum; run `sleep infinity` in place of `llama-server` | set only for the Docker self-stop timer test, passed in `env`, never set for a real GPU run |
| `SPIKE_API_BASE` | provider base URL | default `https://console.vast.ai/api/v0` (research §1.1); a local stub in the step 3 check |
| `SPIKE_OFFER_ID` | skip search and use this offer | used by the step 3 check only |

The script generates `WORKER_API_KEY` per run (32 random bytes, hex) and
passes it in the create body's `env` object (research §1.3). The script
requires bash ≥ 5 for `$EPOCHREALTIME` and fails at start otherwise. All
timing on the laptop and on the host uses `$EPOCHREALTIME`.

### Provider calls (shapes from research §1)

| Step | Call | Body / notes |
|---|---|---|
| search | `POST /api/v0/bundles` | production filter from architecture §9 with field names and units settled by CORE-020: `gpu_name eq L40S`, `num_gpus eq 1`, cores ≥ 8, RAM ≥ 32 GB, `disk_space gte 60`, `reliability gte 0.95`, `inet_down gte 500`, `disk_bw gte 500`, `cuda_max_good gte 12.8`, `dph_total lte 0.80`, `verified`, `rentable`, `type ondemand`, ordered by `dph_total` |
| offer census | same call without the `inet_down` filter | counts offers at `inet_down` ≥ 250, 500, 1000 client-side |
| create | `PUT /api/v0/asks/{offer_id}` | `image`, `label: "vkllm:spike:<unix-ts>"`, `disk: 60`, `runtype`, `env`, `onstart` (≤ 4048 characters) |
| poll | `GET /api/v0/instances/{id}` every 5 s | records `actual_status`, `status_msg` changes with timestamps; records whether the returned `label` equals the sent one |
| destroy | `DELETE /api/v0/instances/{id}` | retried until GET reports absent |
| sweep | `GET /api/v1/instances` | client-side filter on label prefix `vkllm:spike:`; a full page (25) is a hard error, never "absent" |
| balance | `GET /api/v0/users/current` | `balance` before, after, and again ≥ 1 h later |

### Onstart payload (A1, A2)

`hack/spike/onstart.sh`, shipped verbatim in `onstart`:

0. First action, before anything can fail: start the self-stop timer
   `( sleep "${SPIKE_SELF_STOP_SECONDS:-2400}"; log self_stop_fired; pkill -f llama-server; kill -TERM 1 ) &`.
   It fires 40 min after onstart begins, so after the laptop watchdog, and
   matters only when the laptop is gone. M10 measures whether ending PID 1
   is what turns the instance `exited`.
1. `set -euo pipefail`; `: "${WORKER_API_KEY:?}"`; log each event with `$EPOCHREALTIME` to `/root/spike-events.log`.
2. Try `apt-get install -y aria2` with a 60 s timeout; log seconds and outcome.
3. Log the `Content-Length` that `curl -sfIL` reports for the final URL after redirects, compared with 25,913,185,920 bytes (research §3). Fetch the pinned URL to `/models/model.gguf`: `aria2c -x 16 -s 16` (ADR 0005 §2), else single-stream `curl`. Log `download_start`, `download_end`, fetcher.
4. `sha256sum`; log `sha_end`; exit non-zero on mismatch.
5. `unset LLAMA_ARG_HOST` (the image sets it to `0.0.0.0`, research §2), then start `/app/llama-server` with exactly the ADR 0002 §3 arguments, output to `/root/llama.log`; log `server_start`.

The ADR 0002 arguments already pin `--host 127.0.0.1 --port 8080`. Context
and parallelism are changed only for the Q9 matrix, by restarting the
server over SSH with the same command and different `-c`/`-np`.

### Event timeline and stage definitions

Stage names equal the `stage` label values of
`vkllm_coldstart_stage_seconds` (ADR 0005 §4), so specs 050 and 090 reuse
the numbers unchanged.

| Event | Where recorded | Definition |
|---|---|---|
| E0 `search_start` / E1 `search_end` | laptop | around the search call |
| E2 `create_end` | laptop | create returned `new_contract` |
| E3 `running` | laptop | first poll with `actual_status = running` |
| E4 `ssh_ok` | laptop | first `ssh … true` exit 0, retried every 5 s |
| E5 `download_start` / E6 `sha_end` | host log | onstart |
| E7 `server_start` | host log | onstart |
| E8 `health_200` | laptop | first `/health` 200 through the tunnel, polled every 2 s |
| E9 `models_ok` | laptop | `/v1/models` lists `qwen3.6-35b-a3b` |
| E10 `first_token` | laptop | first SSE chunk with a non-empty delta of the first request |

| Stage | Duration |
|---|---|
| `search` | E1 − E0 |
| `create` | E2 − E1 |
| `boot` | E3 − E2 |
| `link` | E4 − E3 |
| `download` | E6 − E5 (sha256 time logged separately) |
| `load` | E8 − E7 |
| `ready` | E9 − E8 |
| `first_token` | E10 − E9 |

Host and laptop clocks differ; host-side stages are differences of host
timestamps only. Cold start total is E10 − E0 on the laptop clock.
Download can start before `ssh_ok`; overlap is recorded, not corrected.

### Measurements after Ready, in priority order

The hard cap can end a run early, so ADR 0006 gate items run first.

| # | Measurement | Gate |
|---|---|---|
| M1 | Listening sockets from `/proc/net/tcp` over SSH; `nmap -Pn` from the laptop against `public_ipaddr` on this instance's ports only (`ports` map and `direct_port_start..direct_port_end`) | A |
| M2 | `/health` without key, `/v1/models` with and without key through `ssh -L 18080:127.0.0.1:8080` | A |
| M3 | Streamed chat completion: chunk count, terminal `data: [DONE]`, `reasoning_content` present with thinking on | A |
| M4 | Q10: 5 streamed requests, ≈ 1,000-token prompt, `enable_thinking: false`, `max_tokens: 512`, through the tunnel; the same 5 on the host via SSH + `curl` to loopback; plus `llamacpp:predicted_tokens_seconds` from `/metrics` | A |
| M5 | Q7: 5 round trips: one tool `get_weather(city)`; leg 1 expects `finish_reason: "tool_calls"`, name `get_weather`, JSON arguments with `city`; leg 2 sends the tool result and expects a text answer with `finish_reason: "stop"` | — |
| M6 | Q11: `--help` grep | — |
| M7 | Q9 matrix with restarts (below), including the 64K request | — |
| M8 | Q3 extras: one stream with `max_tokens: 4096`; bulk `head -c 1G /dev/zero` over the SSH channel, MB/s; channel drops counted over the whole run | — |
| M9 | Q8 extra: 60 s single-stream `curl -o /dev/null` of the model URL on the host, MB/s | — |
| M10 | Worker self-exit, always last (below) | — |

M10, worker self-exit on the real host. llama-server is idle (no request
for ≥ 60 s). Poll `actual_status` every 5 s throughout.

| Step | Action over SSH | Records |
|---|---|---|
| a | `pkill -f llama-server`; the onstart script has already exited | `actual_status` for 120 s; `exited` yes/no, seconds |
| b | only if a did not reach `exited`: `kill -TERM 1` | `actual_status` for 120 s; `exited` yes/no, seconds |
| c | instance `exited`: balance read at `exited` + 5 min and + 20 min | delta over 15 min vs `dph_total × 0.25` |

GPU billing counts as stopped when the 15 min delta is under 25 % of
`dph_total × 0.25` (research §1.5: storage is still billed). If neither a
nor b reaches `exited`, record that; C-8 then has no in-container lever and
spec 070 needs another mechanism. At 32 min after `create_end` the script
skips any unfinished M5–M9 and runs M10, so M10 always happens.

Q9 matrix. Headroom means `memory.used ≤ 0.9 × memory.total` after the
largest prompt.

| Config | `-c` | `-np` | Prompt | Records |
|---|---|---|---|---|
| V1 | 32768 | 1 | ≈ 30K tokens; then ≈ 40K tokens (expected to fail) | MiB after load, after prompt; status code of the overflow |
| V2 | 65536 | 1 | 58K–64K tokens with a needle sentence at the start | MiB, `prompt_tokens`, prefill seconds, needle answered yes/no, reload seconds |
| V3 | 65536 | 2 | two concurrent ≈ 30K prompts | MiB |
| V4 | 131072 | 2 | two concurrent ≈ 60K prompts | MiB |

Prompts are deterministic filler sized from the `usage.prompt_tokens` of
the M4 prompt (characters per token), corrected once if outside the band.

### ADR 0006 decision gate

Bundle A passes when all hold on one run:

| Gate | Threshold |
|---|---|
| G1 SSH | `ssh_ok` within 5 min of `running`, with the account key and no manual step |
| G2 onstart | `/root/spike-events.log` exists; `server_start` logged |
| G3 loopback | M1: port 8080 bound only to `127.0.0.1`; scan shows no open mapped port except SSH |
| G4 readiness | M2: `/health` 200 and `/v1/models` lists the alias through the tunnel |
| G5 latency | M4: median tunnel TTFT − median on-host TTFT ≤ 300 ms; median tunnel tokens/s ≥ 95 % of on-host |
| G6 stability | 0 tunnel channel drops between `ssh_ok` and the end of M8 |

These thresholds are proposed by the spike author; the controller reviews
them in Evidence before ADR 0006 changes status.

G1 or G2 fails → run B. Only G5 or G6 fails and A1 was proxied → run A2
with `ssh_direct`; if A2 passes, ADR 0006 is Accepted with `ssh_direct`
pinned; otherwise run B. G3 failing is a C-17 breach: destroy at once,
record, run B.

### Fallback B trial

Throwaway image `FROM` the pinned `server-cuda` digest plus Caddy, pushed to
a private GHCR path and pulled with `image_login` (research §1.3). Its
entrypoint runs the same download and `llama-server` on `127.0.0.1:8080`,
and Caddy on 8443 with a self-signed certificate (`tls internal`) and a
bearer check, `env` `{"-p 8443:8443": "1"}` (research §1.3). The onstart
writes `/srv/onstart-marker`; Caddy serves it at `/spike/onstart`, so a 200
answers Q2. The client pins the certificate fingerprint read from the first
connection. Records: Dockerfile line count, image size, build and push
seconds, every extra step needed, and the same M2–M5 measurements over TLS.

### Safety and cost controls

| Control | Rule |
|---|---|
| Trap | `trap cleanup EXIT INT TERM HUP` installed before the create call; cleanup destroys by id and verifies absence; with no id (create timed out or the response was lost) it lists by the exact label and destroys every match |
| Destroy verify | `DELETE`, then `GET` every 10 s up to 10 times until absent; failure exits 2 with a loud message naming the instance id |
| Hard cap | a watchdog sends `TERM` to the script 40 min after `create_end`, regardless of state; the only extension is M10 step c, up to 55 min, and only while `actual_status = exited` |
| Host self-stop | onstart step 0 ends llama-server and PID 1 after `SPIKE_SELF_STOP_SECONDS`, without the laptop |
| Boot abort | no `running` 25 min after `create_end` → destroy; the run counts as a Q12 data point |
| Pre-flight | refuse to create if `dph_total × 40/60 + 1.00` exceeds `SPIKE_BUDGET_LEFT_USD` (the $1.00 covers bandwidth and storage) |
| Run count | at most 4 instances in total (A1, A2, B, one retry) |
| Backstop | second layer behind the host self-stop: the account balance at start is ≤ $10, so the worst loss is the balance (research §1.5) |
| Sweep | `launch.sh --sweep` after every session and after any crash |

### Security constraints

- No key, token, IP address or host name is written to the evidence, research §7 or the log committed anywhere; evidence names "direct" or "proxy" only.
- `WORKER_API_KEY` exists in the create body and in the script's memory only; the log records its length.
- The port scan targets only this instance's mapped ports: the public IP is usually shared with other tenants (research §1.6).
- The local event log lives in `$TMPDIR` and is not committed.

### Observability

The script writes one JSON line per event to `$TMPDIR/vkllm-spike-<ts>.jsonl`
with fields `run`, `event`, `t` (laptop `$EPOCHREALTIME`), `host_t`
(host-side events only), `detail` (status_msg, code, value). A final line
`summary` holds the stage table, measurements, `dph_total`, lifetime
seconds and estimated cost. The evidence tables are built from these lines.

## Requirements

- R1. The script MUST create at most one instance at a time, labelled `vkllm:spike:<unix-ts>`. (source: C-1; ADR 0003)
- R2. The script MUST install its destroy trap before the create call and MUST verify absence by `GET` after every destroy; when create returned no id, cleanup MUST sweep by the exact label. (source: C-4; C-2)
- R3. The script MUST end every instance by `DELETE`, never by a stop request. (source: C-5; ADR 0003 §6)
- R4. Every instance MUST be destroyed no later than 40 min after `create_end`, or 55 min when M10 holds it in `exited`. (source: C-1)
- R5. Total spend across all runs MUST NOT exceed $5.00 by the provider balance delta. (source: roadmap M1; C-1)
- R6. The search MUST use the production filter values and MUST NOT create an offer with `dph_total` above 0.80. (source: FR-6.1; FR-2.2)
- R7. llama-server MUST start with the ADR 0002 §3 arguments and MUST NOT listen on a non-loopback interface. (source: ADR 0002 §3; C-17; ADR 0006 §1)
- R8. The model MUST be fetched from Hugging Face with sha256 verification before the server starts. (source: ADR 0005 §1–2)
- R9. All eight cold-start stages MUST be timestamped with the names `search`, `create`, `boot`, `link`, `download`, `load`, `ready`, `first_token`. (source: ADR 0005 §4; FR-2.4)
- R10. The spike MUST answer Q1–Q3 and Q7–Q12 with a measured value or a recorded "moot" with its reason. (source: open-questions §1)
- R11. Fallback B MUST be tried only when bundle A fails the gate. (source: ADR 0006 Alternatives)
- R12. A key, token, address or host name MUST NOT appear in any committed file. (source: C-14)
- R13. Every external fact the write-up relies on MUST cite research §1–§3 or be a measurement in research §7. (source: C-32)
- R14. A recommended value that differs from a default stated in an Accepted ADR MUST be recorded as "needs ADR amendment", not applied. (source: C-31)
- R15. Every spike instance MUST start a host-side self-stop timer as the first onstart action, so it stops without the laptop. (source: C-1; C-8)
- R16. M10 MUST record which in-container action turns the instance `exited` and the balance delta while `exited`. (source: FR-4.7; C-8)

## Implementation steps

1. Write `onstart.sh` and `launch.sh --dry-run`, which prints the search body, the create body with `env` values redacted, and the onstart length. Check: `bash -n` and `shellcheck` clean; dry run asserts onstart ≤ 4048 characters (research §1.3) and makes no network call.
2. Add `--sweep`. Check: on an account with no spike instance it prints `0 instances with prefix vkllm:spike:` and exits 0.
3. Add `--run` up to `models_ok` with trap, watchdog and pre-flight. Checks: `SPIKE_BUDGET_LEFT_USD=0.10 launch.sh --run` refuses before create and exits non-zero; with `SPIKE_API_BASE` pointing at a local `nc -l` stub that never answers and `SPIKE_OFFER_ID` set, create times out, the log shows `cleanup: no id, sweeping label vkllm:spike:<ts>`, and the script exits 2 because absence is unconfirmed.
4. Local self-stop test: run `onstart.sh` in `docker run --init ubuntu:24.04` with `SPIKE_SELF_STOP_SECONDS=60` and `SPIKE_LOCAL_TEST=1` (skips apt, fetch and checksum; starts `sleep infinity` as the server). Check: the events log has `self_stop_fired` and the container exits within 90 s.
5. Pre-flight session: record balance, pick `SPIKE_IMAGE` digest, copy `SPIKE_MODEL_SHA256`, top the balance to ≤ $10. Check: values present in the local log.
6. Run A1 with M1–M10. Check: the JSONL has E0–E10 and a `summary` line; sweep reports 0.
7. Apply the gate. If needed, run A2, then B (build the image first; check: `docker build` succeeds and the image runs `--help` offline). Check per run: sweep reports 0.
8. Re-read the balance ≥ 1 h after the last destroy. Check: delta ≤ $5.00.
9. Write research §7, flip or amend ADR 0006, amend ADR 0005 if download > 300 s (ADR 0005 Consequences), close open-questions rows, fill Evidence, delete `hack/spike/`. Check: `make specs-check` passes.

### Recommended-value rules

| Key | Rule | Inputs |
|---|---|---|
| `provider.filter.minInetDownMBps` | r = measured download MB/s ÷ advertised `inet_down`. Value = 25,913 MB ÷ 180 s ÷ r, rounded up to 50. Keep 500 when the value ≤ 500. | Q8; file size research §3; 180 s from ADR 0005 Alternatives |
| `onDemand.maxWait` (future mode only) | 1.25 × cold start (E10 − E0), rounded up to 30 s, clamped to [300, 540] s; 540 keeps 60 s under the SDK's 600 s (research §4) | A run that passed the gate |
| `lifecycle.startupTimeout` | 2 × (E9 − E2), rounded up to the minute, clamped to [10, 30] min | same |

The census from the search table shows how many offers each candidate
`inet_down` threshold leaves.

## Tests / acceptance criteria

- AC1. Setup: run A1 done. Action: read the summary line. Expected: 8 stage durations, each a number of seconds ≥ 0, and a cold start total. (FR-2.3, FR-2.4)
- AC2. Expected: Q1 recorded as yes or no, with `link` seconds when yes. (NFR-7.3, ADR 0006 gate)
- AC3. Expected: Q3 recorded as `direct` or `proxy`; 5 tunnel and 5 on-host TTFT samples; tokens/s for both; bulk MB/s; channel drop count. (ADR 0006 Consequences)
- AC4. Expected: 5 tool-call round trips attempted, pass count recorded as n/5. (FR-1.4)
- AC5. Expected: download seconds, MB/s, fetcher name, advertised `inet_down`, aria2 install seconds, 60 s single-stream MB/s. (ADR 0005)
- AC6. Expected: `memory.used` and `memory.total` in MiB for V1–V4; V2 `prompt_tokens` between 58,000 and 64,000 with status code and needle result; V1 overflow status code. (Q9)
- AC7. Expected: median warm TTFT and median decode tokens/s through the tunnel, both with 5 samples. (NFR-10.1, NFR-10.2)
- AC8. Expected: Q11 recorded as yes or no. Q2 recorded as yes, no, or "moot: A accepted".
- AC9. Expected: `boot` seconds and the ordered `status_msg` values seen before `running`. (Q12)
- AC10. Expected: M1 output shows port 8080 bound to `127.0.0.1` only and the scan reports no open mapped port except SSH. (NFR-7.3, C-17)
- AC11. Expected: `/health` 200 without key; `/v1/models` 200 with the worker key and non-2xx without it; status codes recorded. (ADR 0006 §4)
- AC12. Setup: last run finished. Action: `launch.sh --sweep`, then `GET` each created id. Expected: `0 instances with prefix vkllm:spike:` and every id absent. (C-4)
- AC13. Action: balance read ≥ 1 h after the last destroy. Expected: start balance − end balance ≤ $5.00. (C-1)
- AC14. Expected: ADR 0006 status is Accepted or the ADR is amended to B; Q1–Q3 and Q7–Q12 marked closed with a pointer to research §7; the three recommended values present in research §7. (R10, R14)
- AC15. Setup: step 4 local test. Action: `docker run --init` with `SPIKE_SELF_STOP_SECONDS=60`. Expected: `self_stop_fired` logged 60 s ± 5 s after start, container exit code recorded, exit within 90 s. (R15)
- AC16. Setup: run A1 at M10. Expected: for steps a and b, `exited` yes/no with seconds; the 15 min balance delta while `exited` and the verdict "GPU billing stopped" yes/no. (FR-4.7, C-8, R16)
- AC17. Setup: step 3 stub check. Expected: the no-id cleanup line in the log and exit code 2. (R2)

## Definition of done

- [x] Run A1, and A2 or B where the gate required them, completed with summary lines. A1 and A2 both ran to a `summary` line (research §7.1–§7.5). B was **not** run, even though A2 also failed G5 and the gate's own decision rule literally says "otherwise run B": the project owner reviewed the real G5 numbers and explicitly chose to skip B (research §7.6), overriding that branch of the rule rather than the rule concluding B wasn't needed. Recorded, not hidden.
- [ ] AC1–AC17 hold, with numbers copied into Evidence and research §7. **Not fully true.** AC1–AC5, AC7–AC12, AC14–AC17 hold (see the AC table below). AC6 does not hold: the Q9 VRAM matrix was never obtained (every restart failed health in both runs). AC13 holds only partly: the $5 cap is not in doubt, but the specific "≥ 1 h after the last destroy" balance reading was never taken (research §7.5).
- [x] ADR 0006 Accepted or amended; ADR 0005 amended only if download > 300 s. ADR 0006 is Accepted (bundle A) with an explicit G5 amendment; ADR 0005 is amended, because one of the two real samples (403.9 s) exceeded 300 s even though the other (289.3 s) did not.
- [ ] Open-questions rows Q1–Q3, Q7–Q12 closed. **Eight of nine closed**: Q1, Q2, Q3, Q7, Q8, Q10, Q11, Q12. **Q9 is not closed** — every restart attempt in both runs failed to become healthy, so no VRAM measurement exists; it is recorded as genuinely open with root-cause context (research §7.2), which is the honest outcome but is not the "measured value or moot" R10 calls for. This is a known gap against R10's literal text, not papered over by marking Q9 closed.
- [x] The gate thresholds reviewed by the controller and the outcome noted in Evidence.
- [x] M10 result recorded in research §7 so spec 070 can cite it.
- [x] Any recommended value that differs from an Accepted ADR is listed as "needs ADR amendment".
- [x] `hack/spike/` deleted in the same pull request.
- [x] `make specs-check` passes in CI.
- [x] Status flipped to DONE and the folder renamed in the same PR; evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Which user name does Vast's SSH mode accept? | the tunnel dialer's config (spec 060) | this spike, from the first successful login | `root` |
| Are `env` values readable back through `GET /api/v0/instances/{id}`? | `WORKER_API_KEY` exposure (ADR 0006 §4, spec 070) | this spike inspects the GET response (spec 070 assigns it here too) | treat as readable by the account holder |
| Does Hugging Face publish the sha256 of the chosen GGUF? | `SPIKE_MODEL_SHA256` and spec 070's pin | operator before the run | compute on the host, record, spec 070 pins |
| Does `apt-get` reach a mirror quickly inside `server-cuda` on Vast? | aria2c vs curl choice in spec 070 | this spike, step 2 of onstart | single-stream curl |
| Does `--host` win over `LLAMA_ARG_HOST` when both are set? | spec 070 may skip the `unset` | spec 070; this spike unsets and proves sockets only | always unset |
| How fast does the `balance` field reflect charges? | AC13 timing | this spike, readings at end and ≥ 1 h later | use the larger delta |
| Does an in-container action (script exit, `kill -TERM 1`) turn an SSH-mode instance `exited`, and does GPU billing stop? | the host self-stop and C-8 depend on it | M10 in this spike | host timer does both; balance ≤ $10 as second layer |
| Can the on-demand contract's maximum duration (research §1.5) cap a spike instance? | a server-side stop independent of the container | not used; it is set by the host, not by us | host self-stop timer |
| Does `curl -sfIL` report the final `Content-Length` after Hugging Face redirects? | spec 070's curl fallback | onstart step 3 of this spike | fetch without a size check, rely on sha256 |
| Tool behaviour assumed, not in research: bash ≥ 5 `$EPOCHREALTIME`; Caddy `tls internal` self-signed | timing and fallback B | `launch.sh` fails at start without `$EPOCHREALTIME`; the B image build and first TLS connection | as stated |
| Is there a maximum `label` length? | production labels may be longer than the spike's 22 characters | spec 020; this spike records only that its label round-trips | ≤ 22 characters proven |
| Does one boot sample say anything about image cache frequency (Q12)? | cold-start budget | spec 140 adds samples | one sample, marked as such |

## Evidence

- `launch.sh`'s laptop-side JSON parsing (the `--run` measurement code, M1-M10)
  uses `jq`, already a dependency of this script since Task 3/4; no new
  dependency was added.
- M10's self-exit table says `pkill -f llama-server`; the implementation
  uses `pkill -x llama-server` instead, at all three call sites
  (`launch.sh`'s restart cycle and M10 step a, and `onstart.sh`'s self-stop
  timer). `-f` matches a process's full command line, so if `onstart`
  ever runs under a wrapper whose own argv happens to contain the text
  "llama-server" (unverified — whether Vast invokes `onstart` as
  `bash -c "<the whole script>"` is itself one of this spike's open
  questions), `-f` would also match and signal that wrapper. `-x` matches
  only the exact process name, so it can never do that.
- **Price cap raised to $1.00 for this run only.** The architecture's
  default `provider.filter.safety.maxHourlyPriceUSD` is $0.80 (CLAUDE.md);
  at run time the live L40S on-demand market had zero offers at or under
  $0.80 (the cheapest was $0.8015/h), so the user explicitly authorized
  raising `SEARCH_MAX_DPH_TOTAL` (and the matching `dph_total.lte` filter
  sent to the API — the two are now the same value, read from one
  constant) to $1.00 for this specific spike run. Per R14, this differs
  from the Accepted default and is not itself a decision to change that
  default; it needs its own review before becoming permanent.

### Pre-flight session (Task 8, read 2026-11-02, live, no spend)

- **Balance.** `GET /api/v0/users/current` (key via `-K -` on stdin, same
  pattern as `vast_call`) returned `balance: 0`, `credit: 25.0`. The
  endpoint's documented schema
  (https://docs.vast.ai/api-reference/accounts/show-user) names only
  `balance` ("the current balance of the user") and does not document
  `credit`; research §1.5 also records a CORE-020 probe (2026-11-02) that
  likewise read `balance: 0`. Whether that probe predates the $25 top-up
  is not recorded, but it shows `balance: 0` is not new, one-off
  behaviour for this account. The $25 of prepaid funds that are actually
  at risk live in the undocumented `credit` field. Treating the
  spendable figure conservatively as the higher number: **$25.00**.
- **Backstop check (implementation step 5 / brief step 4).** $25.00 exceeds the $10 backstop
  (research §1.5; this spec's Safety and cost controls table). **This
  check does not pass.** Manual action on the Vast.ai dashboard is needed
  to bring spendable funds to ≤ $10 before Task 9 runs; this session takes
  no such action (live, read-only, no-spend task).
- **Concern for the controller, not fixed in this task:** `launch.sh`'s
  `read_balance()` (around line 674) reads the `.balance` field for the
  before/after/+1h readings that R5, AC13 and M10 step c depend on. If
  Task 9's spend draws down `.credit` while `.balance` stays pinned at 0,
  every spend delta in that run reads as $0 and the $5 cap check (AC13)
  would be blind to real spend. The controller should decide whether
  `read_balance()` needs to read `.credit` (or both) before Task 9.
- **`SPIKE_IMAGE` digest (ADR 0002 §1, build ≥ b12032).** Queried GHCR
  directly: `docker manifest inspect ghcr.io/ggml-org/llama.cpp:server-cuda -v`
  and `GET https://ghcr.io/v2/ggml-org/llama.cpp/manifests/server-cuda`
  (anonymous pull token, `Accept: application/vnd.oci.image.index.v1+json`).
  The tag resolves to a two-platform (amd64, arm64) OCI index, index digest
  `sha256:0a9f6c7cfe5a6b90b6c048b7a86865bf55a88af34bada927482eab2ef5f25b4a`
  (the `Docker-Content-Digest` response header for the tag). Both
  platform manifests carry
  `org.opencontainers.image.version: "b12033"` and
  `org.opencontainers.image.created: "2026-11-02T06:12:11Z"`, i.e. built
  on the day of this pre-flight read and ≥ b12032. Chosen value for `SPIKE_IMAGE`:
  `ghcr.io/ggml-org/llama.cpp:server-cuda@sha256:0a9f6c7cfe5a6b90b6c048b7a86865bf55a88af34bada927482eab2ef5f25b4a`.
  (The amd64-only platform manifest digest,
  `sha256:ce9c9f0d45794f78f1c99bcb02c32509a40ba3a513d9d3222aaf6f7a98737cad`,
  same version/date, is recorded here too in case the index-digest form
  does not pull cleanly on the Vast host.)
- **`SPIKE_MODEL_SHA256`.** Ran the brief's exact command:
  `curl -sIL "https://huggingface.co/bartowski/Qwen_Qwen3.6-35B-A3B-GGUF/resolve/main/Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf"`.
  The redirect response carried
  `x-linked-etag: "ec438d1326eb11d8bb94785c54739cd4b60b01ae32bd8532e232a046b19d296d"`
  (64 hex characters, a valid sha256 shape) and
  `x-linked-size: 25913185920`, matching the 25,913,185,920-byte size
  already recorded in research §3. Per the brief, this value is used:
  **`SPIKE_MODEL_SHA256 = ec438d1326eb11d8bb94785c54739cd4b60b01ae32bd8532e232a046b19d296d`**.
  Cross-checked against the git LFS pointer file itself
  (`GET …/raw/main/Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf`, which for an LFS
  file returns the pointer, not the bytes): it reads
  `oid sha256:ec438d1326eb11d8bb94785c54739cd4b60b01ae32bd8532e232a046b19d296d`
  and `size 25913185920`, both matching the header exactly. Caveat: this
  is still Hugging Face's own recorded LFS oid, not a hash computed from
  downloaded bytes on this laptop or on the host; onstart's own
  `sha256sum` after download (step 4 of the onstart payload) remains the
  real verification and is expected to match.
- **Open-questions row closed in part:** "Does Hugging Face publish the
  sha256 of the chosen GGUF?" — yes, via `X-Linked-ETag` on the `resolve/`
  redirect; this spike no longer needs to compute it on the host from
  scratch, only to confirm the match.

### Real runs A1, A2 (Tasks 9–10, live, real spend, 2026-11-02)

Full numbers, per-stage timings, the ADR 0006 gate table, all M1–M10
measurements, the cost accounting and the recommended-value derivation
live in [`docs/research.md` §7](../../../docs/research.md). This section
is the pointer and the decisions that follow from it; it does not repeat
the numbers.

- Both runs' instances were independently confirmed destroyed by a direct
  `GET /api/v0/instances/{id}` (`{"instances": null}`) and by
  `launch.sh --sweep` (0 instances), not just trusted from the script's
  own log.
- **A1** (`ssh`): G1/G2/G4/G6 passed; G3 inconclusive (no confirming
  external scan, but no evidence of exposure either); G5 failed (804 ms
  TTFT delta against a 300 ms cap; tokens/s unaffected).
- **A2** (`ssh_direct` requested): G1/G2/G4/G6 passed; G3 passed
  conclusively; G5 failed (941 ms delta). Requesting `ssh_direct` did not
  change the observed SSH routing for this host — both runs were
  classified `proxy` by the script's own test — so direct mode's effect on
  G5 remains unconfirmed, not disproved.
- The gate's own decision rule ("A2 passes → Accepted with `ssh_direct`
  pinned; otherwise run B") has no clean branch for "A2 also fails G5, no
  genuinely-direct sample exists." The project owner reviewed this real
  data and made the call: Accept bundle A anyway, amend the G5 threshold,
  and skip fallback B — a sub-second latency miss with no throughput cost
  did not justify a custom TLS-proxy image.
- **ADR 0006** is now Accepted (bundle A), with the G5 threshold amendment
  recorded above and in the ADR itself.
- **ADR 0005** is amended: one real sample (403.9 s) exceeded the 300 s
  trigger, the other (289.3 s) did not. The S3 mirror is **not** promoted
  from this alone (two samples, real host variance); that call is deferred
  to spec 140's repeated runs.
- **M10 / ADR 0003 §7 flag.** In both runs, neither `pkill -x llama-server`
  nor a subsequent `kill -TERM 1` brought the instance to `actual_status:
  exited` within 120 s each. Only the script's own `DELETE` call ended
  either instance. [ADR 0003](../../../docs/adr/0003-gpu-lifecycle-management.md)
  §7 states that an exited instance stops GPU billing even when the
  control plane is down; this spike's data (2/2 real samples) suggests the
  in-container half of reaching `exited` does not hold within the window
  tested. **ADR 0003 is not amended by this spec** — out of this spike's
  explicit scope — but the finding is recorded here and in research §7.4
  so spec 070 or a dedicated review picks it up deliberately.
- **Q9 (VRAM matrix) stays open.** All eight restart attempts across both
  runs (V1–V4 twice) failed to become healthy; A2's run used a 420 s
  per-config timeout and still timed out at the full duration each time,
  ruling out "just needs more time" within that budget. Two unconfirmed
  root-cause candidates (the restart path's `--api-key-file` vs the boot
  path's inline `--api-key`; the restart path not repeating `onstart.sh`'s
  `unset LLAMA_ARG_HOST`) and new diagnostic logging added to `launch.sh`'s
  restart cycle in this session are recorded in research §7.2 for spec 140
  to retry with.
- **Cost.** A1 $0.3539 (`dph_total` $0.55/h), A2 $0.5380 (`dph_total`
  $0.8111/h) — combined $0.8919, well under the $5.00 cap (R5, AC13). A2's
  real price exceeds R6's literal $0.80 text but is within the $1.00
  ceiling this spec's Evidence already authorized for these runs. A real
  balance delta (not just the estimate) was captured for A1: $25.0000 →
  $24.5727 across the ~32 minutes between A1's destroy and A2's create,
  i.e. $0.4273, about 21% above A1's own estimate — plausibly bandwidth,
  billed separately by Vast. No balance reading was taken ≥ 1 h after A2's
  destroy, so AC13's exact stated check was not performed; the cap is not
  in doubt, but that specific reading is a recorded gap, not silently
  checked off.
- **Backstop deviation, recorded not fixed.** The account balance was
  **not** brought to ≤ $10 before these runs, a deviation from this spec's
  own Safety and cost controls table. The user was told accurately what
  the backstop covers (worst-case loss if every other safeguard fails at
  once, not expected spend) and chose to keep it at $25. Actual spend
  ($0.3539, $0.5380 per run) never came close to testing it.
- **`read_balance()` concern (raised in the Task 8 Evidence above) is
  resolved**: it reads `.credit` before falling back to `.balance`, so the
  before/after readings in both runs' `summary` lines reflect real
  spendable funds, not the `.balance` field that stays pinned at 0 on this
  account.

### AC1–AC17

| AC | Holds? | Where |
|---|---|---|
| AC1 | Yes | research §7.1 (8 stages + cold start total, both runs) |
| AC2 | Yes | research §7.2 Q1 |
| AC3 | Yes | research §7.2 Q3, §7.4 M1/M8 |
| AC4 | Yes | research §7.2 Q7, §7.4 M5 (5/5 both runs) |
| AC5 | Yes | research §7.1, §7.2 Q8 |
| AC6 | **No** | research §7.2 Q9, §7.4 M7 — never obtained, recorded as open |
| AC7 | Yes | research §7.2 Q10, §7.4 M4 |
| AC8 | Yes | research §7.2 Q2, Q11 |
| AC9 | Yes | research §7.1, §7.2 Q12 |
| AC10 | Yes | research §7.3 G3, §7.4 M1 (A2 pass; A1 inconclusive but not failed) |
| AC11 | Yes | research §7.3 G4, §7.4 M2 |
| AC12 | Yes | both runs' `--sweep` and direct `GET` confirmation, above |
| AC13 | **Partly** | research §7.5 — $5 cap not in doubt; the ≥1h-after-last-destroy reading was not taken |
| AC14 | Yes | this Evidence section; research §7.6, §7.7; `docs/open-questions.md` |
| AC15 | Yes (prior task) | implementation step 4's local Docker test, run before any real spend |
| AC16 | Yes | research §7.4 M10 — both steps, both runs; step c never ran (its precondition, `exited`, was never met) |
| AC17 | Yes (prior task) | implementation step 3's stub check |
