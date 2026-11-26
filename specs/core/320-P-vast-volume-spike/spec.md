---
id: "CORE-320"
status: "DRAFT"
updated: "2026-11-26"
depends_on: ["CORE-020", "CORE-070"]
adrs: ["0003", "0005", "0014"]
---

# 320 — Spike: Vast.ai volumes as a model cache

**Status note:** DRAFT, problem statement and spike plan only. Written
2026-11-26. The design in [CORE-180](../180-P-persistent-volume-cache/spec.md)
and ADR 0014 depends on the answers; if volumes cannot do what the table
below asks, CORE-180 stops and [CORE-310](../310-A-cold-start-recording/spec.md)
still stands alone.

**Complexity:** S
**Risk:** low — one hand-made rental, a hard spend ceiling, and a final check that nothing is left
**Estimated effort:** 2 h (30 min of documentation, about 1 h of rental, 30 min to record)
**Recommended model:** standard
**Depends on:** [CORE-020](../020-D-vastai-provider-client/spec.md), [CORE-070](../070-D-worker-runtime/spec.md)

## Problem

Every cold start downloads the 28.6 GB model from Hugging Face. On the last
10 starts this was the largest phase in 8 of 9 good starts, and on two slow
hosts it made the start 5 to 6.5 minutes. A Vast.ai volume stays on one
machine after the GPU is destroyed ([research §1.7](../../../docs/research.md)),
so a model on a volume could skip the download when the same machine is
rented again.

The operator's design (CORE-180) seeds the cache during a normal rental, not
with a separate `cache init`, keeps it 72 h after the last use, and releases
it when it expires or when the monthly budget is used up. That design is
safe only if the provider behaves as the table below assumes. No answer is
in our research today.

## Questions the spike answers

| # | Question | Why CORE-180 needs it |
|---|---|---|
| V1 | Does a volume survive `DestroyInstance` on the GPU that has it attached? | The whole design fails if it does not |
| V2 | Can one `CreateInstance` call create and attach a new volume, or must a separate create-volume call run first on the offer's machine? What is the request shape, and can we set the mount path to `/models`? | Seeding happens inside a normal rental |
| V3 | How is a volume billed: at the bundle's `storage_cost`, or at its own volume price? Is it billed while no GPU is attached? How do we read the price before we create it? | The cost cap (`maxStoragePriceUSDPerGBMonth`) and the ledger entry |
| V4 | Does a volume delete succeed while someone else rents that machine's GPU, and while the machine is offline? | Expiry and the budget release must stop the billing; if a delete can fail for days, the cap needs a different guard |
| V5 | Can the offer search find the cached machine (`machine_id eq`), and does the offer show that our volume is on it? | Preferred-host selection; the filter only supports `notin` today |
| V6 | Can a volume carry a label or a name that `ListVolumes` returns, so the reconcile loop can find our volumes and delete orphans? | The orphan sweep, the same rule as for instances (constitution C-6) |
| V7 | How long does sha256 of 28.6 GB take when the file is read from the volume? | The integrity check runs on every attach; it is part of the cache-hit time |
| V8 | How many volumes can one account have, and how long does a volume create take? | `maxCaches` up to 3; the time cost of seeding |

## Spike plan

1. Read the current Vast.ai documentation and CLI help for volumes
   (`vastai create volume`, `vastai search volumes`, `vastai delete volume`,
   the `volume_info` field of instance create). Record each fact with its
   URL under Evidence, tagged as V1 to V8. No spend. If the documentation
   answers a question clearly, the rental only confirms it.
2. Get the operator's go-ahead, then rent one offer by hand (Vast.ai CLI or
   console, not the controller), with the production filter and a volume
   of 32 GB. Download the model onto the volume, time sha256, destroy the
   GPU, and check that the volume is still listed and billed.
3. Rent the same machine again if an offer exists, attach the volume, and
   time sha256 and the llama-server load. If no offer exists, record that
   fact; it is data for the hit rate.
4. Try a delete while no GPU is attached. If possible, also observe a delete
   while the machine is rented by someone else (V4).
5. Delete everything. List volumes and instances under our account and
   record both as empty under Evidence.

## Safety

- S1. A hard spend ceiling of $3 for the whole spike, checked against the
  account balance before step 2 and after step 5.
- S2. Every resource the spike creates carries a `vkllm-spike` label, and
  step 5 deletes it.
- S3. The spike never uses the lab controller's state document or label
  prefix, so the controller's orphan sweep cannot act on spike resources and
  spike resources cannot be mistaken for controller ones.
- S4. Step 5 is not optional: the spike is not complete until both lists are
  empty.

## Out of scope

Code. Any change to the controller, the chart or the worker. The cache
design itself (CORE-180).

## Evidence

None yet.
