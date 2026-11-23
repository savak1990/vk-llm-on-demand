# ADR 0017: Offers are ranked by estimated session cost, with hard caps on every price and on disk

## Status

Accepted (2026-11-23). Proposed 2026-11-22. Amends CORE-100's cost model
and ADR 0005's "bandwidth is cents" assumption. Implemented by CORE-210.

## Context

The controller ranks Vast.ai offers by `dph_total` alone and caps only
that price. Every cold start downloads the model file (28.6 GB today), and
the download is billed per GB at a price set by each host. On 2026-11-22
the controller chose a host 3 cents per hour cheaper whose download cost
$1.14; the rental cost $1.41 where a $0.64 one was available, and the
ledger reported $0.23.

A read-only search on the same day showed that `dph_total` includes
storage only for the request's `allocated_storage`, which the search did
not send; that `inet_down_cost` is $/GB; and that `inet_down` is Mbps.

## Decision

1. **Rank by estimated session cost in dollars.**
   `sessionUSD = dph_total × (horizon + downloadTime) + modelGB × inet_down_cost`,
   where the horizon is the idle timeout. The weights are physical (hours
   and gigabytes), so there are no tuning knobs, and the score is in the
   same unit as the budget. The ranking lives in a pure package,
   `internal/offercost`; the provider only filters.
2. **Hard caps on each price and on disk.** Hourly (existing, now with
   storage), download $/GB (default $0.10), storage $/GB/month (default
   $1.00), disk GB (default 200). Each is applied in the search body,
   locally after the search, and again before create. Zero is rejected at
   startup.
3. **Storage enters through `dph_total`.** The search sends
   `allocated_storage` equal to the disk that will be created.
4. **The model size is resolved at runtime** by an HTTP HEAD on the model
   URL (`x-linked-size`, else `Content-Length`), cached per URL. Unknown
   size means no create. The disk is derived from it, with
   `minDiskGB` as the floor.
5. **The download is charged to the ledger at create** as a fixed amount,
   so the budget gate sees it before it is spent.

## Alternatives considered

- Explicit weights (for example 0.7 GPU, 0.3 network after
  normalization): the GPU price always dominates, but the score is not in
  dollars and the weights need tuning. Rejected.
- Model size as a chart value next to the checksum: no network at search
  time, but CORE-200 needs runtime resolution for per-start models anyway.
  Rejected.
- Ranking inside `provider.Select` or inside `decide.go`: the first puts
  cost policy and an HTTP call in the provider layer; the second grows the
  largest pure file and puts the full offer list in events. Rejected.

## Consequences

- The controller makes one HTTP call to the model host per process and
  URL. A pinned `networkPolicy.vastApiCidrs` blocks it and stops all
  starts, which is safe for cost.
- `maxHourlyPriceUSD` now includes storage, so a few hosts near the cap
  are excluded that were accepted before.
- The download time uses the advertised `inet_down`, a lower bound. The
  estimate is checked against the billing page in the acceptance run, with
  a 10% tolerance.
- CORE-180 sets the download term to zero on a cache hit; CORE-200 changes
  only the inputs to the estimate.
