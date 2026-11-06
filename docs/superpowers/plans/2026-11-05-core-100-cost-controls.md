# CORE-100: Cost Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the real `internal/budget` package — the ledger bookkeeping and admission gate that CORE-050's already-merged `controller.Budget` interface has been calling through a test fake since it shipped — then retire that fake.

**Architecture:** One small, pure, stateless package (`internal/budget.Checker` plus free helper functions) implementing exactly the 8-method `controller.Budget` interface `internal/controller/budget.go` already declares. No I/O, no clock dependency (every time-sensitive method already takes `now time.Time` from the caller). `internal/controller`'s own `fakeBudget` test double is deleted once the real thing exists, so every existing controller test exercises real ledger math instead of a second, slightly-wrong implementation of the same rules.

**Tech Stack:** Go stdlib only (`math`, `errors`, `time`). Reuses `internal/store.Ledger`/`store.LedgerEntry`/`store.Document` (CORE-040, already shipped) and `internal/provider.Money` (CORE-020, already shipped). No new third-party dependency.

**Spec:** `specs/core/100-D-cost-controls/spec.md`. ADRs: 0003 (lifecycle, `Created` event opens the ledger entry), 0004 (state store, `Ledger`/`LedgerEntry` shape, CAS retry), 0005 (storage/bandwidth cost figures used only in the spec's worked-arithmetic prose), 0010 (explicit provisioning, `backend_unavailable`), 0011 (idle rule, `maxInstanceSpendUSD` default-off semantics — the direct source of this package's `$9.60` reference figure), 0012 (management API contract, `budget_exhausted`/`balance_low` reason strings).

**Why a spec-amendment task comes first:** Spec 100 was written 2026-11-01, before CORE-050 (the GPU lifecycle controller) existed. CORE-050 then shipped and merged its own concrete `controller.Budget` interface (`internal/controller/budget.go`) — one that already answers several design questions the spec guesses differently about: package placement, `Allow`'s exact signature, the admission-denial reason strings, whether `AssertPrice` is a method or a free function, and whether `InstanceSpendExceeded` exists on the interface at all. The shipped interface is the one 8 call sites across `internal/controller` already depend on and cannot be changed without re-touching merged, reviewed, working code for no behavioral gain. Task 1 fixes the spec text to match the shipped reality, exactly as CORE-055's own plan did for its stale spec before implementation began.

**Resolved spec-vs-shipped deltas (verified against the real merged `internal/controller` code, not guessed):**

1. **Package location.** Spec says `internal/controller/budget`. The already-created stub (`internal/budget/doc.go`, doc comment "Package budget will track spend against the monthly cap") and `internal/controller/budget.go`'s own comment ("the future `internal/budget.Checker`") both say `internal/budget`. Shipped wins.
2. **`Allow`'s signature.** Spec: `Allow(doc store.Document, balanceUSD float64) (ok bool, reason string)`. Shipped, already called from `loop.go:gateReason` and `reconcile.go:reconcilePruneAndBudget`: `Allow(doc store.Document, balance provider.Money, known bool) string` — empty string means allow. Shipped wins.
3. **Reason strings.** Spec proposes `"monthly_budget_exceeded"`/`"balance_floor"`. Shipped `isUnavailableReason` (loop.go) and Accepted ADR 0012 §1 both use exactly `"budget_exhausted"`/`"balance_low"` as two of the package's 4-value `Unavailable` enum. Shipped wins. An unknown balance (`known == false`) folds into `"balance_low"` — already the convention CORE-055's plan ledgered for the same reason (ADR 0012's mention of a 5th value, `balance_unknown`, is stale; not reopened, per CLAUDE.md's ADR rule).
4. **`AssertPrice`.** Spec: a free function `AssertPrice(dph, maxHourlyPriceUSD float64) error`. Shipped: a method on the `Budget` interface, called at `saga.go:30` as `c.deps.Budget.AssertPrice(e.offer.Hourly.USD(), c.deps.Config.MaxHourlyPriceUSD)`. Method wins.
5. **`InstanceSpendExceeded` does not exist on the shipped interface.** The per-instance spend cap (`lifecycle.safety.maxInstanceSpendUSD`) is already implemented and already tested inline in `internal/controller/reconcile.go:78-80` (`reconcile_test.go:83-88`), reading `openEntry(doc).EstUSD >= spendCap` directly — it was never delegated to `Budget`. This package does not need the method; R12/AC9 are satisfied by code that already exists and is already tested, not by anything built in this plan.
6. **No clock dependency.** Every time-sensitive method on the interface (`OpenEntry`, `RefreshOpenEntry`, `CloseEntry`, `Rollover`) already takes `now time.Time` as a parameter from the caller. `Allow`, `AssertPrice`, `UsedRatio`, `EstimatedMTD` need no time input at all. `Checker` therefore needs no `clock.Clock` field and no constructor parameter for one — `func New(cfg Config) *Checker`, not `func New(cfg Config, clk clock.Clock) *Checker`. (Ladder rung 1: it doesn't need to exist.)
7. **`UsedRatio` has zero production callers today** (confirmed: no call site in `internal/controller` besides the interface declaration and the test fakes). It is implemented per R11/the spec's own interface contract for a future CORE-090 gauge (`vkllm_budget_used_ratio`); this plan does not add a caller for it, since none exists yet to add.
8. **Two names for one dollar value, left unresolved on purpose.** `internal/config/mgmt.go`'s `config.Mgmt.BudgetMonthUSD` already reads its own env var (`VKLLM_MGMT_BUDGET_MONTH_USD`, default 30) for display in the `/gpu/cost` and `/gpu/status` documents. This plan's `config.Budget.MonthlyUSD` reads a new, different env var (`VKLLM_BUDGET_MONTH_USD`, default 30) for the real admission gate. Both default to the same number today but are two independent reads — a real deployment overriding one without the other would silently desync display from enforcement. This plan does **not** touch `internal/config/mgmt.go` to fix it: `main.go` doesn't exist yet in wired form (see below), and the fix belongs to whoever writes that wiring — they load `config.Budget` once and assign its `MonthlyUSD` into `mgmt.Deps.Config.BudgetMonthUSD` directly, never calling `config.LoadMgmt`'s env read for that one field. Flagged in `docs/open-questions.md` so it isn't lost.

**Explicitly out of scope:**
- `cmd/gpu-on-demand/main.go` wiring. Read in full during planning: it already stubs out `gw`/`mg`/`ctl` construction with comments reading "wait on a real `*controller.Controller` existing" — and `Budget` was the last missing `controller.Deps` input (Provider, Store, Dialer, Worker, WorkerSeed are all real and merged). This plan makes full wiring *possible* for the first time, but wiring itself is a separate deliverable with its own test surface (binary boot, `readyz`, mux mounts) that no spec currently claims, same reasoning CORE-055's plan used to exclude the same file. Noted as an unblocked follow-up in Task 7's PR body, not built here.
- `internal/budget`'s cost-model prose, the Vast.ai billing drift table, and open question Q5 (a possible second `vkllm_cost_provider_usd` series) — these are spec narrative with no executable contract; nothing to implement.
- The four Prometheus metrics (`vkllm_budget_used_ratio`, `vkllm_cost_estimated_usd_total`, `vkllm_cost_instance_usd`, `vkllm_provider_balance_usd`) — CORE-090's job; this package only supplies the pure values they'd be set from.
- `GET /api/v1/gpu/cost`'s HTTP handler — already shipped in CORE-055, already reads `Snapshot.SpendInstanceUSD`/`SpendMTDUSD`/`ProviderBalanceUSD`, which this plan does not change.

## Global Constraints

- Standard library only: `math`, `errors`, `time`. No new third-party module (`go.mod` already fixed to `prometheus/client_golang`, `golang.org/x/crypto`).
- `internal/budget` MUST NOT import `internal/controller` in non-test code (avoids an import cycle: `main.go`, not `internal/controller`, will wire the two together). A compile-time interface assertion (`var _ controller.Budget = (*Checker)(nil)`) is fine in a `_test.go` file, where the cycle doesn't apply.
- `internal/budget` MUST NOT call `Provider`, `Store`, or any I/O. Every method takes the document/values it needs as parameters and returns values (R10, C-26, C-27).
- No metric, no log field, and no error in this package carries an instance id, a raw provider body, a prompt, or a key (C-16, C-34).
- Round elapsed time **up** to the next whole second before computing `dph × seconds / 3600` — never understate a deny-gate input (R4).
- `ledger.Entries` MUST NOT exceed 100; fold-on-overflow only ever removes **closed** entries (`End != nil`), never the entry a `CloseEntry` call just closed (R5, R8).
- `Allow` checks the monthly cap before the balance floor, and returns on the first failure — never both reasons at once (R1, R2).
- Comments ≤3 lines, never name a spec/ADR/requirement by number (CLAUDE.md).
- Attribution footer on every commit (session rule).

## Design decisions this plan makes

1. Spec amendment lands first (Task 1), fixing the 8 deltas listed above against the already-shipped `controller.Budget` interface.
2. `Checker` takes no clock — every time-sensitive method already receives `now time.Time` from its caller.
3. `internal/controller`'s existing `fakeBudget` (in `fakes_test.go`) is deleted once the real `budget.Checker` exists, and every one of its 10 call sites across `decide_test.go`, `saga_test.go`, and `sim_test.go` is switched to the real implementation — verified safe because no controller test outside `fakeBudget`'s own now-deleted unit test (`fakes_test.go:16-65`) asserts on `fakeBudget`-specific behavior (confirmed by grep: no other test file references `Rollover` or inspects `Ledger.Entries` in a way that depends on the fake's — incorrect — "drop the open entry on rollover" behavior).
4. `config.Budget` is a new file following the existing per-domain-loader pattern in `internal/config` (`LoadController`, `LoadMgmt`). It duplicates reading `VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD` (same env var `config.Controller` already reads) for `Allow`'s balance-floor multiplier — same var, same default, no drift risk, unlike the `BudgetMonthUSD` situation in decision 7 above.

---

### Task 1: Spec amendment — close the deltas, flip DRAFT→READY

**Files:**
- Modify: `specs/core/100-D-cost-controls/spec.md`
- Modify: `docs/open-questions.md`

- [ ] **Step 1:** In spec 100's "Design and contracts" section, replace the proposed `internal/controller/budget` package path with `internal/budget` everywhere it appears.
- [ ] **Step 2:** Replace the `Checker.Allow` signature block with the shipped one:
  ```go
  // Allow returns "" when a new provisioning attempt may proceed, or one
  // of "budget_exhausted"/"balance_low" otherwise. An unknown balance
  // (known == false) is treated as "balance_low", never a separate value.
  func (c *Checker) Allow(doc store.Document, balance provider.Money, known bool) string
  ```
  Update the `Allow` check-order table's `reason` column to `"budget_exhausted"` (was `"monthly_budget_exceeded"`) and `"balance_low"` (was `"balance_floor"`). Add one row above the table: "An unknown balance folds into `balance_low` — matches ADR 0012 §1's enum; the ADR's `balance_unknown` value is stale and is not reopened (CORE-055's own ledger already noted this fold)."
- [ ] **Step 3:** Replace the `AssertPrice` block — delete the free-function signature, replace with:
  ```go
  // AssertPrice returns ErrPriceTooHigh when dph exceeds max; equal is allowed.
  func (c *Checker) AssertPrice(hourlyUSD, capUSD float64) error
  ```
- [ ] **Step 4:** Delete the `InstanceSpendExceeded` function signature and its Config field. Replace the "`InstanceSpendExceeded` — per-instance cap" subsection with: "Already implemented and tested inline in `internal/controller/reconcile.go` (`reconcile_test.go`), reading the open ledger entry's `EstUSD` directly against `lifecycle.safety.maxInstanceSpendUSD` — never delegated to this package. R12/AC9 are satisfied by that existing code, not by anything this spec's own package builds." Update R12 and AC9's text to point at `internal/controller/reconcile.go` instead of `Checker.InstanceSpendExceeded`.
- [ ] **Step 5:** Delete `clk clock.Clock` from the `New` constructor signature shown in the spec: `func New(cfg Config) *Checker`. Add one sentence: "No clock dependency — every time-sensitive method takes `now time.Time` from its caller."
- [ ] **Step 6:** In `docs/open-questions.md`, add one new row under "Known spec inconsistencies": CORE-100's `config.Budget.MonthlyUSD` (env `VKLLM_BUDGET_MONTH_USD`) and `config.Mgmt.BudgetMonthUSD` (env `VKLLM_MGMT_BUDGET_MONTH_USD`) read independent env vars for the same dollar figure; settled by whoever wires `cmd/gpu-on-demand/main.go`, who must assign the former into the latter rather than letting both read their own env var; default behavior until then: both default to 30, so they agree unless one is overridden without the other.
- [ ] **Step 7:** Flip spec 100's front-matter `status: "DRAFT"` to `status: "READY"`. Folder stays `100-P-...` (both map to the `P` letter per `specs/README.md`).
- [ ] **Step 8:** `make specs-check`.
- [ ] **Step 9:** `git add specs/core/100-D-cost-controls/spec.md docs/open-questions.md && git commit -m "docs: CORE-100 spec amendment — match the shipped controller.Budget interface"`.

---

### Task 2: `internal/budget` — ledger primitives

**Files:**
- Create: `internal/budget/ledger.go`
- Create: `internal/budget/ledger_test.go`

**Interfaces:**
- Consumes: `internal/store.Document`, `store.Ledger`, `store.LedgerEntry` (all already defined, no changes).
- Produces: `(*Checker) OpenEntry`, `(*Checker) RefreshOpenEntry`, `(*Checker) CloseEntry`, `(*Checker) Rollover`, and the package-level `MaxLedgerEntries` constant — all four methods are part of the `controller.Budget` interface `internal/controller/budget.go` already declares.
- Consumed by: Task 3 (`EstimatedMTD` reuses the same `roundUpSeconds` helper), Task 5 (replaces `fakeBudget`'s equivalents in `internal/controller`'s own tests).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/budget/ledger_test.go
package budget

import (
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func TestRoundUpToSecond(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{Entries: []store.LedgerEntry{{Gen: 1, Start: t0, DPH: 0.71}}}}
	c := New(Config{})

	c.RefreshOpenEntry(&doc, t0.Add(89600*time.Millisecond)) // 89.6s
	got1 := doc.Ledger.Entries[0].EstUSD
	c.RefreshOpenEntry(&doc, t0.Add(90*time.Second)) // 90.0s
	got2 := doc.Ledger.Entries[0].EstUSD

	want := 0.71 * 90 / 3600
	if got1 != want || got2 != want {
		t.Fatalf("EstUSD at 89.6s=%v 90.0s=%v, want both %v (round up to 90 whole seconds)", got1, got2, want)
	}
}

func TestOpenRefreshClose(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	doc := store.Document{}
	c := New(Config{})

	c.OpenEntry(&doc, t0, 7, 0.60)
	if len(doc.Ledger.Entries) != 1 || doc.Ledger.Entries[0].Gen != 7 || doc.Ledger.Entries[0].End != nil {
		t.Fatalf("OpenEntry: Entries = %+v", doc.Ledger.Entries)
	}

	c.RefreshOpenEntry(&doc, t0.Add(time.Hour))
	if doc.Ledger.Entries[0].EstUSD != 0.60 {
		t.Fatalf("RefreshOpenEntry after 1h: EstUSD = %v, want 0.60", doc.Ledger.Entries[0].EstUSD)
	}

	c.CloseEntry(&doc, t0.Add(2*time.Hour), 7)
	e := doc.Ledger.Entries[0]
	if e.End == nil || !e.End.Equal(t0.Add(2*time.Hour)) || e.EstUSD != 1.20 {
		t.Fatalf("CloseEntry after 2h: End=%v EstUSD=%v, want 1.20", e.End, e.EstUSD)
	}
}

func TestCloseEntryDoesNothingForUnknownGen(t *testing.T) {
	doc := store.Document{Ledger: store.Ledger{Entries: []store.LedgerEntry{{Gen: 1, Start: time.Now(), DPH: 0.5}}}}
	c := New(Config{})
	c.CloseEntry(&doc, time.Now(), 99) // no entry with Gen 99
	if doc.Ledger.Entries[0].End != nil {
		t.Fatalf("CloseEntry(unknown gen) mutated the existing entry: %+v", doc.Ledger.Entries[0])
	}
}

func TestFoldOnOverflow(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	c := New(Config{})
	doc := store.Document{Ledger: store.Ledger{Month: "2026-11"}}

	for gen := int64(1); gen <= 100; gen++ {
		start := t0.Add(time.Duration(gen) * time.Hour)
		doc.Ledger.Entries = append(doc.Ledger.Entries, store.LedgerEntry{
			Gen: gen, Start: start, End: &start, DPH: 1.0, EstUSD: 0.01,
		})
	}
	wantFoldedUSD := doc.Ledger.Entries[0].EstUSD // the oldest closed entry

	c.OpenEntry(&doc, t0.Add(101*time.Hour), 101, 0.71)
	c.CloseEntry(&doc, t0.Add(101*time.Hour+time.Minute), 101)

	if len(doc.Ledger.Entries) != 100 {
		t.Fatalf("len(Entries) = %d, want 100 after folding", len(doc.Ledger.Entries))
	}
	if doc.Ledger.TotalUSD != wantFoldedUSD {
		t.Fatalf("TotalUSD = %v, want %v (the oldest closed entry's EstUSD folded in)", doc.Ledger.TotalUSD, wantFoldedUSD)
	}
	last := doc.Ledger.Entries[len(doc.Ledger.Entries)-1]
	if last.Gen != 101 || last.End == nil {
		t.Fatalf("just-closed entry (gen 101) missing or still open: %+v", last)
	}
	for _, e := range doc.Ledger.Entries {
		if e.Gen == 1 {
			t.Fatal("oldest entry (gen 1) should have been folded away, found it still present")
		}
	}
}

func TestRolloverDropsClosedEntries(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	closedStart := now.Add(-time.Hour)
	doc := store.Document{Ledger: store.Ledger{
		Month: "2026-11", TotalUSD: 12.34,
		Entries: []store.LedgerEntry{{Gen: 4, Start: closedStart, End: &closedStart, DPH: 0.5, EstUSD: 0.50}},
	}}
	c := New(Config{})
	c.Rollover(&doc, now)

	if doc.Ledger.Month != "2026-12" || doc.Ledger.TotalUSD != 0 {
		t.Fatalf("Rollover = %+v, want Month=2026-12 TotalUSD=0", doc.Ledger)
	}
	if len(doc.Ledger.Entries) != 0 {
		t.Fatalf("Entries = %+v, want empty (closed entry dropped)", doc.Ledger.Entries)
	}
}

func TestRolloverReopensRunningEntry(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	oldStart := time.Date(2026, 11, 30, 20, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{
		Month: "2026-11", TotalUSD: 12.34,
		Entries: []store.LedgerEntry{{Gen: 5, Start: oldStart, DPH: 0.71}}, // End == nil: still open
	}}
	c := New(Config{})
	c.Rollover(&doc, now)

	if doc.Ledger.Month != "2026-12" || doc.Ledger.TotalUSD != 0 {
		t.Fatalf("Rollover = %+v, want Month=2026-12 TotalUSD=0", doc.Ledger)
	}
	if len(doc.Ledger.Entries) != 1 {
		t.Fatalf("Entries = %+v, want exactly one (the still-open entry, re-opened)", doc.Ledger.Entries)
	}
	e := doc.Ledger.Entries[0]
	if e.Gen != 5 || e.DPH != 0.71 || e.End != nil || !e.Start.Equal(now) || e.EstUSD != 0 {
		t.Fatalf("re-opened entry = %+v, want Gen=5 DPH=0.71 End=nil Start=%v EstUSD=0", e, now)
	}
}

func TestRolloverNoOpWithinSameMonth(t *testing.T) {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	doc := store.Document{Ledger: store.Ledger{Month: "2026-11", TotalUSD: 5}}
	c := New(Config{})
	c.Rollover(&doc, now)
	if doc.Ledger.TotalUSD != 5 {
		t.Fatalf("Rollover within the same month changed TotalUSD to %v, want unchanged 5", doc.Ledger.TotalUSD)
	}
}
```

- [ ] **Step 2:** `go test ./internal/budget/... -v` — compile failure, nothing defined.
- [ ] **Step 3: Implement.**

```go
// internal/budget/ledger.go
package budget

import (
	"math"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

// MaxLedgerEntries bounds the ConfigMap document's size; with one
// instance at a time this is months of history, not a real limit.
const MaxLedgerEntries = 100

// estUSD rounds elapsed time up to the next whole second — never
// understate a deny-gate input — before applying the hourly rate.
func estUSD(dph float64, start, now time.Time) float64 {
	seconds := math.Ceil(now.Sub(start).Seconds())
	return dph * seconds / 3600
}

func (c *Checker) OpenEntry(doc *store.Document, now time.Time, gen int64, dphUSD float64) {
	doc.Ledger.Entries = append(doc.Ledger.Entries, store.LedgerEntry{Gen: gen, Start: now, DPH: dphUSD})
}

func (c *Checker) RefreshOpenEntry(doc *store.Document, now time.Time) {
	for i := range doc.Ledger.Entries {
		e := &doc.Ledger.Entries[i]
		if e.End == nil {
			e.EstUSD = estUSD(e.DPH, e.Start, now)
		}
	}
}

func (c *Checker) CloseEntry(doc *store.Document, now time.Time, gen int64) {
	for i := range doc.Ledger.Entries {
		e := &doc.Ledger.Entries[i]
		if e.Gen == gen && e.End == nil {
			e.EstUSD = estUSD(e.DPH, e.Start, now)
			t := now
			e.End = &t
			break
		}
	}
	foldOverflow(doc)
}

// foldOverflow removes the oldest closed entries once the list exceeds
// MaxLedgerEntries, adding each one's EstUSD into TotalUSD first. It
// never removes an open entry.
func foldOverflow(doc *store.Document) {
	for len(doc.Ledger.Entries) > MaxLedgerEntries {
		idx := -1
		for i, e := range doc.Ledger.Entries {
			if e.End != nil {
				idx = i
				break
			}
		}
		if idx == -1 {
			return
		}
		doc.Ledger.TotalUSD += doc.Ledger.Entries[idx].EstUSD
		doc.Ledger.Entries = append(doc.Ledger.Entries[:idx], doc.Ledger.Entries[idx+1:]...)
	}
}

// Rollover fires at most once per call, when now's UTC month differs
// from the stored one. A still-open entry survives the boundary with a
// fresh Start — its pre-rollover partial cost is dropped, matching the
// metric counter's own rule of never decreasing.
func (c *Checker) Rollover(doc *store.Document, now time.Time) {
	month := now.UTC().Format("2006-01")
	if doc.Ledger.Month == month {
		return
	}
	var stillOpen []store.LedgerEntry
	for _, e := range doc.Ledger.Entries {
		if e.End == nil {
			stillOpen = append(stillOpen, store.LedgerEntry{Gen: e.Gen, Start: now, DPH: e.DPH})
		}
	}
	doc.Ledger.Month = month
	doc.Ledger.TotalUSD = 0
	doc.Ledger.Entries = stillOpen
}
```

- [ ] **Step 4:** `go test ./internal/budget/... -race -v` — compile failure (`Checker`/`Config`/`New` don't exist yet — Task 3 defines them). Add a minimal `checker.go` stub now with just the type and constructor so Task 2's tests can compile and pass on their own:

```go
// internal/budget/checker.go (grown further in Task 3)
package budget

// Config holds every value this package needs; none are hard-coded.
type Config struct {
	MonthlyUSD        float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

type Checker struct {
	cfg Config
}

// New builds a Checker. No clock — every time-sensitive method takes
// now as a parameter from its caller.
func New(cfg Config) *Checker {
	return &Checker{cfg: cfg}
}
```

- [ ] **Step 5:** `go test ./internal/budget/... -race -v` — all pass. `go vet ./internal/budget/...`.
- [ ] **Step 6:** `git add internal/budget/ledger.go internal/budget/checker.go internal/budget/ledger_test.go && git commit -m "feat: budget ledger primitives — open/refresh/close, fold-on-overflow, rollover"`.

---

### Task 3: `internal/budget` — `Checker.Allow`, `UsedRatio`, `EstimatedMTD`, `AssertPrice`

**Files:**
- Modify: `internal/budget/checker.go`
- Create: `internal/budget/checker_test.go`
- Create: `internal/budget/interface_test.go`

**Interfaces:**
- Consumes: `internal/provider.Money` (already has a `.USD() float64` method, confirmed shipped), `internal/controller.Budget` (for the compile-time assertion only, in a test file).
- Produces: `(*Checker) Allow`, `(*Checker) UsedRatio`, `(*Checker) EstimatedMTD`, `(*Checker) AssertPrice`, `ErrPriceTooHigh` — completing the `controller.Budget` interface.
- Consumed by: Task 5 (replaces every `fakeBudget` call site in `internal/controller`'s own tests).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/budget/checker_test.go
package budget

import (
	"errors"
	"testing"
	"time"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
	"github.com/savak1990/vk-llm-on-demand/internal/store"
)

func cfg() Config {
	return Config{MonthlyUSD: 30, WarnFraction: 0.8, MaxHourlyPriceUSD: 0.80}
}

func TestAllowMonthlyCap(t *testing.T) {
	c := New(cfg())
	doc := store.Document{Ledger: store.Ledger{
		TotalUSD: 29.90,
		Entries:  []store.LedgerEntry{{Gen: 1, EstUSD: 0.15}}, // open: End == nil
	}}
	if reason := c.Allow(doc, provider.Money(100_000_000), true); reason != "budget_exhausted" {
		t.Fatalf("Allow = %q, want budget_exhausted (EstimatedMTD 30.05 >= 30)", reason)
	}
}

func TestAllowBalanceFloor(t *testing.T) {
	c := New(cfg()) // floor = 2 * 0.80 = 1.60
	doc := store.Document{}
	if reason := c.Allow(doc, provider.Money(1_590_000), true); reason != "balance_low" {
		t.Fatalf("Allow(balance=1.59) = %q, want balance_low", reason)
	}
	if reason := c.Allow(doc, provider.Money(1_600_000), true); reason != "" {
		t.Fatalf("Allow(balance=1.60) = %q, want \"\" (floor is inclusive)", reason)
	}
}

func TestAllowUnknownBalanceFoldsIntoBalanceLow(t *testing.T) {
	c := New(cfg())
	if reason := c.Allow(store.Document{}, provider.Money(0), false); reason != "balance_low" {
		t.Fatalf("Allow(known=false) = %q, want balance_low", reason)
	}
}

func TestAllowOrderMonthlyBeforeBalance(t *testing.T) {
	c := New(cfg())
	doc := store.Document{Ledger: store.Ledger{TotalUSD: 30}} // both monthly cap and balance floor would fail
	if reason := c.Allow(doc, provider.Money(0), false); reason != "budget_exhausted" {
		t.Fatalf("Allow (both would fail) = %q, want budget_exhausted only", reason)
	}
}

func TestUsedRatio(t *testing.T) {
	c := New(cfg())
	doc := store.Document{Ledger: store.Ledger{TotalUSD: 15}}
	if got := c.UsedRatio(doc); got != 0.5 {
		t.Fatalf("UsedRatio = %v, want 0.5", got)
	}
}

func TestUsedRatioZeroMonthlyCap(t *testing.T) {
	c := New(Config{MonthlyUSD: 0})
	if got := c.UsedRatio(store.Document{Ledger: store.Ledger{TotalUSD: 5}}); got != 0 {
		t.Fatalf("UsedRatio with MonthlyUSD=0 = %v, want 0 (no division by zero)", got)
	}
}

func TestEstimatedMTD(t *testing.T) {
	c := New(cfg())
	closedEnd := time.Now()
	doc := store.Document{Ledger: store.Ledger{
		TotalUSD: 10,
		Entries: []store.LedgerEntry{
			{Gen: 1, End: &closedEnd, EstUSD: 1.0}, // closed: already folded into TotalUSD, must not double-count
			{Gen: 2, EstUSD: 0.25},                 // open
		},
	}}
	if got := c.EstimatedMTD(doc); got != 10.25 {
		t.Fatalf("EstimatedMTD = %v, want 10.25 (TotalUSD + open entries only)", got)
	}
}

func TestAssertPriceBoundary(t *testing.T) {
	c := New(cfg())
	if err := c.AssertPrice(0.80001, 0.80); !errors.Is(err, ErrPriceTooHigh) {
		t.Fatalf("AssertPrice(0.80001, 0.80) = %v, want ErrPriceTooHigh", err)
	}
	if err := c.AssertPrice(0.80, 0.80); err != nil {
		t.Fatalf("AssertPrice(0.80, 0.80) = %v, want nil (equal is allowed)", err)
	}
}
```

```go
// internal/budget/interface_test.go
package budget

import "github.com/savak1990/vk-llm-on-demand/internal/controller"

var _ controller.Budget = (*Checker)(nil)
```

- [ ] **Step 2:** `go test ./internal/budget/... -v` — compile failure, nothing defined.
- [ ] **Step 3: Implement**, appending to `internal/budget/checker.go`:

```go
// (append to internal/budget/checker.go)

import (
	"errors"

	"github.com/savak1990/vk-llm-on-demand/internal/provider"
)

var ErrPriceTooHigh = errors.New("budget: offer price above lifecycle.safety.maxHourlyPriceUSD")

// Allow checks the monthly cap before the balance floor and returns on
// the first failure — never both reasons at once.
func (c *Checker) Allow(doc store.Document, balance provider.Money, known bool) string {
	if c.EstimatedMTD(doc) >= c.cfg.MonthlyUSD {
		return "budget_exhausted"
	}
	if !known || balance.USD() < 2*c.cfg.MaxHourlyPriceUSD {
		return "balance_low"
	}
	return ""
}

// UsedRatio never denies by itself; it exists for a future metric.
func (c *Checker) UsedRatio(doc store.Document) float64 {
	if c.cfg.MonthlyUSD == 0 {
		return 0
	}
	return c.EstimatedMTD(doc) / c.cfg.MonthlyUSD
}

// EstimatedMTD is the closed-entries total plus every open entry's
// current estimate; a closed entry's EstUSD is never added twice.
func (c *Checker) EstimatedMTD(doc store.Document) float64 {
	total := doc.Ledger.TotalUSD
	for _, e := range doc.Ledger.Entries {
		if e.End == nil {
			total += e.EstUSD
		}
	}
	return total
}

// AssertPrice re-checks the price cap immediately before CreateInstance,
// closing the race between a search-time offer and the matching create.
func (c *Checker) AssertPrice(hourlyUSD, capUSD float64) error {
	if hourlyUSD > capUSD {
		return ErrPriceTooHigh
	}
	return nil
}
```

  Task 2's `checker.go` stub has no imports (`Config`/`Checker`/`New` need none). This step adds `checker.go`'s first import block: `"errors"`, `"github.com/savak1990/vk-llm-on-demand/internal/provider"`, `"github.com/savak1990/vk-llm-on-demand/internal/store"`.

- [ ] **Step 4:** `go test ./internal/budget/... -race -v` — all pass. `go vet ./internal/budget/...`.
- [ ] **Step 5:** `git add internal/budget/checker.go internal/budget/checker_test.go internal/budget/interface_test.go && git commit -m "feat: budget Checker — Allow, UsedRatio, EstimatedMTD, AssertPrice"`.

---

### Task 4: `internal/config.Budget`

**Files:**
- Create: `internal/config/budget.go`
- Create: `internal/config/budget_test.go`

**Interfaces:**
- Produces: `config.Budget`, `config.LoadBudget() (Budget, error)`.
- Consumed by: Task 5's test wiring, and eventually `cmd/gpu-on-demand/main.go` (not built in this plan).

- [ ] **Step 1: Write the failing tests.**

```go
// internal/config/budget_test.go
package config

import "testing"

func clearBudgetEnv(t *testing.T) {
	for _, k := range []string{
		"VKLLM_BUDGET_MONTH_USD", "VKLLM_BUDGET_WARN_FRACTION",
		"VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadBudgetDefaults(t *testing.T) {
	clearBudgetEnv(t)
	b, err := LoadBudget()
	if err != nil {
		t.Fatalf("LoadBudget: %v", err)
	}
	if b.MonthlyUSD != 30 {
		t.Errorf("MonthlyUSD = %v, want 30", b.MonthlyUSD)
	}
	if b.WarnFraction != 0.8 {
		t.Errorf("WarnFraction = %v, want 0.8", b.WarnFraction)
	}
	if b.MaxHourlyPriceUSD != 0.80 {
		t.Errorf("MaxHourlyPriceUSD = %v, want 0.80", b.MaxHourlyPriceUSD)
	}
}

func TestLoadBudgetOverrides(t *testing.T) {
	clearBudgetEnv(t)
	t.Setenv("VKLLM_BUDGET_MONTH_USD", "45.5")
	b, err := LoadBudget()
	if err != nil {
		t.Fatalf("LoadBudget: %v", err)
	}
	if b.MonthlyUSD != 45.5 {
		t.Errorf("MonthlyUSD = %v, want 45.5", b.MonthlyUSD)
	}
}

func TestLoadBudgetMalformedFloat(t *testing.T) {
	clearBudgetEnv(t)
	t.Setenv("VKLLM_BUDGET_MONTH_USD", "not-a-number")
	if _, err := LoadBudget(); err == nil {
		t.Fatal("LoadBudget: want error for malformed float, got nil")
	}
}
```

- [ ] **Step 2:** `go test ./internal/config/... -run TestLoadBudget -v` — compile failure.
- [ ] **Step 3: Implement**, reusing the existing `envFloat` helper already in `internal/config/controller.go`:

```go
// internal/config/budget.go
package config

// Budget is internal/budget's own env-parsed configuration.
type Budget struct {
	MonthlyUSD        float64
	WarnFraction      float64
	MaxHourlyPriceUSD float64
}

// LoadBudget reads internal/budget's configuration. MaxHourlyPriceUSD
// reads the same env var as config.Controller's own field — the balance
// floor in Allow needs the same cap value, not a second copy of it.
func LoadBudget() (Budget, error) {
	var b Budget
	var err error

	if b.MonthlyUSD, err = envFloat("VKLLM_BUDGET_MONTH_USD", 30); err != nil {
		return Budget{}, err
	}
	if b.WarnFraction, err = envFloat("VKLLM_BUDGET_WARN_FRACTION", 0.8); err != nil {
		return Budget{}, err
	}
	if b.MaxHourlyPriceUSD, err = envFloat("VKLLM_LIFECYCLE_SAFETY_MAX_HOURLY_PRICE_USD", 0.80); err != nil {
		return Budget{}, err
	}
	return b, nil
}
```

- [ ] **Step 4:** `go test ./internal/config/... -race -v` — all pass (existing `Load`/`LoadController`/`LoadMgmt` tests plus these).
- [ ] **Step 5:** `git add internal/config/budget.go internal/config/budget_test.go && git commit -m "feat: config.LoadBudget for the real budget package"`.

---

### Task 5: Replace `internal/controller`'s `fakeBudget` with the real `budget.Checker`

**Files:**
- Modify: `internal/controller/fakes_test.go` (delete `fakeBudget`, `newFakeBudget`, and their own unit test)
- Modify: `internal/controller/decide_test.go` (6 call sites)
- Modify: `internal/controller/saga_test.go` (1 call site)
- Modify: `internal/controller/sim_test.go` (1 call site)

**Interfaces:**
- Consumes: `internal/budget.New`, `budget.Config` (Task 3/4).
- Produces: nothing new — this task only removes a second, slightly-wrong ledger implementation and points every existing test at the real one.

This is the one task in this plan that touches the already-shipped, already-reviewed `internal/controller` package — test files only, no production code in that package changes. Confirmed safe by grep before this plan was written: no test outside `fakeBudget`'s own unit test (deleted by this task) inspects `Rollover`'s or `Ledger.Entries`' behavior in a way that depends on the fake's specific (and, per Task 2, incorrect) choice to drop a still-open entry on rollover instead of re-opening it.

- [ ] **Step 1:** In `internal/controller/fakes_test.go`, delete the `fakeBudget` type, `newFakeBudget`, every method on `fakeBudget`, the `var _ Budget = (*fakeBudget)(nil)` assertion, and the three tests that exercise it directly (`TestFakeBudget...` — read the file first to get their exact names; they are the three `newFakeBudget(1.60, 30)` call sites at lines 16/35/45 reported during planning). Leave every other fake in that file (`fakeProvider`, `fakeWorker`, etc.) untouched.
- [ ] **Step 2:** Add the import `"github.com/savak1990/vk-llm-on-demand/internal/budget"` to `decide_test.go`, `saga_test.go`, and `sim_test.go` (whichever don't already have it).
- [ ] **Step 3:** Replace every remaining `newFakeBudget(floorUSD, monthlyCapUSD)` call site with `budget.New(budget.Config{MonthlyUSD: monthlyCapUSD, MaxHourlyPriceUSD: floorUSD / 2})` — the fake's `floorUSD` constructor argument was always `2 × maxHourlyPriceUSD` (confirmed: every existing call site uses `1.60`/`30` or `1, 30`, i.e. `floorUSD = 2 × 0.80` and `floorUSD = 2 × 0.50` respectively), so this is an exact behavioral match:
  - `decide_test.go`: 6 sites, all `newFakeBudget(1, 30)` → `budget.New(budget.Config{MonthlyUSD: 30, MaxHourlyPriceUSD: 0.5})`.
  - `saga_test.go`: 1 site, `newFakeBudget(1.60, 30)` → `budget.New(budget.Config{MonthlyUSD: 30, MaxHourlyPriceUSD: 0.80})`.
  - `sim_test.go`: 1 site, `newFakeBudget(1.60, 30)` → `budget.New(budget.Config{MonthlyUSD: 30, MaxHourlyPriceUSD: 0.80})`.
- [ ] **Step 4:** `go build ./internal/controller/...` — fix any remaining reference to the deleted `fakeBudget` type the build surfaces.
- [ ] **Step 5:** `go test ./internal/controller/... -race -count=1 -v` — full package green, including every scenario that previously ran against `fakeBudget` (budget-exhaustion-triggers-drain scenarios in `decide_test.go`'s T22/T8/T11/T23/T27/T29 cases, the saga's `AssertPrice` rejection path, and the simulation harness's month-long run) now exercising the real `budget.Checker`.
- [ ] **Step 6:** Run the full suite twice to rule out flakes: `go test ./internal/controller/... -race -count=2`.
- [ ] **Step 7:** `git add internal/controller/fakes_test.go internal/controller/decide_test.go internal/controller/saga_test.go internal/controller/sim_test.go && git commit -m "test: internal/controller — replace fakeBudget with the real budget.Checker"`.

---

### Task 6: Write-back

**Files:**
- Modify: `specs/core/100-D-cost-controls/spec.md` → `specs/core/100-D-cost-controls/spec.md` (status DONE)
- Modify: `specs/README.md` (path + letter)
- Modify: any spec whose cross-reference still points at `100-P-cost-controls`

- [ ] **Step 1:** Fill spec 100's Evidence section: the real `go test ./internal/budget/... ./internal/config/... ./internal/controller/... -race -count=1` runtime, confirmation all 9 ACs pass (AC9 pointing at `internal/controller/reconcile_test.go` per Task 1's amendment), and the one `internal/controller` touch (Task 5) named as a test-only merged-package change, same category as CORE-055's own precedent.
- [ ] **Step 2:** Check every box in spec 100's Definition of done.
- [ ] **Step 3:** `git mv specs/core/100-D-cost-controls specs/core/100-D-cost-controls`; flip front-matter `status: "DONE"`.
- [ ] **Step 4:** `grep -rn '100-P-cost-controls'` across the repo; fix every stale cross-reference.
- [ ] **Step 5:** `grep -rEn '([0-9]{1,3}\.){3}[0-9]{1,3}' specs/core/100-D-cost-controls/spec.md` — leak check (standing rule); this spec's tests are all-fake/local, nothing real to leak.
- [ ] **Step 6:** `make specs-check`.
- [ ] **Step 7:** `git add -A && git commit -m "docs: CORE-100 cost controls DONE"`.

---

### Task 7: Validation and PR

- [ ] **Step 1:** `make specs-check go-build go-test go-lint` — all pass.
- [ ] **Step 2:** `go test ./internal/budget/... ./internal/config/... ./internal/controller/... -race -count=2 -v` — all green, no flakes.
- [ ] **Step 3:** `go list -deps ./internal/budget/...` — confirm no new third-party module.
- [ ] **Step 4:** Open the PR against `main`, body ending with the required attribution lines. Note explicitly: (a) Task 5's test-only `internal/controller` touch, so reviewers know exactly what changed in the already-shipped package and why; (b) that `cmd/gpu-on-demand/main.go` wiring is now unblocked for the first time (every `controller.Deps` input is real) but is intentionally not done in this PR; (c) the `config.Budget.MonthlyUSD` / `config.Mgmt.BudgetMonthUSD` duplicate-env-var note from `docs/open-questions.md`, for whoever picks up the wiring next.

---

## Verification (end to end)

1. `go test ./internal/budget/... ./internal/config/... ./internal/controller/... -race -count=2` green, no flakes.
2. Every AC1-AC9 in the amended spec is traceable to a test: AC1-AC8 to `internal/budget`'s own tests, AC9 to `internal/controller/reconcile_test.go` (already existed before this plan).
3. `specs/core/100-D-cost-controls/spec.md` exists, old path gone, `make specs-check` passes.
4. `go list -deps ./internal/budget/...` contains no new third-party module.
5. `internal/controller`'s `fakeBudget` no longer exists anywhere in the repo (`grep -rn fakeBudget internal/` returns nothing).
