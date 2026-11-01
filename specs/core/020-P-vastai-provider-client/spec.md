---
id: "CORE-020"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-010"]
adrs: ["0001", "0003", "0004", "0005", "0006", "0009"]
---

# 020 — Vast.ai provider client and offer filter

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** L
**Risk:** high — this package is the only code that spends money or destroys instances
**Estimated effort:** two sessions (8–10 h)
**Recommended model:** strong — idempotent create, error classification and status mapping are cost-safety critical
**Depends on:** [CORE-010](../010-P-repo-skeleton-and-ci/spec.md)

## Outcome

`provider/vastai` implements ADR 0001's `Provider` over the Vast.ai REST API
with timeouts, 429 backoff, a label-idempotent create and a redacted key;
`provider/fake` lets CORE-050/060 script offers, failures and statuses.
Tests run offline on sanitised fixtures; a read-only probe answers Q4–Q6.

## Scope

- `internal/provider`: the `Provider` interface, its types and errors.
- `internal/provider/vastai` (REST client) and `internal/provider/fake`.
- Fixtures in `internal/provider/vastai/testdata/`; read-only probe
  `hack/vastprobe/main.go` for Q4, Q5, Q6.

Excludes: launch mode, `runtype`, `-p` keys and `onstart` content (ADR
0006, CORE-030, CORE-070; opaque fields here); when to create or destroy,
the two-observation rule, denylist bookkeeping, attempt budget (CORE-050);
ledger and balance floor (CORE-100); stop and recycle (ADR 0003 §6); any
paid call.

## Design and contracts

### Interface and types (`internal/provider`)

```go
type Provider interface {
    SearchOffers(ctx context.Context, f OfferFilter) ([]Offer, error)
    CreateInstance(ctx context.Context, r CreateRequest) (InstanceID, error)
    GetInstance(ctx context.Context, id InstanceID) (Instance, error)
    ListInstances(ctx context.Context, labelPrefix string) ([]Instance, error)
    DestroyInstance(ctx context.Context, id InstanceID) error
    Balance(ctx context.Context) (Money, error)
}

type InstanceID int64 // Vast "new_contract" / instance "id"
type Money int64      // micro-USD; avoids float drift in ledger sums

type OfferFilter struct {
    GPUName           string   // provider.filter.gpuName
    NumGPUs           int      // provider.filter.numGpus
    MinCPUCores       int      // provider.filter.minCpuCores
    MinCPURAMGB       int      // provider.filter.minCpuRamGB
    MinDiskGB         int      // provider.filter.minDiskGB
    MinReliability    float64  // provider.filter.minReliability
    MinInetDownMBps   float64  // provider.filter.minInetDownMBps
    MinDiskBwMBps     float64  // provider.filter.minDiskBwMBps
    MinCUDA           float64  // provider.filter.minCudaVersion
    Geolocation       []string // provider.filter.geolocation; empty = any
    MaxHourly         Money    // gpu.maxHourlyPriceUSD
    ExcludeMachineIDs []int64  // caller's 1-hour denylist
}

type Offer struct {
    ID, MachineID int64 // ID is the ask id for PUT /asks/{id}
    GPUName, Geolocation string
    NumGPUs int
    Hourly  Money // dph_total
    Reliability, InetDownMBps, DiskBwMBps, CUDAMaxGood float64
}

type CreateRequest struct {
    OfferID int64
    Image   string            // worker.image, digest-pinned
    DiskGB  int
    RunType string            // chosen by ADR 0006 / CORE-030
    Env     map[string]string // JSON object; may hold "-p X:X" keys
    Onstart string            // ≤ 4048 characters
    Label   string            // vkllm:<installationId>:<generation>
}

type Instance struct {
    ID                InstanceID
    Label, StatusMsg  string
    Status            InstanceStatus
    RawStatus         string // actual_status exactly as received
    MachineID         int64
    PublicIP, SSHHost string
    SSHPort           int
    Hourly            Money
    StartedAt         time.Time // start_date epoch float
}

type InstanceStatus int // StatusOther (zero), StatusRunning, StatusExited, StatusOffline, StatusUnknown
func (s InstanceStatus) Terminal() bool // true for Exited, Offline, Unknown only
```

`Money` from `dph_total` is `round(dph_total × 1e6)`; $0.80 is `800000`;
`Money.USD() float64` converts back. Callers that persist an `InstanceID`
as a string (ADR 0004 §1) use `strconv.FormatInt`.

### Status mapping (research §1.4)

One table in code, one row per string. Extending it is a one-line change.

| `actual_status` | `InstanceStatus` | `Terminal()` |
|---|---|---|
| `running` | `StatusRunning` | false |
| `exited` | `StatusExited` | true (research §1.4) |
| `offline` | `StatusOffline` | true (research §1.4) |
| `unknown` | `StatusUnknown` | true (research §1.4) |
| anything else, including empty | `StatusOther`, raw value in `RawStatus` | false |

Only the three literal strings research §1.4 names as never reaching
`running` are terminal, as in CORE-070. Boot-phase strings are unverified
(Q6) and land in `StatusOther`; a stuck instance is bounded by
`gpu.startupDeadline` (ADR 0003 §5), not by this mapping.

### Endpoints (research §1.1–1.5)

Base URL `https://console.vast.ai/api/v0` (configurable for tests), header
`Authorization: Bearer <key>` (research §1.1).

| Method | Op label | Request | Success shape | Timeout | Retries on retryable |
|---|---|---|---|---|---|
| `SearchOffers` | `search` | `POST /api/v0/bundles`, body below (research §1.2) | offer list | 30 s | 5 |
| `CreateInstance` | `create` | `PUT /api/v0/asks/{offer_id}` with `image`, `disk`, `runtype`, `env` (JSON object), `onstart`, `label` (research §1.3) | `{"success": true, "new_contract": <id>}` | 60 s | 0; label list before and after |
| `GetInstance` | `get` | `GET /api/v0/instances/{id}` (research §1.4) | instance fields | 15 s | 5 |
| `ListInstances` | `list` | `GET /api/v1/instances`, keyset pages of 25 (research §1.4) | all pages | 15 s per HTTP request | 5 per page |
| `DestroyInstance` | `destroy` | `DELETE /api/v0/instances/{id}` (research §1.5) | 2xx | 30 s | 5 |
| `Balance` | `balance` | `GET /api/v0/users/current`, field `balance` (research §1.5) | number | 15 s | 5 |

Create uses PUT: the docs' prose says POST, but the path definition and the
CLI use PUT (research §1.3). `env` is a JSON object, as the CLI sends it.

### Offer filter body

`SearchOffers` builds `{field: {op: value}}` with operators from research
§1.2. Units: `cpu_ram` in MB, `disk_space` GB, `disk_bw` and `inet_*` MB/s
(research §1.2; the `inet_*` unit is U until step 1).

| Field | Operator | Value |
|---|---|---|
| `gpu_name` | `eq` | `f.GPUName` |
| `num_gpus` | `eq` | `f.NumGPUs` |
| `cpu_cores` | `gte` | `f.MinCPUCores` |
| `cpu_ram` | `gte` | `f.MinCPURAMGB × 1000` (MB; see Open questions) |
| `disk_space` | `gte` | `f.MinDiskGB` |
| `dph_total` | `lte` | `f.MaxHourly` in USD |
| `reliability` | `gte` | `f.MinReliability` |
| `inet_down` | `gte` | `f.MinInetDownMBps` |
| `disk_bw` | `gte` | `f.MinDiskBwMBps` |
| `cuda_max_good` | `gte` | `f.MinCUDA` (12.8) |
| `verified` | `eq` | `true` (default made explicit, research §1.2) |
| `rentable` | `eq` | `true` (same) |
| `geolocation` | `in` | `f.Geolocation`, only when non-empty |
| `machine_id` | `notin` | `f.ExcludeMachineIDs`, only when non-empty |
| `type` | — | `ondemand` (research §1.2) |
| `order` | — | `dph_total` ascending |

After decoding, the client re-applies the price cap and denylist locally and
sorts by `Hourly`, then `ID`, ascending. The local check, not the server
filter, is the guarantee for FR-6.1.

### Idempotent create (C-2, C-11)

Contract: **at most one instance per label, ever.** For label `L`:

1. List instances by exact label `L`. One found: return its id, outcome
   `adopted`, no PUT. List fails: Retryable error, no PUT.
2. PUT `/asks/{offer}` once. 2xx with `success: true`: return `new_contract`.
3. Terminal result (4xx other than 429, `success: false`): return it.
4. Ambiguous result (timeout, transport, 5xx, 429): back off, list by `L`
   again. Found: adopt. Not found or list failed: return `ErrOutcomeUnknown`
   (Retryable).

- One PUT per call at most; the client never re-PUTs within a call.
- After `ErrOutcomeUnknown` the caller retries with the **same** label; step
  1 then adopts a late-visible instance.
- More than one instance with label `L`: Terminal error wrapping
  `ErrDuplicateLabel`, message lists the ids; the caller destroys extras.

### List semantics

The caller passes the prefix `vkllm:<installationId>:`, where
`installationId` is configured (ADR 0004 §6). `ListInstances(prefix)` reads every page (25 each) of `GET /api/v1/instances`
and keeps labels starting with `prefix`, client-side: research §1.2 lists no
prefix operator. A failure on any page returns an error and no instances,
never a partial list, so a failed read never looks like absence (C-9).

### Errors (C-28)

```go
type Class int // Retryable, Terminal, Fatal
type Error struct {
    Op     string // search|create|get|list|destroy|balance
    Class  Class
    Status int // HTTP status, 0 for transport
    Err    error
}
var ErrNotFound = errors.New("provider: instance not found")
var ErrDuplicateLabel = errors.New("provider: duplicate label")
var ErrOutcomeUnknown = errors.New("provider: create outcome unknown")
```

| Condition | Class | Note |
|---|---|---|
| transport error, context deadline of the per-call timeout | Retryable | caller's own ctx cancellation is returned as-is, not retried |
| HTTP 429 | Retryable | counted in `vkllm_provider_rate_limited_total` |
| HTTP 5xx | Retryable | |
| HTTP 401, 403 | Fatal | bad or rotated key; caller alerts (ADR 0009 §4) |
| HTTP 404 on `get` or `destroy` | `ErrNotFound` | see Open questions |
| other 4xx, `success: false`, undecodable body | Terminal | |
| `onstart` > 4048 chars, empty `Image` or `Label` | Terminal | before any request |

All errors wrap with `%w`; `errors.Is(err, ErrNotFound)` works.

### Backoff for 429 and retryable errors

No `Retry-After`; limits are per endpoint (research §1.1), so backoff is per op.

- Delay for retry `n` (0-based): `d = min(30 s, 2 s × 2^n)`, sleep
  `d/2 + rand[0, d/2)`. The first retry waits between 1 s and 2 s, so a
  retry never follows a 429 sooner than 1 s (FR-5.1).
- Sleeps use `clock.Clock` from `internal/clock` (`Now`, `After`,
  `NewTicker`; fake in `internal/clock/fake`) and honour `ctx`.

### Concurrency and security (C-27, C-16, ADR 0009)

- Concurrency-safe, no goroutines, one `*http.Client`; bodies read through
  `io.LimitReader` (8 MiB) and always closed.
- The key is read through `APIKey func() (string, error)` on every request,
  so a rotated Secret takes effect without a restart (ADR 0009 §4).
- The `Authorization` header, the key and request bodies are never logged;
  errors carry op and status only.
- Fixtures hold no key, no real IP or host: addresses become `192.0.2.x`,
  labels `vkllm:test:<n>`.

### Observability

| Metric | Type | Labels |
|---|---|---|
| `vkllm_provider_requests_total` | counter | `op` ∈ {search, create, get, list, destroy, balance}; `outcome` ∈ {ok, not_found, retryable, terminal, fatal, adopted} |
| `vkllm_provider_request_seconds` | histogram | `op` |
| `vkllm_provider_rate_limited_total` | counter | none |

Counts are per HTTP attempt. Logs: debug per attempt, warn per failed call;
fields `op`, `status_code`, `attempt`, `duration_ms`, `outcome`, plus
`instance_id` and `label` on create, get, destroy.

### Fake (`internal/provider/fake`)

```go
type Fake struct{ /* mutex-guarded */ }
func New(c clock.Clock) *Fake                                   // internal/clock
func (f *Fake) SetOffers(o ...provider.Offer)
func (f *Fake) SetLatency(op string, d time.Duration)          // via Clock
func (f *Fake) FailNext(op string, n int, err error)           // next n calls of op
func (f *Fake) SetStatusSequence(id provider.InstanceID, s ...provider.InstanceStatus) // advanced per GetInstance/ListInstances observation
func (f *Fake) FailDestroy(n int)                              // first n destroys fail Retryable
func (f *Fake) AddInstance(i provider.Instance)                // orphans, restart tests
func (f *Fake) Calls(op string) int
func (f *Fake) SetBalance(m provider.Money)
func NewSeeded(c clock.Clock) *Fake // for provider.name=fake in the binary
```

`NewSeeded` holds one offer (`L40S`, $0.50/h, machine 1); creates reach
`running` on the first `GetInstance` with `SSHHost` `fake`, port 22;
`Balance` is $100. `provider.name=fake` wires `NewSeeded`.
Both implementations select through one shared `provider.Select(offers, f)`
(cap, denylist, order). Fake creates adopt by label like the real client
and get sequential ids; after a destroy, `GetInstance` returns `ErrNotFound`.

## Requirements

- R1. `Provider` MUST have exactly the six methods and signatures of ADR 0001 §2. (source: ADR 0001 §2)
- R2. `SearchOffers` MUST NOT return an offer with `Hourly` above `MaxHourly` or a `MachineID` in `ExcludeMachineIDs`. (source: FR-6.1, FR-2.2, ADR 0003 §5)
- R3. `SearchOffers` MUST return offers sorted by `dph_total` ascending. (source: FR-2.2)
- R4. The search body MUST express each filter as `{field: {op: value}}` with the field names and operators of research §1.2 and the thresholds of ADR 0005 §3. (source: research §1.2, ADR 0005 §3)
- R5. `CreateInstance` MUST use `PUT /api/v0/asks/{offer_id}` and send `env` as a JSON object. (source: research §1.3)
- R6. `CreateInstance` MUST list by the exact label before any PUT and adopt a found instance without sending a PUT. (source: C-2, C-11)
- R7. After an ambiguous PUT failure `CreateInstance` MUST list by the exact label again and adopt, or return `ErrOutcomeUnknown`. (source: C-2)
- R7a. A caller that got `ErrOutcomeUnknown` MUST retry with the same label, not a new generation. (source: C-2, C-11)
- R8. `CreateInstance` MUST reject `onstart` longer than 4048 characters without calling the API. (source: research §1.3)
- R9. `ListInstances` MUST return an error, never a partial or empty list, when any page fails. (source: C-9, ADR 0003 Risks)
- R10. Every HTTP request MUST run under its per-op timeout: search 30 s, create 60 s, get 15 s, list 15 s (per page), destroy 30 s, balance 15 s. (source: C-27, architecture §7)
- R11. A retry after HTTP 429 MUST wait at least 1 s and MUST NOT depend on `Retry-After`. (source: FR-5.1, research §1.1)
- R12. Read ops MUST retry at most 5 times; create MUST send at most 1 PUT per call. (source: C-27, C-2)
- R13. Every error MUST be classified Retryable, Terminal or Fatal and wrapped with `%w`. (source: C-28)
- R14. Only `exited`, `offline`, `unknown` MUST map to terminal statuses; any other string MUST map to non-terminal `StatusOther` with `RawStatus` kept. (source: research §1.4, CORE-070)
- R15. The API key, the `Authorization` header and request bodies MUST NOT appear in logs, errors or metrics. (source: C-16, ADR 0009 §5)
- R16. The client MUST read the key on each request, not once at start. (source: ADR 0009 §4)
- R17. The client MUST NOT call the stop or recycle endpoints. (source: ADR 0003 §6)
- R18. Metric labels MUST be limited to the enumerations in the metrics table. (source: C-34)
- R19. Unit tests MUST use fixtures, `httptest`, the fake clock and a seeded random source; no network, no sleeps. (source: C-29)

## Implementation steps

1. **Probe (Q4, Q5, Q6).** `hack/vastprobe/main.go`, read-only: `POST
   /bundles` for one L40S offer, `GET /users/current`, `GET
   /api/v1/instances`, `GET /api/v0/charges`, OpenAPI `actual_status`
   schema; compare one offer's `inet_down`, `disk_bw`, `cpu_ram` with the
   console. Record in Evidence and research.md §1.2/§1.4/§1.5; save
   sanitised fixtures. Test: CI grep finds no `PUT`/`DELETE` in the probe.
2. **Types, errors, `Select`.** Tests: `TestStatusMapping` (table rows;
   `""`, `"loading-xyz"` → `StatusOther`), `TestErrorClassIs`, `TestSelectTable`.
3. **Filter body.** Test: `TestFilterBody` (golden JSON variants).
4. **HTTP core** (key, timeouts, body limit, classification, backoff).
   Tests: `TestBackoff429`, `TestClassification`, `TestKeyNotLogged`.
5. **Search, get, list, destroy, balance** on `httptest` with fixtures.
   Tests: `TestSearchFixture`, `TestGetFixture`, `TestListPagination`,
   `TestListPageFailure`, `TestDestroyNotFound`, `TestBalanceFixture`.
6. **Create with adopt.** Tests: `TestCreateAdoptBeforePut`,
   `TestCreateAdoptAfterAmbiguous`, `TestCreateOnePerLabel`,
   `TestCreateDuplicateLabel`, `TestCreateOnstartTooLong`, `TestMetrics`.
7. **Fake.** Tests: `TestFakeScript`, `TestFakeSharesSelect`,
   `TestFakeSeeded` (offer, `running`, balance $100).

## Tests / acceptance criteria

- AC1 (FR-2.2, FR-6.1). Setup: 10 offers incl. $0.81/h, $0.80/h, a
  denylisted machine at $0.55/h, two at $0.60/h with ids 7 and 3. Action:
  `Select`, default config. Expected: $0.81 and denylisted absent; first is
  id 3; $0.80 present; prices non-decreasing.
- AC2 (FR-2.2). Setup: default config. Action: build the search body.
  Expected: byte-equal to `testdata/filter_default.json`, which contains
  `"cpu_ram": {"gte": 32000}`, `"dph_total": {"lte": 0.8}`,
  `"cuda_max_good": {"gte": 12.8}` and no `geolocation` key.
- AC3 (FR-5.1). Setup: 429 `API requests too frequent endpoint
  threshold=2` twice, then 200; fake clock. Action: `GetInstance`.
  Expected: 3 attempts; sleeps ≥ 1 s and ≥ 2 s; `rate_limited_total` = 2;
  `requests_total{op="get",outcome="ok"}` = 1.
- AC4 (FR-5.1). Setup: server always returns 429. Action: `GetInstance`.
  Expected: exactly 6 attempts (1 + 5 retries), Retryable error, no
  real sleep (test wall time < 1 s).
- AC5 (C-2). Setup: list holds label `vkllm:test:4`. Action: create with
  that label. Expected: 0 PUTs, the listed id, `outcome="adopted"` = 1.
- AC6 (C-2). Setup: list empty; PUT returns 504; the second list holds the
  label. Expected: 1 PUT, 2 lists, the listed id, adopted.
- AC7 (C-2, at most one per label). Setup: `httptest` server that stores
  created labels; PUT 1 creates then returns 504, the next list hides it
  (lag); later lists show it. Action: 10 `CreateInstance` calls with one
  label. Expected: call 1 returns `ErrOutcomeUnknown`; calls 2–10 send
  0 PUTs and return the same id; the server holds exactly 1 instance.
- AC8. List empty; PUT returns 400 `{"success": false}`. Expected: 1 PUT,
  1 list, Terminal error.
- AC9. `onstart` of 4049 characters. Expected: 0 requests, Terminal error.
- AC10 (C-9). Setup: pages of 25, 25, 7 instances, 12 with prefix
  `vkllm:test:`; page 2 fails 6 times. Expected: error, nil slice. Without
  the failure: 12 instances.
- AC11. Setup: `DELETE` 200 then `GET` 404. Expected: `DestroyInstance`
  nil; `GetInstance` error satisfies `errors.Is(err, ErrNotFound)`.
- AC12 (NFR-7.2, C-16). Setup: key `test-key-ABCDEF` from `APIKey`; server
  returns 401; log captured to a buffer. Action: every op once.
  Expected: Fatal class; captured logs and every `err.Error()` contain
  neither `test-key-ABCDEF` nor `Bearer`.
- AC13. `APIKey` yields `k1` then `k2`: server sees `Bearer k1`, `Bearer k2`.
- AC14. Fixture `instance_get.json` (`running`, `ssh_port` 41022): yields
  `StatusRunning`, `SSHPort` 41022, `StartedAt` = `start_date` to the second.
- AC15. Fake: `FailDestroy(3)` → calls 1–3 return Retryable, call 4 nil,
  `Calls("destroy")` = 4 (supports FR-4.6 in CORE-050).
- AC16 (C-29). `go test ./internal/provider/...` passes offline, < 5 s.

## Definition of done

- [ ] Tests AC1–AC16 pass in CI.
- [ ] Q4, Q5, Q6 answered in Evidence, `docs/research.md` and
      `docs/open-questions.md`; fixtures hold no key, IP or host.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE and folder renamed in the same PR.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Q4: unit of `inet_down`, `disk_bw` in REST search (research §1.2 U) | 500 vs 62.5 MB/s threshold | step 1 probe vs console | MB/s |
| GB→MB factor for `cpu_ram` (1000 or 1024) | 32 GB hosts at the edge | step 1: compare a 32 GB host's `cpu_ram` | 1000; lower bound never excludes a host advertised as 32 GB |
| Q5: does `GET /api/v0/charges` or `/api/v1/invoices` exist (research §1.5 U) | real spend vs estimated ledger | step 1 probe | not used; CORE-100 ledger |
| Q6: full `actual_status` enum (research §1.4 U) | naming boot strings in metrics and logs | step 1 (OpenAPI schema); CORE-030 logs raw strings every poll | unrecognised → `StatusOther`, non-terminal |
| Does a new contract appear in `/api/v1/instances` immediately after create, or can the list lag? | adopt-after-ambiguous may miss it | CORE-030 create | assume lag; same-label retry covers it |
| Does Vast limit `label` length? | label must fit `vkllm:<installationId>:<generation>` | settled by CORE-030 creating with a 32-character installation id label | assume ≥ 64 chars |
| `Instance.Ports` (research §1.4) | only ADR 0006 bundle B dials a mapped port | CORE-030 | omitted; added only if B is chosen |
| Name of the keyset cursor parameter for `/api/v1/instances` | pagination code | step 1 probe, OpenAPI document | follow the OpenAPI document |
| Does `select_filters` support a label prefix operator | one request instead of all pages | step 1 | client-side prefix filter |
| Body shape of `type` and `order` in `/bundles` | ordering and on-demand filter | step 1 against OpenAPI | `type` top-level string; client sorts regardless |
| Format of `geolocation` values (country code or name) | region filter | step 1 offer response | pass config values unchanged |
| Response of `GET /api/v0/instances/{id}` for a destroyed id | `ErrNotFound` detection, ADR 0003 §6 | CORE-030 destroy | 404 → `ErrNotFound`; anything else Retryable |
| 429 threshold units (research §1.1 U) | tuning backoff base | not needed | not parsed |

## Evidence

None yet.
