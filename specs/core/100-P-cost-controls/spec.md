---
id: "CORE-100"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-050"]
adrs: ["0003", "0004", "0005", "0007"]
---

# 100 — Cost controls

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** M
**Risk:** medium — a bug either lets spend run past C-1/C-7's caps or wrongly kills a paying request
**Estimated effort:** one session (4–6 h)
**Recommended model:** strong — rounding and fold-on-overflow logic on a money path must be exact, not "close enough"
**Depends on:** [CORE-050](../050-P-gpu-lifecycle-controller/spec.md)

## Outcome

`internal/controller/budget` gives the controller one gate, `Checker.Allow`,
that must pass before any offer search and again before any `CreateInstance`
call. It drives the month-to-date ledger bookkeeping on `CORE-040`'s
`store.Ledger`, the $30/0.80 USD arithmetic behind
`vkllm_budget_used_ratio`, and the provider-balance floor. Done, a
simulated month of demand stops provisioning the instant month-to-date
spend would reach `budget.monthlyUSD`, drains a `Serving` instance the same
way, never provisions with less than two hours of prepaid balance at
stake, and reports estimated cost and its own blind spots through four
metrics and `/admin/v1/status`.

## Scope

- `internal/controller/budget`: `Checker`, `Config`, `Allow`, `UsedRatio`,
  `AssertPrice`, and the ledger bookkeeping functions (`OpenEntry`,
  `RefreshOpenEntry`, `CloseEntry`, `Rollover`, `EstimatedMTD`) operating on
  `CORE-040`'s `store.Ledger`/`store.LedgerEntry` as already defined there.
- Month rollover (UTC, first of month): what gets dropped, what persists.
- The cost model: how Vast.ai bills, why a `dph × uptime` estimate drifts
  from it, what it ignores, reconciliation against the console, and the
  worked arithmetic for the shipped defaults, including open question Q5.
- Four metrics (`vkllm_budget_used_ratio`, `vkllm_cost_estimated_usd_total`,
  `vkllm_cost_instance_usd`, `vkllm_provider_balance_usd`): this package
  computes their values with pure functions; `CORE-050` calls `CORE-090`'s
  obs setters after each commit. The alert expectation placed on
  `CORE-050`'s orphan metric.
- Documented semantics of `POST /admin/v1/scale {desired:0}` and of budget
  exhaustion while `Serving`, from the cost side.

Excludes: the `State` enum, transition table, provisioning saga, reconcile
loop and restart recovery (`CORE-050`); idle, max-lifetime, startup-deadline
and attempt-budget enforcement, already decided in ADR 0003 §5 and owned by
`CORE-050`; the orphan sweep's detection logic, its metric, and max-instances
enforcement — FR-6.2 is enforced by `CORE-050`'s orphan reconcile rows, not
here; the `/admin/v1/scale` and `/admin/v1/status` handlers (`CORE-060`);
the Grafana dashboard and alert YAML, and metric registration (`CORE-090`);
`Provider.Balance`'s HTTP implementation (`CORE-020`).

## Design and contracts

### Package choice: `internal/controller/budget`, not `internal/cost`

The common brief's fixed package list is
`internal/{gateway,controller,provider,store,tunnel,worker,config,obs}`; no
`cost`/`budget` entry exists, so this spec adds no new top-level package.
`internal/controller/budget` nests inside the package that calls it,
mirroring `provider/vastai` and `tunnel/ssh`. `Checker` holds only `Config`
and a `clock.Clock`; every method takes values the caller already has.

### Ledger

`internal/controller/budget` uses `store.Ledger`/`store.LedgerEntry`
(CORE-040) as already defined there: `Month`, `TotalUSD` (folded cost of
entries evicted past the cap), `Entries` (oldest first, at most 100); each
`LedgerEntry` carries `Gen`, `Start`, `End` (nil while open), `DPH`,
`EstUSD`. No amendment to CORE-040's types is needed.

`MaxLedgerEntries = 100`. With `gpu.maxInstances = 1`, one entry opens and
closes per cycle, so 100 is months of history; the cap guards the
pathological case (attempt-budget churn), not normal operation.

### Cost estimation

Estimated cost for one entry is `dph × hours`, `hours` being elapsed wall
time **rounded up to the next whole second** before dividing by 3600 —
never nearest, so an in-progress instance feeding `Allow`'s deny gate is
never understated.

```go
// OpenEntry runs on the Created event (ADR 0003 §2, Creating→Booting).
func OpenEntry(doc *store.Document, now time.Time, gen int64, dph float64)
// RefreshOpenEntry recomputes EstUSD for the single open entry. Called
// every Tick so vkllm_cost_instance_usd and EstimatedMTD stay current.
func RefreshOpenEntry(doc *store.Document, now time.Time)
// CloseEntry runs on Destroying→Idle: sets End, does a final refresh, then
// folds the oldest closed entries into TotalUSD past MaxLedgerEntries.
func CloseEntry(doc *store.Document, now time.Time, gen int64)
// EstimatedMTD is TotalUSD + the sum of every open entry's current EstUSD.
func EstimatedMTD(doc store.Document) float64
```

Fold-on-overflow only removes entries with `End != nil`, never the open
entry, so `CloseEntry` cannot drop the entry it just closed.

### Month rollover

```go
// Rollover fires at most once per Tick, before Allow.
func Rollover(doc *store.Document, now time.Time)
```

Checked once per `Tick`, before `Allow`: `now.UTC().Format("2006-01") !=
ledger.Month`. On rollover, `ledger.Month` becomes the new string,
`TotalUSD` resets to `0`, and every closed entry is dropped: Prometheus
already holds `vkllm_cost_estimated_usd_total`'s history (architecture §5).
A still-open entry (an instance `Serving` across midnight UTC) is **not**
carried over with its old `Start`; it is replaced by a fresh entry for the
same `gen`/`dph` with `Start = now`, so no entry's `Start` is ever outside
its own `Month`, and the previous month's partial cost is dropped with the
rest.

The ledger functions and `Allow` are pure and call no `obs` setter; they
run inside a CAS function that `Transition` may retry up to 8 times
(`CORE-040` R3/R4). The caller (`CORE-050`) sets the four metrics once,
after `Transition` commits, from the committed document — never from
inside the retried function, so a conflict retry never double-counts.
Rollover resets the ledger's month-to-date view; `vkllm_cost_estimated_usd_total`
is a counter the caller only ever increases, never decreases.

### `Checker.Allow`: the monthly cap and the balance floor

```go
type Config struct {
	MonthlyUSD        float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

type Checker struct {
	cfg Config
	clk clock.Clock
}
func New(cfg Config, clk clock.Clock) *Checker
// Allow reports whether a new provisioning attempt (offer search or create)
// may proceed. balanceUSD is the caller's most recent Provider.Balance read.
func (c *Checker) Allow(doc store.Document, balanceUSD float64) (ok bool, reason string)
// UsedRatio is EstimatedMTD(doc) / cfg.MonthlyUSD; it never denies by itself.
func (c *Checker) UsedRatio(doc store.Document) float64
```

`Allow` runs two checks in order and returns on the first failure:

| Order | Check | Deny condition | `reason` |
|---|---|---|---|
| 1 | Monthly cap | `EstimatedMTD(doc) >= cfg.MonthlyUSD` | `"monthly_budget_exceeded"` |
| 2 | Balance floor | `balanceUSD < 2 * cfg.MaxHourlyPriceUSD` | `"balance_floor"` |

`reason == ""`, `ok == true` otherwise. `WarnFraction` is never checked
here; warning is a metric-and-alert concern (FR-6.4), not a deny.

**Why `2 × maxHourlyPrice`, not 1×:** one hour covers a full `Serving` hour
at the worst price without the account reaching $0, where Vast schedules
the instance and its data for deletion (research §1.5). The second hour is
margin: `startupDeadline` (15 min) + `drainTimeout` (5 min) + the
attempt-backoff ceiling (30 min, ADR 0003 §5) sum to under an hour of delay
before destroy confirms.

### `AssertPrice`: the second price check

`CORE-020`'s offer filter excludes offers above `gpu.maxHourlyPriceUSD` at
search time. Nothing guarantees the price holds between `SearchOffers` and
the matching `CreateInstance` (research §1.2 is silent on atomicity), so the
controller asserts again right before `CreateInstance`:

```go
var ErrPriceTooHigh = errors.New("budget: offer price above gpu.maxHourlyPriceUSD")

// AssertPrice returns ErrPriceTooHigh when dph exceeds max; equal is allowed.
func AssertPrice(dph, maxHourlyPriceUSD float64) error
```

A failed assertion is terminal for that attempt (counted against
`gpu.maxProvisionAttemptsPerHour` by `CORE-050`): C-1 extends to never
creating a GPU above the configured ceiling, even once.

### Max instances: owned by `CORE-050`

FR-6.2 (at most `gpu.maxInstances` surviving) is enforced by `CORE-050`'s
orphan reconcile rows, not by this package: the saga adopts the lowest
provider id on a race, and the orphan rows destroy every id that is not
`providerId`. `internal/controller/budget` has no survivor-selection
function.

### Kill switch and exhaustion while `Serving`

`POST /admin/v1/scale {desired: 0}` (ADR 0007 §3) sets `doc.Desired = 0`,
indistinguishable here from the monthly cap being hit: both raise a
`DesiredZero`/`BudgetExhausted` event treated like idle timeout or max
lifetime — stop admitting work, drain, destroy. `Allow` needs no special
case for `desired == 0`: the controller never calls it once desired is zero.

Budget exhaustion while `Serving` follows the same `Draining → Destroying`
path (ADR 0003 §5). New requests during that drain get `503
gpu_unavailable` with no `Retry-After` (ADR 0007 §2) so clients stop
polling rather than hammer a GPU that will not come back this month.
In-flight requests finish inside `gpu.drainTimeout`; the ledger entry closes
when `Destroying` confirms, at whatever `EstUSD` the elapsed drain time
produced — drain time is billed time, and the estimate says so.

### Cost model

**How Vast actually bills (research §1.5):** GPU time per second while
active/connected, **not** charged during `Loading` (image-pull); storage per
second in every state but `offline`; bandwidth per byte in any state. The
`dph` read once at `Created` is used for that entry's entire life; whether
`dph_total` can drift on a running instance is an open question below.

**Why `dph × uptime` drifts from the real bill:**

| Effect | Direction | Why |
|---|---|---|
| Our entry runs from `Created` through the provider's own image-pull `Loading` window | **overcounts** | that window is billed $0 for GPU time (research §1.5), but our entry is already open and accruing at `dph` |
| Storage for the instance's disk | **undercounts** | billed continuously in every state but `offline`; never in `dph` or in this ledger at all |
| Bandwidth for the 26 GB model download (ADR 0005) and every proxied token | **undercounts** | billed per byte; never in `dph` or in this ledger |

**Ignored entirely:** storage, bandwidth, any undocumented fee, and
anything in the console invoice beyond `dph × active seconds`.
**Reconciliation:** compare `vkllm_cost_estimated_usd_total`'s monthly
`increase()` (or `/admin/v1/status`'s "spend this month") against the
console's monthly total; expect it low by the storage/bandwidth amounts
above — a large unexplained gap signals re-checking `dph_total` via
`GetInstance`, not a bug here by default. **Q5 — a provider charges
endpoint:** research §1.5 could not confirm `GET /api/v0/charges` or `GET
/api/v1/invoices` exist; if `CORE-020` finds one, add a **second**,
independently-sourced `vkllm_cost_provider_usd` series, never a
replacement, since the endpoint can lag, rate-limit (research §1.1), or be
unavailable when `Allow` needs an answer.

**Worked arithmetic** (`budget.monthlyUSD=30`, `gpu.maxHourlyPriceUSD=0.80`,
`gpu.idleTimeout=20m`): `$30 ÷ $0.80/h ≈ 37.5 h` of continuous max-price GPU
time per month before the cap stops provisioning (~1.25 h/day evenly,
ignoring the undercount above). One idle-timeout cycle with zero useful
serving costs at most `0.80 × (20/60) = $0.2667`, rounded to **≤ $0.27**.
One failed cold start dying during `Loading` is billed $0 GPU time; its
real cost is storage for the few minutes it existed (ADR 0005's
$0.05–0.20/GB-month, 60 GB, ~10 min: a fraction of a cent) plus whatever
partial bytes of the 26 GB model were pulled, at a typical `inet_down_cost`
ADR 0005 calls "cents" for the full download — **a few cents at most**,
while our entry (open from `Created`, spanning `Loading`) reports the full
`dph` rate — the overcount above, made concrete.

### Observability

The ledger functions and `Allow` are pure and call no `obs` setter; they
run inside a CAS function that may be retried up to 8 times. The caller
(`CORE-050`) sets the four metrics after the commit, from the committed
document, never from inside the retried function (a conflict retry must
never double-count a cost):

| Metric | Type | Caller computes it as |
|---|---|---|
| `vkllm_budget_used_ratio` | gauge | `Budget.UsedRatio(doc)` |
| `vkllm_cost_estimated_usd_total` | counter | each entry's `EstUSD` increase versus the previous committed document, ignoring rollover drops — `obs.AddCostEstimatedUSD(delta)` only ever adds |
| `vkllm_cost_instance_usd` | gauge | the open entry's `EstUSD`, or `0` with none |
| `vkllm_provider_balance_usd` | gauge | the `balanceUSD` just read |

`obs` (CORE-090) owns registration; this package owns none of the four.
None carries a label (C-34). Log fields on a denied `Allow`:
`reason`, `estimated_mtd_usd`, `monthly_budget_usd`, `balance_usd` (never
the raw provider body, prompts, keys or instance ids — C-16).

**Alert expectation added to `CORE-050`'s orphan metric:** any
`vkllm_orphans_destroyed_total` increment pages inside 5 minutes
(`CORE-090` renders the rule) — an orphan is a GPU running unobserved by
this system's own budget math, exactly what C-1 exists to prevent.

## Requirements

- R1. `Checker.Allow` MUST deny with `"monthly_budget_exceeded"` when
  `EstimatedMTD(doc) >= cfg.MonthlyUSD`. (source: FR-6.3, C-1, C-7)
- R2. `Checker.Allow` MUST deny with `"balance_floor"` when `balanceUSD < 2 * cfg.MaxHourlyPriceUSD`, checked only after R1 passes. (source: research §1.5, ADR 0003 §5)
- R3. `EstimatedMTD` MUST equal `ledger.TotalUSD` plus the sum of every open
  entry's current `EstUSD`. (source: ADR 0004 Consequences, FR-6.3)
- R4. Every `EstUSD` computation MUST round elapsed time up to the next
  whole second before multiplying by `dph / 3600`, so a deny-gate input is
  never understated. (source: C-1, FR-6.3)
- R5. `ledger.Entries` MUST NOT exceed 100; the oldest closed entry MUST be
  folded into `ledger.TotalUSD` before any append exceeding the cap.
  (source: ADR 0004 Consequences)
- R6. Rollover MUST fire when `now.UTC().Format("2006-01") != ledger.Month`,
  reset `TotalUSD` to `0`, drop every closed entry, and re-open any
  still-running entry with `Start = now`; `vkllm_cost_estimated_usd_total`
  MUST NOT decrease, ever. (source: ADR 0004 Consequences; architecture §5)
- R7. `AssertPrice` MUST return `ErrPriceTooHigh` for any `dph` strictly above `gpu.maxHourlyPriceUSD`, called immediately before every `CreateInstance`, in addition to `CORE-020`'s search-time filter. (source: FR-6.1, C-1)
- R8. `OpenEntry` MUST run on the `Created` event with the offer's `dph` and
  MUST NOT run twice for the same generation; `CloseEntry` MUST run once
  `Destroying` confirms and MUST set `End` before any fold-on-overflow
  check. (source: ADR 0003 §2, C-1)
- R9. Budget exhaustion while `Serving` MUST result in `Draining` then `Destroying`; new requests during that drain MUST see `503 gpu_unavailable` with no `Retry-After`. (source: ADR 0003 §5, ADR 0007 §2)
- R10. `Checker` MUST NOT make a `Provider` or `Store` call itself; `doc`
  and `balanceUSD` MUST be supplied by the caller; no metric here MUST carry
  an instance id or other unbounded label. (source: C-26, C-27, C-34)
- R11. `budget.monthlyUSD`, `budget.warnFraction`, `gpu.maxHourlyPriceUSD`
  MUST NOT be hard-coded inside `internal/controller/budget`; they arrive
  only through `Config`. (source: C-7)

## Implementation steps

1. `OpenEntry`, `RefreshOpenEntry`, `CloseEntry`, fold-on-overflow,
   `Rollover`, operating on `CORE-040`'s `store.Ledger`. Tests:
   `TestRoundUpToSecond`, `TestOpenRefreshClose`, `TestFoldOnOverflow`,
   `TestRolloverDropsClosedEntries`, `TestRolloverReopensRunningEntry`.
2. `EstimatedMTD`, `Checker.Allow`, `Checker.UsedRatio` — the pure values
   `CORE-050` feeds to `CORE-090`'s obs setters after each commit. Tests:
   `TestAllowMonthlyCap`, `TestAllowBalanceFloor`,
   `TestAllowOrderMonthlyBeforeBalance`, `TestUsedRatio`.
3. `AssertPrice`. Test: `TestAssertPriceBoundary`.
4. Simulation harness reusing `CORE-050`'s fake clock/store/provider:
   month-boundary crossing, budget exhaustion mid-`Serving` signalled to a
   fake state observer. Tests: `TestSimMonthBoundary`,
   `TestSimExhaustionSignalDuringServing`.

## Tests / acceptance criteria

- AC1. Setup: `Month="2026-11"`, `TotalUSD=12.34`, open entry `gen=5,
  dph=0.71, start=2026-11-30T20:00:00Z`. Action: `Rollover` at
  `2026-12-01T00:00:00Z`. Expected: `Month=="2026-12"`, `TotalUSD==0`, one
  entry (`gen=5`) with `Start==2026-12-01T00:00:00Z`, `End==nil`,
  `EstUSD==0`; `vkllm_cost_estimated_usd_total` unchanged. (R6)
- AC2. Setup: `TotalUSD=29.90`, one open entry whose `RefreshOpenEntry`
  yields `EstUSD=0.15` (`EstimatedMTD=30.05`). Action: `Allow(doc,
  balanceUSD=100)`. Expected: `ok=false, reason="monthly_budget_exceeded"`.
  (FR-6.3, R1)
- AC3. Setup: `EstimatedMTD < MonthlyUSD`; `MaxHourlyPriceUSD=0.80` (floor
  `1.60`). Action: `Allow` with `balanceUSD=1.59`, then `1.60`. Expected:
  `ok=false, reason="balance_floor"`, then `ok=true`. A variant where both
  R1 and R2 would fail yields `"monthly_budget_exceeded"` only, never
  `"balance_floor"`. (R2, R2 ordering)
- AC4. Setup: a fake state observer reporting `Serving` receives a
  `BudgetExhausted` signal. Action: the simulation harness delivers it.
  Expected: recorded transitions exactly `Serving → Draining →
  Destroying`; the ledger entry's `End` is set only after
  `Destroying→Idle`. (R9 — the state machine itself is `CORE-050`'s own
  simulation; this AC proves the signal and the ledger's response.)
- AC5. Setup: `dph = 0.80001`, `maxHourlyPriceUSD = 0.80`. Action:
  `AssertPrice`. Expected: `ErrPriceTooHigh`. With `dph = 0.80`: `nil`. (R7)
- AC6. Setup: 100 closed entries plus one about to close (101 total).
  Action: `CloseEntry`. Expected: `len(Entries) == 100` afterwards, the
  oldest closed entry's `EstUSD` added to `TotalUSD`, the just-closed entry
  still present. (R5, R8)
- AC7. Setup: entry opened at `t0`, `dph = 0.71`. Action: `RefreshOpenEntry`
  at `t0+89.6s` and `t0+90s`. Expected: identical `EstUSD`
  (`0.71 × 90/3600`) both times — both round up to 90 whole seconds, the
  boundary only moving past `t0+90s`. (R4)
- AC8. Action: pure arithmetic assertion in the test. Expected: `30 /
  0.80 == 37.5`, matching the worked figure above. (guards the doc against
  silent default drift)

## Definition of done

- [ ] AC1–AC8 pass in CI.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE in the same PR; folder renamed `100-D-cost-controls`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Does a Vast charges/invoices endpoint exist (research §1.5, Q5)? | decides whether `vkllm_cost_provider_usd` ever ships | `CORE-020` | no second series; ledger estimate only |
| Exact `inet_down_cost` and `storage_cost` values for the offers we actually rent | the worked arithmetic above uses ranges, not pinned numbers | `CORE-020`'s recorded live snapshot, compared against a real invoice in `CORE-140` | the ranges cited from ADR 0005's alternatives table |
| Should `Checker.Allow` ever be called with a stale cached `balanceUSD` to avoid hammering `GET /api/v0/users/current` under Vast's per-endpoint rate limit (research §1.1)? | an `Allow` call on every `Tick` (60 s) is cheap; one on every pending-request retry could not be | `CORE-050`'s call-site design | the controller refreshes `balanceUSD` once per `Tick`, not more often |
| Can `dph_total` change on a running instance? | if it can, the `dph` cached at `Created` could under- or over-state the real bill for that entry | `CORE-030`/`CORE-140` logging `dph_total` on every `GetInstance` | treat `dph` as fixed for the entry's life |

## Evidence

None yet.
