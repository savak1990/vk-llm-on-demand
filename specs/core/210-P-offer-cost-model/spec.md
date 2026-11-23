---
id: "CORE-210"
status: "READY"
updated: "2026-11-23"
depends_on: ["CORE-020", "CORE-050", "CORE-070", "CORE-100"]
adrs: ["0017", "0004", "0005", "0011", "0014", "0015"]
---

# 210 — Offer selection by estimated session cost

**Status note:** READY. Problem and requirements written 2026-11-22 from
the first two real rentals (CORE-130 / Task 9); design agreed the same day
after a read-only offer-search probe (Evidence). Decisions are recorded in
[ADR 0017](../../../docs/adr/0017-offer-selection-by-session-cost.md).

**Complexity:** L — one new pure package, one new HTTP resolver, new offer
fields end to end (provider → state → status → CLI), three new caps, one
ledger field, worker script changes.
**Risk:** medium — the ranking decides what is rented; a wrong unit or a
cap that is not applied at create time costs real money.
**Estimated effort:** two sessions (8–12 h)
**Recommended model:** strong — touches the provider, the controller saga,
the state schema and the budget ledger.
**Depends on:** [CORE-020](../020-D-vastai-provider-client/spec.md),
[CORE-050](../050-D-gpu-lifecycle-controller/spec.md),
[CORE-070](../070-D-worker-runtime/spec.md),
[CORE-100](../100-D-cost-controls/spec.md)

## Problem

A Vast.ai offer has four prices, and the controller looks at one of them.

| Price on the offer | Unit | What the controller does with it |
|---|---|---|
| `dph_total` | $/h | the only ranking key: offers sorted by `Hourly`, then id (`internal/provider/select.go`); the only cap (`maxHourlyPriceUSD`) |
| `inet_down_cost` | $/GB | ignored; not in the `Offer` struct |
| `inet_up_cost` | $/GB | ignored |
| `storage_cost` | $/GB/month | ignored |

Every cold start downloads the whole model file (28.6 GB for Qwen3.8-27B
Q8_0) onto the rented host, so the download price is paid on every start.
For a short session it is the largest line on the bill, and the controller
cannot see it.

Two rentals on 2026-11-22, same model file, same GPU class:

| Instance | GPU | Download | Storage | Total | Share of bill that is download |
|---|---|---|---|---|---|
| run 1, machine 117871 | 0.51 h × $0.53 = $0.27 | 28.8 GB × $0.01 = $0.35 | $0.02 | $0.64 | 55% |
| run 2, machine 40508 | 0.42 h × $0.56 = $0.23 | 29.2 GB × $0.039 = **$1.14** | $0.03 | $1.41 | 81% |

The controller chose machine 40508 because its GPU was 3 cents per hour
cheaper. That choice cost 77 cents more. The status document's
`estimatedInstanceUSD` is `dph_total × hours`, so it reported about $0.23
for a $1.41 rental; the monthly budget check (CORE-100) is fed by the same
number and under-counts by the same amount.

Two more gaps found while designing:

- The created disk is the fixed `config.provider.filter.minDiskGB` (60). The curl
  fallback in `internal/worker/onstart.sh.tmpl` writes 8 parts, then
  concatenates, then deletes, so its peak disk use is 2 × the model file
  (57 GB of 60 today). A larger model does not fit.
- The worker downloads the model on every start, even when a file with the
  right checksum is already on disk.

## Outcome

`vk-llm start` rents the offer with the lowest estimated cost for the
session: GPU and storage for the download time plus the idle-timeout
horizon, plus the model download at that host's $/GB. Run 1's machine wins
over machine 40508. No offer above the hourly, download or storage cap, or
without enough disk for the model, is ever created. The disk is sized from
the model file. `vk-llm status` shows where the money goes:

```
Price     $0.62/h (GPU $0.56 + storage $0.06)
Download  $1.14 (29.2 GB × $0.039/GB, est. 4m23s)
Est. cost $1.41 this instance, $4.20 / $30.00 this month (estimate)
```

and the monthly budget counts the download from the moment of create.

## Scope

In this repository:

- `internal/offercost/` (new, pure) — `Inputs`, `Breakdown`, `Rank`,
  `DiskGB`; `estimate` is internal, `Rank` is its only caller and holds
  the `inet_down <= 0` guard.
- `internal/modelsize/` (new) — `Resolver` interface, HTTP implementation,
  in-memory cache.
- `internal/provider/provider.go`, `select.go` — new `Offer` and
  `OfferFilter` fields; `Select` becomes `Filter` (no sort).
- `internal/provider/vastai/` — `filter.go`, the offer DTO in `client.go`,
  `testdata/filter_default.json`, `testdata/offer_search.json`.
- `internal/provider/fake/` — uses `Filter`.
- `internal/controller/` — `saga.go` (`dispatchSearch`, `dispatchCreate`),
  `decide.go` (T5, `OpenEntry` calls, `searchFilter`), events, `status.go`.
- `internal/store/document.go` — `Offer.Prices`, `Offer.Estimate`,
  `LedgerEntry.FixedUSD`.
- `internal/budget/ledger.go` — `OpenEntry` takes a fixed amount; `estUSD`
  adds it; rollover rule.
- `internal/config/controller.go` — the three new caps.
- `internal/worker/` — `create.go` takes `DiskGB` per create;
  `onstart.sh.tmpl` checksum skip and append-and-delete fallback; golden.
- `cmd/gpu-on-demand/` — wiring of the resolver; `worker_adapter.go` no
  longer holds a fixed disk size.
- `internal/mgmt/documents.go`, `cmd/vk-llm/client.go`, `render.go` —
  status and cost documents, CLI lines.
- `deploy/helm/gpu-on-demand/` — `values.yaml`, `values.schema.json`,
  `_helpers.tpl`; a values comment on `networkPolicy.vastApiCidrs`.
- Docs: ADR 0017 (new); amendments to spec 100 (cost model), ADR 0005
  (bandwidth is not "cents"), spec 140 (R8 checklist line), spec 180 and
  spec 200 (one line each, see Interactions).

Not in scope: persistent volumes and host affinity (CORE-180), per-start
model or GPU (CORE-200), multiple providers, reading actual charges from
`GET /api/v0/charges`.

## Design and contracts

### Facts the design rests on (probe, Evidence)

| Fact | Consequence |
|---|---|
| `dph_total = dph_base + storage_cost × allocated_storage ÷ 720` | send `allocated_storage`; `dph_total` then carries storage, and the ledger, the hourly cap and the ranking count it with no new code |
| the search sends no `allocated_storage` today, so `dph_total` prices a small default disk, not ours | today's hourly cap and ledger under-count storage |
| `inet_down_cost` is $/GB: machine 40508 shows `0.0390625`; 29.2 GB × 0.0390625 = $1.14, the billed amount | use it directly |
| `inet_down` is Mbps (megabits): observed 416 to 3527 | the field `InetDownMBps` is misnamed by a factor of 8; rename to `InetDownMbps` |
| storage prices seen: $0.13, $0.20, $0.67 /GB/month | storage cap default $1.00 lets normal hosts pass |
| GPU time is not billed while the image loads, but the model download runs in onstart, after the container starts | the GPU is billed during the model download |

### Cost model (`internal/offercost`, pure)

No I/O and no clock. All money is `provider.Money` (micro-USD).

```go
type Inputs struct {
    ModelBytes int64
    Horizon    time.Duration // idle timeout; maxLifetime when idle shutdown is off
    DiskGB     int
}

type Breakdown struct {
    DiskGB          int
    ModelBytes      int64
    HorizonSeconds  int64
    DownloadSeconds int64
    GPUHourly       provider.Money // dph_base
    StorageHourly   provider.Money // dph_total − dph_base
    DownloadUSD     provider.Money
    SessionUSD      provider.Money
}
```

Per offer:

```
downloadSec = ceil(ModelBytes × 8 ÷ (inet_down × 1e6))
downloadUSD = ModelBytes ÷ 1e9 × inet_down_cost
sessionUSD  = dph_total × (Horizon + downloadSec) + downloadUSD
```

Disk:

```
DiskGB(modelBytes, minDiskGB) = max(minDiskGB, ceil(modelBytes ÷ 1e9 × 1.15) + 15)
```

The 15 GB covers the worker image and logs. The factors are constants, not
chart values. For Qwen3.8-27B (28.6 GB) the formula gives 48, so the floor
of 60 applies. If `DiskGB > MaxDiskGB`, no search runs (see Search and
create).

`Rank(offers, in) []Ranked` (`Ranked{Offer, Breakdown}`):

1. Drop an offer with `inet_down ≤ 0` (no estimate is possible).
2. Sort by `SessionUSD`, then `Hourly`, then `ID`. The order is total.

The caps and the disk are not checked here: `provider.Filter` already
removed every offer that `provider.Admit` refuses (see Provider).

Known limits, stated and accepted:

- `inet_down` is the host's advertised speed; Hugging Face can be slower,
  so `downloadSec` is a lower bound. It is a ranking input, not a
  prediction.
- The worker image pull and upload traffic are not in the estimate. On run
  2 the billed download was 0.6 GB above the model file; R8's 10% tolerance
  covers this.
- The ranking depends on the horizon. A longer idle timeout gives the GPU
  price more weight.

### Model size (`internal/modelsize`)

```go
type Resolver interface {
    Bytes(ctx context.Context, url string) (int64, error)
}
```

The HTTP implementation sends `HEAD` without following redirects and reads
`x-linked-size` (Hugging Face sets it on the 302; spec 030 recorded it). If
the header is absent, it follows redirects and reads `Content-Length`. The
timeout is 10 s. Only a success is cached, keyed by URL, for the process
life. A missing or non-positive size is an error: no implementation in the
package, including `Fixed`, returns a non-positive size with a nil error.

Network policy: the default egress rule already allows public 443. An
operator who pins `networkPolicy.vastApiCidrs` blocks Hugging Face; every
start then logs `model_size_unknown`, retries each tick, and creates nothing. The values
comment says so.

### Provider

- `Offer` adds `DphBase`, `DownloadPerGB`, `UploadPerGB`,
  `StoragePerGBMonth` (all `Money`) and `DiskSpaceGB float64`; renames
  `InetDownMBps` to `InetDownMbps`.
- `OfferFilter` adds `AllocatedStorageGB`, `MaxDownloadPerGB`,
  `MaxStoragePerGBMonth`. `MinDiskGB` now carries the computed `DiskGB`.
- `buildFilterBody` adds `allocated_storage`, `inet_down_cost lte`,
  `storage_cost lte`; `disk_space gte` uses the computed disk.
- The DTO decodes `dph_base`, `inet_down_cost`, `inet_up_cost`,
  `storage_cost`, `disk_space`.
- `provider.Select` becomes `provider.Filter`: it keeps each offer for
  which `provider.Admit(offer, filter)` is true (price caps, disk, deny
  list), as a second check after the server; no sort. The fake uses it.

### Search and create (`internal/controller/saga.go`, the only I/O)

`dispatchSearch`:

1. `bytes, err := ModelSize.Bytes(ctx, modelURL)`. On error, log
   `reason=model_size_unknown` and post `EvNoOffer{Err, Reason:
   "provider_error"}`: the existing retry path (Idle, desired stays
   RUNNING, the next tick searches again).
2. `disk := offercost.DiskGB(bytes, minDiskGB)`. Above `MaxDiskGB`, log
   `reason=model_too_large` and post `EvNoOffer{Reason: "no_offer"}`: the
   existing stop path (desired becomes STOPPED). No search runs.
3. `SearchOffers` with the filter for `disk`; then `offercost.Rank`.
   Empty: `EvNoOffer{no_offer}`.
4. Post `EvOfferChosen{Offer, Breakdown, Filter}` for the first.

No new reason values: the metrics outcome and `Snapshot.Unavailable` enums
are unchanged.

`dispatchCreate` refuses the offer (`EvCreateFailed`, `Fatal`) when
`provider.Admit(offer, filter)` is false, next to `AssertPrice`, before
`BuildCreate`; `BuildCreate` takes `Breakdown.DiskGB`. `decide.go` stays
pure: T5 copies the breakdown into `Instance.Offer`.

### Configuration

| values.yaml | Default | Env var | Rule |
|---|---|---|---|
| `config.lifecycle.safety.maxDownloadPriceUSDPerGB` | 0.10 | `VKLLM_LIFECYCLE_SAFETY_MAX_DOWNLOAD_PRICE_USD_PER_GB` | > 0, finite |
| `config.lifecycle.safety.maxStoragePriceUSDPerGBMonth` | 1.00 | `VKLLM_LIFECYCLE_SAFETY_MAX_STORAGE_PRICE_USD_PER_GB_MONTH` | > 0, finite |
| `config.lifecycle.safety.maxDiskGB` | 200 | `VKLLM_LIFECYCLE_SAFETY_MAX_DISK_GB` | ≥ `minDiskGB` |

The schema and the Go loader both reject 0, so a missing value can never
exclude every offer. `config.provider.filter.minDiskGB` stays as the disk floor.
`maxHourlyPriceUSD` keeps its value and now includes storage: a GPU at
$0.75/h with storage at $1.00/GB/month on 60 GB is $0.83/h and is excluded.

### State document (additive, no schema version change; ADR 0015 §5 pattern)

```jsonc
"instance": { "offer": {
  "machineId": 40508, "gpu": "RTX 6000Ada",
  "dph": 0.6156,          // dph_total incl. storage for diskGB (existing key)
  "inetDown": 889.4,      // existing key; Mbps
  "prices": { "gpuUSDPerHour": 0.56, "storageUSDPerGBMonth": 0.667,
              "downloadUSDPerGB": 0.039, "uploadUSDPerGB": 0.039 },
  "estimate": { "modelBytes": 29200000000, "diskGB": 60, "horizonSeconds": 1800,
                "downloadSeconds": 263, "downloadUSD": 1.14, "sessionUSD": 1.49 }
}}
```

A document written before this spec has neither block; its instance shows
a download cost of $0.

### Ledger

`LedgerEntry` adds `FixedUSD float64 "fixedUsd,omitempty"`.
`EstUSD = FixedUSD + DPH × ceil(seconds) ÷ 3600`. `OpenEntry` takes the
fixed amount; T8 and T11 pass `Instance.Offer.Estimate.DownloadUSD`. The
budget gate and `maxInstanceSpendUSD` see the download at once. On a month
rollover the fixed amount stays in the closed month, and the restarted open
entry has `FixedUSD = 0`.

### Status and cost documents

```jsonc
"instance": { /* existing */, "prices": { /* the four prices */ }, "inetDownMbps": 889.4 },
"cost": {
  "estimatedInstanceUSD": 1.41, "estimatedMonthUSD": 4.20, "budgetMonthUSD": 30,
  "breakdown": { "gpuUSD": 0.24, "storageUSD": 0.02, "downloadUSD": 1.14 },
  "selection": { "modelBytes": 29200000000, "horizonSeconds": 1800,
                 "estimatedDownloadSeconds": 263, "estimatedSessionUSD": 1.49 }
}
```

`breakdown` is the cost so far (GPU and storage over elapsed time, plus the
fixed download); its fields sum to `estimatedInstanceUSD`. `selection` is
the estimate that ranked the offer. `/cost` carries the same blocks, and
its `limitations` adds "Upload bandwidth and the worker image pull are not
counted." The CLI prints the lines in Outcome.

### Worker (`onstart.sh.tmpl`)

- Before `st download_begin`: if `$M` exists and `sha256sum -c` passes,
  record `st download_skipped` and skip `dl`.
- The curl fallback appends each part to `$M` and deletes it at once
  (`: >"$M"` first). Peak disk is about 1.13 × the model. A failed append
  leaves a short file, and the existing checksum step fails it with
  `fail_checksum`, so a bad file is never served.
- Before a download attempt, a stale `/models/model.gguf` (and its
  `.aria2` control file) is deleted so a retry never leaves a partial
  file doubling disk use; the curl fallback deletes a partial model file
  before re-downloading parts for the same reason.

### Interactions

- CORE-180: on a cache hit, `DownloadUSD = 0`; the volume's storage is a
  separate ledger entry, so the instance's `allocated_storage` must not
  include the volume. One line in spec 180.
- CORE-200: a per-start model changes only `Inputs.ModelBytes` (resolved
  for that URL) and the disk. A per-start price cap can only lower a Helm
  cap. One line in spec 200.

## Requirements

- R1. `provider.Offer` MUST carry `dph_base`, download $/GB, upload $/GB,
  storage $/GB/month and `disk_space` from the search response. (source:
  research §1.2, probe)
- R2. Offers MUST be ranked by `SessionUSD` as defined in Cost model, then
  `Hourly`, then `ID`. (source: the two rentals)
- R3. An offer above `maxHourlyPriceUSD`, `maxDownloadPriceUSDPerGB` or
  `maxStoragePriceUSDPerGBMonth`, or with less disk than the computed
  `DiskGB`, MUST NOT be created; the caps MUST be applied in the search body,
  in `provider.Filter`, and again before create. (source: FR-2.2, FR-6.1;
  user decision)
- R4. The search MUST send `allocated_storage` equal to the disk that will
  be created, so `dph_total` includes storage. (source: probe)
- R5. The ledger MUST count the estimated download as a fixed amount from
  the moment of create; `estimatedInstanceUSD`, month-to-date spend and
  `maxInstanceSpendUSD` MUST include it. The admission gate MUST refuse an
  offer whose download alone would reach the cap. (source: CORE-100; user
  decision)
- R6. `Instance.Offer` MUST record the four prices and the selection
  estimate; the status and cost documents MUST expose them as structured
  JSON. (source: ADR 0004; user decision)
- R7. The model size MUST be resolved at runtime from the model URL; if it
  cannot be resolved, an instance MUST NOT be created, the controller MUST
  log `reason=model_size_unknown`, and the next tick MUST retry. (source:
  user decision)
- R8. The created disk MUST be `DiskGB` from the model size; above
  `maxDiskGB` no search MUST run, the controller MUST log
  `reason=model_too_large`, and desired MUST become STOPPED. (source: user)
- R9. The worker MUST NOT re-download a model file already on disk with the
  right checksum, and the curl fallback MUST NOT need twice the model size
  on disk. (source: ADR 0014; worker script read)
- R10. Upload price MAY be ignored in ranking but MUST be recorded under R6.
- R11. The three new caps MUST reject 0, negative and non-finite values at
  startup. (source: the `MaxHourly == 0` trap in `select.go`)
- R12. The acceptance run MUST compare the controller's estimate with the
  Vast.ai billing page for the same instance id and report the difference;
  a difference above 10% fails. (source: the under-count above)

## Implementation steps

1. `internal/offercost`: types, `estimate` (internal), `DiskGB`, `Rank`.
   Check: table tests with the probe offers and the two billed runs.
2. `internal/modelsize`: resolver and cache. Check: `httptest` cases.
3. Provider: `Offer`/`OfferFilter` fields, `Filter`, filter body, DTO,
   golden and fixture, fake. Check: provider and vastai tests.
4. Config and chart: three caps, validation, env, schema, values comment.
   Check: config tests; `make helm-lint helm-template`.
5. Store and ledger: `Prices`, `Estimate`, `FixedUSD`, rollover. Check:
   store and budget tests.
6. Controller: saga search and create, T5, `OpenEntry` callers, wiring of
   the resolver, worker adapter disk. Check: controller and wire tests.
7. Status, cost documents and CLI. Check: mgmt golden JSON, CLI render.
8. Worker script: checksum skip, append-and-delete. Check: onstart golden.
9. Docs: ADR 0017 to Accepted; spec 100, ADR 0005, specs 140, 180, 200.
   Check: `make specs-check`.

## Tests / acceptance criteria

Tier 1 and tier 2 only; R12 runs in the CORE-140 acceptance run.

- AC1. With `ModelBytes = 29.2e9`, `Horizon = 30m`, `DiskGB = 60`, run 1's
  machine (dph $0.53, $0.012/GB) ranks before machine 40508 (probe values);
  the `Breakdown` of each matches hand-computed values to the micro-USD.
- AC2. Each cap excludes an offer just above it and keeps one at it.
- AC3. An offer with `inet_down = 0` or `disk_space < DiskGB` is dropped.
- AC4. `DiskGB(28.6e9, 60) = 60`; `DiskGB(70e9, 60) = 96`; above
  `maxDiskGB` the saga posts `EvNoOffer{Reason: "no_offer"}` with no `Err`,
  logs `model_too_large`, and calls no search.
- AC5. Resolver: 302 with `x-linked-size` → size; 200 with
  `Content-Length` → size; no size → error; a failure is not cached.
- AC6. Saga: resolver error → `EvNoOffer{Err, Reason: "provider_error"}`, a
  `model_size_unknown` log line, and no
  `SearchOffers` call.
- AC7. `filter_default.json` contains `allocated_storage`,
  `inet_down_cost`, `storage_cost`; the fixture decodes all new fields.
- AC8. `dispatchCreate` refuses an offer that passes `AssertPrice` but
  fails `provider.Admit`.
- AC9. Ledger: `OpenEntry` with a fixed $1.14 makes `EstimatedMTD` rise by
  $1.14 at once; rollover keeps it in the closed month.
- AC10. Config: each new cap rejects `0`, `-1`, and accepts the default.
- AC11. Status and cost golden JSON carry `prices`, `breakdown`,
  `selection`; `breakdown` sums to `estimatedInstanceUSD`.
- AC12. CLI render shows the Price, Download and Est. cost lines.
- AC13. Onstart golden: checksum skip before `download_begin`;
  append-and-delete fallback.
- AC14. Existing controller sim tests pass unchanged (fake offers have
  download cost 0).
- AC15. The admission gate returns `budget_exhausted` for an offer whose
  `DownloadUSD` plus month-to-date spend reaches the cap.

## Definition of done

- [x] AC1–AC15 pass (tier 1/2).
- [ ] `make specs-check go-build go-test go-lint helm-lint helm-template`
      all exit 0. **Not yet:** `go-lint` fails on `internal/modelsize`
      (`bodyclose` ×2, `revive` ×1); every other target is 0. See
      Evidence.
- [x] ADR 0017 flipped to Accepted in the same PR.
- [x] Amendments to spec 100, ADR 0005, specs 140, 180, 200 merged.
- [ ] Status flipped to DONE; folder renamed `210-D-...`.
- [x] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Is the worker image pull billed as download | 2 to 3 GB per start | R12 comparison on the next real run | not counted; inside the 10% tolerance |
| Is Vast's billed GB decimal or binary | 2.4% difference | R12 comparison | decimal (1e9) |
| How far below `inet_down` real Hugging Face speed is | download time in ranking | stage timings on the next real run | advertised speed; lower bound |

## Evidence

- Billing rows for instance ids 62028631 and 62032999, 2026-11-22.
- Read-only offer search, 2026-11-22, `RTX 6000Ada`, with
  `allocated_storage` 5 and 60:
  - machine 8296: `dph_base` 1.0667, `dph_total` 1.0676 at 5 GB and 1.0778
    at 60 GB, `storage_cost` 0.1333, `storage_total_cost` 0.000926 →
    0.0111.
  - machine 40508: `dph_base` 0.56, `dph_total` 0.6156 at 60 GB,
    `storage_cost` 0.6667, `inet_down` 889.4, `inet_down_cost` 0.0390625.
  - `inet_down` across the sample: 416.0 to 3527.4.

**Commits, `git log --oneline origin/main..HEAD`:**

```
be520eb worker: clean stale model before downloading to prevent double-disk usage
f55e3f0 worker: skip a verified model on disk; reassemble parts without twice the disk
c8f4604 status, cost, CLI: show recorded prices, the cost breakdown and the selection estimate
b8e06dc controller: size the model and disk, rank offers by session cost, record the estimate
25193fd store, budget: record offer prices and estimate; charge a fixed download amount at open
76747ad config: hard caps on download price, storage price and disk size
4711f3e modelsize: reject Fixed with non-positive size
ea9efc8 modelsize: resolve a model file's size by HTTP HEAD, cached per URL
dd6936a offercost: unexport Estimate, add discriminating rank test
560112d offercost: estimate session cost per offer and rank by it
6511d1f provider: offer price fields, Admit/Filter without ranking, inet_down in Mbps
dbbee38 CORE-210: Breakdown carries model size and horizon
24e2dae CORE-210: align search reasons and cap check with the existing event paths
72538d0 CORE-210: design — rank offers by estimated session cost
```

**Validation, 2026-11-23 (Task 9):**

| Target | Result |
|---|---|
| `make specs-check` | 0 |
| `make go-build` | 0 |
| `make go-test` | 0 (all packages `ok`, including `internal/offercost`, `internal/modelsize`, `internal/controller`, `internal/worker`) |
| `make go-lint` | **1** — `bodyclose` on `internal/modelsize/modelsize.go:54` and `:60` (the `HEAD` response body is not closed on both the no-redirect and the follow-redirect paths); `revive` unused-parameter on `internal/modelsize/modelsize_test.go:19`. Introduced in `ea9efc8`; not fixed in this commit. |
| `make helm-lint` | 0 |
| `make helm-template` | 0 |
