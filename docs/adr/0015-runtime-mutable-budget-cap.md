# ADR 0015: The monthly budget cap moves from a Helm value to a runtime-mutable document field, behind a fixed ceiling

## Status

Proposed (2026-11-21). Amends CORE-100 (DONE); does not reopen ADR 0012,
but narrows one of its threat-model mitigations (see Consequences).

## Context

CORE-100 shipped a hard monthly spend cap: `internal/budget.Checker.Allow`
blocks `POST /api/v1/gpu/start` and drains a running instance once
month-to-date spend reaches `budget.monthlyUSD` (default $30). That value
is a Helm chart value, rendered into a ConfigMap, read once by the
controller process at startup through `config.LoadBudget()`. Raising it
needs `helm upgrade` and a pod restart.

The user wants to raise or lower the cap without a redeploy — set it to
$40 mid-month and have the next `vk-llm start` work immediately, using
only the $10 of fresh headroom, with no change to already-spent month-to-
date totals.

A second, independent copy of the same nominal value already exists:
`config.Mgmt.BudgetMonthUSD` (env `VKLLM_MGMT_BUDGET_MONTH_USD`), read
separately by `internal/mgmt` purely to render `budgetMonthUSD` in
`GET /api/v1/gpu/status` and `GET /api/v1/gpu/cost`. It is never compared
against spend; `internal/budget.Checker`'s own `cfg.MonthlyUSD` is the only
value that gates anything. Any runtime-mutable design must leave exactly
one value as the source of truth for both enforcement and display, or the
status/cost endpoints will keep showing the old number after a change.

ADR 0012's threat model states: "A leaked management token starts GPUs.
Mitigation: budget caps." If the same `management-tokens` credential that
starts/stops the GPU can also raise its own spending cap, a leaked token's
worst case stops being a fixed, operator-chosen number and becomes
whatever the attacker sets it to. The user was told this directly and
chose to keep one credential tier for simplicity rather than add a second.

## Decision

1. **The cap lives in the state document, not in config.** `store.Document`
   gains `BudgetMonthUSD *float64` (`json:"budgetMonthUsd,omitempty"`), a
   sibling of `Ledger`, not nested inside it — `Rollover` only ever reads
   or writes `doc.Ledger.*` fields, so this field is untouched by month
   rollover regardless of placement, but a sibling field makes the "this
   is a limit, not ledger data" distinction explicit to a reader. It is a
   pointer so `nil` unambiguously means "never set" — a plain `float64`
   would make an old document's implicit zero value indistinguishable from
   a deliberately-exhausted budget, and `EstimatedMTD(doc) >= 0` is always
   true, hard-blocking every start on the first reconcile after upgrade.
   `internal/budget.Checker.Allow`, `EstimatedMTD`, and `UsedRatio` read
   this field directly from the `doc` argument they already receive; the
   `Config.MonthlyUSD` the `Checker` is constructed with stops being the
   enforced value and becomes only the seed default for a document that
   has never set `BudgetMonthUSD`.

2. **A fixed ceiling, outside the document, that the API can never exceed.**
   `config.Budget` gains `MaxMonthlyUSD` (env `VKLLM_BUDGET_MAX_MONTHLY_USD`,
   default 100; Helm value `config.budget.maxMonthlyUSD`). `POST
   /api/v1/gpu/budget` rejects any value above it with `400`. Raising the
   ceiling itself still needs `helm upgrade` — only the value between $0
   and the ceiling is runtime-mutable. This is the one piece of this ADR
   that is not optional: without it, a leaked management token's worst
   case goes from a fixed, operator-chosen number to the full prepaid
   Vast.ai balance, for the cost of one HTTP request. With it, the worst
   case is the ceiling (default $100) — larger than today's $30 default,
   but still a number the operator set in advance and the token cannot
   change.

3. **One credential tier, by explicit user choice.** `POST
   /api/v1/gpu/budget` is authenticated by the same `management-tokens`
   credential as `start`/`stop`/`status`/`cost`. No new Secret key, no
   approval step, no cooldown between changes. Every change is logged
   (`keyID`, old value, new value) as the only forensic trail if a token
   leaks — this is the minimum due diligence for an endpoint that can,
   within the ceiling, change what a credential is allowed to spend.

4. **`config.Mgmt.BudgetMonthUSD` is removed, not deprecated.**
   `controller.Snapshot` gains a `BudgetMonthUSD float64` field, populated
   from the controller's own view of `doc.BudgetMonthUSD` (falling back to
   `config.Budget.MonthlyUSD` only in the gap before the bootstrap backfill
   in Decision 5 runs). `buildStatusDocument` and `buildCostDocument` read
   `snap.BudgetMonthUSD` instead of `cfg.BudgetMonthUSD`. The env var
   `VKLLM_MGMT_BUDGET_MONTH_USD`, the `config.Mgmt.BudgetMonthUSD` field,
   and wherever the Helm chart renders that env var are deleted outright.
   Keeping a second, now-provably-redundant copy around "just in case"
   reintroduces the exact two-values problem this ADR exists to close.

5. **A document missing the field is backfilled once, at controller
   startup, before the management API begins serving.** The controller's
   existing bootstrap sequence gains one step: if `doc.BudgetMonthUSD ==
   nil`, set it to `config.Budget.MonthlyUSD` and persist it through the
   same `store.Transition` machinery every other mutation already uses.
   This runs once, synchronously, before the first request is served, so
   there is no window where `GET /status` can observe a `nil`-derived
   value.

## Consequences

ADR 0012's threat-model line "a leaked management token starts GPUs;
mitigation: budget caps" now reads "mitigation: a budget ceiling" — the
bound a leaked token is subject to is the Helm-configured ceiling (default
$100), not the operator's day-to-day budget setting (which the same token
can now also change, within that ceiling). This is a deliberate,
user-approved widening of the worst case from "$30, operator-chosen" to
"up to $100, operator-chosen ceiling, attacker-chosen value within it" —
recorded as an accepted risk in CORE-110's threat-model table, not as a
gap CORE-110 itself must close.

Lowering the budget below current month-to-date spend reuses the existing
`EvBudgetExhausted` drain path (CORE-100) unchanged — no new drain logic.
It takes effect on the controller's next provider-balance refresh, the
same `known` guard `reconcilePruneAndBudget` already applies to every
`Allow` check, not necessarily the very next reconcile tick. This ADR does
not change that cadence.

## Alternatives considered

**A second `budget-tokens` Secret key**, mirroring the existing
`client-keys`/`management-tokens` split. Rejected per the user's explicit
choice: one credential tier, accept the ceiling as the mitigation instead
of a second credential to manage and rotate.

**Hot-reloading the Helm-rendered ConfigMap** (no new auth surface,
`helm upgrade`/`kubectl edit` plus a controller file-watch, no API
endpoint at all). Rejected: does not meet the brief — the user wants an
API-driven change with no redeploy, reusable from `vk-llm` and
`gpu-control.yml`, not a cluster-admin-only path.

**Cooldown between budget increases.** Rejected: with a fixed ceiling in
place, a cooldown buys no additional bound on worst-case exposure and adds
state to track for no benefit.
