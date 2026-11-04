# CORE-055 Write-back Report - Task 11

## Test Results

### Actual Test Runtime

```
$ go test ./internal/auth/... ./internal/mgmt/... -race -count=1 -v

ok  	github.com/savak1990/vk-llm-on-demand/internal/auth	1.156s
ok  	github.com/savak1990/vk-llm-on-demand/internal/mgmt	1.429s
```

**Total wall-clock: 2.6 seconds** (`auth` 1.156 s + `mgmt` 1.429 s)

### Acceptance Criteria Verification

All 12 AC test functions exist and pass:

- `TestAC5_IdleDeadlineUntouchedAcross210Calls` ✓
- `TestAC6_RealControllerThroughMgmt` ✓
- `TestAC10_KeyReloadThroughFullServer` ✓
- `TestAC11_StatusAndCostFullSnapshotMapping` ✓
- `TestAC12_RedactionAcrossAllRoutes` ✓

Plus support tests covering AC1-4, AC7-9:
- `TestStartReadyReturns200AlreadyRunning` (AC1)
- `TestStartAllocatingReturns202Accepted` (AC1)
- `TestStartDenialReturns503WithReason` (AC2)
- `TestStopReadyReturns202Draining` (AC1)
- `TestStatusAndCost200` (AC1)
- `TestUnknownPathAnd405` (AC1)
- `TestInvalidJSONBody` (AC1)
- `TestForceNotEnabled` (AC9)
- `TestStartCoalescesConcurrentCalls` (AC4)
- `TestAuthMatrix` (AC3)
- `TestRateLimit` (AC7)
- `TestBodyLimit` (AC8)
- Plus observability and documents tests

**Result: All pass with `-race` flag; no data races detected.**

### Merged-Package Touch

`internal/controller.Snapshot` gained `ProviderBalanceUSD *float64` field to
carry the provider's account balance for inclusion in the cost document
(FR-6.8). This is a merged-package touch in the same category as CORE-050's
`internal/clock/fake.NextDeadline()` addition.

## Specification Work

### Steps Completed

- [x] **Step 1:** Evidence section filled with:
  - Real `go test` runtime (1.156s auth + 1.429s mgmt)
  - Confirmation all 12 ACs pass
  - `controller.Snapshot.ProviderBalanceUSD` field addition named as merged-package touch
  
- [x] **Step 2:** All boxes in Definition of Done checked
  
- [x] **Step 3:** Folder renamed: `specs/core/055-P-management-api` → `specs/core/055-D-management-api`
  - Status flipped from `"READY"` to `"DONE"` in front-matter
  - Updated timestamp to 2026-11-04
  
- [x] **Step 4:** Stale cross-references grep results:
  ```
  Before: 4 stale references found
  After grep -rn '055-P-management-api': 0 results
  
  Updated files:
  - specs/README.md (1 reference)
  - specs/core/150-P-vk-llm-cli/spec.md (1 reference)
  - specs/core/140-P-e2e-acceptance/spec.md (1 reference)
  - specs/core/160-P-github-actions-remote-control/spec.md (1 reference)
  ```
  
- [x] **Step 5:** IP leak check:
  ```
  grep -En '([0-9]{1,3}\.){3}[0-9]{1,3}' specs/core/055-D-management-api/spec.md
  (no results - clean)
  ```
  
- [x] **Step 6:** `make specs-check` result:
  ```
  SPECS-CHECK: specs/ layout is valid.
  ```
  
- [x] **Step 7:** Committed with message:
  ```
  docs: CORE-055 management API DONE
  
  Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01GiCsYKEsCUyCWNa8k9xGKE
  ```

### Known Limitations Added

Added three known limitations to spec 055, following CORE-050's pattern:

1. **AC5 cost-body byte-identity check** — uses stronger substitute (all cost
   bodies byte-identical to each other + direct controller state check) instead
   of literal field-in-all-bodies check

2. **`reason` field in structured log** — populated only for 503
   `gpu_unavailable` case; threading through all error paths not required by
   spec's own log-field list

3. **`withObserve` state/generation logging** — requires one `Controller.Status()`
   call per request (ConfigMap read) including unauthenticated/rejected ones;
   acceptable at this scale but worth noting for future abuse-rate protection

## Commit Information

- **Commit Hash:** `801bb32`
- **Branch:** `core-055-management-api`
- **Files Changed:** 5
  - Renamed: `specs/core/055-P-management-api/spec.md` → `specs/core/055-D-management-api/spec.md`
  - Modified: `specs/README.md`
  - Modified: `specs/core/150-P-vk-llm-cli/spec.md`
  - Modified: `specs/core/140-P-e2e-acceptance/spec.md`
  - Modified: `specs/core/160-P-github-actions-remote-control/spec.md`

## Status

**DONE** - All steps completed successfully.

## Post-DONE Review Fixes (Task 11)

### Fix 1: Unresolved Commit Hash in Evidence Section

**Before:**
```
`internal/controller` gained the field `ProviderBalanceUSD *float64` on
`Snapshot` (commit `<to-be-determined>`), carrying the provider's account
balance from the most recent balance read so `mgmt` can render it in the
cost document (FR-6.8) — a merged-package touch in the same category as
CORE-050's own `internal/clock/fake.NextDeadline()` addition.
```

**After:**
```
`internal/controller` gained the field `ProviderBalanceUSD *float64` on
`Snapshot` (commit `248631b`), carrying the provider's account
balance from the most recent balance read so `mgmt` can render it in the
cost document (FR-6.8) — a merged-package touch in the same category as
CORE-050's own `internal/clock/fake.NextDeadline()` addition.
```

**Verification:**
```
$ git log --oneline -- internal/controller | grep ProviderBalanceUSD
248631b feat: Snapshot.ProviderBalanceUSD for the management API cost document
```

### Fix 2: Status Note Contradiction with Front-Matter

**Before:**
```
**Status note:** READY 2026-11-04. Written 2026-11-01 before any code exists; the result field, nextCreateAt and the 4-value reason enum were clarified before promotion.
```

**After:**
```
**Status note:** DONE 2026-11-04. Written 2026-11-01 before any code exists; the result field, nextCreateAt and the 4-value reason enum were clarified before promotion.
```

**Rationale:** Corrects contradiction with front-matter `status: "DONE"`. Pattern verified against CORE-050 equivalent.

### Validation

```
$ make specs-check
SPECS-CHECK: specs/ layout is valid.
```

**Result:** Both fixes applied; all checks pass.
