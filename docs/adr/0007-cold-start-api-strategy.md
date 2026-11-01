# ADR 0007: Cold starts are hidden behind a bounded wait with heartbeats, plus an explicit warm-up API

## Status

Accepted as the design of the **future `on-demand` provisioning mode**;
**not in the MVP**. Superseded for the MVP by [ADR 0010](0010-explicit-provisioning-mode.md)
(explicit mode). §3's `/admin/v1` endpoints are replaced by the management
API of [ADR 0012](0012-management-api-cli-and-github-actions.md); the lease
rows are removed by [ADR 0011](0011-rolling-inactivity-timeout.md). §4's
1080 s Envoy timeout applies to on-demand mode only.

## Context

A cold start takes minutes: offer search, instance creation, image pull,
model download, model load. The OpenAI API is synchronous. The official SDKs
connect within 5 seconds, read for up to 600 seconds, retry twice on
connection errors and 408/409/429/5xx, and honour `Retry-After` up to 120 s
(Python) or 60 s (Node) ([research §4](../research.md)). The brief forbids
holding connections open indefinitely and asks for a strategy that serves
ordinary clients and explicit asynchronous workflows.

## Decision

### 1. Always accept, authenticate, and decide fast

The gateway accepts the TCP connection and validates the bearer key
immediately, well inside the 5-second connect budget. If a Ready worker
exists the request is proxied at once.

### 2. No worker: trigger and wait, bounded

The request registers demand (which moves the controller out of Idle,
ADR 0003) and joins a bounded pending queue (`gateway.maxPending`, 32). It
waits up to `gateway.maxWait` (480 s, under the SDK's 600 s) for the worker
to become Ready, then is proxied.

- **Streaming requests** (`stream: true`): the gateway sends `200` with
  `Content-Type: text/event-stream` immediately and emits an SSE comment line
  `: warming up <stage>` every 15 s while waiting. Comment lines are ignored
  by every SSE client and keep Envoy and the SDK from timing out. Once the
  status line is sent the gateway cannot change it, so a provisioning
  failure or an exhausted wait becomes an in-stream event
  `data: {"error": {...}}` followed by `data: [DONE]`.
- **Non-streaming requests:** the gateway holds the request without writing
  anything. On success it returns the completion. On exhausted wait or
  provisioning failure it returns `503` with an OpenAI-shaped error body
  `{"error": {"type": "server_error", "code": "gpu_cold_start", "message": "GPU is starting; retry in 30s"}}`
  and `Retry-After: 30`.
- **Queue full:** `429` with `Retry-After: 15` and `code: "queue_full"`.
- **Budget exhausted or attempts exhausted:** `503` with
  `code: "gpu_unavailable"` and no `Retry-After`, so clients do not hammer.
- **Client disconnect** while waiting removes the request from the queue;
  demand already registered is not withdrawn (the idle timer handles it).

### 3. Explicit asynchronous path

Admin endpoints under `/admin/v1`, authenticated by the admin key:

| Endpoint | Purpose |
|---|---|
| `POST /admin/v1/warmup` | register demand without a completion; returns current state |
| `GET /admin/v1/status` | state, stage timings, ETA estimate, instance summary, spend this month |
| `POST /admin/v1/leases` `{owner, ttlSeconds}` | create a workflow lease (max TTL 2 h, renewable) that blocks idle shutdown; returns lease id |
| `DELETE /admin/v1/leases/{id}` | release early |
| `PUT /admin/v1/leases/{id}` `{ttlSeconds}` | renew; same TTL limits apply |
| `POST /admin/v1/scale` `{desired: 0}` | kill switch: drain and destroy now |

`GET /v1/models` always answers immediately from configuration and is the
cheap readiness probe for clients.

### 4. Envelope and limits

With SDK defaults a client tolerates 3 attempts × 480 s plus backoff,
roughly 25 minutes, before giving up; a normal cold start of 3–6 minutes
fits in the first attempt. An uncached image pull (10–60 minutes per Vast's
docs) does not fit; it surfaces as `gpu_cold_start` and the warm-up endpoint
is the documented workaround. A streamed completion can run for
`gateway.maxWait + model.generationTimeout` (480 s + 600 s), so the Envoy
route request timeout is either disabled for this route or set at or above
1080 s, with a stream idle timeout of 120 s as the protection; heartbeats
and tokens keep the stream alive (spec 130).

## Alternatives considered

- **Return 503 immediately and let the client retry.** Python stops retrying
  when `Retry-After` exceeds 120 s and gives up after two retries, so a
  multi-minute start would fail every ordinary client. Rejected as the
  default; kept as the behaviour after `maxWait`.
- **Hold forever.** Forbidden by the brief and breaks at the first proxy
  timeout.
- **202 Accepted with a polling URL.** Not OpenAI-compatible; no client would
  use it. The admin endpoints cover the explicit case.
- **Long-poll on `/v1/models` as readiness.** Clients do not do this on their
  own.

## Rationale

Heartbeats make the common case, a coding assistant with default SDK
settings, work unchanged through a normal cold start. The admin API gives
agents and scripts an honest asynchronous path. Bounds keep connections and
costs finite.

## Consequences

- The gateway must stream and heartbeat before it knows whether the request
  will succeed; error reporting for streaming requests is in-band.
- `gateway.maxWait`, `gpu.startupDeadline` and the Envoy timeout are coupled;
  spec 060 and 130 state the ordering `maxWait < envoyTimeout` and
  `maxWait < startupDeadline` is not required (a request may give up before
  the instance does).
- Metrics: `vkllm_gateway_pending_requests`, `vkllm_gateway_wait_seconds`,
  `vkllm_gateway_cold_start_outcomes_total{outcome}`.

## Risks

- An intermediate proxy that strips SSE comments. Envoy does not; documented
  for other ingresses.
- A client that sets a short read timeout. The warm-up endpoint and the
  documented `Retry-After` behaviour are the answer.
