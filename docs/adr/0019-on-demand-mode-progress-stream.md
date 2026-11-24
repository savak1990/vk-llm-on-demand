# ADR 0019: On-demand mode streams progress, refuses non-streaming waits, and cancels unattended starts

## Status

Accepted (2026-11-23). Implements the on-demand mode that
[ADR 0010](0010-explicit-provisioning-mode.md) §4 left for later, and amends
[ADR 0007](0007-cold-start-api-strategy.md) §2 and §4. Explicit mode stays
the chart default. Spec CORE-220 carries the contracts.

## Context

A household member who opens a chat app while the GPU is stopped gets
`503 backend_unavailable` and cannot fix it; only a management-token holder
can start the GPU. ADR 0010 named this risk ("users forget to start the GPU
and see 503s") and kept ADR 0007 as the design for a future on-demand mode.

Three things changed since ADR 0007 was written:

- Chat apps (Open WebUI, Conduit, OpenCode, Chatbox) show
  `reasoning_content` as a collapsible thought, so a wait can be shown to
  the user instead of hidden behind invisible SSE comments.
- Measured cold starts are 2m51s and 6m20s (2026-11-22), so a 480 s wait
  leaves little margin.
- The controller can already cancel a start: `EvStop` moves `Searching`
  to `Idle` and `Creating`, `Booting` or `Loading` to `Destroying`.

All inference keys belong to the operator's household. The monthly budget,
`maxHourlyPriceUSD`, the attempts-per-hour cap and the single-instance rule
bound what an automatic start can spend.

## Decision

### 1. Mode

`config.lifecycle.provisioningMode: on-demand` is implemented. `explicit`
stays the default, so a fresh install spends nothing until the operator
opts in. In on-demand mode every valid inference key can start the GPU;
there is no per-key opt-in.

### 2. Demand, not Start

The gateway calls a new controller method `Demand(keyID, until)`. It never
gets `Start` or `Stop`. `Demand` starts the GPU through the same gates as
`POST /api/v1/gpu/start` (budget, balance, attempts per hour, backoff) and
extends an in-memory **demand deadline** to `until`. It never touches the
idle deadline (ADR 0011).

### 3. Streaming requests: progress in the thinking channel

A streaming request that finds no Ready backend gets `200 text/event-stream`
at once and is held for up to `maxWait` (600 s, was 480 s). While held it
receives:

- **checkpoint lines** at each real milestone (offer search, offer chosen
  with GPU, location, hourly price and machine id, worker up, download
  percentage, checksum, model load, ready);
- **a ticker phrase** from a configurable set after 30 s without a line,
  never the same phrase twice in a row;
- **an SSE comment** every 15 s.

Every line is a `reasoning_content` delta prefixed `vk-llm › `, never
`content`, so an agent loop does not take it as the answer. At `READY` the
original request is forwarded and the model's chunks follow in the same
stream. On denial, failure or `maxWait` expiry the stream ends with a
reason line, an error chunk with `finish_reason`, and `[DONE]`; the start
continues after a `maxWait` expiry.

**Amendment (2026-11-24).** Live testing showed OpenCode v2.3.2 hides the
thinking block by default, so a held stream looked stuck. The gateway now
also sends one visible `content` line, no prefix, once at the start of the
hold: "⏳ Starting the GPU (3–6 min). Progress is in the thinking block."
The thinking-line prefix changes from `[vk-llm] ` to `vk-llm › `, since
OpenCode's markdown renderer showed the old prefix as an underlined link.

### 4. History stripping

Some clients (OpenCode) resend `reasoning_content` in later turns. Before
forwarding any request, the gateway deletes every line that starts with
`[vk-llm] ` from `reasoning_content` in incoming messages.

**Amendment (2026-11-24).** The filter now also matches the new
`vk-llm › ` prefix, and removes the visible status line (§3 amendment)
from an assistant message's `content` when it starts with it, so the
model never sees either one on a later turn.

### 5. Non-streaming requests: start, then refuse at once

A non-streaming request that finds no Ready backend calls `Demand` and gets
`503 gpu_cold_start` at once with `Retry-After` set to the estimated
remaining seconds. ADR 0007 held it silently; most non-streaming callers
time out before a cold start ends.

### 6. Unattended starts are cancelled

Each held stream keeps the demand deadline at now + `demandGrace` (60 s
default) while it is connected; a non-streaming request sets it to now +
`Retry-After` + `demandGrace`; a `maxWait` expiry extends it by 360 s +
`demandGrace`, so the start that §3 lets continue survives until the user
sends the message again. When the deadline passes before `Ready`, the
controller sends itself `EvStop`, unless an explicit start
(`POST /api/v1/gpu/start`) also asked for this instance. ADR 0007 kept
demand after a disconnect.

### 7. Envoy route timeout

In on-demand mode the route's total request timeout is at least
`maxWait + model.generationTimeout` = 600 s + 600 s = **1200 s**. The 660 s
of ADR 0010 stays correct for explicit mode.

## Alternatives considered

- **Per-key opt-in** (a "may start" mark on a client key). Rejected: every
  key belongs to the household, the budget caps the worst case, and the
  mark needs a key-file format change.
- **A start-only management scope and a "Start GPU" button.** Rejected:
  the user still waits and retries by hand, and only Open WebUI has
  buttons.
- **Hold non-streaming requests silently** (ADR 0007). Rejected: most
  non-streaming callers time out first, and a silent wait shows nothing.
- **Keep demand after a disconnect** (ADR 0007). Rejected: a closed app
  would rent a GPU for up to 36 minutes with nobody waiting.
- **Rely on the chat template to drop old reasoning.** Rejected: it ties
  correctness to one model's template and to clients we cannot inspect.

## Rationale

The user sees what the money buys while waiting, never retries, and an
abandoned start costs at most about one minute of rental. The gateway
still cannot stop the GPU or change the budget.

## Consequences

- The gateway gains a bounded queue (`maxPending` 32; `429 queue_full`
  when full), a progress writer and a history filter. The metrics
  `vkllm_gateway_pending_requests`, `_wait_seconds` and
  `_cold_start_outcomes_total` stop reading zero in on-demand mode.
- The controller gains `Demand`, a persisted start origin (explicit or
  demand), download progress read over the SSH tunnel, and `Geolocation`
  in the stored offer.
- A cancelled create counts against the attempts-per-hour cap, so a user
  who opens and closes the app three times in an hour sees
  `attempts_exhausted`.
- `vk-lab-platform` sets `provisioningMode: on-demand` and the 1200 s route
  timeout.

## Risks

- A forgotten script that sends inference requests keeps the GPU alive.
  Mitigation: the monthly budget is the hard cap; the `gpu_start` log line
  names the key.
- A reconnect that takes longer than `demandGrace` cancels the start.
  Mitigation: `demandGrace` is a chart value.
- A client that rejects a stream whose first chunks carry only
  `reasoning_content`, or whose chunk `id` changes when the model's answer
  starts. Mitigation: spec CORE-220 tests each supported client.
