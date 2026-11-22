---
id: "CORE-210"
status: "DRAFT"
updated: "2026-11-22"
depends_on: ["CORE-020", "CORE-050", "CORE-100"]
adrs: ["0004", "0007", "0014"]
---

# 210 — Offer selection must count bandwidth and storage, not GPU time alone

**Status note:** DRAFT, problem and requirements only. No design yet.
Written 2026-11-22 from the first two real rentals (CORE-130 / Task 9).

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
| run 1, host 117871 | 0.51 h × $0.53 = $0.27 | 28.8 GB × $0.01 = $0.35 | $0.02 | $0.64 | 55% |
| run 2, host 40508 | 0.42 h × $0.56 = $0.23 | 29.2 GB × $0.04 = **$1.14** | $0.03 | $1.41 | 81% |

The controller chose host 40508 because its GPU was 3 cents per hour
cheaper. That choice cost 77 cents more. The status document's
`estimatedInstanceUSD` is `dph_total × hours`, so it reported about $0.23
for a $1.41 rental; the monthly budget check (CORE-100) is fed by the same
number and under-counts by the same amount.

## Requirements

- R1. The provider's offer type MUST carry the three non-GPU prices the
  API returns: download $/GB, upload $/GB, storage $/GB/month. (source:
  research §1, fields `inet_down_cost`, `inet_up_cost`, `storage_cost`)
- R2. Offer ranking MUST use an estimated total cost for the session, not
  `dph_total` alone. The estimate MUST include the download of the model
  file at the offer's download price. (source: the two rentals above)
- R3. The chart MUST let the operator cap the download price per GB, the
  same way `maxHourlyPriceUSD` caps the hourly price; an offer above the
  cap MUST never be created. (source: FR-2.2, FR-6.1 pattern)
- R4. The status document's cost estimate and the monthly spend used by
  the budget check MUST include bandwidth and storage charges, so that
  the budget protects against the real bill. (source: FR-6.x; CORE-100)
- R5. The chosen offer's four prices MUST be recorded on `Instance` in the
  state document and shown in the status document, so a rental's cost can
  be explained after the fact. (source: ADR 0004 document schema)
- R6. The worker MUST NOT re-download a file that is already on the host's
  disk with the right checksum. This is a no-op today (fresh host every
  start) and becomes the main saving once CORE-180 keeps a volume.
  (source: ADR 0014)
- R7. Upload price MAY be ignored in ranking (uploads are 0.1 to 0.3 GB per
  session today) but MUST still be recorded under R5.
- R8. The acceptance run MUST compare the controller's estimate with the
  Vast.ai billing page for the same instance id and report the difference;
  a difference above 10% fails. (source: the under-count above)

## Facts the design needs

- Research §1 confirms the API exposes `inet_down_cost`, `inet_up_cost`,
  `storage_cost`, `dph_total` as filterable fields; the two-run probe did
  not record their magnitudes.
- Today's `Offer` struct: `Hourly`, `Reliability`, `InetDownMBps`,
  `DiskBwMBps`, `CUDAMaxGood`, `MachineID`, `GPUName`
  (`internal/provider/provider.go`).
- `select.go` filters on `MaxHourly` and a deny list, then sorts by
  `Hourly` ascending.
- The model file size is known before the search (the chart pins URL and
  sha256; size can be read from the `x-linked-size` header or stored next
  to the sha256).
- A session's expected length is not known at start. The design must pick
  a horizon for the estimate (for example the idle timeout) or make it a
  parameter; CORE-200 is deciding what `start` can carry.

## Open questions

| Question | Why it matters | Default until decided |
|---|---|---|
| Horizon for the per-session estimate | download is a fixed cost; GPU is per hour; the ranking flips with session length | the idle timeout (30 min) |
| Cap value for download $/GB | hosts seen today charge $0.01 and $0.04 | $0.02 |
| Does Vast.ai charge for the download of the worker image too | 2 to 3 GB per start | assume yes, include it |
| Interaction with CORE-200 per-start parameters | a per-start model changes the file size | estimate from the file that will be downloaded |

## Out of scope

Persistent volumes and host affinity (CORE-180). Multiple providers.

## Evidence

The two Vast.ai billing rows above (instance ids 62028631 and 62032999,
2026-11-22). No code yet.
