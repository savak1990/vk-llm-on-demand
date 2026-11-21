---
id: "CORE-190"
status: "DRAFT"
updated: "2026-11-21"
depends_on: ["CORE-055", "CORE-100", "CORE-150", "CORE-160"]
adrs: ["0015", "0010", "0011", "0012"]
---

# 190 — Runtime budget reconfiguration

**Status note:** DRAFT, written 2026-11-21 before any code exists.

**Complexity:** M — one new document field, one new config ceiling, one
new management-API route, one new CLI subcommand, one new `gpu-control`
action; no new state-machine transitions (reuses CORE-100's existing
`EvBudgetExhausted` drain path unchanged).
**Risk:** medium — a leaked management token's worst-case cost changes
from a fixed, operator-chosen number to a fixed ceiling the token cannot
raise; getting the ceiling check or the `nil`-vs-zero migration wrong
either breaks every existing deployment on upgrade (an old document has
no `budgetMonthUsd` field) or weakens the mitigation further than intended.
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
Helm-configured ceiling (default $100): a leaked management token can
raise the budget, but only up to a number the operator set in advance,
never past it. A budget-exhausted `start` now explains itself: `message`
names the current cap and current spend instead of being empty.

## Scope

In this repository:

- `internal/store/document.go` — add `Document.BudgetMonthUSD *float64`.
- `internal/budget/checker.go` — `Allow`, `EstimatedMTD`, `UsedRatio` read
  the cap from `doc.BudgetMonthUSD` instead of `c.cfg.MonthlyUSD`.
- `internal/config/budget.go` — add `Budget.MaxMonthlyUSD` (env
  `VKLLM_BUDGET_MAX_MONTHLY_USD`, default 100).
- `internal/config/mgmt.go` — remove `Mgmt.BudgetMonthUSD` and the env var
  `VKLLM_MGMT_BUDGET_MONTH_USD` outright.
- `internal/controller` — a new bootstrap step that backfills a `nil`
  `BudgetMonthUSD` from `config.Budget.MonthlyUSD` before the management
  API starts serving; `controller.Snapshot` gains `BudgetMonthUSD float64`;
  a new `Controller.SetBudgetMonthUSD(ctx, usd float64) (Snapshot, error)`
  method (validates nothing itself — mgmt validates before calling it);
  the `Budget` interface gains `SetMonthlyUSD` or equivalent so the
  controller can reach `doc.BudgetMonthUSD` through the same
  `store.Transition` machinery every other mutation uses.
- `internal/mgmt/mgmt.go` — widen the narrow `Controller` interface this
  package depends on with the one new method above.
- `internal/mgmt/handlers.go` — new route `POST /api/v1/gpu/budget`; the
  budget-exhausted branch of `respondOp` gains a non-empty `message`.
- `internal/mgmt/documents.go` — `buildStatusDocument`/`buildCostDocument`
  read `snap.BudgetMonthUSD` instead of `cfg.BudgetMonthUSD`.
- `cmd/vk-llm/client.go` — new `(c *client) setBudget(ctx, usd float64)
  (apiResult, int, error)`.
- `cmd/vk-llm/commands.go` — new `budget` subcommand (`vk-llm budget` to
  show, `vk-llm budget set <amount>` to change).
- `scripts/gpu-control.sh` — `req()` regains an optional body parameter;
  new `do_budget()`.
- `.github/workflows/gpu-control.yml` — `action` input gains `budget`;
  new optional `monthly_usd` string input.
- `deploy/helm/gpu-on-demand/values.yaml` and `templates/` — add
  `config.budget.maxMonthlyUSD: 100`; remove whatever renders
  `VKLLM_MGMT_BUDGET_MONTH_USD` (grep `_helpers.tpl`/`configmap.yaml` for
  the exact line — it was not conclusively located during this spec's
  research and must be found and removed, not assumed absent).
- `specs/core/110-P-security-and-secrets/spec.md` — one new threat-model
  row: a leaked management token can raise its own budget cap, up to the
  Helm-configured ceiling; accepted per ADR 0015, not a gap this spec or
  CORE-110 must close further.
- `docs/adr/0015-runtime-mutable-budget-cap.md` — already written
  (Proposed); flip to Accepted when this spec's status flips to DONE.

Excludes: a second credential tier for budget changes (ADR 0015
"Alternatives considered"); any change to `MaxInstanceSpendUSD` (the
per-instance cap is independent and untouched); any change to `/v1/*`'s
`backend_unavailable` error body (stays static — budget/spend numbers
never reach an inference-key holder); a cooldown between budget changes;
a GET-only read route (status/cost already carry the cap).

## Design and contracts

### Document field (ADR 0015 §1)

```go
// internal/store/document.go
type Document struct {
	// ... existing fields unchanged ...
	BudgetMonthUSD *float64 `json:"budgetMonthUsd,omitempty"`
}
```

A sibling of `Ledger`, not nested inside it — `budget.Checker.Rollover`
only ever touches `doc.Ledger.*`, so this field survives month rollover
regardless of placement, but keeping it outside `Ledger` keeps "this is a
limit" visually distinct from "this is spend history."

### Checker changes

```go
// internal/budget/checker.go
func (c *Checker) Allow(doc store.Document, balance provider.Money, known bool) string {
	if c.EstimatedMTD(doc) >= capUSD(doc) {
		return "budget_exhausted"
	}
	if !known || balance.USD() < 2*c.cfg.MaxHourlyPriceUSD {
		return "balance_low"
	}
	return ""
}

func capUSD(doc store.Document) float64 {
	if doc.BudgetMonthUSD == nil {
		return 0 // unreachable once the bootstrap backfill (below) has run
	}
	return *doc.BudgetMonthUSD
}
```

`EstimatedMTD` and `UsedRatio` call the same `capUSD(doc)` helper instead
of `c.cfg.MonthlyUSD`. `Config.MonthlyUSD` is kept on `Checker` but used
only as the seed value the bootstrap step writes into a fresh document —
`Checker` itself never falls back to it mid-flight, so a `nil` reaching
`Allow` after bootstrap is a bug, not an expected path (it returns `0`,
the safe failure direction: an unset cap blocks spending rather than
allowing it unbounded).

### Bootstrap backfill (ADR 0015 §5)

In the controller's existing startup sequence, before `mgmt.New(...)` is
called and the HTTP server starts accepting connections:

```go
_, err := store.Transition(ctx, deps.Store, func(d *store.Document) error {
	if d.BudgetMonthUSD == nil {
		v := cfg.Budget.MonthlyUSD
		d.BudgetMonthUSD = &v
	}
	return nil
})
```

This runs exactly once per process start, using the same `store.Transition`
CAS-retry loop every other mutation already uses — no new concurrency
primitive.

### New management API route

```
POST /api/v1/gpu/budget
Authorization: Bearer <management-token>   (same tier as start/stop)
Content-Type: application/json

{"monthlyUSD": 40}
```

Validation order, each failure a distinct `400` (checked in this order,
first match wins):

1. Strict JSON decode (reuse `decodeStrict`, already rejects unknown
   fields) → `invalid_json` on failure.
2. `math.IsNaN(v) || math.IsInf(v, 0)` → `400 budget_invalid`, message
   `"monthlyUSD must be a finite number"`.
3. `v <= 0` → `400 budget_invalid`, message `"monthlyUSD must be positive"`.
4. `v > cfg.Budget.MaxMonthlyUSD` → `400 budget_above_ceiling`, message
   `fmt.Sprintf("monthlyUSD must not exceed the configured ceiling of $%.2f", cfg.Budget.MaxMonthlyUSD)`.

On success: `200`, body is the same `statusDocument` shape `GET
/api/v1/gpu/status` already returns (so `cmd/vk-llm/client.go`'s `do()`
needs no new response type), reflecting the new `cost.budgetMonthUSD`.

`POST`, not `PATCH` — every other mutating route on this API
(`start`/`stop`) is wired through `s.method(http.MethodPost, ...)`;
introducing `PATCH` for one route alone is an inconsistency this spec
does not need to create.

Every successful change is logged:
```go
s.d.Log.Info("mgmt: budget changed", "keyID", keyID, "oldUSD", old, "newUSD", new)
```
(`keyID` is already available from request context, the same way the
existing auth middleware makes it available to other handlers.)

### Budget-exhausted error message (ADR 0015 §1, user-facing "clear error")

```go
message := fmt.Sprintf(
	"monthly budget of $%.2f reached ($%.2f spent); raise it with vk-llm budget set",
	cap, spent,
)
```
Used in the `budget_exhausted` branch of `respondOp`'s error path for
`POST /api/v1/gpu/start` — `message` stops being the empty string for
this one reason code; every other error code's `message` is unchanged.

### `controller.Snapshot` / document builders

```go
// internal/controller/controller.go (Snapshot struct)
BudgetMonthUSD float64
```
Populated from the controller's current view of `doc.BudgetMonthUSD`
(dereferenced; by the time `Status()` can be called, bootstrap has
already run, so this is never nil in practice — defend with a `0`
fallback only, never a panic).

```go
// internal/mgmt/documents.go
Cost: costSummary{
	EstimatedInstanceUSD: snap.SpendInstanceUSD,
	EstimatedMonthUSD:    snap.SpendMTDUSD,
	BudgetMonthUSD:       snap.BudgetMonthUSD, // was: cfg.BudgetMonthUSD
},
```
Same change in `buildCostDocument`. `config.Mgmt.BudgetMonthUSD` and its
env var `VKLLM_MGMT_BUDGET_MONTH_USD` are deleted, not kept as unused
fields.

### `vk-llm budget` CLI

```go
// cmd/vk-llm/commands.go
case "start", "status", "stop", "budget":   // the early allow-list guard
...
case "budget":
    return cmdBudget(cfg, cl, stdout, stderr, args, jsonOut)
```

`vk-llm budget` (no further args): one `status()` call, print two rows —
current cap and current month-to-date spend, same field names `vk-llm
status` already uses for cost.

`vk-llm budget set <amount>`: parse `<amount>` as a float; reject
non-numeric input locally with exit 2 (usage error, no network call) the
same way an unknown subcommand is rejected today; otherwise call the new
`setBudget` and print the confirmed new value from the response.

```go
// cmd/vk-llm/client.go
func (c *client) setBudget(ctx context.Context, usd float64) (apiResult, int, error) {
	body := bytes.NewReader([]byte(fmt.Sprintf(`{"monthlyUSD":%g}`, usd)))
	return c.do(ctx, http.MethodPost, "/api/v1/gpu/budget", body)
}
```

### `gpu-control.yml` / `scripts/gpu-control.sh`

```yaml
inputs:
  action:
    type: choice
    options: [start, status, stop, budget]
    default: status
  monthly_usd:
    description: New monthly budget in USD (only used when action is budget; leave blank to just show the current value)
    required: false
    type: string
```
`workflow_dispatch` inputs are always strings — `monthly_usd` reaches the
script as `env: MONTHLY_USD: ${{ inputs.monthly_usd }}`, an empty string
when not supplied.

`req()` regains the body parameter the original CORE-160 spec draft had
and Task 1 of that spec dropped as unused — it is needed now:

```bash
req() {
  local method=$1 path=$2 body=${3-}
  local extra=()
  if [[ -n "$body" ]]; then
    extra=(-d "$body" -H 'Content-Type: application/json')
  fi
  local raw status
  raw=$(printf 'Authorization: Bearer %s\n' "$VK_LLM_TOKEN" |
    curl -s --max-time 30 -X "$method" -H @- "${extra[@]+"${extra[@]}"}" \
         -w '\n%{http_code}' \
         "$ENDPOINT/api/v1/gpu/$path")
  status=${raw: -3}
  local resp_body=${raw%???}
  resp_body=${resp_body%$'\n'}
  printf '%s' "$resp_body"
  [[ "$status" -ge 200 && "$status" -lt 300 ]]
}

do_budget() {
  if [[ -z "${MONTHLY_USD:-}" ]]; then
    do_status
    return
  fi
  if ! [[ "$MONTHLY_USD" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
    echo "gpu-control: monthly_usd must be a positive number" >&2
    exit 2
  fi
  local body
  body=$(req POST budget "{\"monthlyUSD\":$MONTHLY_USD}") || fail_from_body "$body"
  print_status_row "$body"
}
```
`"${extra[@]+"${extra[@]}"}"` rather than bare `"${extra[@]}"` — an empty
array under `set -u` on bash 3.2 (macOS's default `/bin/bash`, relevant
if anyone runs this script locally rather than only in CI) errors on
plain `"${extra[@]}"`; the `+` expansion sidesteps it. Add `budget)
do_budget ;;` to the existing `case "$ACTION" in` dispatch.

## Requirements

- R1. `Document.BudgetMonthUSD` MUST be `*float64`; a `nil` value MUST
  never reach `budget.Checker.Allow` after controller startup — the
  bootstrap backfill MUST run before the management API begins serving.
  (source: ADR 0015 §1, §5)
- R2. `POST /api/v1/gpu/budget` MUST be rejected with `400` for a
  non-finite value (`NaN`/`Inf`), a value `<= 0`, or a value above
  `config.Budget.MaxMonthlyUSD`, each with a distinct error `code` and a
  `message` naming what was wrong. (source: ADR 0015 §2, fable security
  review)
- R3. `config.Budget.MaxMonthlyUSD` MUST be read only from Helm-rendered
  config (env var), never from `store.Document` — the ceiling itself MUST
  require a redeploy to change. (source: ADR 0015 §2)
- R4. `POST /api/v1/gpu/budget` MUST use the same `management-tokens`
  credential tier as `start`/`stop`. (source: ADR 0015 §3, user's explicit
  choice)
- R5. Every successful budget change MUST be logged with the requesting
  `keyID`, the old value, and the new value. (source: ADR 0015 §3, fable
  security review)
- R6. `config.Mgmt.BudgetMonthUSD` and its env var MUST be deleted, not
  left as an unused/dead field; `GET /api/v1/gpu/status` and `GET
  /api/v1/gpu/cost` MUST report the same cap value that
  `budget.Checker.Allow` enforces, sourced from `controller.Snapshot`.
  (source: ADR 0015 §4)
- R7. A `budget_exhausted` response from `POST /api/v1/gpu/start` MUST
  include a non-empty `message` naming the current cap and current
  month-to-date spend. (source: Outcome, user's "clear error response")
- R8. Raising the budget MUST take effect on the very next `POST
  /api/v1/gpu/start` or reconcile tick, with no process restart. (source:
  Outcome, user's explicit requirement)
- R9. Changing the budget MUST NOT alter month-to-date spend
  (`doc.Ledger.TotalUSD`/open-entry estimates) in any way. (source:
  Outcome, "additional $10" framing)
- R10. `vk-llm budget` and `gpu-control`'s `budget` action MUST both
  support a read (no amount given) and a set (amount given) form.
  (source: user's explicit interface choice)
- R11. The `/v1/*` inference API's `backend_unavailable` error body MUST
  NOT change — no budget or spend number reaches an inference-key holder.
  (source: fable security review, ADR 0010 §2)

## Implementation steps

1. Add `Document.BudgetMonthUSD *float64` and the bootstrap backfill
   Transition. Check: a fresh `NewDocument` plus one bootstrap run
   produces a document with `BudgetMonthUSD` set to the configured
   default; an existing (pre-this-spec) document loaded and bootstrapped
   gets backfilled, not zeroed.
2. Change `Checker.Allow`/`EstimatedMTD`/`UsedRatio` to read the cap from
   `doc.BudgetMonthUSD`. Check: existing CORE-100 unit tests for `Allow`
   still pass once their fixtures set `BudgetMonthUSD` instead of relying
   on `Checker.cfg.MonthlyUSD`; a new test proves a document with
   `BudgetMonthUSD=40` and `$35` spent allows a start that a `$30` cap
   would have denied.
3. Add `config.Budget.MaxMonthlyUSD` (env, default 100). Check:
   `LoadBudget` test asserts the default and an overridden value.
4. Add `POST /api/v1/gpu/budget` with the four-step validation order and
   audit log. Check: each of the three rejection cases returns the right
   `code`/`message`/`400`; a valid value returns `200` with the updated
   `cost.budgetMonthUSD`; the log line is asserted in a test (capture the
   logger output).
5. Add `controller.Snapshot.BudgetMonthUSD`, wire `buildStatusDocument`/
   `buildCostDocument` to read it, delete `config.Mgmt.BudgetMonthUSD`
   and its env var, find and remove wherever the Helm chart renders
   `VKLLM_MGMT_BUDGET_MONTH_USD`. Check: `GET /api/v1/gpu/status`
   immediately after a successful `POST /api/v1/gpu/budget` shows the new
   value; `grep -r VKLLM_MGMT_BUDGET_MONTH_USD` across the repo returns
   nothing.
6. Fill in the `budget_exhausted` `message`. Check: a test asserts the
   exact formatted string against a fixture cap/spend pair.
7. Add `vk-llm budget` / `vk-llm budget set <amount>` and
   `client.setBudget`. Check: against the scripted fake management server
   (the same one CORE-150 built), both the read and set forms produce the
   right request and render the right output; a non-numeric `<amount>`
   exits 2 with no network call.
8. Add the `budget` action to `gpu-control.yml` and `do_budget()` to
   `scripts/gpu-control.sh`. Check: against the scripted fake server
   (CORE-160's harness), a read (`MONTHLY_USD` unset) calls `do_status`;
   a set calls `POST budget` with the right body; shellcheck 0 findings;
   `golangci-lint`/`go test` unaffected.
9. Add the Helm value `config.budget.maxMonthlyUSD: 100` and wire it to
   `VKLLM_BUDGET_MAX_MONTHLY_USD`. Check: `helm template` renders it;
   `helm lint` passes.
10. Add the threat-model row to CORE-110's spec. Check: `make specs-check`
    still passes (no broken links, front matter unchanged).
11. Flip ADR 0015 to Accepted in the same PR this spec flips to DONE.

## Tests / acceptance criteria

Tier 1 (`go test`) and tier 2 (scripted fake management server, the same
one CORE-150/CORE-160 use for `vk-llm`/`gpu-control.sh`) only — no real
Vast.ai spend anywhere in this spec's own test suite.

- AC1. Nil-document migration. A `store.Document` with `BudgetMonthUSD ==
  nil` (simulating a pre-this-spec deployment), after the bootstrap
  Transition runs, has `BudgetMonthUSD` set to the configured default and
  `Allow` behaves exactly as it did before this spec (same $30-equivalent
  threshold).
- AC2. Raise takes effect immediately. Fixture: `doc.Ledger.TotalUSD =
  30`, `doc.BudgetMonthUSD = 30` → `Allow` returns `budget_exhausted`.
  After `POST /api/v1/gpu/budget {"monthlyUSD": 40}`, the same document
  (spend untouched) → `Allow` returns `""` (allowed). A subsequent `POST
  /api/v1/gpu/start` against the fake management flow succeeds.
- AC3. Ceiling enforced. `POST /api/v1/gpu/budget {"monthlyUSD": 150}`
  with `MaxMonthlyUSD=100` → `400`, `code: budget_above_ceiling`,
  `message` names `$100.00`. The document's `BudgetMonthUSD` is
  unchanged.
- AC4. Invalid input rejected. `{"monthlyUSD": -5}`, `{"monthlyUSD": 0}`,
  `{"monthlyUSD": "NaN"}` (via raw JSON `NaN`/`Infinity` is not valid JSON
  — test the Go-level `math.IsNaN`/`IsInf` guard directly against a
  crafted float, and separately test that malformed JSON hits
  `invalid_json`) — each rejected `400` before any document mutation.
- AC5. Spend untouched by a budget change. Before: `TotalUSD = 30`. After
  `POST /api/v1/gpu/budget {"monthlyUSD": 40}`: `TotalUSD` still `30`,
  `BudgetMonthUSD` now `40`.
- AC6. Status/cost reflect the live cap. After a successful budget
  change, `GET /api/v1/gpu/status` and `GET /api/v1/gpu/cost` both show
  the new `budgetMonthUSD`, sourced from `Snapshot`, not from any
  `config.Mgmt` field (which no longer exists).
- AC7. Clear error message. A `budget_exhausted` `POST
  /api/v1/gpu/start` response's `message` matches the exact format string
  in Design and contracts, for a fixture cap/spend pair.
- AC8. Audit log. A successful budget change produces one log line
  containing the requesting `keyID`, the old value, and the new value.
- AC9. `vk-llm budget` (no args) and `vk-llm budget set 40` against the
  scripted fake server produce the right request and the right stdout;
  `vk-llm budget set notanumber` exits 2 with zero fake-server calls
  recorded.
- AC10. `gpu-control.yml`'s `budget` action, both the read and set forms,
  against the scripted fake server from CORE-160's harness.
- AC11. `config.Mgmt.BudgetMonthUSD` and `VKLLM_MGMT_BUDGET_MONTH_USD` no
  longer exist anywhere in the Go code or the Helm chart (a repo-wide
  grep in CI or as a one-off check during review — not a runtime test).
- AC12. `/v1/*`'s `backend_unavailable` body is byte-identical before and
  after this spec (a regression test, not a new behavior).

## Definition of done

- [ ] AC1–AC12 pass (tier 1/2).
- [ ] `make specs-check go-build go-test go-lint helm-lint helm-template`
      all exit 0.
- [ ] `grep -r VKLLM_MGMT_BUDGET_MONTH_USD` across the repo returns
      nothing.
- [ ] CORE-110's threat-model table has the new row.
- [ ] ADR 0015 flipped to Accepted in the same PR.
- [ ] Status flipped to DONE; folder renamed `190-D-...`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Exact location of `VKLLM_MGMT_BUDGET_MONTH_USD`'s Helm rendering | this spec's research did not conclusively find it wired to a `values.yaml` key | grep `deploy/helm/gpu-on-demand/templates/` when implemented | remove wherever found; if truly unwired, remove only the Go-side field/env var |
| Whether lowering the budget below current spend drains on the very next tick or only on the next provider-balance refresh | user didn't ask for lowering explicitly, but the CLI/API support it symmetrically | existing `reconcilePruneAndBudget`'s `if known` guard, unchanged by this spec | document the existing cadence in the CLI's output; do not add a new refresh path |
| Whether `vk-llm budget` (no subcommand) duplicates fields `vk-llm status` already prints, or is a strict subset | avoids a confusing two-commands-same-output UX | implementer's judgment, reviewed | print only the two budget-specific rows (cap, month-to-date spend), not the full status table |

## Evidence

None yet.
