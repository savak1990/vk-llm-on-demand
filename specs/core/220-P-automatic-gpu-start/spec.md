---
id: "CORE-220"
status: "DRAFT"
updated: "2026-11-23"
depends_on: ["CORE-050", "CORE-055", "CORE-060", "CORE-110", "CORE-130"]
adrs: ["0007", "0010", "0011", "0012", "0015"]
---

# 220 — Automatic GPU start from a chat client

**Status note:** DRAFT, problem and requirements, plus the agreed direction
for progress feedback (2026-11-23). No full design and no
decision yet. Written 2026-11-22 while designing a household chat front end
(Open WebUI) for this service. That front end ships first with a manual
start; this spec is the follow-up that removes the manual step.

## Problem

Only a holder of a management token can start the GPU (`vk-llm start`, the
`gpu-control` workflow, or `POST /api/v1/gpu/start`). A household member who
opens a chat app while the GPU is stopped gets `503 backend_unavailable`,
and has no way to fix it. The operator must start the GPU before family use,
every time, and restart it after each 30-minute idle shutdown.

Two facts make this worse in a chat app:

- `GET /v1/models` answers from configuration with the GPU stopped, so the
  app shows the model as available and only fails when a message is sent.
- A cold start takes 3 to 6 minutes (measured 2026-11-22: 2m51s and
  6m20s), longer than most clients wait for a first byte.

## What exists today (verified 2026-11-22)

- Provisioning mode is `explicit` only. The chart schema enum allows only
  `explicit` (`deploy/helm/gpu-on-demand/values.schema.json`); the
  controller refuses any other value at start-up
  (`internal/controller/controller.go`).
- An **on-demand mode** is fully designed in ADR 0007 and never built: the
  gateway registers demand, holds the request in a bounded queue
  (`maxPending` 32) for up to `maxWait` 480 s, sends `: warming up <stage>`
  SSE heartbeats every 15 s on streaming requests, and returns
  `503 gpu_cold_start` with `Retry-After` on non-streaming timeouts. The
  Envoy route timeout must then be at least 1080 s (480 + 600). The gateway
  metrics for it (`vkllm_gateway_pending_requests`, `_wait_seconds`,
  `_cold_start_outcomes_total`) are registered and always zero; there is no
  `Demand` method, queue or heartbeat code.
- ADR 0010 deferred on-demand mode for three reasons: it couples spend to
  any authenticated request, it makes the first request of a session slow,
  and the start decision was to stay with the user. A hybrid (explicit plus
  auto-start on the first request) was rejected as "two triggers for one
  expensive action". ADR 0010's own Risks section names this spec's problem:
  "Users forget to start the GPU and see 503s."
- Management tokens are unscoped: any token can `start`, `stop`, read
  `status`/`cost`, and raise the monthly budget up to the Helm ceiling
  (`POST /api/v1/gpu/budget`). A token handed to a chat app could do all of
  that.
- `start` is idempotent and coalesced; repeated calls never create a second
  instance and never move the idle deadline (ADR 0011 §2).
- Client keys are per line with a key id; logs carry `key_id`, metrics do
  not. Keys hot-reload every 10 s without a restart.
- Spend controls today: monthly budget ($30, runtime-mutable up to $100),
  `maxHourlyPriceUSD` 0.80, `maxLifetime` 12h, 3 provision attempts per hour,
  30-minute rolling idle timeout. There is no per-user or per-key quota.

## Candidate approaches (to evaluate, not chosen)

1. **On-demand mode per ADR 0007.** The first chat request starts the GPU
   and waits. Largest change: gateway queue, heartbeats, controller
   `Demand`, schema, Envoy timeout.
2. **Start-only scope plus a "Start GPU" control in the chat app.** A new
   token scope that can call `start` and `status` and nothing else, used by
   an Open WebUI function or button. Smallest change to this service; the
   user still waits and retries by hand.
3. **Per-key auto-start opt-in.** Explicit mode stays the default; a client
   key marked "may start" turns its first chat request into a `start` plus
   an immediate, informative error ("starting, ready in about N minutes").
   A hybrid, so it needs ADR 0010 amended.

## Progress feedback while the user waits (direction agreed 2026-11-23)

Whichever approach holds a request during a start, the gateway answers a
streaming request at once with `200 text/event-stream` and keeps the user
informed in the same stream, instead of returning `503`:

1. **Checkpoint lines**, one per real milestone, built from the
   controller's state and the chosen offer, for example:
   - "Looking for a GPU offer…" (`ALLOCATING`)
   - "Rented an RTX 6000Ada in Sweden at $0.55/h (machine 117871); booting the
     worker…" (offer chosen; `Offer` already carries `GPUName`,
     `Geolocation`, `Hourly`, `MachineID`)
   - "Worker up; downloading and loading the model (28.6 GB)…"
     (`LOADING_MODEL`)
   - "Ready." (`READY`)
2. **A ticker:** every 30 s with no checkpoint, one phrase chosen at random
   from a predefined set ("Still warming up the GPU…", "Almost there…"),
   never the same phrase twice in a row. The set is a chart value with a
   built-in default.
3. **Keep-alive:** an SSE comment line every 15 s, invisible to the user,
   so no client or proxy closes an idle connection between ticker lines.
4. **The real answer** follows in the same stream: at `READY` the gateway
   forwards the original request to the worker and pipes the model's chunks
   after the progress lines. The user never retries.

Two kinds of timeout apply to a held stream, and the ticker fixes only the
first:

- **Idle timeouts** ("no byte for N seconds"): in clients and proxies. The
  15 s keep-alive and the 30 s ticker keep every byte gap short, so these
  never fire.
- **Total timeouts** ("the whole response must end within N seconds"):
  bytes do not reset them. The gateway's own HTTP server sets none
  (`WriteTimeout: 0`), and Open WebUI sets none by default. The Envoy route
  does: `requestTimeout: 660s` (CORE-130). A held request can need the cold
  start (up to about 6.5 min measured) plus generation (up to
  `model.generationTimeout`, 10 min), so the route's total timeout MUST be
  raised to at least 1080 s, as ADR 0007 §4 already states, or disabled.

Checkpoint and ticker lines go out as `reasoning_content` deltas (the
"thinking" channel), never as `content`. OpenCode, Msty and Open WebUI show
that channel as a collapsible thought; clients do not store it as the
answer and do not resend it in the next turn, so the progress text never
enters the conversation history; and in an agent loop (OpenCode) a thought
does not end the turn, where `content` such as "Almost there…" would be
taken as the final answer. Every line is a truthful service status,
prefixed so it does not read as the model's own reasoning.

## Requirements

- R1. A chat-client user who holds no management token MUST be able to get
  the GPU started, but only through a client the operator explicitly
  enabled for it. Explicit mode MUST remain available and stay the default.
- R2. Every existing spend control MUST still apply to an automatic start:
  monthly budget, `maxHourlyPriceUSD`, `maxLifetime`, attempts per hour. A
  start denied by one of them MUST reach the user as a readable reason, not
  a bare 503.
- R3. The user MUST see that a start is in progress and roughly how long it
  takes, in the chat app itself, instead of `backend_unavailable`.
- R4. No client MUST fail a request because of the cold start's length
  alone: the design MUST state, per supported client (Open WebUI, OpenCode,
  Chatbox, Msty, Conduit), what timeout each applies and how the design
  stays inside it, including the Envoy route timeout.
- R5. Requests arriving during a start MUST NOT cause a second instance,
  and MUST NOT count as activity before the GPU is `READY` (ADR 0011).
- R6. Every automatic start MUST be attributable: the audit log MUST record
  which key triggered it.
- R7. A credential that can trigger a start MUST NOT also be able to stop
  the GPU or change the budget.
- R8. A background request (title, tag, follow-up generation) from a chat
  app MUST NOT by itself start the GPU, or the design MUST explain why that
  is acceptable.
- R9. While a start is in progress, a streaming chat request MUST get
  `200 text/event-stream` at once, then progress lines, then the model's
  real answer in the same response.
- R10. Progress lines MUST be sent only as `reasoning_content` deltas,
  never as `content`, and MUST carry a prefix that marks them as service
  status, not model output.
- R11. The gateway MUST send a checkpoint line at each state change
  (`ALLOCATING`, offer chosen, `LOADING_MODEL`, `READY`); the offer-chosen
  line MUST name the GPU, its location, its hourly price and the machine id.
- R12. Between checkpoints the gateway MUST send one phrase from a
  configurable set every 30 s, chosen at random and never the same phrase
  twice in a row, and an SSE comment every 15 s.
- R13. If the start fails or is denied, the stream MUST end with the reason
  as a progress line followed by an in-stream error and `[DONE]`.
- R14. Time-to-first-token metrics MUST measure the model's first chunk,
  not the first progress line; the wait MUST be measured separately
  (`vkllm_gateway_wait_seconds` already exists).
- R15. A non-streaming request MUST NOT receive progress lines; the design
  MUST state whether it waits silently or is refused.
- R16. The Envoy route's total request timeout MUST be at least the longest
  expected cold start plus `model.generationTimeout` (at least 1080 s), or
  disabled; keep-alive bytes do not reset a total timeout.

## Open questions

| Question | Why it matters |
|---|---|
| Hold the request (ADR 0007) or fail fast with a clear message and let the user retry? | Holding needs heartbeats and long timeouts end to end; failing fast is simpler but needs a retry |
| Which clients tolerate a 3–6 minute held stream with SSE comments? | Open WebUI's backend, Conduit, OpenCode (which retries up to 5 times) behave differently |
| Does a cold start with an uncached worker image (10–60 min) stay out of scope? | ADR 0007 §4 already excludes it from the wait |
| Which model and GPU does an automatic start use? | CORE-200 (per-start parameters) may make them choosable; an automatic start needs a default |
| Should CORE-180 (cached model volume) land first? | A shorter cold start makes every approach above better |
| Per-key or per-user spend limits | An enabled household client can spend up to the monthly budget today |
| Can the worker report download progress (bytes of 28.6 GB)? | A "42%" checkpoint is more useful than a ticker phrase; today only the controller state is known |
| Does OpenCode's AI SDK provider tolerate SSE comment lines and a first chunk that carries only `reasoning_content`? | R9/R10; must be tried against a real stream |
| Does Open WebUI's optional between-chunks idle cap stay off by default? | Its total upstream timeout is off by default in v0.13.4 (`AIOHTTP_CLIENT_TIMEOUT` unset) |

## Out of scope

The chat front end itself (Open WebUI deployment, a platform spec).
Multiple concurrent GPUs. Uncached image pulls inside a held request.

## Evidence

None. Problem and requirements only.
