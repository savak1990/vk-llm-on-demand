---
id: "CORE-100"
status: "DONE"
updated: "2026-11-06"
depends_on: ["CORE-050"]
adrs: ["0003", "0004", "0005", "0010", "0011", "0012"]
---

# 100 — Cost controls

**Status note:** Originally written before CORE-050 existed; amended to match the shipped `controller.Budget` interface.

**Complexity:** M
**Risk:** medium — a bug either lets spend run past C-1/C-7's caps or wrongly kills a paying request
**Estimated effort:** one session (4–6 h)
**Recommended model:** strong — rounding and fold-on-overflow logic on a money path must be exact, not "close enough"
**Depends on:** [CORE-050](../050-D-gpu-lifecycle-controller/spec.md)

## Outcome

`internal/budget` gives the controller one gate, `Checker.Allow`, that must pass before any offer search and again before any `CreateInstance` call. It drives the month-to-date ledger bookkeeping on `CORE-040`'s `store.Ledger`, the $30/0.80 USD arithmetic behind `vkllm_budget_used_ratio`, and the provider-balance floor. Done, a simulated month of demand stops provisioning the instant month-to-date spend would reach `budget.monthlyUSD`, drains a `Serving` instance the same way — never provisions with less than two hours of prepaid balance at stake, and reports estimated cost and its own blind spots through four metrics, `GET /api/v1/gpu/status` and `GET /api/v1/gpu/cost`.

## Scope

- `internal/budget`: `Checker`, `Config`, `Allow`, `UsedRatio`, `AssertPrice`, and the ledger bookkeeping functions (`OpenEntry`, `RefreshOpenEntry`, `CloseEntry`, `Rollover`, `EstimatedMTD`) operating on `CORE-040`'s `store.Ledger`/`store.LedgerEntry` as already defined there.
- Month rollover (UTC, first of month): what gets dropped, what persists.
- The cost model: how Vast.ai bills, why a `dph × uptime` estimate drifts from it, what it ignores, reconciliation against the console, and the worked arithmetic for the shipped defaults, including open question Q5.
- Four metrics (`vkllm_budget_used_ratio`, `vkllm_cost_estimated_usd_total`, `vkllm_cost_instance_usd`, `vkllm_provider_balance_usd`): this package computes their values with pure functions; `CORE-050` calls `CORE-090`'s obs setters after each commit. The alert expectation placed on `CORE-050`'s orphan metric.
- Documented semantics of `POST /api/v1/gpu/stop` (ADR 0012 §1) and of budget exhaustion — monthly cap or `maxInstanceSpendUSD` — while `Serving`, including the `vkllm_shutdowns_total{reason=budget}` increment `CORE-050` makes on either trigger, from the cost side.

Excludes: the `State` enum, transition table, provisioning saga, reconcile loop and restart recovery (`CORE-050`); idle, max-lifetime, startup-deadline and attempt-budget enforcement, already decided in ADR 0011 §1, §5 and owned by `CORE-050`; the orphan sweep's detection logic, its metric, and max-instances enforcement — FR-6.2 is enforced by `CORE-050`'s orphan reconcile rows, not here; the `POST /api/v1/gpu/stop`, `GET /api/v1/gpu/status` and `GET /api/v1/gpu/cost` handlers (`CORE-055`); the Grafana dashboard and alert YAML, and metric registration (`CORE-090`); `Provider.Balance`'s HTTP implementation (`CORE-020`).

## Design and contracts

### Package choice: `internal/budget`, not `internal/cost`

The common brief's fixed package list is `internal/{gateway,controller,provider,store,tunnel,worker,config,obs,budget}`; `internal/budget` is a top-level package. `Checker` holds only `Config`; every method takes values the caller already has.

### Ledger

`internal/budget` uses `store.Ledger`/`store.LedgerEntry` (CORE-040) as already defined there: `Month`, `TotalUSD` (running month-to-date total of every closed entry's cost, added when each entry closes), `Entries` (oldest first, at most 100); each `LedgerEntry` carries `Gen`, `Start`, `End` (nil while open), `DPH`, `EstUSD`. No amendment to CORE-040's types is needed.

`MaxLedgerEntries = 100`. With `lifecycle.safety.maxInstances = 1`, one entry opens and closes per cycle, so 100 is months of history; the cap guards the pathological case (attempt-budget churn), not normal operation.

### Cost estimation

Estimated cost for one entry is `dph × hours`, `hours` being elapsed wall time **rounded up to the next whole second** before dividing by 3600 — never nearest, so an in-progress instance feeding `Allow`'s deny gate is never understated.

```go
// OpenEntry runs on the Created event (ADR 0003 §2, Creating→Booting).
func OpenEntry(doc *store.Document, now time.Time, gen int64, dph float64)
// RefreshOpenEntry recomputes EstUSD for the single open entry. Called
// every Tick so vkllm_cost_instance_usd and EstimatedMTD stay current.
func RefreshOpenEntry(doc *store.Document, now time.Time)
// CloseEntry runs on Destroying→Idle: sets End, does a final refresh, adds
// the entry's final EstUSD to TotalUSD, then drops the oldest closed
// entries past MaxLedgerEntries (their cost is already in TotalUSD).
func CloseEntry(doc *store.Document, now time.Time, gen int64)
// EstimatedMTD is TotalUSD + the sum of every open entry's current EstUSD.
func EstimatedMTD(doc store.Document) float64
```

Fold-on-overflow only removes entries with `End != nil`, never the open entry, so `CloseEntry` cannot drop the entry it just closed.

### Month rollover

```go
// Rollover fires at most once per Tick, before Allow.
func Rollover(doc *store.Document, now time.Time)
```

Checked once per `Tick`, before `Allow`: `now.UTC().Format("2006-01") != ledger.Month`. On rollover, `ledger.Month` becomes the new string, `TotalUSD` resets to `0`, and every closed entry is dropped: Prometheus already holds `vkllm_cost_estimated_usd_total`'s history (architecture §5). A still-open entry (an instance `Serving` across midnight UTC) is **not** carried over with its old `Start`; it is replaced by a fresh entry for the same `gen`/`dph` with `Start = now`, so no entry's `Start` is ever outside its own `Month`, and the previous month's partial cost is dropped with the rest.

The ledger functions and `Allow` are pure and call no `obs` setter; they run inside a CAS function that `Transition` may retry up to 8 times (`CORE-040` R3/R4). The caller (`CORE-050`) sets the four metrics once, after `Transition` commits, from the committed document — never from inside the retried function, so a conflict retry never double-counts. Rollover resets the ledger's month-to-date view; `vkllm_cost_estimated_usd_total` is a counter the caller only ever increases, never decreases.

### `Checker.Allow`: the monthly cap and the balance floor

```go
type Config struct {
	MonthlyUSD        float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

type Checker struct {
	cfg Config
}
func New(cfg Config) *Checker

// Allow returns "" when a new provisioning attempt may proceed, or one
// of "budget_exhausted"/"balance_low" otherwise. An unknown balance
// (known == false) is treated as "balance_low", never a separate value.
func (c *Checker) Allow(doc store.Document, balance provider.Money, known bool) string

// UsedRatio is EstimatedMTD(doc) / cfg.MonthlyUSD; it never denies by itself.
func (c *Checker) UsedRatio(doc store.Document) float64
```

No clock dependency — every time-sensitive method takes `now time.Time` from its caller.

An unknown balance folds into `balance_low` — matches ADR 0012 §1's enum; the ADR's `balance_unknown` value is stale and is not reopened (CORE-055's own ledger already noted this fold).

`Allow` runs two checks in order and returns on the first failure:

| Order | Check | Deny condition | `reason` |
|---|---|---|---|
| 1 | Monthly cap | `EstimatedMTD(doc) >= cfg.MonthlyUSD` | `"budget_exhausted"` |
| 2 | Balance floor | `balance < 2 * cfg.MaxHourlyPriceUSD` | `"balance_low"` |

`Allow` returns the empty string `""` otherwise. `WarnFraction` is never checked here; warning is a metric-and-alert concern (FR-6.4), not a deny.

**Why `2 × maxHourlyPrice`, not 1×:** one hour covers a full `Serving` hour at the worst price without the account reaching $0, where Vast schedules the instance and its data for deletion (research §1.5). The second hour is margin: `lifecycle.startupTimeout` (15 min) + `lifecycle.shutdown.drainTimeout` (60s) + the attempt-backoff ceiling (30 min, ADR 0003 §5, unchanged by ADR 0011) sum to under an hour of delay before destroy confirms.

### Per-instance spend cap

Already implemented and tested inline in `internal/controller/reconcile.go` (`reconcile_test.go`), reading the open ledger entry's `EstUSD` directly against `lifecycle.safety.maxInstanceSpendUSD` — never delegated to this package. R12/AC9 are satisfied by that existing code, not by anything this spec's own package builds.

### `AssertPrice`: the second price check

`CORE-020`'s offer filter excludes offers above `lifecycle.safety.maxHourlyPriceUSD` at search time. Nothing guarantees the price holds between `SearchOffers` and the matching `CreateInstance` (research §1.2 is silent on atomicity), so the controller asserts again right before `CreateInstance`:

```go
// AssertPrice returns ErrPriceTooHigh when dph exceeds max; equal is allowed.
func (c *Checker) AssertPrice(hourlyUSD, capUSD float64) error
```

A failed assertion is terminal for that attempt (counted against `lifecycle.safety.maxProvisionAttemptsPerHour` by `CORE-050`): C-1 extends to never creating a GPU above the configured ceiling, even once.

### Max instances: owned by `CORE-050`

FR-6.2 (at most `lifecycle.safety.maxInstances` surviving) is enforced by `CORE-050`'s orphan reconcile rows, not by this package: the saga adopts the lowest provider id on a race, and the orphan rows destroy every id that is not `providerId`. `internal/budget` has no survivor-selection function.

### Kill switch and exhaustion while `Serving`

`POST /api/v1/gpu/stop` (ADR 0012 §1) sets `desired = STOPPED`, indistinguishable here from the monthly cap or `maxInstanceSpendUSD` being hit: all three raise a `StopRequested`/`BudgetExhausted` event treated like idle timeout or max lifetime — stop admitting, drain, destroy. `Allow` needs no special case for `desired == STOPPED`: the controller never calls it once desired is `STOPPED`.

Budget exhaustion while `Serving` — monthly cap or per-instance spend limit — follows the same `Draining → Destroying` path (ADR 0011 §1, §3) and increments `vkllm_shutdowns_total{reason=budget}` (`CORE-050`). New requests during that drain get `503 backend_unavailable` with no `Retry-After` (ADR 0010 §2–§3) so clients stop polling rather than hammer a GPU that will not come back this month. In-flight requests finish inside `lifecycle.shutdown.drainTimeout` (60s); the ledger entry closes when `Destroying` confirms, at whatever `EstUSD` the elapsed drain time produced — drain time is billed time, and the estimate says so.

### `GET /api/v1/gpu/cost` payload

The handler is `CORE-055`'s; this package supplies `estimatedInstanceUSD` (open entry's `EstUSD`, `0` with none), `estimatedMonthUSD` (`EstimatedMTD(doc)`), `budgetMonthUSD` (`cfg.MonthlyUSD`) and `priceUSDPerHour` (open entry's `dph`). `CORE-050` adds `uptimeSeconds` (`now - readyAt`) and `providerBalanceUSD` (the `balanceUSD` `Allow`'s caller already read this tick):

```json
{"estimatedInstanceUSD": 0.36, "estimatedMonthUSD": 3.42, "budgetMonthUSD": 30,
 "priceUSDPerHour": 0.71, "uptimeSeconds": 1830, "providerBalanceUSD": 42.10,
 "estimates": true, "limitations": "excludes storage and bandwidth; see the cost model below"}
```

`estimates` is a literal `true`; `limitations` a static string naming what the cost model below ignores — FR-6.8 requires exactly these markers.

### Cost model

**How Vast actually bills (research §1.5):** GPU time per second while active/connected, **not** charged during `Loading` (image-pull); storage per second in every state but `offline`; bandwidth per byte in any state. The `dph` read once at `Created` is used for that entry's entire life; whether `dph_total` can drift on a running instance is an open question below.

**Why `dph × uptime` drifts from the real bill:**

| Effect | Direction | Why |
|---|---|---|
| Our entry runs from `Created` through the provider's own image-pull `Loading` window | **overcounts** | that window is billed $0 for GPU time (research §1.5), but our entry is already open and accruing at `dph` |
| Storage for the instance's disk | **undercounts** | billed continuously in every state but `offline`; never in `dph` or in this ledger at all |
| Bandwidth for the 26 GB model download (ADR 0005) and every proxied token | **undercounts** | billed per byte; never in `dph` or in this ledger |

**Ignored entirely:** storage, bandwidth, any undocumented fee, and anything in the console invoice beyond `dph × active seconds`. **Reconciliation:** compare `vkllm_cost_estimated_usd_total`'s monthly `increase()` (or `GET /api/v1/gpu/cost`'s `estimatedMonthUSD`) against the console's monthly total; expect it low by the storage/bandwidth amounts above — a large unexplained gap signals re-checking `dph_total` via `GetInstance`, not a bug here by default. **Q5 — a provider charges endpoint:** research §1.5 could not confirm `GET /api/v0/charges` or `GET /api/v1/invoices` exist; if `CORE-020` finds one, add a **second**, independently-sourced `vkllm_cost_provider_usd` series, never a replacement, since the endpoint can lag, rate-limit (research §1.1), or be unavailable when `Allow` needs an answer.

**Worked arithmetic** (`budget.monthlyUSD=30`, `lifecycle.safety.maxHourlyPriceUSD=0.80`, `lifecycle.idleShutdown.timeout=30m`): `$30 ÷ $0.80/h ≈ 37.5 h` of continuous max-price GPU time per month before the cap stops provisioning (~1.25 h/day evenly, ignoring the undercount above). One idle-timeout cycle with zero useful serving costs exactly `0.80 × (30/60) = $0.40`. One failed cold start dying during `Loading` is billed $0 GPU time; its real cost is storage for the few minutes it existed (ADR 0005's $0.05–0.20/GB-month, 60 GB, ~10 min: a fraction of a cent) plus whatever partial bytes of the 26 GB model were pulled, at a typical `inet_down_cost` ADR 0005 calls "cents" for the full download — **a few cents at most**, while our entry (open from `Created`, spanning `Loading`) reports the full `dph` rate — the overcount above, made concrete. With `maxInstanceSpendUSD` at its default `0`, the per-instance ceiling is the `$9.60` product above, not this monthly figure.

### Observability

The ledger functions and `Allow` are pure and call no `obs` setter; they run inside a CAS function that may be retried up to 8 times. The caller (`CORE-050`) sets the four metrics after the commit, from the committed document, never from inside the retried function (a conflict retry must never double-count a cost):

| Metric | Type | Caller computes it as |
|---|---|---|
| `vkllm_budget_used_ratio` | gauge | `Budget.UsedRatio(doc)` |
| `vkllm_cost_estimated_usd_total` | counter | each entry's `EstUSD` increase versus the previous committed document, ignoring rollover drops — `obs.AddCostEstimatedUSD(delta)` only ever adds |
| `vkllm_cost_instance_usd` | gauge | the open entry's `EstUSD`, or `0` with none |
| `vkllm_provider_balance_usd` | gauge | the `balanceUSD` just read |

`obs` (CORE-090) owns registration; this package owns none of the four. None carries a label (C-34). Log fields on a denied `Allow`: `reason`, `estimated_mtd_usd`, `monthly_budget_usd`, `balance_usd` (never the raw provider body, prompts, keys or instance ids — C-16).

**Alert expectation added to `CORE-050`'s orphan metric:** any `vkllm_orphans_destroyed_total` increment pages inside 5 minutes (`CORE-090` renders the rule) — an orphan is a GPU running unobserved by this system's own budget math, exactly what C-1 exists to prevent.

## Requirements

- R1. `Checker.Allow` MUST return `"budget_exhausted"` when `EstimatedMTD(doc) >= cfg.MonthlyUSD`. (source: FR-6.3, C-1, C-7)
- R2. `Checker.Allow` MUST return `"balance_low"` when `balance < 2 * cfg.MaxHourlyPriceUSD`, checked only after R1 passes. (source: research §1.5, ADR 0011 §5)
- R3. `EstimatedMTD` MUST equal `ledger.TotalUSD` plus the sum of every open entry's current `EstUSD`. (source: ADR 0004 Consequences, FR-6.3)
- R4. Every `EstUSD` computation MUST round elapsed time up to the next whole second before multiplying by `dph / 3600`, so a deny-gate input is never understated. (source: C-1, FR-6.3)
- R5. `ledger.Entries` MUST NOT exceed 100 after any `CloseEntry` call returns; a closed entry's cost MUST already be in `ledger.TotalUSD` by the time it is folded away, so folding never changes `TotalUSD` — the array MAY transiently hold up to 101 entries between an `OpenEntry` and its matching `CloseEntry` (single-instance, one open generation at a time), never permanently. (source: ADR 0004 Consequences)
- R6. Rollover MUST fire when `now.UTC().Format("2006-01") != ledger.Month`, reset `TotalUSD` to `0`, drop every closed entry, and re-open any still-running entry with `Start = now`; `vkllm_cost_estimated_usd_total` MUST NOT decrease, ever. (source: ADR 0004 Consequences; architecture §5)
- R7. `AssertPrice` MUST return `ErrPriceTooHigh` for any `dph` strictly above `lifecycle.safety.maxHourlyPriceUSD`, called immediately before every `CreateInstance`, in addition to `CORE-020`'s search-time filter. (source: FR-6.1, C-1)
- R8. `OpenEntry` MUST run on the `Created` event with the offer's `dph` and MUST NOT run twice for the same generation; `CloseEntry` MUST run once `Destroying` confirms and MUST set `End` before any fold-on-overflow check. (source: ADR 0003 §2, C-1)
- R9. Budget exhaustion while `Serving` — monthly cap or per-instance spend limit — MUST result in `Draining` then `Destroying`; new requests during that drain MUST see `503 backend_unavailable` with no `Retry-After`. (source: ADR 0011 §1, §3, ADR 0010 §2–§3)
- R10. `Checker` MUST NOT make a `Provider` or `Store` call itself; `doc` and `balanceUSD` MUST be supplied by the caller; no metric here MUST carry an instance id or other unbounded label. (source: C-26, C-27, C-34)
- R11. `budget.monthlyUSD`, `budget.warnFraction`, `lifecycle.safety.maxHourlyPriceUSD`, `lifecycle.safety.maxInstanceSpendUSD` MUST NOT be hard-coded inside `internal/budget`; they arrive only through `Config`. (source: C-7)
- R12. The per-instance spend check in `internal/controller/reconcile.go` MUST return `true` only when `cfg.MaxInstanceSpendUSD > 0` and the open entry for `gen`'s current `EstUSD >= cfg.MaxInstanceSpendUSD`; it MUST return `false` whenever `MaxInstanceSpendUSD == 0`. (source: ADR 0011 §5)

## Implementation steps

1. `OpenEntry`, `RefreshOpenEntry`, `CloseEntry`, fold-on-overflow, `Rollover`, operating on `CORE-040`'s `store.Ledger`. Tests: `TestRoundUpToSecond`, `TestOpenRefreshClose`, `TestFoldOnOverflow`, `TestRolloverDropsClosedEntries`, `TestRolloverReopensRunningEntry`.
2. `EstimatedMTD`, `Checker.Allow`, `Checker.UsedRatio` — the pure values `CORE-050` feeds to `CORE-090`'s obs setters, and its own drain trigger, after each commit. Tests: `TestAllowMonthlyCap`, `TestAllowBalanceFloor`, `TestAllowOrderMonthlyBeforeBalance`, `TestUsedRatio`.
3. `AssertPrice`. Test: `TestAssertPriceBoundary`.
4. Simulation harness reusing `CORE-050`'s fake clock/store/provider: month-boundary crossing, budget exhaustion mid-`Serving` signalled to a fake state observer. No new tests are owed by this step; existing tests provide the coverage: month-boundary crossing by `internal/budget/ledger_test.go`'s `TestRolloverDropsClosedEntries` and `TestRolloverReopensRunningEntry`, and budget exhaustion mid-`Serving` by `internal/controller/sim_test.go`'s `TestAC20_BudgetMidServing` (CORE-050's simulation harness, run against the real `budget.Checker`).

## Tests / acceptance criteria

- AC1. Setup: `Month="2026-11"`, `TotalUSD=12.34`, open entry `gen=5, dph=0.71, start=2026-11-30T20:00:00Z`. Action: `Rollover` at `2026-12-01T00:00:00Z`. Expected: `Month=="2026-12"`, `TotalUSD==0`, one entry (`gen=5`) with `Start==2026-12-01T00:00:00Z`, `End==nil`, `EstUSD==0`; `vkllm_cost_estimated_usd_total` unchanged. (R6)
- AC2. Setup: `TotalUSD=29.90`, one open entry whose `RefreshOpenEntry` yields `EstUSD=0.15` (`EstimatedMTD=30.05`). Action: `Allow(doc, balance=100, known=true)`. Expected: return value `"budget_exhausted"` (monthly cap exceeded). (FR-6.3, R1)
- AC3. Setup: `EstimatedMTD < MonthlyUSD`; `MaxHourlyPriceUSD=0.80` (floor `1.60`). Action: `Allow(doc, balance, known=true)` with `balance=1.59`, then `1.60`. Expected: return values `"balance_low"`, then `""` (allow). A variant where both R1 and R2 would fail yields `"budget_exhausted"` only, never `"balance_low"` (monthly cap checked first). (R2, R2 ordering)
- AC4. Setup: a fake state observer reporting `Serving` receives a `BudgetExhausted` signal. Action: the simulation harness delivers it. Expected: recorded transitions exactly `Serving → Draining → Destroying`; the ledger entry's `End` is set only after `Destroying→Idle`. (R9 — the state machine itself is `CORE-050`'s own simulation; this AC proves the signal and the ledger's response.)
- AC5. Setup: `dph = 0.80001`, `maxHourlyPriceUSD = 0.80`. Action: `AssertPrice`. Expected: `ErrPriceTooHigh`. With `dph = 0.80`: `nil`. (R7)
- AC6. Setup: 100 closed entries plus one about to close (101 total). Action: `CloseEntry`. Expected: `len(Entries) == 100` afterwards; `TotalUSD` unchanged by the fold itself (the oldest entry's cost was already added when IT closed, earlier); the just-closed entry (gen 101) still present, having added its own cost to `TotalUSD` when `CloseEntry` ran. (R5, R8)
- AC7. Setup: entry opened at `t0`, `dph = 0.71`. Action: `RefreshOpenEntry` at `t0+89.6s` and `t0+90s`. Expected: identical `EstUSD` (`0.71 × 90/3600`) both times — both round up to 90 whole seconds, the boundary only moving past `t0+90s`. (R4)
- AC8. Action: pure arithmetic assertion in the test. Expected: `30 / 0.80 == 37.5`, matching the worked figure above. (guards the doc against silent default drift)
- AC9. The per-instance check is tested in `internal/controller/reconcile_test.go` with `MaxInstanceSpendUSD=5`, open entry `EstUSD=4.99`, then `5.00`. Expected: `false`, then `true`. With `MaxInstanceSpendUSD=0` and `EstUSD=1000`: `false`. (R12)

## Definition of done

- [x] AC1–AC9 pass in CI.
- [x] `make specs-check` passes.
- [x] Status flipped to DONE in the same PR; folder renamed `100-D-cost-controls`.
- [x] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Does a Vast charges/invoices endpoint exist (research §1.5, Q5)? | decides whether `vkllm_cost_provider_usd` ever ships | `CORE-020` | no second series; ledger estimate only |
| Exact `inet_down_cost` and `storage_cost` values for the offers we actually rent | the worked arithmetic above uses ranges, not pinned numbers | `CORE-020`'s recorded live snapshot, compared against a real invoice in `CORE-140` | the ranges cited from ADR 0005's alternatives table |
| Should `Checker.Allow` ever be called with a stale cached `balanceUSD` to avoid hammering `GET /api/v0/users/current` under Vast's per-endpoint rate limit (research §1.1)? | an `Allow` call on every `Tick` (60 s) is cheap; one on every pending-request retry could not be | `CORE-050`'s call-site design | the controller refreshes `balanceUSD` once per `Tick`, not more often |
| Can `dph_total` change on a running instance? | if it can, the `dph` cached at `Created` could under- or over-state the real bill for that entry | `CORE-030`/`CORE-140` logging `dph_total` on every `GetInstance` | treat `dph` as fixed for the entry's life |

## Evidence

### Test command output

```
$ go test ./internal/budget/... ./internal/config/... ./internal/controller/... -race -count=1 -v 2>&1 | tail -80
=== RUN   TestAC11_RestartAfterDeadlinePassed
=== RUN   TestAC11_RestartAfterDeadlinePassed/deadline_already_past:_destroy_without_redialing
=== RUN   TestAC11_RestartAfterDeadlinePassed/deadline_still_ahead:_redial_and_idle_out_normally_later
--- PASS: TestAC11_RestartAfterDeadlinePassed (0.05s)
    --- PASS: TestAC11_RestartAfterDeadlinePassed/deadline_already_past:_destroy_without_redialing (0.01s)
    --- PASS: TestAC11_RestartAfterDeadlinePassed/deadline_still_ahead:_redial_and_idle_out_normally_later (0.04s)
=== RUN   TestAC12_RestartWithReadyInstance
--- PASS: TestAC12_RestartWithReadyInstance (0.00s)
=== RUN   TestAC13_StartupTimeout
--- PASS: TestAC13_StartupTimeout (0.16s)
=== RUN   TestAC14_ProviderListError
--- PASS: TestAC14_ProviderListError (0.02s)
=== RUN   TestAC15_InstanceVanishes
--- PASS: TestAC15_InstanceVanishes (0.01s)
=== RUN   TestAC16_OrphanInjection
=== RUN   TestAC16_OrphanInjection/behind_the_current_generation:_destroyed_after_two_observations
=== RUN   TestAC16_OrphanInjection/ahead_of_the_current_generation:_advances_the_fence,_then_destroys
=== RUN   TestAC16_OrphanInjection/a_foreign_installation_id_is_never_a_destroy_target
--- PASS: TestAC16_OrphanInjection (0.10s)
    --- PASS: TestAC16_OrphanInjection/behind_the_current_generation:_destroyed_after_two_observations (0.00s)
    --- PASS: TestAC16_OrphanInjection/ahead_of_the_current_generation:_advances_the_fence,_then_destroys (0.00s)
    --- PASS: TestAC16_OrphanInjection/a_foreign_installation_id_is_never_a_destroy_target (0.10s)
=== RUN   TestAC17_RestartWithEmptyStore
--- PASS: TestAC17_RestartWithEmptyStore (0.00s)
=== RUN   TestAC18_DestroyRetries
--- PASS: TestAC18_DestroyRetries (0.02s)
=== RUN   TestAC19_MaxLifetime
--- PASS: TestAC19_MaxLifetime (0.19s)
=== RUN   TestAC20_BudgetMidServing
=== RUN   TestAC20_BudgetMidServing/the_request_ending_destroys_at_once
=== RUN   TestAC20_BudgetMidServing/no_RequestEnd_still_destroys_at_the_drain_timeout
--- PASS: TestAC20_BudgetMidServing (0.02s)
    --- PASS: TestAC20_BudgetMidServing/the_request_ending_destroys_at_once (0.01s)
    --- PASS: TestAC20_BudgetMidServing/no_RequestEnd_still_destroys_at_the_drain_timeout (0.01s)
=== RUN   TestAC21_CASConflictStorm
--- PASS: TestAC21_CASConflictStorm (0.63s)
=== RUN   TestAC22_TerminalStatus
--- PASS: TestAC22_TerminalStatus (0.02s)
=== RUN   TestAC23_NonTerminalString
--- PASS: TestAC23_NonTerminalString (0.20s)
=== RUN   TestAC24_AttemptsCap
--- PASS: TestAC24_AttemptsCap (0.29s)
=== RUN   TestAC25_ReadyGate
--- PASS: TestAC25_ReadyGate (0.06s)
=== RUN   TestAC26_SSRFBoundaryAndHostKey
=== RUN   TestAC26_SSRFBoundaryAndHostKey/dials_the_provider's_live_address,_never_the_stored_one
=== RUN   TestAC26_SSRFBoundaryAndHostKey/a_flaky_recovery_get_never_dials_before_it_resolves
=== RUN   TestAC26_SSRFBoundaryAndHostKey/a_host_key_mismatch_destroys_without_ever_creating
--- PASS: TestAC26_SSRFBoundaryAndHostKey (0.07s)
    --- PASS: TestAC26_SSRFBoundaryAndHostKey/dials_the_provider's_live_address,_never_the_stored_one (0.00s)
    --- PASS: TestAC26_SSRFBoundaryAndHostKey/a_flaky_recovery_get_never_dials_before_it_resolves (0.06s)
    --- PASS: TestAC26_SSRFBoundaryAndHostKey/a_host_key_mismatch_destroys_without_ever_creating (0.00s)
=== RUN   TestAC27_WorkerCrash
=== RUN   TestAC27_WorkerCrash/two_misses_destroy_the_instance
=== RUN   TestAC27_WorkerCrash/a_Ready_probe_at_the_second_check_saves_the_instance
=== RUN   TestAC27_WorkerCrash/a_balance_already_under_the_floor_denies_Start_before_searching
--- PASS: TestAC27_WorkerCrash (0.19s)
    --- PASS: TestAC27_WorkerCrash/two_misses_destroy_the_instance (0.13s)
    --- PASS: TestAC27_WorkerCrash/a_Ready_probe_at_the_second_check_saves_the_instance (0.06s)
    --- PASS: TestAC27_WorkerCrash/a_balance_already_under_the_floor_denies_Start_before_searching (0.00s)
=== RUN   TestIdleShutdownDisabledColdStartNeverDestroys
--- PASS: TestIdleShutdownDisabledColdStartNeverDestroys (0.17s)
=== RUN   TestIdleShutdownDisabledWarmRestartIgnoresLeftoverDeadline
--- PASS: TestIdleShutdownDisabledWarmRestartIgnoresLeftoverDeadline (0.11s)
=== RUN   TestGaugesRefreshWithoutStatusCall
--- PASS: TestGaugesRefreshWithoutStatusCall (0.02s)
=== RUN   TestReportUpstreamFailureStopsOnShutdown
--- PASS: TestReportUpstreamFailureStopsOnShutdown (0.15s)
=== RUN   TestPublicStateMap
--- PASS: TestPublicStateMap (0.00s)
=== RUN   TestStateString
--- PASS: TestStateString (0.00s)
=== RUN   TestStatusReflectsMemoryAndDocument
--- PASS: TestStatusReflectsMemoryAndDocument (0.00s)
=== RUN   TestStatusNeverCallsProvider
--- PASS: TestStatusNeverCallsProvider (0.00s)
=== RUN   TestStatusProviderBalanceUnknownUntilFirstRead
--- PASS: TestStatusProviderBalanceUnknownUntilFirstRead (0.00s)
PASS
ok  	github.com/savak1990/vk-llm-on-demand/internal/controller	7.421s
```

```
$ go test ./internal/budget/... -race -count=1 -v
=== RUN   TestAllowMonthlyCap
--- PASS: TestAllowMonthlyCap (0.00s)
=== RUN   TestAllowBalanceFloor
--- PASS: TestAllowBalanceFloor (0.00s)
=== RUN   TestAllowUnknownBalanceFoldsIntoBalanceLow
--- PASS: TestAllowUnknownBalanceFoldsIntoBalanceLow (0.00s)
=== RUN   TestAllowOrderMonthlyBeforeBalance
--- PASS: TestAllowOrderMonthlyBeforeBalance (0.00s)
=== RUN   TestUsedRatio
--- PASS: TestUsedRatio (0.00s)
=== RUN   TestUsedRatioZeroMonthlyCap
--- PASS: TestUsedRatioZeroMonthlyCap (0.00s)
=== RUN   TestEstimatedMTD
--- PASS: TestEstimatedMTD (0.00s)
=== RUN   TestAssertPriceBoundary
--- PASS: TestAssertPriceBoundary (0.00s)
=== RUN   TestClosedEntryCountsTowardMonthlyCap
--- PASS: TestClosedEntryCountsTowardMonthlyCap (0.00s)
=== RUN   TestWorkedArithmeticMonthlyHoursAtCap
--- PASS: TestWorkedArithmeticMonthlyHoursAtCap (0.00s)
=== RUN   TestRoundUpToSecond
--- PASS: TestRoundUpToSecond (0.00s)
=== RUN   TestOpenRefreshClose
--- PASS: TestOpenRefreshClose (0.00s)
=== RUN   TestCloseEntryDoesNothingForUnknownGen
--- PASS: TestCloseEntryDoesNothingForUnknownGen (0.00s)
=== RUN   TestFoldOnOverflow
--- PASS: TestFoldOnOverflow (0.00s)
=== RUN   TestRolloverDropsClosedEntries
--- PASS: TestRolloverDropsClosedEntries (0.00s)
=== RUN   TestRolloverReopensRunningEntry
--- PASS: TestRolloverReopensRunningEntry (0.00s)
=== RUN   TestRolloverNoOpWithinSameMonth
--- PASS: TestRolloverNoOpWithinSameMonth (0.00s)
PASS
ok  	github.com/savak1990/vk-llm-on-demand/internal/budget	1.263s
```

```
$ go test ./internal/budget/... ./internal/config/... ./internal/controller/... -race -count=1
ok  	github.com/savak1990/vk-llm-on-demand/internal/budget	1.229s
ok  	github.com/savak1990/vk-llm-on-demand/internal/config	1.183s
ok  	github.com/savak1990/vk-llm-on-demand/internal/controller	7.507s
```

### Acceptance criteria mapping

| AC | Requirement | Test name(s) | Status |
|----|---|---|---|
| AC1 | Rollover drops closed entries, keeps/reopens running entry | `internal/budget/ledger_test.go`: `TestRolloverDropsClosedEntries`, `TestRolloverReopensRunningEntry` | PASS |
| AC2 | Allow denies on monthly cap | `internal/budget/checker_test.go`: `TestAllowMonthlyCap`, `TestClosedEntryCountsTowardMonthlyCap` (closed spend counts toward the cap) | PASS |
| AC3 | Allow denies on balance floor, ordering | `internal/budget/checker_test.go`: `TestAllowBalanceFloor`, `TestAllowOrderMonthlyBeforeBalance` | PASS |
| AC4 | Budget exhaustion while Serving drains the instance | `internal/controller/sim_test.go`: `TestAC20_BudgetMidServing` | PASS |
| AC5 | AssertPrice boundary | `internal/budget/checker_test.go`: `TestAssertPriceBoundary` | PASS |
| AC6 | Fold-on-overflow at 100 entries | `internal/budget/ledger_test.go`: `TestFoldOnOverflow` | PASS |
| AC7 | Round-up-to-second, 89.6s and 90.0s identical | `internal/budget/ledger_test.go`: `TestRoundUpToSecond` | PASS |
| AC8 | Worked arithmetic: 30/0.80 == 37.5 | `internal/budget/checker_test.go`: `TestWorkedArithmeticMonthlyHoursAtCap` | PASS |
| AC9 | InstanceSpendExceeded boundary | `internal/controller/reconcile_test.go`: `TestReconcileMaxInstanceSpendExceededPostsBudgetExhausted` | PASS |

### Implementation notes

Task 5 of this plan replaced one test-only fake in `internal/controller`'s test files: deleted `fakeBudget` struct and its methods, replacing the fake with the real `budget.Checker` package. Zero production code changed in that package; only `*_test.go` files were modified. This merges the budget package into the controller's test suite, consistent with CORE-055's own test-rewrite pattern.
