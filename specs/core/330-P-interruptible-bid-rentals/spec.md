---
id: "CORE-330"
status: "DRAFT"
updated: "2026-11-27"
depends_on: ["CORE-020", "CORE-050", "CORE-100", "CORE-180", "CORE-210", "CORE-220", "CORE-310"]
adrs: ["0023", "0003", "0011", "0014", "0018", "0019"]
---

# 330 — Interruptible (bid) rentals, opt-in

**Status note:** DRAFT, written 2026-11-27 after a design session with the
operator. Section 1 of the design (behavior and config) was agreed in
chat; the operator reviews the rest in depth before READY. Decisions are in
[ADR 0023](../../../docs/adr/0023-interruptible-bid-rentals.md) (Proposed).
[CORE-180](../180-A-persistent-volume-cache/spec.md) is merged and comes
first; this spec builds on its offer choice.

**Complexity:** M
**Risk:** medium — a wrong price field over-bids; a missed interruption leaves a stopped instance billing storage
**Estimated effort:** 1 to 2 days
**Recommended model:** standard
**Depends on:** [CORE-020](../020-D-vastai-provider-client/spec.md), [CORE-050](../050-D-gpu-lifecycle-controller/spec.md), [CORE-100](../100-D-cost-controls/spec.md), [CORE-180](../180-A-persistent-volume-cache/spec.md), [CORE-210](../210-D-offer-cost-model/spec.md), [CORE-220](../220-A-automatic-gpu-start/spec.md), [CORE-310](../310-A-cold-start-recording/spec.md)

## Problem

The lab rents RTX 6000 Ada on demand at $0.60 to $0.68/h. The same
machines rent as interruptible instances from a `min_bid` of about
$0.40 to $0.43/h (live search, 2026-11-27, two free offers). An
interruption kills the answer in flight and costs a new cold start, which
the operator accepts in exchange for the lower price, first on dev.

## Outcome

With `config.provider.rental.type: bid`, a start rents the cheapest bid
offer at `min_bid` plus a margin, falls back to an on-demand offer when
bids fail (if allowed), and treats an interruption like any loss after
Ready: the in-flight request fails with a retryable error, the instance is
destroyed at once, and the next chat request starts a new GPU. With the
default `ondemand`, nothing changes.

## Scope

Includes: config keys, bid pricing in offer choice, the bid create,
interruption detection before and after Ready, the fallback, the
`gpu_interrupted` error, rental records and ledger fields, metrics, and the
dev values.

Excludes: changing a bid on a running instance
(`PUT /instances/bid_price/{id}/`), waiting for a resume, multi-GPU offers
(CORE-290), bid-only offers that an on-demand search does not return, and
turning bids on in prod (an operator values change after the dev review).

## Design and contracts

### Config

| Chart key | Env | Default | Dev |
|---|---|---|---|
| `config.provider.rental.type` | `VKLLM_RENTAL_TYPE` | `ondemand` | `bid` |
| `config.provider.rental.bid.margin` | `VKLLM_RENTAL_BID_MARGIN` | `0.10` | — |
| `config.provider.rental.bid.attempts` | `VKLLM_RENTAL_BID_ATTEMPTS` | `1` | — |
| `config.provider.rental.bid.fallbackOnDemand` | `VKLLM_RENTAL_BID_FALLBACK_ON_DEMAND` | `true` | — |

`values.schema.json` bounds: `type` in {`ondemand`, `bid`}; `margin` 0 to
1; `attempts` 1 to `maxProvisionAttemptsPerHour`.

### Provider

- `provider.Offer` gains `MinBid` and `StoragePerHour`
  (`dph_total − dph_base`), decoded in `vastai/client.go`.
- `provider.CreateRequest` gains `BidPrice float64`; when it is above 0,
  the create body carries `"price": BidPrice`.
- The instance DTO decodes `intended_status` and `is_bid`.
  `MapStatus` returns a new `StatusInterrupted` when `is_bid` is true,
  `intended_status` is `running` and `actual_status` is `stopped`.
  `Terminal()` is true for it.
- The search filter keeps `"type": "ondemand"`: that search returns
  `min_bid` for every offer, so one search gives both prices.

### Offer choice (`dispatchSearch`)

1. Search and rank as today.
2. When `type` is `bid` and the start still has bid attempts left: for
   each offer with `MinBid > 0`, bid = round-up-to-cent(`MinBid` ×
   (1 + margin)). Drop the offer when bid ≥ `Hourly`. Rank again with
   `Hourly` = bid + `StoragePerHour`.
3. `cacheChoice` (CORE-180), `AssertPrice` and the budget checks run on
   that list unchanged.
4. The chosen offer stores `RentalType` (`bid` or `ondemand`) and
   `BidPrice` in the state document, inside the same CAS as the create op.
5. When no bid attempts are left: if `fallbackOnDemand` is true, rank
   on-demand prices; otherwise the start fails with `bid_unavailable`.

### Attempts and backoff

- The document counts `BidAttempts` per start (reset at Idle → Searching).
- A bid instance that reaches `StatusInterrupted` before Ready goes to
  Destroying with op `outbid`. The backoff in `saga.go` skips ops
  `outbid`, so the next create runs at once.
- Every create, bid or not, still increments `AttemptsThisHour` and is
  bounded by `maxProvisionAttemptsPerHour`.

### Interruption after Ready

- Reconcile and the Booting/Loading poll see `StatusInterrupted`. One
  observation is enough for this status (the two-observation rule guards
  against flaky absences, and `stopped` with `intended_status=running` is
  explicit).
- Transition: Ready/Serving → Destroying, desired `STOPPED`, op
  `interrupted`. The ledger entry and rental record close with outcome
  `interrupted`.
- An in-flight stream gets the error tail
  `{"error":{"type":"server_error","code":"gpu_interrupted","message":"The provider interrupted the GPU; send your message again to start a new one."}}`
  then `[DONE]`. A non-streaming request gets `503 gpu_interrupted`.
- While Destroying after `interrupted`, `Demand` and non-streaming requests
  get `Retry-After: 10` instead of 60.

### Rental records, ledger, status

- `store.Rental` and `store.LedgerEntry` gain `RentalType` and
  `BidPrice`. `DPH` stays the price actually charged per hour.
- `vk-llm status` and `vk-llm rentals` show the rental type.
- Metrics: `vkllm_rentals_total{type,outcome}` and
  `vkllm_interruptions_total{phase}` (`booting`, `loading`, `ready`).

### Failure conditions

| Condition | Result |
|---|---|
| No offer has `min_bid` | Counts as a failed bid attempt; fallback rules apply |
| Create with `price` returns 4xx | Op `failed_create`; normal backoff (not an outbid) |
| `volume_info` refused on a bid create | The CORE-180 retry without the volume |
| Interrupted while Destroying | Destroy continues until absence is confirmed |
| Interruption during a drain | Drain ends at once; destroy continues |

## Requirements

- R1. With `type: ondemand` the search body, create body and ranking MUST
  be byte-identical to today, and the new state fields MUST be omitted
  when empty (ADR 0023 §1).
- R2. A bid MUST NOT exceed the offer's on-demand `dph_total` or
  `maxHourlyPriceUSD` (ADR 0023 §2, constitution C-7).
- R3. Every create MUST count toward `maxProvisionAttemptsPerHour`; only
  the backoff is skipped after an outbid (ADR 0023 §3, CLAUDE.md cost
  safety).
- R4. An interrupted instance MUST be destroyed, never left stopped, and
  the destroy MUST retry until the provider confirms absence (ADR 0003,
  ADR 0023 §5).
- R5. After an interruption the controller MUST NOT start a new GPU by
  itself; only a new request or `start` does (ADR 0023 §5, ADR 0019).
- R6. The idle deadline rules of ADR 0011 MUST stay unchanged.
- R7. Bid rentals MUST appear in the budget at the price charged
  (CORE-100).

## Implementation steps

1. Provider: decode `min_bid`, `dph_base`, `intended_status`, `is_bid`;
   `StatusInterrupted`; `BidPrice` in the create body. Golden-file tests.
2. Config and chart: the four keys, schema bounds, env wiring.
3. Offer choice: bid pricing, drop rule, re-rank, fallback. Table tests on
   fixed offer lists, with and without a cache hit.
4. Controller: `BidAttempts`, op `outbid` without backoff, the
   `interrupted` transition after Ready. Decide-table rows and fake
   provider tests.
5. Gateway: `gpu_interrupted` stream tail and 503, `Retry-After: 10`.
6. Records: rental type and bid price in rentals, ledger, status, CLI,
   metrics.
7. Dev: `gitops/values-dev.yaml` sets `rental.type: bid`.
8. Dev acceptance run (AC7) and Evidence.

## Tests / acceptance criteria

- AC1. With `type: ondemand`, the existing filter and create golden files
  pass unchanged.
- AC2. Given offers (dph 0.68, min_bid 0.40) and (dph 0.50, min_bid
  0.48), bid mode with margin 0.10 picks the first at bid 0.44 and drops
  the second (0.53 ≥ 0.50).
- AC3. A fake instance reporting `is_bid`, `intended_status=running`,
  `actual_status=stopped` in Booting causes destroy, then a second create
  within 10 s, then (attempts 1, fallback true) an on-demand create.
- AC4. With fallback false, the same case ends the start with
  `bid_unavailable` after one create.
- AC5. The same status after Ready ends a held stream with
  `gpu_interrupted`, destroys the instance, writes `STOPPED`, and creates
  nothing until a new request arrives.
- AC6. Four creates in one hour return `attempts_exhausted` in bid mode
  too.
- AC7. On `llm-dev`, one real bid start reaches Ready; the Vast.ai billing
  page shows the bid price (±$0.01) and the rental record shows
  `RentalType: bid`.

## Definition of done

AC1 to AC6 pass in CI; AC7 recorded under Evidence; `make specs-check
go-build go-test go-lint helm-lint helm-template` pass; ADR 0023 Accepted;
status DONE and folder renamed to `330-D-`.

## Open questions

- Q1. Is `price` per machine or per GPU? Answered by AC7 (one GPU, so
  equal today; it matters only for CORE-290).
- Q2. Does a bid create accept `volume_info`? Answered by AC7 on a cache
  hit or seed.
- Q3. How often does an interruption happen on RTX 6000 Ada? Answered by
  `vkllm_interruptions_total` after two weeks on dev; the operator decides
  on prod from it.
- Q4. Bid floors for RTX PRO 6000 and H100 were $0.63 to $0.81/h on
  2026-11-27, inside the $0.80 cap. Allowing other GPUs in bid mode is a
  separate decision.

## Evidence

None yet.
