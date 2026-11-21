---
id: "CORE-190"
status: "DONE"
updated: "2026-11-22"
depends_on: ["CORE-055", "CORE-100", "CORE-150", "CORE-160"]
adrs: ["0015", "0010", "0011", "0012"]
---

# 190 — Runtime budget reconfiguration

**Status note:** DONE (2026-11-22). Implemented and reviewed task by task,
then confirmed green on a real GitHub Actions run (PR #33, run
44959242039). Written 2026-11-21 and amended the same day after reading
the code for the implementation plan:
the startup backfill was replaced by a read-time fallback (a backfill
could race the first API request), the ceiling now also clamps
enforcement, and the NaN/Infinity check was dropped (JSON cannot carry
those values).

**Complexity:** M — one new document field, one new config ceiling, one
new management-API route, one new CLI subcommand, one new `gpu-control`
action; no new state-machine transitions (reuses CORE-100's existing
`EvBudgetExhausted` drain path unchanged).
**Risk:** medium — a leaked management token's worst-case cost changes
from a fixed, operator-chosen number to a fixed ceiling the token cannot
raise; getting the ceiling or the `nil` handling wrong either breaks
every existing deployment on upgrade or weakens the mitigation further
than intended.
**Estimated effort:** one session (4–6 h)
**Recommended model:** strong — touches the controller/mgmt boundary,
a stored-document schema change, and a security-relevant validation path.
**Depends on:** [CORE-055](../055-D-management-api/spec.md) (the management
API this adds a route to), [CORE-100](../100-D-cost-controls/spec.md) (the
`budget.Checker`/`store.Document` this changes), [CORE-150](../150-D-vk-llm-cli/spec.md)
(the CLI this adds a subcommand to), [CORE-160](../160-D-github-actions-remote-control/spec.md)
(the workflow this adds an action to)

## Outcome

An operator runs `vk-llm budget set 40` (or `POST /api/v1/gpu/budget
{"monthlyUSD": 40}`, or dispatches `gpu-control` with `action: budget,
monthly_usd: 40`) and the new cap applies immediately, with no redeploy:
a `vk-llm start` that was failing with `budget_exhausted` at the old $30
cap now succeeds, using only the $10 of fresh headroom — month-to-date
spend is untouched. `vk-llm status` / `GET /api/v1/gpu/status` show the
new cap right away. The API will not accept a value above a fixed,
Helm-configured ceiling (default $100), and enforcement never exceeds
it either: a leaked management token can raise the budget, but only up
to a number the operator set in advance. A budget-exhausted `start` now
explains itself: `message` names the current cap and current spend
instead of being empty.

## Scope

In this repository:

- `internal/store/document.go` — add `Document.BudgetMonthUSD *float64`.
- `internal/budget/checker.go` — `Config.MaxMonthlyUSD`; new
  `CapUSD(doc)` and `CeilingUSD()`; `Allow` and `UsedRatio` use `CapUSD`.
- `internal/config/budget.go` — add `Budget.MaxMonthlyUSD` (env
  `VKLLM_BUDGET_MAX_MONTHLY_USD`, default 100); fail on `<= 0` or on
  `MonthlyUSD > MaxMonthlyUSD`.
- `internal/config/mgmt.go` — remove `Mgmt.BudgetMonthUSD` and the env var
  `VKLLM_MGMT_BUDGET_MONTH_USD`.
- `cmd/gpu-on-demand/wire.go` — pass `MaxMonthlyUSD` into `budget.Config`;
  delete the `d.Mgmt.BudgetMonthUSD = d.Budget.MonthlyUSD` override.
- `internal/controller` — `Budget` interface gains `CapUSD`/`CeilingUSD`;
  `Snapshot` gains `BudgetMonthUSD` and `BudgetCeilingUSD`; new
  `Controller.SetBudgetMonthUSD(ctx, usd float64) (Snapshot, error)` that
  validates and writes through `store.Transition`; new errors
  `ErrBudgetInvalid`, `ErrBudgetAboveCeiling`.
- `internal/mgmt` — the `Controller` interface gains
  `SetBudgetMonthUSD`; new route `POST /api/v1/gpu/budget`; documents read
  `snap.BudgetMonthUSD`; the `budget_exhausted` start denial gains a
  `message`.
- `cmd/vk-llm` — the argument parser accepts extra positionals for
  `budget` only; new `budget` subcommand; new `client.setBudget`.
- `scripts/gpu-control.sh` — `req()` takes an optional body; new
  `do_budget()`.
- `.github/workflows/gpu-control.yml` — `action` gains `budget`; new
  optional `monthly_usd` string input. This amends CORE-160 R2 (options
  were exactly `start`, `status`, `stop`).
- `deploy/helm/gpu-on-demand/values.yaml`, `templates/_helpers.tpl` — add
  `config.budget.maxMonthlyUSD: 100` → `VKLLM_BUDGET_MAX_MONTHLY_USD`.
- `specs/core/080-D-helm-chart/spec.md` — its sentence about
  `VKLLM_MGMT_BUDGET_MONTH_USD` becomes stale; reword it, and add the new
  `config.budget.maxMonthlyUSD` row to its config table.
- `specs/core/110-P-security-and-secrets/spec.md` — one new threat-model
  row: a leaked management token can raise its own budget cap, up to the
  Helm-configured ceiling; accepted per ADR 0015.
- `README.md` — document `vk-llm budget` and the `gpu-control` budget
  action.
- `docs/adr/0015-runtime-mutable-budget-cap.md` — Accepted together with
  this spec's DONE flip.

Excludes: a second credential tier for budget changes (ADR 0015
"Alternatives considered"); any change to `MaxInstanceSpendUSD`; any
change to `/v1/*`'s `backend_unavailable` error body (budget and spend
numbers never reach an inference-key holder); a cooldown between budget
changes; a GET-only read route (status/cost already carry the cap); a
startup backfill (ADR 0015 §5).

## Design and contracts

### Document field (ADR 0015 §1)

```go
// internal/store/document.go
type Document struct {
	// ... existing fields unchanged ...
	BudgetMonthUSD *float64 `json:"budgetMonthUsd,omitempty"`
}
```

A sibling of `Ledger`, not inside it. `Rollover` touches only
`doc.Ledger.*`, so the value survives month rollover. A document written
before this spec decodes to `nil`.

### Checker (ADR 0015 §1, §2, §5)

```go
// internal/budget/checker.go
type Config struct {
	MonthlyUSD        float64
	MaxMonthlyUSD     float64 // 0 means no ceiling (tests only; LoadBudget rejects 0)
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

func (c *Checker) CapUSD(doc store.Document) float64 {
	v := c.cfg.MonthlyUSD
	if doc.BudgetMonthUSD != nil {
		v = *doc.BudgetMonthUSD
	}
	if c.cfg.MaxMonthlyUSD > 0 && v > c.cfg.MaxMonthlyUSD {
		v = c.cfg.MaxMonthlyUSD
	}
	return v
}

func (c *Checker) CeilingUSD() float64 { return c.cfg.MaxMonthlyUSD }
```

`Allow` compares `EstimatedMTD(doc) >= c.CapUSD(doc)`. `UsedRatio`
divides by `c.CapUSD(doc)` (still `0` when the cap is `0`).

### Controller

```go
var (
	ErrBudgetInvalid      = errors.New("controller: budget must be a positive number")
	ErrBudgetAboveCeiling = errors.New("controller: budget above ceiling")
)

func (c *Controller) SetBudgetMonthUSD(ctx context.Context, usd float64) (Snapshot, error)
```

Order: `usd <= 0` → `ErrBudgetInvalid`; ceiling `> 0` and `usd >
ceiling` → `ErrBudgetAboveCeiling`; otherwise one `store.Transition`
that sets `doc.BudgetMonthUSD = &usd`, then return `c.Status()`. Every
other controller write already goes through `store.Transition`, which
reloads the document on each attempt, so no write can overwrite the new
value with a stale copy. Raising the cap needs no wake-up: `gateReason`
calls `Budget.Allow` against a freshly loaded document on every `Start`.

`Snapshot` gains `BudgetMonthUSD float64` (`Budget.CapUSD(doc)`) and
`BudgetCeilingUSD float64` (`Budget.CeilingUSD()`), both filled in
`Status()`.

### Management API route

```
POST /api/v1/gpu/budget
Authorization: Bearer <management-token>   (same tier as start/stop)

{"monthlyUSD": 40}
```

| Case | Status | `code` | `message` |
|---|---|---|---|
| body not valid JSON, unknown field, or a number out of float64 range (`1e400`) | 400 | `invalid_json` | `""` |
| `monthlyUSD <= 0` or missing | 400 | `budget_invalid` | `monthlyUSD must be a positive number` |
| `monthlyUSD` above the ceiling | 400 | `budget_above_ceiling` | `monthlyUSD must not exceed the configured ceiling of $100.00` |
| success | 200 | — | status document with the new `cost.budgetMonthUSD` |

`POST`, not `PATCH` — every other mutating route on this API uses
`s.method(http.MethodPost, ...)`. JSON cannot encode `NaN` or
`Infinity`, and `encoding/json` rejects out-of-range numbers, so the
decoder is the finite-number guard; no separate `math.IsNaN` check.

Every successful change logs one line:

```go
s.d.Log.Info("mgmt: budget changed", "key_id", keyID, "oldUSD", before.BudgetMonthUSD, "newUSD", snap.BudgetMonthUSD)
```

`key_id` comes from `keyIDSink(r.Context())`, which `withAuth` already
fills.

### Budget-exhausted message

```go
fmt.Sprintf("monthly budget of $%.2f reached ($%.2f spent); raise it with vk-llm budget set",
	snap.BudgetMonthUSD, snap.SpendMTDUSD)
```

Used only when `POST /api/v1/gpu/start` is denied with reason
`budget_exhausted`. Every other error's `message` is unchanged.

### Status and cost documents

`buildStatusDocument` and `buildCostDocument` set `BudgetMonthUSD:
snap.BudgetMonthUSD` instead of `cfg.BudgetMonthUSD`.

### `vk-llm budget`

The parser today accepts exactly one positional (the subcommand). It
collects the positionals after the subcommand into `subArgs`; every
subcommand except `budget` still rejects a non-empty `subArgs` with
`unexpected argument: <x>`, exit 2.

- `vk-llm budget` → one `GET status`; prints two tab-aligned rows:

  ```
  Budget  $30.00 / month
  Spent   $12.34 this month (estimate)
  ```
- `vk-llm budget set <amount>` → `<amount>` must parse as a float `> 0`,
  else exit 2 with no request; then `POST budget`; prints the confirmed
  new cap.
- Anything else after `budget` → exit 2.
- `--json` prints the raw response body, as for `status`.

### `gpu-control`

```yaml
inputs:
  action:
    type: choice
    options: [start, status, stop, budget]
    default: status
  monthly_usd:
    description: New monthly budget in USD (budget action only; blank shows the current value)
    required: false
    type: string
```

`MONTHLY_USD: ${{ inputs.monthly_usd }}` reaches the script through
`env:` only. `req METHOD PATH [BODY]` adds `-d BODY -H 'Content-Type:
application/json'` when `BODY` is non-empty. `do_budget`: blank
`MONTHLY_USD` → `do_status`; a value not matching
`^[0-9]+(\.[0-9]+)?$` → exit 2 with no request; otherwise `POST budget`
and print the status row; the row (shared with `status`) includes
`budgetMonthUSD`.

## Requirements

- R1. A document without `budgetMonthUsd` MUST enforce the Helm-configured
  `budget.monthlyUSD`, exactly as before this spec. (source: ADR 0015 §5)
- R2. The enforced cap MUST never exceed `config.Budget.MaxMonthlyUSD`,
  whatever the document holds. (source: ADR 0015 §2)
- R3. `POST /api/v1/gpu/budget` MUST reject a non-positive value and a
  value above the ceiling with `400` and distinct codes, without changing
  the document. (source: ADR 0015 §2)
- R4. `config.Budget.MaxMonthlyUSD` MUST come only from the environment
  (Helm), never from `store.Document`; startup MUST fail when it is `<= 0`
  or below `MonthlyUSD`. (source: ADR 0015 §2)
- R5. `POST /api/v1/gpu/budget` MUST use the `management-tokens` tier.
  (source: ADR 0015 §3)
- R6. Every successful change MUST log `key_id`, the old cap and the new
  cap. (source: ADR 0015 §3)
- R7. `config.Mgmt.BudgetMonthUSD` and `VKLLM_MGMT_BUDGET_MONTH_USD` MUST
  be deleted; status and cost MUST report the cap `Allow` enforces.
  (source: ADR 0015 §4)
- R8. A `budget_exhausted` start denial MUST carry the message format in
  Design and contracts. (source: Outcome)
- R9. A raised cap MUST apply to the next `POST /api/v1/gpu/start` with no
  restart. (source: Outcome)
- R10. Setting the budget MUST NOT change `doc.Ledger`. (source: Outcome)
- R11. `vk-llm budget` and the `gpu-control` budget action MUST each have
  a read form and a set form. (source: user's interface choice)
- R12. `/v1/*`'s `backend_unavailable` body MUST NOT change. (source:
  ADR 0010 §2, security review)

## Implementation steps

1. Document field, `Checker.CapUSD`/`CeilingUSD`, `config.Budget.MaxMonthlyUSD`,
   wiring and Helm value. Check: budget and config unit tests; `helm
   template` renders `VKLLM_BUDGET_MAX_MONTHLY_USD`.
2. Controller: `Snapshot` fields, `SetBudgetMonthUSD`, errors. Check:
   controller unit tests against the fake store.
3. Management API: documents read the snapshot, `Mgmt.BudgetMonthUSD`
   removed, exhausted-start message. Check: mgmt and wire tests.
4. Management API: `POST /api/v1/gpu/budget`. Check: handler tests for
   every row of the table and the audit log line.
5. CLI: parser, `budget` subcommand, `client.setBudget`. Check: tests
   against the CLI's scripted fake server.
6. `gpu-control`: `req` body, `do_budget`, workflow input. Check: tests
   against CORE-160's TLS fake; shellcheck; `TestWorkflowShape`.
7. Documentation: spec 080, CORE-110 threat-model row, README, Evidence.
   Check: `make specs-check`.

## Tests / acceptance criteria

Tier 1 and tier 2 only — no real Vast.ai spend.

- AC1. Legacy document. `CapUSD(store.Document{})` with `MonthlyUSD=30`
  returns `30`; existing CORE-100 `Allow` tests pass unchanged.
- AC2. Raise takes effect. `Ledger.TotalUSD=30`, no document cap →
  `Allow` returns `budget_exhausted`; after `BudgetMonthUSD=40` → `""`.
- AC3. Ceiling clamps enforcement. `BudgetMonthUSD=150`,
  `MaxMonthlyUSD=100` → `CapUSD` returns `100`.
- AC4. API rejects. `{"monthlyUSD":150}` → `400 budget_above_ceiling`;
  `{"monthlyUSD":0}`, `{"monthlyUSD":-5}`, `{}` → `400 budget_invalid`;
  `{"monthlyUSD":1e400}` and `{"monthlyUSD":"x"}` → `400 invalid_json`.
  The controller is never called for the `invalid_json` cases.
- AC5. Ledger untouched. `TotalUSD` is the same before and after
  `SetBudgetMonthUSD(40)`.
- AC6. Status and cost show the live cap from `Snapshot`.
- AC7. The exhausted-start `message` equals the format in Design and
  contracts for a fixture cap and spend.
- AC8. A successful change logs one line with `key_id`, `oldUSD`, `newUSD`.
- AC9. `vk-llm budget`, `vk-llm budget set 40`, and `vk-llm budget set x`
  (exit 2, no request) against the CLI fake server.
- AC10. `gpu-control` budget action, read and set forms, against the TLS
  fake; a non-numeric `monthly_usd` exits 2 with no request.
- AC11. `git grep VKLLM_MGMT_BUDGET_MONTH_USD` finds nothing outside
  specs, ADRs and docs that describe its removal.
- AC12. `/v1/*`'s `backend_unavailable` body is unchanged (existing
  gateway tests pass unchanged).

## Definition of done

- [x] AC1–AC12 pass (tier 1/2).
- [x] `make specs-check go-build go-test go-lint helm-lint helm-template`
      all exit 0.
- [x] CORE-110's threat-model table has the new row.
- [x] ADR 0015 flipped to Accepted in the same PR.
- [x] Status flipped to DONE; folder renamed `190-D-...`. Done after CI
      run 44959242039 (PR #33) went green, with the user's go-ahead.
- [x] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Where `VKLLM_MGMT_BUDGET_MONTH_USD` is rendered in Helm | removal scope | settled: spec 080 kept it out of the chart on purpose; removal is Go-only | n/a |
| Does lowering the cap below spend drain on the next tick or the next balance refresh | user-visible timing | `reconcilePruneAndBudget`'s existing `if known` guard, unchanged | next balance refresh; the CLI does not promise an instant drain |
| Is a Helm change to `budget.monthlyUSD` ignored after an API set | operator surprise | ADR 0015 §1 | yes: once set through the API, the document value wins; change it with `vk-llm budget set`; documented in the README |

## Evidence

| SHA | Task | Content |
|---|---|---|
| `0478c47` | Task 1 | Document field, `Checker.CapUSD`, `config.Budget.MaxMonthlyUSD`, wiring, Helm ceiling value |
| `0f62fa5` | Task 2 | Controller `Snapshot` fields, `SetBudgetMonthUSD`, errors |
| `d26d501` | Task 3 | Status/cost documents read the live cap from `Snapshot`; `budget_exhausted` message |
| `1b0d270` | Task 4 | `POST /api/v1/gpu/budget` with ceiling rejection and audit log |
| `94fa3a0` | Task 4 (fix) | Review fixes: `key_id` naming, AC12 budget coverage |
| `d04de3a` | Task 5 | `vk-llm budget` and `vk-llm budget set` |
| `4505064` | Task 5 (fix) | Fix stale parse-loop comment after `budget` subArgs change |
| `5002432` | Task 6 | `gpu-control` budget action |

| AC | Test |
|---|---|
| AC1 | `TestCapUSDDefaultsToConfig`, `TestStatusBudgetDefaultsToConfig`, existing CORE-100 `Allow` tests unchanged |
| AC2 | `TestAllowUsesDocumentCap`, `TestRaisedBudgetUnblocksNextStart` |
| AC3 | `TestCapUSDClampsToCeiling` |
| AC4 | `TestBudgetRejections`, `TestSetBudgetMonthUSDRejects` |
| AC5 | `TestSetBudgetMonthUSDStoresValueAndKeepsLedger` |
| AC6 | `TestStatusDocumentBudgetFromSnapshot`, `TestWireCostShowsBudgetMonthlyUSD` |
| AC7 | `TestStartBudgetExhaustedMessage` |
| AC8 | `TestBudgetChangeIsLogged` |
| AC9 | `TestBudgetShow`, `TestBudgetSet`, `TestBudgetSetRejectsBadAmount` |
| AC10 | `TestBudgetShowCallsStatus`, `TestBudgetSetPostsBody`, `TestBudgetSetRejectsNonNumeric`, `TestWorkflowShape` |
| AC11 | `git grep VKLLM_MGMT_BUDGET_MONTH_USD -- '*.go' '*.yaml' '*.tpl'` returns nothing |
| AC12 | existing gateway tests pass unchanged |
