---
id: "CORE-060"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-040", "CORE-050"]
adrs: ["0007", "0002", "0003", "0006", "0009"]
---

# 060 — Gateway: OpenAI-compatible API

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** L
**Risk:** high — the cold-start wait commits a `200` before success is known, so every later failure must be reported in-band
**Estimated effort:** three sessions (10–12 h)
**Recommended model:** strong — streaming, cancellation, shutdown and auth in one package
**Depends on:** [CORE-040](../040-P-state-store/spec.md), [CORE-050](../050-P-gpu-lifecycle-controller/spec.md)

## Outcome

`internal/gateway` serves the OpenAI-compatible and admin APIs on port 8080.
An official OpenAI SDK with a client key completes a plain and a streamed chat
call; with the GPU Idle the call triggers a cold start, receives
`: warming up <stage>` every 15 s, then streams tokens. Bad key, body, model
or rate gets an OpenAI-shaped error. A disconnect cancels upstream within 1 s;
SIGTERM drains in under 30 s; outcomes show as `vkllm_gateway_*` on 9090.

## Scope

- `net/http` mux with method patterns, middleware, key loading, constant-time auth, per-key token bucket.
- `POST /v1/chat/completions`, `GET /v1/models`, and the five admin routes of ADR 0007 §3.
- Validation, `max_tokens` clamp, pending queue, cold-start wait, plain and SSE proxy,
  error mapping, in-flight accounting, graceful shutdown, metrics, one readiness check.
- Tests with `httptest`, fake upstream, fake controller, fake `Transport`, fake clock; an optional SDK job.

Excludes: the 9090 endpoints (CORE-010, CORE-090); `internal/tunnel`, the
SSH link and host-key pinning (CORE-050); state machine, lease persistence,
`lastRequestAt` writes, ETA (CORE-050); spend (CORE-100); key
rotation and threat model (CORE-110); Envoy timeout (CORE-130); dashboards (CORE-090).

## Design and contracts

### Package surface

```go
package gateway

type Config struct{ /* one field per key below */ }

func New(cfg Config, ctl Controller, clk clock.Clock, reg prometheus.Registerer) (*Gateway, error)
func (g *Gateway) Handler() http.Handler            // the :8080 mux
func (g *Gateway) Shutdown(ctx context.Context) error // see Shutdown
func (g *Gateway) KeysLoaded() error                  // appended to /readyz checks
```

`Config` carries `model.alias`, `model.parallel`, `gateway.maxWait`,
`gateway.maxPending`, `gateway.maxBodyBytes`, `gateway.maxTokensCap`,
`gateway.rateLimitPerKeyRPM`, `gateway.heartbeatInterval` (architecture §9),
`model.generationTimeout` (10 m), `installationId` (`VKLLM_INSTALLATION_ID`,
CORE-080) for the worker key, and the two key file paths.

`Controller` is the method set used from `*controller.Controller`; it exists
because a test fake does (C-26).

```go
type Controller interface { // the gateway-facing methods of CORE-050
	Demand(ctx context.Context) (<-chan controller.Ready, error) // one value then close, or close without a value
	RequestStart(gen int64) error // controller.ErrDraining: do not proxy
	RequestEnd(gen int64)
	ReportUpstreamFailure(gen int64)
	ObserveFirstToken(gen int64, d time.Duration)
	CreateLease(ctx context.Context, owner string, ttl time.Duration) (store.Lease, error)
	RenewLease(ctx context.Context, id string, ttl time.Duration) (store.Lease, error)
	ReleaseLease(ctx context.Context, id string) error
	SetDesired(ctx context.Context, n int) error
	Status() controller.Snapshot
}
// controller.Ready{Generation int64; Transport http.RoundTripper} (CORE-050)
```

The gateway has no `Dialer`: the controller owns the one tunnel `Conn` per
instance and hands out `Ready.Transport`. `clock.Clock` is CORE-010's.

### Routes and middleware

`/v1/*` accepts a client or the admin key; `/admin/v1/*` only the admin key.
`/healthz`, `/readyz`, `/metrics` on 9090 belong to `internal/obs`.
Any other path or method: `404 not_found` / `405 method_not_allowed` in the
error schema, after auth.

Middleware, outermost first:

| # | Stage | Behaviour |
|---|---|---|
| 1 | request id | `obs.RequestIDMiddleware` (CORE-090); then wrap the writer in a status recorder with `Unwrap()` so `http.ResponseController` still flushes; emit the access log line and `requests_total` for every request |
| 2 | recover | panic → `500 internal_error` if headers unsent, else close the stream; log `panic=true`, no stack values |
| 3 | body limit | `http.MaxBytesReader(gateway.maxBodyBytes)`; lazy, so the 413 surfaces at decode, after auth |
| 4 | auth | `Authorization: Bearer <key>`; see Auth |
| 5 | rate limit | per-key token bucket; see Rate limit |
| 6 | logging | attaches `key_id` and `route` to the request's `slog` context |

`route` labels (CORE-090): `chat_completions`, `models`, `admin_warmup`, `admin_status`, `admin_leases_create`, `admin_leases_delete`, `admin_scale`, `other`; never the raw path.

### Auth

- Keys load from the mounted files (ADR 0009 §1), one client key per line;
  re-read every 10 s (mtime poll, CORE-110 R3), swapped atomically on change (stdlib, no watch module).
- `subtle.ConstantTimeCompare` of SHA-256 digests against every key, no early
  exit; hashing removes the length side channel.
- `key_id` for logs is the first 8 characters after `sk-vkllm-` (ADR 0009 §5).

| Case | Status | `code` | `auth_failures_total{reason}` |
|---|---|---|---|
| no header | 401 | `invalid_api_key` | `missing` |
| not `Bearer <token>` | 401 | `invalid_api_key` | `malformed` |
| unknown key | 401 | `invalid_api_key` | `unknown_key` |
| client key on admin route | 403 | `permission_denied` | `admin_required` |

`KeysLoaded` fails while the client set or the admin key is empty.

### Rate limit

Hand-rolled token bucket (no `x/time/rate`: C-25), one per `key_id`, capacity
`rateLimitPerKeyRPM`, refill `RPM/60` per second from `Clock.Now`, dropped on
key removal. Empty → `429 rate_limit_exceeded`, `Retry-After` = seconds to one token, minimum 1.

### Request validation (chat)

1. Decode into `map[string]json.RawMessage`; unknown fields are forwarded.
   Over the limit → `413 request_too_large`; bad JSON → `400 invalid_json`.
2. `model` must equal `model.alias`, else `404 model_not_found` (ADR 0002 §5).
3. `stream`: absent or `false` → plain; `true` → SSE; other type → `400 invalid_request`.
4. Clamp: each of `max_tokens` and `max_completion_tokens` that is present and
   above `gateway.maxTokensCap` is set to the cap; if neither is present,
   `max_tokens` is set to the cap. Clamping is silent to the client and logged
   as `max_tokens_clamped=true` with the original value. Non-integer → `400 invalid_request`.
5. Nothing else is validated; llama-server validates `messages` and tools.

### Admission: pending queue and cold-start wait

`pending` counts waiters for a worker **or** a slot, capped at
`gateway.maxPending`; `slots` is a channel semaphore of size `model.parallel`.
llama-server's own queue has no documented cap (research §2), so the gateway queues (FR-3.5).

```text
admit(req):
  deadline := clk.After(maxWait)          // one timer for both waits
  ready, err := ctl.Demand(ctx)           // registers demand; ADR 0003 §2
  if err is ErrUnavailable: fail(gpu_unavailable)
  non-blocking receive on ready: value → target, ready = nil; closed empty → fail(Status().FailureCode); target + free slot → proxy
  if !pending.tryInc(): fail(queue_full)  // before any byte is written
  (pending.dec() on every exit from the loop below, before proxying)
  if stream: write 200, text/event-stream, Cache-Control: no-cache; flush;
             write ": warming up <stage>\n\n"; flush
  hb := clk.After(heartbeatInterval)
  loop select:
    case r, ok := <-ready: if !ok: fail(Status().FailureCode); else target = r; ready = nil
    case slots <- {}:    only enabled once target is set → proxy(target)
    case <-hb:           if stream: write ": warming up <strings.ToLower(Status().State.String())|queued>\n\n"; flush
                         hb = clk.After(heartbeatInterval)
    case <-deadline:     fail(gpu_cold_start)
    case <-ctx.Done():   outcome client_gone; return (demand stays, ADR 0007 §2)
    case <-g.stopping:   fail(shutting_down)
proxy(target):
  defer { <-slots }
  if err := ctl.RequestStart(target.Generation); err != nil: release slot, re-Demand, continue loop
  defer ctl.RequestEnd(target.Generation)
  ...
```

`defer RequestEnd` directly follows a successful `RequestStart`, so panic,
cancel, shutdown and upstream errors all pass through it.

`fail(code)` before any byte is written sends the error response from the
table. After the stream `200` it sends the in-stream error, each line followed
by a blank line: `data: {"error":{"message":"GPU is starting; retry in 30s","type":"server_error","code":"gpu_cold_start"}}`
then `data: [DONE]`.

| Wait outcome | `cold_start_outcomes_total{outcome}` | Non-stream | Stream |
|---|---|---|---|
| target arrived | `ready` | proxied | proxied |
| `maxWait` elapsed | `gpu_cold_start` | 503 `gpu_cold_start`, `Retry-After: 30` | in-stream `gpu_cold_start` |
| closed, `FailureCode` `gpu_cold_start` | `gpu_cold_start` | 503 `gpu_cold_start`, `Retry-After: 30` | in-stream `gpu_cold_start` |
| `ErrUnavailable` or `FailureCode` `gpu_unavailable` | `gpu_unavailable` | 503 `gpu_unavailable`, no `Retry-After` | in-stream `gpu_unavailable` |
| queue full | `queue_full` | 429 `queue_full`, `Retry-After: 15` | same (nothing written yet) |
| client gone | `client_gone` | none | none |
| shutdown | `shutting_down` | 503 `shutting_down`, `Retry-After: 30` | in-stream `shutting_down` |

Outcomes count only requests that entered the wait; `wait_seconds` observes
every admitted request, including zero waits.

### Proxy

- Upstream URL is the constant `http://worker/v1/chat/completions`; `worker`
  is never resolved: `Ready.Transport` reaches the worker's loopback port.
  This package reads and dials no address (C-18, ADR 0006 §5).
- Headers sent: `Content-Type: application/json`, `Accept:
  text/event-stream` when streaming, `Authorization: Bearer
  <worker.WorkerKey(adminKey, cfg.InstallationID, Generation)>` (CORE-070),
  computed per request, never logged. No client header is forwarded.
- `http.Client{Transport: target.Transport, CheckRedirect: return http.ErrUseLastResponse}`,
  no client timeout; the request context carries
  `gateway.maxWait + model.generationTimeout` from admission (1080 s default)
  and is a child of `r.Context()`, so a disconnect cancels the upstream call
  (FR-3.4) and llama-server stops generating (research §2).
- Plain: copy status, `Content-Type` and body. The body is also teed into a
  buffer capped at `MaxBodyBytes` to read `usage.completion_tokens`.
- SSE: if headers are unsent, copy status and `Content-Type`; then loop
  `Read` into a 32 KiB buffer, write the bytes, flush via
  `http.ResponseController`, with no line assembly. The first byte stamps
  `ttft_seconds` (from proxy start) and calls `ctl.ObserveFirstToken(gen, ttft)`
  once per request. Each `data: ` prefix seen counts one output chunk.

| Upstream result | Client sees | Side effect |
|---|---|---|
| 2xx | status and body copied | — |
| 4xx except 401/403, body `{"error":{…}}` | copied | — |
| 4xx except 401/403, other body | same status, `upstream_rejected` | — |
| 401 / 403 (worker key rejected) | 502 `upstream_error` | `ReportUpstreamFailure` |
| 5xx | 502 `upstream_error` | `ReportUpstreamFailure` |
| Transport closed by the controller | 502 `upstream_error` | — |
| transport error before headers | 502 `upstream_error` | `ReportUpstreamFailure` |
| read error mid-stream | in-stream `upstream_error`, `[DONE]` | `ReportUpstreamFailure` (FR-5.6) |
| deadline exceeded | 504 `upstream_timeout` or in-stream | — |
| client disconnect | nothing; `requests_total{code="499"}` | upstream cancelled |

After the stream `200` every row except the first becomes the in-stream
error with that code. Rows with an in-stream error count as
`requests_total{code="stream_error"}`.

### Error body

Every non-2xx body and in-stream error is `{"error":{"message","type","code"}}`
(FR-1.5), all strings. `type`: `invalid_request_error` (400/404/413), `authentication_error` (401),
`permission_error` (403), `rate_limit_error` (429), else `server_error`. Messages never echo input.

### `/v1/models`

Answered from configuration, no controller call, no store read (FR-1.2):

```json
{"object":"list","data":[{"id":"<model.alias>","object":"model","created":<process start, unix s>,"owned_by":"gpu-on-demand"}]}
```

### Admin API (ADR 0007 §3)

| Endpoint | Body | Success | Errors |
|---|---|---|---|
| `POST /admin/v1/warmup` | empty or `{}` | `202` + status JSON; calls `Demand` | 503 `gpu_unavailable` |
| `GET /admin/v1/status` | — | `200` + status JSON | — |
| `POST /admin/v1/leases` | `{"owner":string,"ttlSeconds":int}` | `201` `{"id","owner","expiresAt"}`; calls `CreateLease` then `Demand` | 400 if owner not `[A-Za-z0-9._-]{1,64}` or ttl outside 60–7200 s (narrower than CORE-050's 1 s–2 h) |
| `DELETE /admin/v1/leases/{id}` | — | `204` | 404 `lease_not_found` |
| `POST /admin/v1/scale` | `{"desired":0}` or `{"desired":1}` | `202` + status JSON; calls `SetDesired` | 400 for any other value |

A second POST with the same `owner` (found in `Status().LeaseList`) calls
`RenewLease(ctx, id, ttl)` and returns that id (ADR 0007 §3); caps are CORE-050's.

Status JSON. No `sshHost`, `sshPort`, host key or key material:

```json
{
  "installationId": "…", "labelPrefix": "vkllm:…:", "state": "loading", "since": "2026-11-01T10:00:00Z",
  "generation": 7, "desired": 1, "unavailable": "", "failureCode": "", "stages": {"search": 3.1, "boot": 41.2},
  "etaSeconds": 140, "instance": {"providerId": "…", "hourlyUsd": 0.62, "createdAt": "…", "readyAt": null},
  "leases": [{"id": "…", "owner": "ci", "expiresAt": "…"}], "spendMtdUsd": 4.12, "gateway": {"active": 1, "pending": 3}
}
```

Fields map 1:1 to `Snapshot` (`InstallationID`, `LabelPrefix`, `State`, `Since`, `Generation`, `Desired`,
`Unavailable`, `FailureCode`, `Stages`, `ETA`, `ProviderID`/`Hourly`/`CreatedAt`/`ReadyAt`, `LeaseList`,
`SpendMTDUSD`); `gateway` is local. `etaSeconds` (`ETA` 0) and `instance` are `null` when absent.

### Shutdown

`main` calls `g.Shutdown(ctx)` and `api.Shutdown(ctx)` together under the
30 s cap of CORE-010.

1. `t=0`: close `stopping`; the listener closes; waiters leave with outcome `shutting_down`.
2. Active proxies continue until the `ctx` deadline − 5 s (25 s by default).
3. Then upstream contexts are cancelled: streams get in-stream `shutting_down`
   + `[DONE]`; plain requests get `503 shutting_down`, `Retry-After: 30`.
4. `Shutdown` returns when every handler has returned. The instance is never
   destroyed (FR-4.1, ADR 0003 §8).

Server: `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `IdleTimeout` 120 s,
`WriteTimeout` 0 (per-request contexts bound streams). Every gateway
goroutine is owned by a request or by `Gateway` and exits on its context (C-27).

### Observability

| Metric | Type | Labels |
|---|---|---|
| `vkllm_gateway_requests_total` | counter | `route`, `code` ∈ {200,201,202,204,400,401,403,404,405,413,429,499,500,502,503,504,stream_error} |
| `vkllm_gateway_active_requests` | gauge | — |
| `vkllm_gateway_pending_requests` | gauge | — |
| `vkllm_gateway_wait_seconds` | histogram | — |
| `vkllm_gateway_request_seconds` | histogram | `route` |
| `vkllm_gateway_ttft_seconds` | histogram | — |
| `vkllm_gateway_output_tokens_per_second` | histogram | — |
| `vkllm_gateway_cold_start_outcomes_total` | counter | `outcome` ∈ {ready,gpu_cold_start,queue_full,gpu_unavailable,client_gone,shutting_down} |
| `vkllm_gateway_auth_failures_total` | counter | `reason` ∈ {missing,malformed,unknown_key,admin_required} |

Enumerations and buckets per CORE-090. Tokens per second: plain = `usage.completion_tokens` / generation time; stream
= `data:` chunks minus `[DONE]` / first-to-last-byte time. Log fields: `request_id`, `route`, `status_code`, `duration_ms`, `wait_ms`,
`key_id`, `stream`, `instance_gen`, `outcome`, `max_tokens_clamped`,
`max_tokens_requested`. Never `Authorization`, request or response bodies,
prompts, completions, worker keys (C-16, ADR 0009 §5, NFR-7.7).

## Requirements

- R1. Every `/v1` and `/admin/v1` request MUST be authenticated before its body is read or any controller call is made. (source: C-16, NFR-7.1)
- R2. Key comparison MUST use `subtle.ConstantTimeCompare` over SHA-256 digests with no early exit; a missing or unknown key MUST get 401, a client key on `/admin/v1` 403. (source: ADR 0009 §2, C-16, NFR-7.1)
- R3. Bodies above `gateway.maxBodyBytes` MUST get 413; `max_tokens` and `max_completion_tokens` MUST NOT reach the worker above `gateway.maxTokensCap`; each key MUST be limited to `gateway.rateLimitPerKeyRPM` with 429 and `Retry-After`. (source: C-19, NFR-7.5)
- R4. A `model` other than `model.alias` MUST get 404 `model_not_found`. (source: ADR 0002 §5, FR-1.3)
- R5. Every non-2xx body and every in-stream error MUST match `{"error":{"message","type","code"}}`. (source: FR-1.5)
- R6. `GET /v1/models` MUST answer from configuration without a controller or store call. (source: ADR 0007 §3, FR-1.2)
- R7. Waiting requests MUST be capped at `gateway.maxPending`; the next MUST get 429 `queue_full` with `Retry-After: 15`. (source: ADR 0007 §2, FR-3.5)
- R8. A streaming waiter MUST receive `: warming up <stage>` at least every `gateway.heartbeatInterval`. (source: ADR 0007 §2, FR-3.3)
- R9. A non-streaming waiter MUST get 503 `gpu_cold_start` with `Retry-After: 30` after `gateway.maxWait`; a streaming one MUST get the in-stream error then `data: [DONE]`. (source: ADR 0007 §2, FR-3.6)
- R10. Budget, attempts, no-offer and desired-zero MUST map to 503 `gpu_unavailable` without `Retry-After`. (source: ADR 0007 §2, FR-2.6)
- R11. SSE chunks MUST be written and flushed as read, without line buffering; a client disconnect MUST cancel the upstream request via context. (source: FR-3.2, FR-3.4, research §2)
- R12. Every successful `RequestStart` MUST be followed by exactly one `RequestEnd` on every path, including panic; the first upstream byte MUST call `ObserveFirstToken` once. (source: ADR 0003 §5, CORE-050, FR-2.4)
- R13. The upstream URL host MUST be the literal `worker` and the transport MUST be `Ready.Transport`; the package MUST NOT read or dial an address. (source: C-18, ADR 0006 §5, CORE-050)
- R14. The worker key MUST be derived with `worker.WorkerKey` per request and MUST NOT be logged or stored. (source: ADR 0006 §4, CORE-070 R8)
- R15. Pending queue and in-flight counts MUST live in process memory only. (source: C-12)
- R16. Shutdown MUST finish within 30 s and MUST NOT destroy the instance. (source: FR-4.1, ADR 0003 §8)
- R17. Logs MUST NOT contain `Authorization`, bodies, prompts, completions or keys. (source: C-16, NFR-7.7)
- R18. Every gateway goroutine MUST have an owner and a context, every upstream call a deadline; upstream errors MUST be wrapped with `%w` and classified by the mapping table. (source: C-27, C-28)
- R19. Metric labels MUST be the enumerations listed; no key ids, request ids or paths. (source: C-34)
- R20. Unit tests MUST use fakes and the fake clock; no sleeps, no network beyond loopback `httptest`. (source: C-29)

## Implementation steps

1. Config fields, error body, route table, `obs.RequestIDMiddleware` wiring and recover middleware. Tests: `TestErrorBodyShape`, `TestRequestIDEcho`, `TestRecoverReturns500`.
2. Key loading, reload and auth. Tests: `TestAuthMatrix`, `TestKeyReload`, `TestConstantTimeNoEarlyExit` (code review item plus a test that all keys are compared), `TestKeysLoadedReadiness`.
3. Token bucket. Test: `TestRateLimitFakeClock`.
4. Chat validation and clamp. Tests: `TestBodyLimit413`, `TestModelMismatch404`, `TestClampMaxTokens`, `TestUnknownFieldsForwarded`.
5. `/v1/models` and admin handlers against the fake controller. Tests: `TestModelsNoController`, `TestAdminEndpoints`, `TestStatusHasNoAddress`.
6. Proxy, plain and SSE, against a fake upstream behind a fake `Ready.Transport`. Tests: `TestProxyPlain`, `TestSSEPassThroughLatency`, `TestObserveFirstTokenCalled`, `TestUpstreamErrorMapping`, `TestWorkerKeyHeader`, `TestUpstreamHostIsWorker`.
7. Admission: pending queue, slots, cold-start wait. Tests: `TestHeartbeatCadence`, `TestMaxWaitNonStream`, `TestMaxWaitStream`, `TestQueueFull`, `TestUnavailable`, `TestDisconnectWhileWaiting`.
8. In-flight accounting, cancellation, shutdown. Tests: `TestRequestEndOnEveryPath`, `TestDisconnectCancelsUpstream`, `TestShutdownDrains`.
9. Metrics and log redaction. Tests: `TestMetricsLabels`, `TestLogRedaction`.
10. SDK compatibility job: `test/sdk/compat.py` run by `uv run` against the binary with fake controller wiring, CI job `sdk-compat` (optional, non-blocking until it is stable).

## Tests / acceptance criteria

- AC1. Auth matrix. Setup: one client key, one admin key. Action: each route with no header, `Basic x`, an unknown key, the client key, the admin key. Expected: 401/401/401 on all; client key → 403 on all five admin routes and 2xx on `/v1`; admin key → 2xx everywhere; `auth_failures_total` = 3 per route for 401s plus 5 `admin_required`. (NFR-7.1)
- AC2. Body limit. Action: POST of 1,048,577 bytes with a valid key. Expected: 413, `request_too_large`; same body without a key: 401. (NFR-7.5)
- AC3. Model mismatch. Action: `model: "gpt-4o"`. Expected: 404, `code` `model_not_found`, 0 upstream calls. (FR-1.3)
- AC4. Clamp. Action: `max_tokens: 100000`; then no `max_tokens`. Expected: fake upstream receives 8192 both times; client receives 200; log line has `max_tokens_clamped=true`. (NFR-7.5)
- AC5. Rate limit. Setup: RPM 60, fake clock frozen. Action: 61 requests. Expected: 60 × 2xx, 1 × 429 with `Retry-After: 1`; after advancing 1 s, one more 2xx. (NFR-7.5)
- AC6. Error shape. Action: collect every non-2xx body from AC1–AC5 and AC9–AC12. Expected: 100 % parse into the error schema with non-empty `message`, `type`, `code`. (FR-1.5)
- AC7. `/v1/models` with the fake controller panicking on any call. Expected: 200, `data[0].id` = alias, `len(data)` = 1; 1000 sequential calls p99 < 50 ms. (FR-1.2)
- AC8. SSE pass-through. Setup: Ready target; fake upstream writes 20 chunks, each released by the test through a channel and flushed. Action: streamed request. Expected: each chunk is read by the client < 50 ms (wall clock, loopback) after its upstream flush; bytes identical, including fixture chunks carrying `reasoning_content` and `tool_calls`; no sleeps; `ObserveFirstToken` called exactly once with the target generation. (FR-3.2, FR-1.4, FR-2.4)
- AC9. Heartbeat. Setup: fake controller never sends `Ready`; fake clock, synchronised with its `BlockUntil(n)` (CORE-010). Action: streamed request; advance 15 s three times. Expected: 200 with `text/event-stream`; comment lines at t=0, 15, 30, 45 s (4 lines); none between advances. (FR-3.3)
- AC10. `maxWait`. Same setup; advance 480 s. Expected: plain request → 503, `gpu_cold_start`, `Retry-After: 30`; streamed → in-stream error event then `data: [DONE]`; `cold_start_outcomes_total{outcome="gpu_cold_start"}` = 2. (FR-3.6)
- AC11. Queue full. Setup: never-ready controller, `maxPending` 32. Action: 40 concurrent requests. Expected: 32 waiting (`pending_requests` = 32), 8 × 429 `queue_full` with `Retry-After: 15`. Warm variant (`Parallel` 1, upstream blocks): 1 active, 32 waiting, 7 × 429. (FR-3.5)
- AC12. Unavailable. Setup: `Demand` returns `ErrUnavailable{budget}`. Expected: 503 `gpu_unavailable`, no `Retry-After` header, 0 bytes of SSE written. Streamed wait whose channel closes empty with `Status().FailureCode` = `gpu_unavailable`: in-stream `gpu_unavailable`. (FR-2.6)
- AC13. Disconnect cancels upstream. Setup: fake upstream streaming slowly. Action: client closes after the first chunk. Expected: fake upstream sees `ctx.Done` within 1 s; `RequestEnd` called once; `requests_total{code="499"}` = 1. (FR-3.4) Disconnect while waiting instead: `pending_requests` returns to 0 within 1 s; `outcome="client_gone"` = 1; the controller fake sees no demand withdrawal. (ADR 0007 §2)
- AC14. `RequestEnd` on every path. Upstream variants: 200, 500, Transport closed by the controller, mid-stream reset, panic in the fake upstream reader, deadline, client cancel. Expected: `RequestStart` count = `RequestEnd` count = 7. (R12)
- AC15. Error mapping, asserted on the fake `Transport`. Upstream 500 → 502 `upstream_error` and upstream 401 → 502, each with exactly one `ReportUpstreamFailure(gen)`; Transport returning a closed-connection error → 502 `upstream_error`; mid-stream reset → in-stream `upstream_error`. (FR-5.6)
- AC16. SSRF. Expected: every request observed by the fake `Ready.Transport` has `URL.Host == "worker"`; a grep finds no `SSHHost`, `SSHPort`, `tunnel.` or `net.Dial` in `internal/gateway`. The crafted-document test of NFR-7.4 belongs to CORE-050. (NFR-7.4, C-18) Worker key: upstream `Authorization` equals `Bearer ` + `worker.WorkerKey(admin, cfg.InstallationID, gen)`; the client's own `Authorization` value is absent upstream.
- AC17. Shutdown. Setup: one open proxied stream, one streaming waiter. Action: `Shutdown` with a 30 s context; fake upstream never finishes. Expected: waiter gets in-stream `shutting_down` at t≈0; stream gets in-stream `shutting_down` at 25 s on the fake clock; `Shutdown` returns before 30 s; the fake controller sees no `SetDesired`. (FR-4.1)
- AC18. Leases. Action: POST `{owner:"ci",ttlSeconds:1800}` twice, DELETE the id, DELETE again. Expected: 201, 201 with the same id and one `RenewLease` call, 204, 404 `lease_not_found`; `ttlSeconds: 7201` → 400. (FR-4.3)
- AC19. Status. Expected: JSON has every top-level field above, including `installationId`, `labelPrefix`, `failureCode` equal to the fake `Snapshot`; the body contains neither the fake `SSHHost` value nor `sshPort`. (FR-6.8 fields from CORE-100)
- AC20. Log redaction. Action: run AC1–AC19 with the slog handler captured. Expected: 0 occurrences of any key, the worker key, a prompt marker string or a completion marker string (NFR-7.7); the gathered registry has only the label values listed in Observability. (C-34)
- AC21. Overhead. Setup: Ready target, upstream answering at once. Action: 10,000 plain requests. Expected: p99 gateway time minus upstream time < 5 ms. (NFR-10.3)
- AC22. SDK compatibility (optional job). The official `openai` Python client with `base_url` and `api_key` only completes a chat call, a streamed chat call, `models.list()`, and raises `NotFoundError` for a wrong model and an exception carrying `gpu_cold_start` for an in-stream error. (FR-1.1, FR-1.3)

## Definition of done

- [ ] AC1–AC21 pass in CI; AC22 runs as the `sdk-compat` job; `make specs-check` passes.
- [ ] `/readyz` includes `KeysLoaded`; `main` wires `New` and `Shutdown`.
- [ ] Status flipped to DONE in the same PR, folder renamed to `060-D-gateway-openai-api`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| NFR-7.4 "crafted store document with a foreign address is rejected" | the gateway dials nothing, so it cannot test this | CORE-050 | tested in CORE-050's tunnel/reconcile ACs |
| OpenAI error `type`/`code` vocabulary, `model_not_found`, the `/v1/models` object fields, and whether the Python SDK raises on a `data: {"error":…}` event are not in research.md | SDK exception mapping, OpenCode model match (research §4), in-stream errors (ADR 0007 §2) | AC22 job; add research §4 rows | values above; assumed the SDK raises |
| Does llama-server honour `max_completion_tokens`, return OpenAI-shaped 4xx bodies, send one token per SSE chunk, send `usage` in streams? | clamp, error pass-through, tokens/s accuracy | CORE-030 / AC22 | clamp both fields; wrap non-conforming 4xx; chunk count as an estimate |
| Admin-key rotation changes every derived worker key | the running worker rejects the gateway | CORE-110 | worker 401 → 502 + `ReportUpstreamFailure`; the instance is replaced |

## Evidence

None yet.
