# ADR 0023: Interruptible (bid) rentals as an opt-in rental type

## Status

Proposed (2026-11-27). Implemented by [CORE-330](../../specs/core/330-P-interruptible-bid-rentals/spec.md).
Extends ADR 0018 (the offer cost model ranks the bid price) and ADR 0014
(a bid offer on the cached machine is a cache hit). ADR 0003 (destroy,
never stop), ADR 0011 (idle timeout) and ADR 0019 (on-demand start) stay.

## Context

Vast.ai rents the same machines as interruptible instances. The renter
sets a bid; the highest bid wins, and any on-demand rental of the same
machine pre-empts every bid. An interrupted instance is stopped, not
destroyed: the disk stays, storage keeps billing, and the instance resumes
by itself when its bid wins again, after an unknown wait.

A live search on 2026-11-27 with the lab's filters found two free
RTX 6000 Ada offers at $0.602 to $0.683/h on demand, with a `min_bid` of
$0.400 to $0.433/h (about 35 % lower). For L40S the floor was 0 to 13 %
lower. An on-demand search already returns `min_bid` for every offer,
equal to the value a `type: bid` search returns, so one search gives both
prices. In a bid search `dph_total` is `min_bid` plus the storage part
(`dph_total − dph_base`).

Today `"type": "ondemand"` is fixed in the search filter, the offer has no
`min_bid`, the create request has no `price`, and the raw status
`stopped` maps to a non-terminal status, so an interruption is noticed only
when the tunnel and the probes fail.

## Decision

1. **Opt-in.** A chart value `config.provider.rental.type` is `ondemand`
   (default) or `bid`. The dev installation sets `bid`; prod changes only
   after the operator reviews dev results.
2. **Bid price.** bid = `min_bid` × (1 + `bid.margin`, default 0.10). An
   offer whose bid is not below its on-demand `dph_total` is dropped. The
   ranked hourly price is the bid plus the offer's storage part, and
   `maxHourlyPriceUSD`, the budget and the cache preference apply to it
   unchanged.
3. **Attempts.** A start makes up to `bid.attempts` (default 1) bid
   creates. A bid instance stopped before Ready is destroyed and the next
   offer is tried at once, with no backoff. Every create still counts
   toward `maxProvisionAttemptsPerHour`.
4. **Fallback.** When the bid attempts fail and `bid.fallbackOnDemand`
   (default true) is set, the next create of the same start uses the
   on-demand price. When it is false, the start fails.
5. **Interruption.** A bid instance with `intended_status=running` and
   `actual_status=stopped` is interrupted. The controller never waits for
   a resume: it fails the in-flight request with the retryable code
   `gpu_interrupted`, destroys the instance at once and writes `STOPPED`.
   The client's retry starts a new GPU through `Demand` (ADR 0019).

## Alternatives considered

- **Wait for the resume.** Rejected: the wait is unknown, storage bills
  meanwhile, and a chat cannot wait minutes for an outbid machine.
- **Replace the GPU at once after an interruption.** Rejected: it rents a
  GPU when nobody may be waiting; the retry already carries the demand.
- **Bid exactly `min_bid`.** Rejected: the first other bidder interrupts.
- **A separate hourly cap for bid creates.** Rejected: more config, and
  the shared cap keeps the cost bound unchanged.

## Consequences

- The provider client decodes `min_bid`, `dph_base`, `intended_status` and
  `is_bid`, and sends `price` on a bid create.
- A rental record and its ledger entry carry the rental type and the price
  paid.
- An interruption costs the user one failed answer and one cold start.

## Risks

- Vast may refuse `volume_info` on a bid create. Mitigation: the existing
  4xx retry without the volume (CORE-180); the first dev run records which
  case happens.
- The `price` field may be per GPU, not per machine (sources disagree).
  Mitigation: only one-GPU offers are rented today; CORE-330 verifies the
  billed price on the first dev rental.
