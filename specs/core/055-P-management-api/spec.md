---
id: "CORE-055"
status: "READY"
updated: "2026-11-01"
depends_on: ["CORE-040", "CORE-050"]
adrs: ["0012", "0010", "0011", "0009"]
---

# 055 — Management API

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** M
**Risk:** medium — a management token can rent a GPU, so auth, rate limit and idempotency guard money
**Estimated effort:** one to two sessions (5–8 h), half of it the contract and 403-matrix tests
**Recommended model:** standard — a thin HTTP layer over the controller interface of ADR 0012 §2
**Depends on:** [CORE-040](../040-D-state-store/spec.md), [CORE-050](../050-D-gpu-lifecycle-controller/spec.md)

## Outcome

`internal/mgmt` serves `POST /api/v1/gpu/start`, `GET /api/v1/gpu/status`,
`POST /api/v1/gpu/stop` and `GET /api/v1/gpu/cost` on port 8080 next to `/v1`.
With a management token, `start` returns within 200 ms with 202 and the status
document, `status` shows the GPU walk `ALLOCATING → … → READY` with
`secondsUntilIdleShutdown`, `stop` returns 202 with state `DRAINING`, and
`cost` shows estimates labelled as such. An inference key gets 403 on every
route. 50 concurrent starts reach the controller as one `Start`. No call
moves the inactivity deadline. Outcomes show as `vkllm_mgmt_*` on 9090.

## Scope

- `internal/mgmt`: the four handlers, a consumer-side `Controller` interface
  (three methods), per-op call coalescing, the error body, body limit,
  metrics and log fields.
- `internal/auth`: one key loader shared with the gateway (CORE-060): two
  lists (`client-keys`, `management-tokens`), surface tagging, constant-time
  check, 10 s reload, and the per-key token bucket.
- Tests: `internal/mgmt/*_test.go` with `httptest` and a fake controller;
  `internal/auth/*_test.go`; one integration test against the CORE-050
  simulation harness.

Excludes: the 8080 server, mux root and the request-id middleware (CORE-060,
CORE-090); `/v1/*` handlers (CORE-060); state machine, drain and
`force` semantics (CORE-050); cost arithmetic and the ledger (CORE-100);
Secret mounts and file paths (CORE-080); rotation runbooks (CORE-110); the
`vk-llm` CLI (CORE-150) and `gpu-control.yml` (CORE-160); on-demand mode
(ADR 0010 §4).

## Design and contracts

### Routes (ADR 0012 §1)

`mgmt.New(d Deps) http.Handler` is mounted by `cmd/gpu-on-demand` at
`/api/v1/` on the CORE-060 server, behind `obs.RequestIDMiddleware`.
Port 9090 never serves it (ADR 0012 §6).

| Method, path | Request body | Success | Failure |
|---|---|---|---|
| `POST /api/v1/gpu/start` | empty or `{}` | 200 status document + `result: "already_running"` if state `READY`; 202 status document + `result: "accepted"` otherwise (`ALLOCATING`…`LOADING_MODEL`, or `DRAINING`/`DELETING`/`FAILED` with `desired=RUNNING` recorded) | 503 `gpu_unavailable` + `reason`; 400; 401; 403; 413; 429; 500 |
| `GET /api/v1/gpu/status` | none | 200 status document | 401; 403; 429; 500 |
| `POST /api/v1/gpu/stop` | empty, `{}`, `{"force": false}` or `{"force": true}` | 200 status document + `result: "already_stopped"` if state `STOPPED`; 202 status document + `result: "accepted"` otherwise (`DRAINING`, `DELETING`, or a provisioning state being cancelled) | 400 `force_not_enabled`; 400; 401; 403; 413; 429; 500 |
| `GET /api/v1/gpu/cost` | none | 200 cost document | 401; 403; 429; 500 |
| any other path or method under `/api/v1/` | — | — | 404 `not_found` / 405 `method_not_allowed`, after auth |

The HTTP status carries `already_running`/`already_stopped` (200) versus
`accepted` (202); no 409 is ever returned (plan R-O). `503 gpu_unavailable`
carries no `Retry-After`.

### Status document (ADR 0012 §1, verbatim field set)

```json
{
  "provider": "vastai", "state": "READY", "desired": "RUNNING", "generation": 7,
  "instance": {"gpu": "L40S", "machineId": 12345, "priceUSDPerHour": 0.71,
               "createdAt": "…", "readyAt": "…"},
  "model": {"alias": "qwen3.6-35b-a3b", "contextSize": 32768},
  "ready": true, "uptimeSeconds": 1830,
  "lastInferenceFinishedAt": "…", "idleDeadline": "…", "secondsUntilIdleShutdown": 1440,
  "cost": {"estimatedInstanceUSD": 0.36, "estimatedMonthUSD": 3.42, "budgetMonthUSD": 30},
  "unavailable": "", "installationId": "4f2c1a9b0e77", "labelPrefix": "vkllm:4f2c1a9b0e77:"
}
```

| Field | Source | Rule |
|---|---|---|
| `provider` | `provider.name` | |
| `state` | snapshot public state (ADR 0011 §6) | one of `STOPPED, ALLOCATING, STARTING, LOADING_MODEL, READY, DRAINING, DELETING, FAILED` |
| `desired` | snapshot | `RUNNING` or `STOPPED` |
| `instance` | snapshot | `null` when no provider instance exists; `readyAt` `null` before READY |
| `model` | `model.alias`, `model.contextSize` | from configuration |
| `ready` | derived | `state == "READY"` |
| `uptimeSeconds` | derived | `now − instance.createdAt`, whole seconds; 0 with no instance |
| `lastInferenceFinishedAt` | snapshot | rendered as given; `null` when the snapshot's value is zero |
| `idleDeadline` | snapshot | `null` unless `state` is `READY` |
| `secondsUntilIdleShutdown` | derived | `max(0, idleDeadline − now)` whole seconds; `null` when `idleDeadline` is `null` |
| `nextCreateAt` | snapshot | RFC 3339 UTC timestamp, `null` unless a future next-allowed-create time exists (render `null` if the stored time is in the past) |
| `cost` | snapshot cost block (CORE-100) | estimates |
| `unavailable` | snapshot | `""` or a reason below |
| `installationId`, `labelPrefix` | snapshot | |

Timestamps are RFC 3339 UTC. `now` comes from `Clock.Now`. The document
never holds an SSH host, port, host key, provider id or any credential.

### Cost document (FR-6.8; semantics owned by CORE-100)

```json
{"estimates": true, "priceUSDPerHour": 0.71, "uptimeSeconds": 1830,
 "estimatedInstanceUSD": 0.36, "estimatedMonthUSD": 3.42, "budgetMonthUSD": 30,
 "providerBalanceUSD": 12.5, "limitations": "…"}
```

`priceUSDPerHour` 0 and `estimatedInstanceUSD` 0 with no instance;
`providerBalanceUSD` `null` until the first successful balance read;
`limitations` is the fixed text CORE-100 defines. mgmt does no arithmetic
beyond `uptimeSeconds`.

### Error body (ADR 0012 §1)

`{"error": {"code": "…", "message": "…", "reason": "…"}}`; `reason` only on
`gpu_unavailable`. Messages never echo input. No `type` field: that is the
`/v1` schema (FR-1.5).

| Status | `code` | When |
|---|---|---|
| 400 | `invalid_json` | body not JSON, not an object, or an unknown field |
| 400 | `force_not_enabled` | `"force": true` while `lifecycle.shutdown.allowForce` is false |
| 401 | `invalid_token` | header missing, not `Bearer <token>`, or token unknown |
| 403 | `permission_denied` | an inference key (`client-keys`) |
| 404 / 405 | `not_found` / `method_not_allowed` | unknown path / method |
| 413 | `request_too_large` | body above 65 536 bytes |
| 429 | `rate_limit_exceeded` | token bucket empty; `Retry-After` = seconds to one token, minimum 1 |
| 500 | `internal_error` | any other controller error |
| 503 | `gpu_unavailable` | start denied; `reason` ∈ `budget_exhausted, attempts_exhausted, no_offer, balance_low` |

The reason enum is these four values only; an unknown provider balance is
folded into `balance_low` by `Budget.Allow`.

The 64 KiB limit is a package constant `maxBodyBytes = 65536`, not a
config key: management bodies are at most `{"force":true}`.

### Controller dependency (ADR 0012 §2)

```go
package mgmt

// Controller is the subset of *controller.Controller this package may call.
// A test fake implements it; Admit/RequestEnd are deliberately absent.
type Controller interface {
    Start(ctx context.Context) (controller.Snapshot, error)            // idempotent
    Stop(ctx context.Context, force bool) (controller.Snapshot, error) // idempotent; force reserved
    Status() controller.Snapshot
}

type Deps struct {
    Controller Controller
    Keys       *auth.Keys
    Limiter    *auth.Limiter        // its own instance; buckets are not shared with /v1
    Clock      clock.Clock
    Config     config.Mgmt          // provider.name, model.alias, model.contextSize, lifecycle.shutdown.allowForce
    Reg        prometheus.Registerer
    Log        *slog.Logger
}
func New(d Deps) http.Handler
```

The interface has three methods, so mgmt cannot reach `Admit`,
`RequestEnd` or anything that writes `lastInferenceFinishedAt` (ADR 0011 §2).
A `Start` denial is an error whose snapshot carries `Unavailable` set to one
of the four reasons; mgmt maps it to 503. Every controller call gets a
10 s context derived from the request.

### Call coalescing

Concurrent `start` requests share one in-flight `Controller.Start` call; so
do concurrent `stop` requests. A request arriving while a call of the same
op is in flight waits for it and returns its result; one arriving after it
returns makes a new call. `start` and `stop` are not coalesced with each
other: the controller orders them (architecture §7). About 20 lines with
`sync.Mutex` and a `done` channel; no `x/sync` (C-25).

### Auth (`internal/auth`, ADR 0009 §2, ADR 0012 §3)

```go
package auth

type Surface int // Inference, Management
var (
    ErrMissing      = errors.New("auth: missing")
    ErrMalformed    = errors.New("auth: malformed")
    ErrUnknown      = errors.New("auth: unknown")
    ErrWrongSurface = errors.New("auth: wrong surface")
)
func Load(clientPath, mgmtPath string, c clock.Clock, log *slog.Logger) (*Keys, error)
func (k *Keys) Run(ctx context.Context)                       // mtime poll every 10 s, atomic swap
func (k *Keys) Check(authorization string, want Surface) (keyID string, err error)
func (k *Keys) Loaded() bool                                  // both lists non-empty

func NewLimiter(rpm int, c clock.Clock) *Limiter
func (l *Limiter) Allow(keyID string) (ok bool, retryAfter time.Duration)
```

- One line per key; blank lines and surrounding spaces ignored.
- `Check` compares the SHA-256 digest of the presented token with
  `subtle.ConstantTimeCompare` against every entry of both lists, no early
  exit; a match in the other list returns `ErrWrongSurface` (403).
- A value present in both lists fails `Load`; on reload it keeps the
  previous sets and logs `keys_reload_failed`.
- `keyID` is the first 8 characters after `sk-vkllm-` (ADR 0009 §5).
- The token bucket moves here from CORE-060 so both surfaces share one
  implementation: capacity and refill from `gateway.rateLimitPerKeyRPM`
  (60; architecture §9), one bucket per `keyID`, clock-driven, dropped on
  key removal.

### Middleware order

auth → rate limit → body limit (`http.MaxBytesReader`, 413 at decode) →
handler. Auth runs before the body is read or any controller call.

### Observability

| Metric | Type | Labels |
|---|---|---|
| `vkllm_mgmt_requests_total` | counter | `op` ∈ {start,status,stop,cost,other}, `code` ∈ {200,202,400,401,403,404,405,413,429,500,503} |
| `vkllm_mgmt_request_seconds` | histogram | `op` |

One JSON log line per request: `request_id`, `op`, `status_code`, `code`,
`reason`, `duration_ms`, `key_id`, `state`, `generation`. Never
`Authorization`, the token, or the body (C-16, ADR 0009 §5).

## Requirements

- R1. The four routes MUST exist with the methods, bodies and status codes of the Routes table. (source: ADR 0012 §1, FR-7.1)
- R2. Every `/api/v1` request MUST be authenticated before its body is read or any controller call is made; missing, malformed or unknown tokens MUST get 401 `invalid_token`. (source: C-16, NFR-8.1)
- R3. An inference key MUST get 403 `permission_denied` on every `/api/v1` route; a management token MUST get 403 on `/v1` through the same `auth.Check`. (source: ADR 0012 §3, FR-7.2)
- R4. Token comparison MUST use `subtle.ConstantTimeCompare` over SHA-256 digests against every entry with no early exit. (source: ADR 0009 §2)
- R5. Tokens and `Authorization` MUST NOT appear in logs, metrics or responses. (source: ADR 0009 §5, C-16)
- R6. A removed or added token MUST take effect within 10 s plus one poll, without a restart. (source: ADR 0009 §4, CORE-110)
- R7. Each token MUST be limited to `gateway.rateLimitPerKeyRPM` requests per minute with 429 and `Retry-After`. (source: NFR-8.5, ADR 0012 Risks)
- R8. Bodies above 65 536 bytes MUST get 413 `request_too_large`. (source: NFR-8.5)
- R9. `start` and `stop` MUST be idempotent: repeats return 200 or 202 with the current status document, never 409. (source: ADR 0012 §1, FR-2.1)
- R10. Concurrent requests of the same op MUST reach the controller as one call. (source: FR-2.1)
- R11. mgmt MUST NOT call anything that writes `lastInferenceFinishedAt`, `readyAt` or `idleDeadline`; its controller dependency MUST be the three-method interface. (source: ADR 0011 §2, FR-3.4, FR-3.5)
- R12. A start denial MUST return 503 `gpu_unavailable` with `reason` from the snapshot and no `Retry-After`. (source: ADR 0012 §1, FR-2.6)
- R13. `"force": true` MUST get 400 `force_not_enabled` unless `lifecycle.shutdown.allowForce` is true, in which case mgmt MUST call `Stop(ctx, true)`. (source: ADR 0011 §3)
- R14. `stop` MUST return the snapshot the controller returns, which is `DRAINING` for a READY GPU; mgmt MUST NOT wait for the drain. (source: ADR 0011 §3, FR-3.6)
- R15. The cost document MUST carry `"estimates": true` and the `limitations` text. (source: FR-6.8, CORE-100)
- R16. The status document MUST NOT contain SSH addresses, provider ids or credentials. (source: C-18, ADR 0012 §1)
- R17. Metric labels MUST be the enumerations in Observability. (source: C-34)

## Implementation steps

1. `internal/auth`: `Load`, `Check`, `Loaded`, reload loop. Tests: `TestCheckMatrix`, `TestConstantTimeAllEntries` (counting compare hook sees every entry), `TestDuplicateAcrossListsRefused`, `TestReloadFakeClock`.
2. `auth.Limiter` (moved from CORE-060). Test: `TestLimiterFakeClock`.
3. `internal/mgmt` skeleton: routing, error body, middleware order, metrics. Tests: `TestUnknownRoute404`, `TestWrongMethod405`, `TestBodyLimit413`, `TestErrorShape`.
4. Status and cost mapping from a snapshot. Tests: `TestStatusDocumentGolden` (one golden file per public state), `TestCostDocument`, `TestStatusHasNoAddress`.
5. Start/stop handlers with coalescing and force gate. Tests: `TestStartCodes`, `TestStopCodes`, `TestCoalesceStart`, `TestForceRejected`.
6. Deadline isolation and log redaction. Tests: `TestNoDeadlineCalls`, `TestLogRedaction`.
7. Integration against the CORE-050 simulation harness. Test: `TestMgmtWithController`.

## Tests / acceptance criteria

All tier 1 (`go test ./internal/auth/... ./internal/mgmt/...`), fake clock,
no network beyond `httptest`. Every AC except AC7 sets
`rateLimitPerKeyRPM` to 10 000 so the limiter never interferes. Tier 2 is a `status` call on kind through
CORE-080 and CORE-150.

- AC1. Contract (FR-7.1). Fake controller scripted per state. Expected: start on STOPPED → snapshot ALLOCATING → 202; start on READY → 200; start on DRAINING → 202 with `desired` `RUNNING`; stop on READY → 202 `state` `DRAINING`; stop on STOPPED → 200; stop on ALLOCATING → 202; status → 200; cost → 200. Every body validates against the documents above.
- AC2. Start denial (FR-2.6). Fake `Start` returns an error with `Unavailable` `no_offer`, then each other reason. Expected: 503, `code` `gpu_unavailable`, `reason` equal to the snapshot's, no `Retry-After` header; `vkllm_mgmt_requests_total{op="start",code="503"}` = 4.
- AC3. 401/403 matrix (NFR-8.1, FR-7.2). One inference key, one management token. Action: each of the four routes with no header, `Basic x`, an unknown `sk-vkllm-REPLACE_ME`, the inference key, the token. Expected: 401/401/401/403/2xx per route (12 × 401, 4 × 403, 4 × 2xx); fake controller call count 0 for the 16 rejected requests. `auth` unit test: the token with `want=Inference` → `ErrWrongSurface`.
- AC4. Coalescing (FR-2.1). Fake `Start` blocks until released and returns READY. Action: 50 concurrent `POST start`; release once the package's waiter count is 49. Expected: fake `Start` calls = 1; 50 × 200 with identical bodies. Same for stop with the fake `Stop` returning `DRAINING`: 1 call, 50 × 202.
- AC5. Deadline untouched (FR-3.4, FR-3.5). Fake READY snapshot with fixed `idleDeadline`. Action: 10 starts, 100 status, 100 cost. Expected: `idleDeadline` byte-identical in all 210 bodies; the fake records only `Start`, `Stop`, `Status` (compile-time: it implements no other method).
- AC6. Real controller (FR-2.1, FR-2.5, FR-3.4, FR-3.6). CORE-050 harness, fake provider and store. Action: 50 concurrent starts from STOPPED; drive to READY; 10 more starts; 100 status; one stop. Expected: provider `CreateInstance` = 1; start p99 < 200 ms measured in-process (mgmt and controller overhead only; the real figure is CORE-140's); persisted `idleDeadline` unchanged across the 110 calls; stop response `state` `DRAINING`.
- AC7. Rate limit (NFR-8.5). RPM 60, fake clock frozen. Action: 61 status calls with one token. Expected: 60 × 200, 1 × 429 `rate_limit_exceeded` with `Retry-After: 1`; after advancing 1 s, one more 200. A second token is unaffected.
- AC8. Body limit (NFR-8.5). Action: `POST stop` with 65 537 bytes and a valid token. Expected: 413 `request_too_large`, fake `Stop` calls 0. Same body without a token: 401.
- AC9. Force (ADR 0011 §3). `allowForce` false: `{"force":true}` → 400 `force_not_enabled`, `Stop` calls 0; `{"force":false}` → 202, `Stop(ctx,false)` once. `allowForce` true: `{"force":true}` → `Stop(ctx,true)` once. `{"force":"yes"}` and `{"x":1}` → 400 `invalid_json`.
- AC10. Reload (ADR 0009 §4). Remove the token's line; advance the fake clock 10 s; poll runs. Expected: the token gets 401; a newly added token gets 200. Writing the token into both files leaves the previous sets active and logs `keys_reload_failed` once.
- AC11. Status mapping (FR-6.8). Snapshot READY, `idleDeadline` = now + 1440 s, `createdAt` = now − 1830 s. Expected: `secondsUntilIdleShutdown` 1440, `uptimeSeconds` 1830, `ready` true. STOPPED snapshot: `instance`, `idleDeadline`, `secondsUntilIdleShutdown` all `null`, `uptimeSeconds` 0. Cost body has `"estimates": true` and non-empty `limitations`.
- AC12. Redaction (R5, R16, R17). Run AC1–AC11 with the slog handler captured. Expected: 0 occurrences of any token or key; no `sshHost`/`sshPort`/provider id in any body; gathered registry has only the label values listed.

## Definition of done

- [ ] AC1–AC12 pass in CI.
- [ ] `make specs-check` passes.
- [ ] CORE-060 uses `internal/auth` for `/v1` and its management-token 403 test passes.
- [ ] Status flipped to DONE in the same PR; folder renamed `055-D-management-api`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Resolution |
|---|---|---|---|
| Exact `controller.Snapshot` field names (public state, desired, instance block, `LastInferenceFinishedAt`, `IdleDeadline`, cost block, `Unavailable`) and the Start-denial error value | mgmt maps them field by field | CORE-050 | snapshot carries the public state string per ADR 0011 §6; denial error wraps `controller.ErrUnavailable` |
| Is `no_offer` known synchronously on `Start`, or only later in status? | FR-2.6 wants 503, FR-2.5 wants < 200 ms | CORE-050 | 503 only when the snapshot already holds `no_offer` from the last search; otherwise 202 and `unavailable` in status; 202 may also be returned during a create-retry backoff (distinct from `no_offer`-based denial) |
| Should 2xx bodies carry a `result` field (`accepted`, `already_running`, `already_stopped`)? | ADR 0012 §1 lists the codes but no field | 055 | start/stop responses carry `result`: 200 → `"already_running"`/`"already_stopped"`; 202 → `"accepted"`; status and cost responses do not |
| `nextCreateAt` field on status document | mgmt needs to advertise when start will be allowed again | 055 | nullable RFC 3339 timestamp, rendered `null` unless the controller currently holds a future next-allowed-create time |
| `uptimeSeconds`: since `createdAt` or `readyAt`? | ADR 0012's example does not say | ADR 0012 clarification | `createdAt` (billed time, research §1.5) |
| Start/stop on `FAILED` | ADR 0011 §6 has it, ADR 0012 §1 does not | CORE-050 | treated like `DELETING`: 202 |
| Cost field names | CORE-100 is being rewritten | CORE-100 | the list above |

## Evidence

None yet.
