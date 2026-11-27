---
id: "CORE-340"
status: "DRAFT"
updated: "2026-11-27"
depends_on: ["CORE-060", "CORE-180", "CORE-220", "CORE-230"]
adrs: ["0011", "0019", "0020"]
---

# 340 — Start the GPU when a client opens, before the first prompt

**Status note:** DRAFT, research and a proposed design only, written
2026-11-27. The operator ranked it second, after
[CORE-330](../330-P-interruptible-bid-rentals/spec.md). The proposed
design is not agreed; the open questions below go through a design session
before READY. It changes a decision of ADR 0011 §2 and ADR 0019 (a
`/v1/models` call is not demand), so a new ADR comes first.

**Complexity:** M
**Risk:** high — a false signal rents a GPU with nobody present
**Estimated effort:** 1 day after the design session
**Recommended model:** standard
**Depends on:** [CORE-060](../060-D-gateway-openai-api/spec.md), [CORE-180](../180-A-persistent-volume-cache/spec.md), [CORE-220](../220-A-automatic-gpu-start/spec.md), [CORE-230](../230-P-open-webui-chat-front-end/spec.md)

## Problem

In on-demand mode (ADR 0019) the first chat message starts the GPU, so the
user waits the whole cold start after sending. A start at the moment the
user opens the chat site, Conduit or OpenCode overlaps the cold start with
the time spent typing.

The gain is bounded by the typing time, about 15 to 60 s. A cold start is
about 126 s on a CORE-180 cache hit and 2 to 6 min on a miss
([CORE-310](../310-A-cold-start-recording/spec.md) records). CORE-180 is
the larger gain; this spec adds to it.

## What exists today (verified 2026-11-27)

### Gateway

- `GET /v1/models` answers from config with no controller call
  (`internal/gateway/models.go:20-34`). It never starts a GPU and is not
  activity (ADR 0011 §2).
- An unattended demand start is cancelled when its deadline passes before
  Ready. A held stream keeps the deadline at now + `demandGrace` (60 s); a
  non-streaming request sets it to now + `Retry-After` + 60 s
  (ADR 0019 §6, `internal/controller/demand.go`).
- A cancelled create counts toward `maxProvisionAttemptsPerHour` (3)
  (ADR 0019, Consequences).

### Open WebUI v0.13.4 (browser and Conduit)

Measured on the lab, 2026-11-27, gateway and Open WebUI logs:

- One page load (`GET /` then `GET /api/models`) made Open WebUI call the
  gateway's `GET /v1/models` 2 to 3 times within 25 s, all with Open
  WebUI's one key.
- Open WebUI has two connections, prod and `llm-dev`, and called **both**
  gateways at the same moments. A naive trigger starts two GPUs.
- One call (10:05:03) had no browser request behind it: probably a
  background call (not verified; the gateway does not log headers).

From the v0.13.4 source:

- `MODELS_CACHE_TTL` defaults to 1 s per user, so nearly every
  `/api/models` reaches the gateway. `ENABLE_BASE_MODELS_CACHE=true` or a
  fixed `model_ids` list on the connection removes the call. Both live in
  the Open WebUI database (persistent config), not only in the chart.
- With `ENABLE_FORWARD_USER_INFO_HEADERS=true`, calls made for a user carry
  `X-OpenWebUI-User-Id` and related headers. Background calls (startup
  prefetch, admin "verify") carry none. The automations scheduler runs as
  the owner and does carry them.
- The model-list call times out after 10 s
  (`AIOHTTP_CLIENT_TIMEOUT_MODEL_LIST`), so `/v1/models` must keep
  answering at once.
- No Filter, Function or webhook runs on "chat opened" or on typing; in a
  new chat nothing is sent until the first message.
- Conduit loads `/api/models` 200 to 600 ms after a cold app start; on
  resume it refreshes chats and reconnects the socket, and probably does
  not refetch models (not verified).

### OpenCode v2.3.2

Measured locally, 2026-11-27:

- Launch, TUI open, a keystroke and a session create send no request to the
  provider. The model list is fixed in `~/.config/opencode/opencode.jsonc`;
  no provider option discovers models.
- A TUI plugin's `setup()` runs about 1 s after every TUI open and can
  `fetch` with `VK_LLM_INFERENCE_KEY` (plugin directory needs a
  `server.ts` next to `tui.ts`).
- The top-level `warming` option sends inference requests that would
  reset the idle timer; it must stay off.

## Proposed design (not agreed)

1. **Signal.** The gateway treats a `GET /v1/models` as presence when it
   carries `X-OpenWebUI-User-Id` (Open WebUI with forwarded user headers)
   or `X-VKLLM-Prewarm: 1` (the OpenCode plugin). A plain `/v1/models`
   stays inert.
2. **Action.** In on-demand mode, presence calls `Demand` in the
   background with deadline now + `prewarm.grace`, and `/v1/models`
   answers at once as today.
3. **Switch.** `config.gateway.prewarm.enabled`, default false, set per
   installation, so a page load does not start both prod and dev.
4. **Debounce.** At most one prewarm `Demand` per `prewarm.minInterval`
   per installation, so 2 to 3 calls per page load and repeated opens use
   one create attempt.
5. **OpenCode.** A documented TUI plugin (README, not shipped code) that
   sends `GET /v1/models` with `X-VKLLM-Prewarm: 1`.

## Open questions

- Q1. `prewarm.grace`: the start must survive the cold start plus the
  typing time, about 3 to 7 min, far above `demandGrace` (60 s). What
  value, and does a page that stays open extend it?
- Q2. A prewarm that reaches Ready with no prompt bills until the idle
  deadline, readyAt + 30 min (about $0.35 on demand). Keep ADR 0011 as is,
  or add a shorter idle window after a prewarm-only start (changes
  ADR 0011)?
- Q3. A cancelled prewarm uses one of the 3 creates per hour. Exempt
  prewarm creates, or accept that three abandoned opens block the next
  chat for up to an hour?
- Q4. Enable forwarded user headers in Open WebUI: any privacy cost (user
  name and email reach the gateway; the gateway must not log them)?
- Q5. Does Conduit refetch models on resume? Test on the phone.
- Q6. Prewarm on dev first, as with CORE-330?

## Evidence

Research only; see "What exists today".
