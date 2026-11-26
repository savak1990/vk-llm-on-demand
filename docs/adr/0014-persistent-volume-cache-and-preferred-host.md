# ADR 0014: Model cache on Vast.ai volumes with a preferred cached host

## Status

**Accepted** (2026-11-26; proposed 2026-11-01, revised 2026-11-26). The
spike in [CORE-320](../../specs/core/320-D-vast-volume-spike/spec.md)
confirmed that a volume survives the destroy of its GPU, and the operator
decided the ranking and the price cap on the same day. Extends
[ADR 0005](0005-model-storage.md), whose Status carries a pointer here. The
2026-11-01 version (one volume made by `vk-llm cache init`, no expiry,
several volumes rejected) is replaced by this one after a design session
with the operator on 2026-11-26.

## Context

Every provision downloads the model (28.6 GB today, ADR 0005). On the last
10 starts (2026-11-24 to 11-25) the model phase was the largest phase in 8
of 9 good starts; a start took 2:16 to 3:11 on a fast host and up to 6:32 on
a slow one. The controller already rented the same machine up to 4 times in
a row, because it was the cheapest. Vast.ai volumes are tied to one machine
and stay after its GPU is destroyed ([research §1.7](../research.md)). A
stopped instance is not an option (ADR 0003 §6).

## Decision

1. **Seed during a normal rental.** When a rental downloads the model and a
   cache slot is free (or the oldest can be replaced), the controller
   creates a volume on that machine and downloads onto it. No separate
   `cache init` step exists.
2. **Prefer a cached host, within a threshold.** Among the offers that pass
   every filter and cap, an offer on a machine with a verified cached volume
   wins unless an offer without a cache is at least `cache.preferThreshold`
   (default 15 %) cheaper in session cost. On a miss, an offer whose
   machine can start a cache (it has a volume offer within the price cap)
   wins in the same way, unless an offer that cannot is at least
   `preferThreshold` cheaper. Neither preference gets a price premium above
   any cap.
3. **Bounded count, rolling retention.** At most `cache.maxCaches` (default
   1) volumes. A volume expires `cache.retention` (default 72 h) after the
   end of its last use; each use extends it. A new cache replaces the oldest.
4. **Cost is capped and counted.** A volume is created only on a host whose
   storage price is at most `cache.maxStoragePriceUSDPerGBMonth` (default
   $0.40, set by the operator after the spike found volume prices of
   $0.20–0.667/GB-month), so the worst case is about $13.60 per month at one
   volume. Storage
   counts in the monthly budget; when the budget is used up, every volume
   not in use is deleted. The budget default goes from $30 to $35 with this
   change.
5. **Same safety rules as instances.** The registry is the `cache` field of
   the state ConfigMap (ADR 0004). A volume is recorded before it is
   created; every delete is retried until the provider confirms absence; a
   labelled volume that the document does not know is deleted; a failed list
   deletes nothing; empty-store recovery deletes every labelled volume.
6. **Integrity.** sha256 is verified on every attach; a mismatch downloads
   again.

## Alternatives considered

- **Postgres for the registry.** Rejected on 2026-11-26: it adds a driver,
  migrations and a second system to the destroy path, for 1 to 3 records.
  The cache tracks things that cost money, so it belongs in the same store
  and loop as the GPU (ADR 0004).
- **Prefer the last machine without a volume.** It saves 30 to 60 s of image
  pull for $0. Dropped: the cached-host rule covers the same case with a
  larger gain, and two preference rules mean two settings.
- **Prefer a location (EU).** Dropped: no data shows that location changes
  the download speed. CORE-310 records it; a later ADR can add it.
- **Keep the instance stopped.** Rejected in ADR 0003 §6.
- **S3 mirror** (ADR 0005). Still a full download per provision.

## Consequences

- CORE-180 holds the design; CORE-310 records each rental, and CORE-180
  adds a `cache` field to that record.
- The provider interface gains volume calls in the shape CORE-320 finds.
- Constitution C-7 and `CLAUDE.md` change the budget default to $35 in the
  same PR as the cache code, not before.

## Risks

- A delete can fail while someone else rents the machine (CORE-320 V4). The
  volume then stays in the ledger and in a failure metric until the delete
  succeeds.
- The cached machine is often rented by others; the hit rate can be low.
  CORE-310's records and the hit/miss metric show it, and `maxCaches: 0`
  turns the cache off.
- Vast volume behavior changes; CORE-320 records the behavior with dates.
