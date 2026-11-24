---
id: "CORE-220"
status: "IN_PROGRESS"
updated: "2026-11-23"
depends_on: ["CORE-050", "CORE-055", "CORE-060", "CORE-070", "CORE-100", "CORE-130", "CORE-210"]
adrs: ["0019", "0007", "0010", "0011", "0012", "0006", "0018"]
---

# 220 — Automatic GPU start from a chat client

**Status note:** IN_PROGRESS (2026-11-23), implementation under way on
branch core-220-design. Problem written 2026-11-22 while
designing the Open WebUI front end (CORE-230), which ships first with a
manual start. Design agreed with the user on 2026-11-23 in a question-by-
question review; the decisions are recorded in ADR 0019. Facts about the
chat clients were read from their source code on 2026-11-23 (Open WebUI
v0.13.4, OpenCode v2.3.2, Chatbox v1.24.4, Conduit v4.3.2; Msty is closed
source).

**Complexity:** L — a new controller method and start origin, a gateway
wait queue with a progress writer, a history filter, download progress
read over the SSH tunnel, and chart and platform changes.
**Risk:** high — an inference request now spends money. A wrong cancel
rule either leaks a GPU or kills a start the user still waits for.
**Estimated effort:** 3–4 sessions (12–16 h)
**Recommended model:** strong — the change crosses the controller/gateway
boundary on the cost-critical start path.
**Depends on:** [CORE-050](../050-D-gpu-lifecycle-controller/spec.md),
[CORE-055](../055-D-management-api/spec.md),
[CORE-060](../060-D-gateway-openai-api/spec.md),
[CORE-070](../070-D-worker-runtime/spec.md),
[CORE-100](../100-D-cost-controls/spec.md),
[CORE-130](../130-D-platform-integration/spec.md).
Related, not required: [CORE-180](../180-P-persistent-volume-cache/spec.md)
(a cached model shortens every wait).

## Outcome

With `config.lifecycle.provisioningMode: on-demand`, a household member
sends a chat message while the GPU is stopped and, in the same chat
bubble, sees a collapsible "thinking" block that reports the offer search,
the rented GPU with its location, price and machine id, the model download
percentage, the checksum and the model load, then reads the model's real
answer. Nobody runs `vk-llm start` and nobody retries. If the user closes
the app before the GPU is ready, the start is cancelled within about a
minute. Explicit mode stays the chart default and behaves as today.

## Scope

Controller: `Demand`, the demand deadline and its cancellation, the
persisted start origin, the `gpu_start` audit line, `Geolocation` in the
stored offer, download progress over the tunnel. Gateway: the wait queue,
the progress stream, the immediate `503 gpu_cold_start` for non-streaming
requests, the history filter, the cold-start metrics; the milestone tracker
moves to `internal/progress`, shared with the CLI. Management API: the
status document gains `instance.geolocation` and a `progress` object.
`vk-llm start` prints the same milestone lines on stderr. Tunnel: an exec
session. Chart: the mode enum, the `onDemand` block.
Platform: one `vk-lab-platform` pull request.

Excludes: the chat front end itself (CORE-230); per-key or per-user spend
limits (accepted risk, ADR 0019); a per-key "may start" mark (rejected,
ADR 0019); per-start model or GPU choice (CORE-200; an automatic start uses
the chart defaults, and the one supported model is the one the chat app
is configured with); an uncached worker image inside a held request (it
surfaces as a `maxWait` expiry); multiple concurrent GPUs.

## Design and contracts

### Configuration

| Chart value | Env | Default | Meaning |
|---|---|---|---|
| `config.lifecycle.provisioningMode` | `VKLLM_LIFECYCLE_PROVISIONING_MODE` | `explicit` | enum gains `on-demand` (schema and controller) |
| `config.onDemand.maxWait` | `VKLLM_ONDEMAND_MAX_WAIT` | `600s` | longest a stream is held |
| `config.onDemand.maxPending` | `VKLLM_ONDEMAND_MAX_PENDING` | `32` | held streams at once |
| `config.onDemand.heartbeatInterval` | `VKLLM_ONDEMAND_HEARTBEAT_INTERVAL` | `15s` | SSE comment and demand refresh |
| `config.onDemand.tickerInterval` | `VKLLM_ONDEMAND_TICKER_INTERVAL` | `30s` | silence before a ticker phrase |
| `config.onDemand.demandGrace` | `VKLLM_ONDEMAND_DEMAND_GRACE` | `60s` | how long an unattended start survives |
| `config.onDemand.tickerPhrases` | `VKLLM_ONDEMAND_TICKER_PHRASES` (newline-separated) | built-in set | empty means the built-in set |

The download percentage uses the model file size CORE-210's
`internal/modelsize` resolver already puts on the chosen offer
(`store.Offer.Estimate.ModelBytes`); there is no chart value for it. An
offer chosen before that resolver ran, or one with no estimate, shows GB
downloaded with no percentage.

### Controller

```go
// Demand starts the GPU through the same gates as Start and extends the
// in-memory demand deadline. It never moves the idle deadline.
func (c *Controller) Demand(ctx context.Context, keyID string, until time.Time) (Snapshot, error)
```

- Only in on-demand mode; in explicit mode it returns `ErrBackendUnavailable`.
- State `Idle`: same path as `Start` (`gateReason`: budget, balance,
  attempts, no-offer window, backoff). A denial returns `ErrGPUUnavailable`
  with `Snapshot.Unavailable` set, as `Start` does today. Origin becomes
  `demand`.
- State `Searching` to `Loading`: coalesced, no second instance (ADR 0011
  §2). The deadline becomes `max(deadline, until)`.
- State `Ready` or `Serving`: no-op.
- State `Draining` or `Destroying`: returns `ErrDraining`.

**Start origin.** `store.Document.StartOrigin` is `explicit` or `demand`,
written when a start is accepted (T1, or the backoff row that records the
desire). `Start` during a `demand` start rewrites it to `explicit`. Absent
(old documents) reads as not `demand`, so nothing is cancelled.

**Cancellation.** On every loop tick, if the state is `Searching`,
`Creating`, `Booting` or `Loading`, the origin is `demand`, and now is past
the demand deadline, the controller processes `EvStop` (existing
transitions: `Searching → Idle`, the others `→ Destroying`) and logs
`gpu_start_cancelled reason=demand_expired`. The demand deadline lives in
memory only: after a pod restart, recovery sets it to now + `demandGrace`,
because a restart drops every held request.

A cancelled create still counts against `maxProvisionAttemptsPerHour`
(the counter moves at T5). Accepted in ADR 0019.

**Audit.** When a start moves `Idle → Searching`, the controller logs one
`gpu_start` line with `origin` (`explicit` or `demand`), `key_id` and
`generation`. `Demand` passes the inference key's id. An explicit start
logs an empty `key_id`; its management token is already in the
`mgmt_request` line (`op=start`, `key_id`).

**Offer.** `store.Offer` gains `Geo string` from
`provider.Offer.Geolocation`; `Snapshot` gains `Geolocation`.

**Download progress.** While the state is `Loading` and the dialer is SSH,
the controller runs one command over the tunnel on every readiness poll
(5 s), through a new `Worker.Progress` method:

```sh
cat /var/log/vkllm-stages.log 2>/dev/null
echo "VKLLM_BYTES $(du -cB1 /models/model.gguf /models/model.gguf.[0-9]* 2>/dev/null | tail -1 | cut -f1)"
```

`du` counts allocated blocks, which is correct because `onstart.sh.tmpl`
already runs `aria2c --file-allocation=none` and the `curl` fallback writes
part files; the apparent file size would be wrong for a sparse file. A new
file `internal/worker/download.go` holds
`ParseProgress(out []byte) (Progress, error)`, which reuses `ParseStages`
for the stage lines and reads the `VKLLM_BYTES` line. `Snapshot` gains
`DownloadedBytes`, `ModelBytes` (from the chosen offer's resolved
`Estimate.ModelBytes`, CORE-210) and `WorkerStages` (stage names seen so
far). A failed read keeps the last value; it never changes the state.

**Tunnel.** `tunnel.Conn` gains `Exec(ctx, cmd string) ([]byte, error)`,
one `ssh.Session` per call on the existing `*ssh.Client`. The direct TCP
dialer (kind job) returns `tunnel.ErrExecUnsupported`; progress lines then
show no download figures.

### Gateway

The gateway's `Controller` interface gains `Demand` only; `Start` and
`Stop` stay absent, so an inference key cannot stop the GPU or change the
budget.

**Admission in on-demand mode.** When `Admit` returns `ErrBackendUnavailable`:

| Request | Answer |
|---|---|
| `stream: true`, queue not full | held: progress stream (below) |
| `stream: true`, queue full | `429 queue_full`, `Retry-After: 15` |
| `stream: false` | `Demand(key, now + retryAfter + demandGrace)`, then `503 gpu_cold_start`, `Retry-After: retryAfter` |
| any, `Demand` denied | `503 gpu_unavailable` with the readable reason, no `Retry-After` |

When `Admit` returns `ErrDraining`: `503 backend_unavailable` with the
message "GPU is shutting down; try again in a minute" and
`Retry-After: 60` (outcome `shutting_down`). In explicit mode nothing
changes.

`retryAfter` = max(30 s, 360 s − time since the start began). 360 s is
the longest measured cold start, rounded down to whole minutes.

**Held stream.** Lifecycle of one held request:

1. Write `200`, `Content-Type: text/event-stream`, flush, then send the one
   visible status line (below) once, before anything else.
2. Call `Demand(key, now + demandGrace)`. On denial go to step 6.
3. Loop until `READY`, denial, failure, `maxWait`, or client disconnect:
   - every 2 s, read a `Status()` snapshot that is cached for 2 s and
     shared by every held request;
   - a new milestone sends a checkpoint line;
   - every `heartbeatInterval`: write `: keep-alive` and call
     `Demand(key, now + demandGrace)`;
   - after `tickerInterval` with no line sent: send a ticker phrase, never
     the one sent last.
4. At `READY`: `Admit(gen)` and forward the original body. Response
   headers are already sent, so the gateway pipes the upstream chunks only.
5. Disconnect: leave the queue and stop refreshing demand (outcome
   `client_gone`). The controller cancels after `demandGrace` if no other
   request refreshes the deadline.
6. Denial, failure or `maxWait`: send the reason as a progress line, then
   `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"error":{...}}`,
   then `data: [DONE]`. After `maxWait` the gateway first calls
   `Demand(key, now + 360 s + demandGrace)`, then the line says the GPU
   is still starting and to send the message again within 5 minutes; the
   start continues. Gateway shutdown ends every held stream the same way
   with code `shutting_down`.

**Visible status line.** Sent exactly once per held stream, right after the
`200 text/event-stream` headers and before any progress line, as a
`delta.content` chunk with no prefix:

```json
{"id":"vkllm-progress","object":"chat.completion.chunk","created":<unix>,
 "model":"<alias>","choices":[{"index":0,
 "delta":{"role":"assistant","content":"⏳ Starting the GPU (3–6 min). Progress is in the thinking block.\n\n"},
 "finish_reason":null}]}
```

**Progress line.** One SSE chunk per line:

```json
{"id":"vkllm-progress","object":"chat.completion.chunk","created":<unix>,
 "model":"<alias>","choices":[{"index":0,
 "delta":{"role":"assistant","reasoning_content":"vk-llm › <text>\n\n"},
 "finish_reason":null}]}
```

Checkpoints, each sent once, in this order:

| Trigger | Text |
|---|---|
| held, state `ALLOCATING` | Looking for a GPU offer… |
| `MachineID` set | Renting an `<GPU>` in `<Geolocation>` at $`<Hourly>`/h (machine `<MachineID>`); booting the worker… |
| state `LOADING_MODEL` | Worker up; downloading the model (`<size>`)… |
| downloaded bytes cross each 10 % | Downloading the model: `<p>`% (`<x>` of `<y>` GB) |
| stage `download_end` | Download done; verifying the checksum… |
| stage `server_spawn` | Checksum OK; loading the model into the GPU… |
| state `READY` | Ready. |

A request that joins mid-start gets only the milestones that are true at
that moment. When the chosen offer has no resolved model size the
download lines show GB downloaded at each 2 GB step instead. Built-in
ticker phrases: "Still warming up the GPU…", "Almost there…", "The GPU is
still getting ready…", "Still working on it…", "Hang on, nearly ready…".

**History filter.** Before every forward (held or not): for each message
with a string `reasoning_content`, delete each line that starts with
`vk-llm › ` or the legacy `[vk-llm] ` and the blank line after it; drop the
field when it becomes empty. For each **assistant** message whose string
`content` starts with the visible status line, remove that line and the
blank line after it; if `content` becomes empty, keep it as `""`. Nothing
else in the body changes.

**Design note (2026-11-24).** Live testing with OpenCode v2.3.2 (thinking
hidden by default) showed a held stream with no visible token looking
stuck. Open WebUI, Conduit and Chatbox glue the thinking block to the
answer or may use the first 100 characters of the answer for the chat
title, so one short, prefix-free `content` line up front covers every
client without polluting the title or the answer; the line is stripped
from history so the model never sees it. The old `reasoning_content`
prefix `[vk-llm] ` rendered as an underlined link in OpenCode's markdown
viewer; `vk-llm › ` renders as plain text.

**Metrics.** `vkllm_gateway_pending_requests` (held streams),
`vkllm_gateway_wait_seconds` (hold start to `READY`),
`vkllm_gateway_cold_start_outcomes_total{outcome}` with the pre-seeded
labels. `vkllm_gateway_ttft_seconds` starts at the forward in step 4, so
it measures the model's first chunk, never a progress line.

### Timeouts per client

| Client | Total timeout | Idle timeout | How the design fits |
|---|---|---|---|
| Open WebUI | none (`AIOHTTP_CLIENT_TIMEOUT` unset) | none (`AIOHTTP_CLIENT_STREAM_IDLE_TIMEOUT` unset) | fits; if an operator sets an idle cap, 15 s comments stay below it |
| Conduit | Open WebUI's (tokens arrive over Socket.IO) | Open WebUI's | same as Open WebUI |
| OpenCode | none; `headerTimeout` 300 s | `chunkTimeout` 900 s | `200` is sent at once; byte gaps ≤ 15 s |
| Chatbox | none found | none found | fits |
| Msty | unconfirmed (closed source) | unconfirmed | manual test AC14 |
| Envoy route | 1200 s (raised from 660 s) | n/a | 600 s `maxWait` + 600 s `model.generationTimeout` |

### Background requests

Open WebUI sends title, tag and follow-up requests with `stream: false`
to the chat model; OpenCode and Chatbox stream their title request. Any of
them can call `Demand`. This is acceptable: each one follows a user's chat
message, which has already started the GPU, and `Demand` coalesces.

## Requirements

- R1. With `provisioningMode: on-demand`, any valid inference key MUST be
  able to start the GPU through a chat request. `explicit` MUST stay the
  chart default and keep today's behaviour (ADR 0019 §1, ADR 0010).
- R2. An automatic start MUST pass every gate `POST /api/v1/gpu/start`
  passes: budget, balance, `maxHourlyPriceUSD`, attempts per hour,
  backoff. A denial MUST reach the user as a readable reason (ADR 0019 §2).
- R3. The user MUST see, in the chat app, that a start is in progress and
  how far it has got (ADR 0019 §3).
- R4. No supported client MUST fail because of the cold start's length;
  the per-client table above MUST hold (ADR 0019 §7).
- R5. Requests during a start MUST NOT create a second instance and MUST
  NOT move the idle deadline before `READY` (ADR 0011).
- R6. Every start MUST log one `gpu_start` line naming its origin; a
  demand start MUST also name the inference key id.
- R7. The gateway MUST NOT be able to call `Start` or `Stop` or change
  the budget; it gets `Demand` only (ADR 0012, ADR 0019 §2).
- R8. A background request MAY call `Demand`; the reason is stated under
  Background requests.
- R9. A streaming request held for a start MUST get `200
  text/event-stream` at once, then progress lines, then the model's answer
  in the same response.
- R10. Progress lines MUST be `reasoning_content` deltas prefixed
  `vk-llm › `, except one visible `content` status line (exact text above)
  sent once at the start of the hold.
- R11. The gateway MUST send each checkpoint in the table at most once, in
  order; the offer line MUST name the GPU, location, hourly price and
  machine id.
- R12. The gateway MUST send a ticker phrase after `tickerInterval` without
  a line, never the same phrase twice in a row, and an SSE comment every
  `heartbeatInterval`.
- R13. A denied, failed or timed-out held stream MUST end with a reason
  line, a chunk with `finish_reason` and an `error` object, and `[DONE]`.
- R14. `ttft_seconds` MUST measure the model's first chunk;
  `wait_seconds` MUST measure the hold.
- R15. A non-streaming request with no Ready backend MUST call `Demand`
  and get `503 gpu_cold_start` with `Retry-After` at once (ADR 0019 §5).
- R16. The Envoy route total timeout MUST be at least 1200 s in on-demand
  mode (ADR 0019 §7).
- R17. A `demand`-origin start MUST be cancelled when the demand deadline
  passes before `Ready`; an `explicit`-origin start MUST NOT be. A
  `maxWait` expiry MUST extend the deadline by 360 s + `demandGrace`
  (ADR 0019 §6).
- R18. The gateway MUST remove `vk-llm › ` and legacy `[vk-llm] ` lines
  from incoming `reasoning_content`, and the visible status line from an
  assistant message's `content`, before forwarding (ADR 0019 §4).
- R19. `vk-llm start` MUST print the same milestone lines as the chat
  progress stream, on stderr, from the status document; `vk-llm stop` is
  unchanged.

## Implementation steps

1. Config and chart: mode enum, `onDemand` block, schema. Check: config
   tests; `make helm-lint helm-template`.
2. Store and snapshot: `Offer.Geo`, `Instance.StartOrigin`, snapshot
   fields. Check: store round-trip test with an old document.
3. Controller `Demand`, start origin, `gpu_start` log, demand deadline and
   cancellation, recovery reset. Check: AC1–AC5 with the fake clock and
   fake provider.
4. Tunnel `Exec`; `internal/worker/download.go` `ParseProgress`; the
   progress read on every 5 s readiness poll in `Loading`. Check: AC6; an
   `Exec` test against an in-process SSH server.
5. Gateway admission table and the non-streaming `503 gpu_cold_start`.
   Check: AC7, AC8.
6. Gateway held stream: queue, shared poller, progress writer, ticker,
   heartbeat, forward at `READY`, error tail. Check: AC9–AC12 with the
   fake controller and fake clock.
7. History filter. Check: AC13.
8. Docs and platform: README on-demand section, spec 080 values table;
   `vk-lab-platform` PR with `provisioningMode: on-demand` and the 1200 s
   BackendTrafficPolicy. Check: `make specs-check`; platform PR merged.
9. Live check on the lab with Open WebUI, Conduit and OpenCode. Check:
   AC14–AC16.

## Tests / acceptance criteria

- AC1. `Demand` from `Idle` with budget left runs T5 once; ten concurrent
  `Demand` calls create one instance.
- AC2. `Demand` with the budget spent returns `ErrGPUUnavailable` with
  `Unavailable == "budget_exhausted"` and makes no provider call.
- AC3. The demand deadline passes in `Booting` with origin `demand`: state
  becomes `Destroying` and one `gpu_start_cancelled` line is logged. Same
  with origin `explicit`: no transition.
- AC4. `Start` during a `demand` start sets origin `explicit`; the
  deadline passing afterwards changes nothing.
- AC5. `Demand` never changes `IdleDeadline`. A restart in `Loading` with
  origin `demand` and no request cancels after `demandGrace`.
- AC6. `ParseProgress` on fixture output returns the bytes and stages; a
  missing `VKLLM_BYTES` line returns zero bytes and no error.
- AC7. On-demand, GPU stopped, `stream:false`: `503`, code
  `gpu_cold_start`, `Retry-After` ≥ 30, `Demand` called once.
- AC8. Explicit mode: the `backend_unavailable` body is byte-identical to
  today's.
- AC9. A held stream with a scripted controller (`ALLOCATING` → offer →
  `LOADING_MODEL` with bytes at 0 %, 30 % and 100 % → stages → `READY`)
  emits the visible status line as the first chunk, once, then the
  checkpoint lines in order, each once, then the fake worker's chunks;
  every other progress chunk has `reasoning_content` and no `content`.
- AC10. No state change for 600 s (fake clock, 2 s polls): at least 30
  comments, at least 15 ticker phrases, no phrase twice in a row, at least
  30 demand refreshes, then the `maxWait` tail.
- AC11. A `maxWait` expiry and a denial each end with a reason line, a
  `finish_reason` chunk with `error`, and `[DONE]`.
- AC12. With 32 streams held, the 33rd gets `429 queue_full`.
- AC13. An assistant message with `reasoning_content`
  `"vk-llm › A\n\nreal thought\nvk-llm › B\n\n"` is forwarded as
  `"real thought\n"`; the same holds for the legacy `[vk-llm] ` prefix. An
  assistant message whose `content` starts with the visible status line
  is forwarded with that line removed.
- AC14. Live, lab, GPU stopped: one message from Open WebUI (desktop) and
  from Conduit shows the thinking block and then the answer. Msty does the
  same, or its result is recorded as unsupported in the README.
- AC15. Live: OpenCode completes a turn after a cold start, and the next
  turn's request to the worker carries no `vk-llm › ` line and no visible
  status line.
- AC16. Live: close the app during `ALLOCATING`; the instance is destroyed
  within `demandGrace` + 60 s and no instance remains.
- AC17. `vk-llm start` with a scripted status walk whose documents carry
  an instance (gpu, geolocation, price, machine id) and a progress object
  at 0 %, 40 % and `download_end` prints the offer line, the 40 % download
  line and the verifying-checksum line in order on stderr, and the final
  line is still the `ready:` line; a walk with no instance or progress
  fields prints exactly the state-change and `ready:` lines, unchanged.

## Definition of done

- [ ] AC1–AC13 and AC17 pass (tier 1/2); AC14–AC16 recorded under Evidence.
- [ ] `make specs-check go-build go-test go-lint helm-lint helm-template`
      all exit 0.
- [ ] `vk-lab-platform` PR merged: `provisioningMode: on-demand`, route
      timeout 1200 s.
- [ ] README documents on-demand mode, its cost risk and Msty's result.
- [ ] Status DONE, folder renamed `220-D-...`, Evidence filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Does each client accept a chunk `id` change between the progress lines and the model's answer? | a strict client could drop the answer | AC14, AC15 | if one fails, the gateway rewrites upstream chunk ids to `vkllm-progress` |
| Msty's timeouts and reasoning display | closed source | AC14 | documented as unsupported if it fails |
| Should `retryAfter` use measured stage timings instead of 360 s? | accuracy of the hint | Evidence after AC14 | 360 s constant |

## Evidence

None yet.
