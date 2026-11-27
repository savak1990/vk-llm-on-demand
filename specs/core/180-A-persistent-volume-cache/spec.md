---
id: "CORE-180"
status: "IN_PROGRESS"
updated: "2026-11-26"
depends_on: ["CORE-020", "CORE-050", "CORE-100", "CORE-210", "CORE-220", "CORE-310", "CORE-320"]
adrs: ["0014", "0003", "0004", "0005", "0015", "0018"]
---

# 180 — Model cache on Vast.ai volumes, preferred cached host

**Status note:** DRAFT, rewritten 2026-11-26 after a design session with the
operator; it replaces the 2026-11-01 draft (one volume, `vk-llm cache init`,
no expiry). [CORE-320](../320-D-vast-volume-spike/spec.md) is DONE
(2026-11-26): a volume survives the destroy of its GPU (V1), and a hit made
the model ready in about 126 s against about 368 s for a miss, and saved the
$1.12 download fee. The answers V2 to V9 are written into the sections below.
The operator decided the two questions from the spike on 2026-11-26: offers
that can start a cache are preferred within the same 15 % threshold, and
the storage price cap is $0.40/GB-month.
[CORE-310](../310-A-cold-start-recording/spec.md) comes first, because it
gives the rental records that this spec extends.

**Complexity:** M
**Risk:** medium — a forgotten volume bills with nothing using it; a wrong attach serves stale weights
**Estimated effort:** 1 to 2 days after CORE-320
**Recommended model:** standard
**Depends on:** [CORE-050](../050-D-gpu-lifecycle-controller/spec.md), [CORE-100](../100-D-cost-controls/spec.md), [CORE-210](../210-D-offer-cost-model/spec.md), [CORE-220](../220-A-automatic-gpu-start/spec.md), [CORE-310](../310-A-cold-start-recording/spec.md), [CORE-320](../320-D-vast-volume-spike/spec.md)

## Problem

The 28.6 GB model download was the largest cold-start phase in 8 of 9 good
starts (2026-11-24 to 11-25). A cache hit saves about 2 minutes on a fast
host and about 5 minutes on a slow one. In money, a hit saves only the
download fee ($0.07 to $1.12) and 2 to 5 GPU minutes (about $0.03); the gain
is time.

## Outcome

When a rental downloads the model, the controller puts the model on a Vast.ai
volume on that machine. After the GPU is destroyed, the volume stays for
`cache.retention` (72 h) after the end of its last use; each use extends it.
At the next start, an offer on a machine with a cached volume wins unless an
offer without a cache is at least `cache.preferThreshold` (15 %) cheaper. On
a hit the worker checks sha256 and skips the download. At most
`cache.maxCaches` (1) volumes exist; a new cache replaces the oldest. The
one-minute reconcile loop deletes expired, replaced and orphaned volumes,
and deletes all of them when the monthly budget is used up. Storage cost
counts in the monthly budget, which goes from $30 to $35 in the same PR.

## Scope

- Provider calls for volumes (shape from CORE-320 V2, V6).
- The `cache` field of the state document (reserved by ADR 0004).
- Offer selection with the cached-host preference.
- Seeding a volume during a normal rental; attach on a hit.
- Release: expiry, replacement, budget, model change, orphan sweep.
- Storage in the ledger, the budget and `GET /api/v1/gpu/cost`.
- Chart values under `config.cache`; the budget default change to $35.
- Progress text, rental record field and metrics for the cache.

Excludes: `vk-llm cache` commands and a manual release endpoint (set
`maxCaches: 0` to release all); Postgres or another store (operator decision
2026-11-26); stopped instances (ADR 0003 §6, constitution C-5); a location
preference (operator decision 2026-11-26, see CORE-310); the S3 mirror
(ADR 0005).

## Design and contracts

### Chart values

```yaml
config:
  cache:
    maxCaches: 1                         # 0 disables the cache and releases every volume
    retention: 72h                       # from the end of the last use
    preferThreshold: 0.15                # a host without a cache wins only if ≥ 15 % cheaper
    maxStoragePriceUSDPerGBMonth: 0.40   # no volume on a host with a higher storage price
  budget:
    monthlyUSD: 35                       # was 30; storage now counts in it
```

The chart default `maxCaches: 1` is on purpose, unlike the `explicit`
default of CORE-220: the operator decided on 2026-11-26 that a new install
keeps one cache, and the lab uses the default with no override in
`gitops/values.yaml`.

The volume size is derived, never configured:
`ceil(modelBytes × 1.1 / 1e9) + 2` GB (34 GB for the 28.6 GB model). With
`maxCaches: 1` the worst storage cost is 34 GB × $0.40 = $13.60 per month.

### State document

The reserved `cache` key holds one object:

```json
"cache": {
  "volumes": [
    {
      "volumeId": "987",
      "machineId": 12345,
      "label": "vkllm_<installationId>_v11",
      "modelURL": "https://huggingface.co/.../Qwen3.8-27B-Q8_0.gguf",
      "sha256": "…",
      "sizeGB": 34,
      "storageUSDPerGBMonth": 0.20,
      "state": "seeding",
      "createdGen": 11,
      "inUseGen": 11,
      "createdAt": "2026-11-27T10:00:00Z",
      "expiresAt": null,
      "deleteRequestedAt": null
    }
  ]
}
```

| `state` | Meaning |
|---|---|
| `seeding` | created in this rental; the model is not verified yet |
| `ready` | holds a verified model for `modelURL` and `sha256` |
| `deleting` | a delete is requested; it is retried until the provider confirms absence |

`inUseGen` is the generation of the GPU that has the volume attached, or
`0`. `expiresAt` is set when that GPU is destroyed: `destroy time +
retention`. A volume with `inUseGen ≠ 0` never expires and is never deleted.

### Offer selection

The search, the filters, every cap (`maxHourlyPriceUSD`, download, storage
and disk caps, the machine denylist) and the session-cost estimate are
unchanged (ADR 0018). Then:

1. For each admitted offer whose `machineId` has a `ready` volume for the
   configured `modelURL` and `sha256`, the session cost is computed again
   with download time `0` and download fee `0`.
2. `C` is the cheapest such offer; `N` is the cheapest offer on a machine
   without a cache.
3. If `C` exists and `N.sessionUSD > (1 − preferThreshold) × C.sessionUSD`,
   choose `C` (a hit). If `C` exists and no `N` exists, choose `C`.
   Otherwise the result is a miss, and step 4 chooses among the offers
   without a cache.
4. On a miss, `S` is the cheapest offer whose machine can start a cache
   (every condition of "Seeding on a miss" holds for it), and `U` is the
   cheapest offer whose machine cannot. If `S` exists and
   `U.sessionUSD > (1 − preferThreshold) × S.sessionUSD`, choose `S`. If `S`
   exists and no `U` exists, choose `S`. Otherwise choose `U`. When
   `maxCaches` is `0`, no offer can start a cache, so step 4 is the plain
   session-cost order.

Both steps use the one `preferThreshold` setting. The volume offers come
from one `search volumes` call per search, joined to the GPU offers by
`machine_id`; a failed volume search makes every offer a `U` and never fails
the start (R9).

The cached host never gets a price premium above a cap: it must pass every
filter like any other offer. If the search does not return the cached
machine (CORE-320 V5), the result is a miss.

### Seeding on a miss

A volume is created on the chosen offer's machine only if all of these hold:
`maxCaches > 0`; the machine has a volume offer (`search volumes
"machine_id==M"`; 5 of 6 machines had one, CORE-320 V9) with enough space;
that volume offer's `storage_cost` ≤ `maxStoragePriceUSDPerGBMonth` (the
volume has its own price, not the GPU offer's `storage_cost`, CORE-320 V3);
the month-to-date spend plus the volume's cost for one retention period is
below the budget. The controller keeps that price in `storageUSDPerGBMonth`,
because `show volumes` returns `storage_cost: null`. Otherwise the rental runs
the unchanged ADR 0005 path on the ephemeral disk.

If the slots are full, the volume with the oldest `expiresAt` that is not in
use goes to `deleting` in the same transition. The new volume is created and
attached with the instance in one create call (`volume_info{create_new:
true, volume_id: <volume offer id>, size, mount_path: "/models", name}`,
CORE-320 V2), and recorded
as `seeding` before `CreateInstance` is sent, so that a crash leaves a record
that the sweep can resolve. When the worker writes `verified`, the volume
becomes `ready` in the READY transition.

### Attach on a hit

`CreateInstance` attaches the existing volume at `/models`. The worker's
existing check (`onstart.sh.tmpl`: skip the download if `/models/model.gguf`
passes sha256) runs unchanged. A mismatch or a missing file downloads again
onto the volume; the volume stays `ready` only if the new download is
verified. `vkllm_cache_checksum_mismatch_total` counts each mismatch.

### Release

The reconcile loop (every minute) does these steps, in this order:

| Condition | Action |
|---|---|
| `maxCaches` is lower than the number of volumes | the volumes with the oldest `expiresAt`, not in use, go to `deleting` |
| the GPU of a `seeding` volume is destroyed before `verified` (a failed or cancelled start) | `deleting` at once (reason `seed_failed`); it never takes a slot for 72 h |
| the GPU of a hit is destroyed before READY (a failed or cancelled start) | `deleting` at once (reason `hit_failed`): the worker may have removed a file that failed sha256 |
| the provider refuses a create because of its `volume_info` (not a duplicate label) | the create is sent once more without a volume; on success the volume goes to `deleting` (reason `attach_failed`) and the rental records `cache: "none"` (R9) |
| a volume is not in use and `now ≥ expiresAt` | `deleting` (reason `expired`) |
| month-to-date spend ≥ budget | every volume not in use goes to `deleting` (reason `budget`) |
| `modelURL` or `sha256` in the config differs from the volume | `deleting` (reason `model_changed`) |
| a volume is `deleting` | `DeleteVolume`; retry every tick until the provider confirms absence, then remove the entry |
| a provider volume under our label prefix is not in the document | delete after two observations (reason `orphan`), the same rule as instances (constitution C-6) |
| a document volume is absent at the provider for two observations | remove the entry |

A delete that returns success counts as confirmed; a volume that is still
listed later is deleted by the orphan rule. A failed `ListVolumes` never
deletes anything. Empty-store recovery deletes
every volume under our label prefix (constitution C-13). Vast refuses a
delete while an instance uses the volume (`409`, CORE-320 V4), so a delete
runs only after our GPU on it is destroyed. A delete while another customer
rents the machine, or while it is offline, was not observed; if one fails,
the volume stays `deleting` and keeps counting in the ledger until the
delete succeeds, and `vkllm_cache_delete_failures_total` makes it visible.

The volume label is `vkllm_<installationId>_v<gen>`: Vast allows only
letters, digits and underscore, at most 64 characters, and returns it as
`label` in `show volumes` (CORE-320 V6). The instance labels start with
`vkllm:`, so the instance sweep never acts on a volume and the volume sweep
never acts on an instance.

### Cost and budget

Each volume opens a ledger entry when it is created, with
`dph = sizeGB × storageUSDPerGBMonth / 730`, and closes it when the provider
confirms the delete. The entry counts in the month-to-date spend like a GPU
entry, so the existing budget stop covers storage.

Ledger entries are keyed by `gen` today (`internal/budget/ledger.go`), and a
volume is created in the same generation as its GPU. `LedgerEntry` therefore
gains `volume` (the volume label, because the provider id is unknown when
the entry opens at record time) (`omitempty`): a GPU entry has none, a
storage entry has its label, and storage entries close by label. The
ledger also keeps `storageTotalUsd`, the closed storage spend of the
month, so `estimatedStorageUSD` (this total plus the open storage entries)
stays correct after the ledger trims old entries; `Rollover` resets it.
`Rollover` already keeps an open entry with a new `Start` at the first of
the month, so the new month counts storage from that moment; it must also
copy `volume`. `GET /api/v1/gpu/cost` adds `estimatedStorageUSD`. The
budget default goes to $35 in this PR: the
chart value, the Go default, constitution C-7, `CLAUDE.md` and the docs
that state it (`gitops/values.yaml` has no budget key), because $35 is safe
only when storage counts in it.

### Rollback

A controller older than this change does not know volumes: it never deletes
them, and on its first write it drops the volume label from storage ledger
entries, after which it mistakes them for GPU entries. Before a rollback:

1. Run `vk-llm stop` and wait until `vk-llm status` shows `STOPPED`.
2. Set `config.cache.maxCaches: 0` and deploy it.
3. Wait until `vk-llm status` shows no `Cache:` row and `GET /api/v1/gpu/cost`
   shows no open storage spend growing (one or two reconcile ticks).
4. Then roll back.

### Progress, records and metrics

- On a hit the progress text says "The model is cached on this host;
  checking it (sha256)" in place of the download percentage (CORE-220).
- The rental record (CORE-310) gains `cache: "hit" | "seeded" | "none"`.
- `vkllm_cache_lookups_total{result="hit|miss"}`,
  `vkllm_cache_volumes{state}`,
  `vkllm_cache_volumes_deleted_total{reason}`,
  `vkllm_cache_checksum_mismatch_total`,
  `vkllm_cache_delete_failures_total`.
- The alert `VkllmCacheDeleteStuck` fires when a volume has been `deleting`
  for over 30 minutes.
- `vk-llm status` shows each volume: machine, state, expiry.

## Requirements

- R1. The cached host MUST pass every filter and cap; the preference MUST
  only choose between admitted offers (source: constitution cost safety;
  ADR 0018).
- R2. At most `maxCaches` volumes MUST be `seeding` or `ready`; a volume in
  `deleting` MUST count in the ledger until its delete is confirmed (source:
  ADR 0014).
- R3. A volume with `inUseGen ≠ 0` MUST NOT be deleted (source: this spec).
- R4. A volume MUST be recorded in the document before the create that makes
  it is sent (source: constitution, every create is recorded first).
- R5. Every delete MUST be retried until the provider confirms absence; a
  failed list MUST NOT delete (source: constitution C-6; ADR 0003 §4).
- R6. Every attach MUST verify sha256 before `llama-server` starts (source:
  ADR 0005 §1).
- R7. Storage MUST count in the monthly budget, and reaching the budget MUST
  release every volume not in use (source: operator decision 2026-11-26).
- R8. The budget default MUST change to $35 only in the PR that ships R7
  (source: operator decision 2026-11-26).
- R9. A provider error in any cache step MUST NOT fail a start; the start
  falls back to the ephemeral path (source: ADR 0014).

## Implementation steps

1. Provider: volume list, delete and attach-on-create (`vastai`, `fake`), in
   the shape CORE-320 found. Test: fixture tests on `fake`.
2. Document: `cache.volumes`, states and transitions. Test: encode and
   decode; a crash between record and create is resolved by the sweep.
3. Offer selection with `preferThreshold`. Test: table of offers with and
   without a cache, at 10 %, 15 % and 20 % price gaps, plus a cached host
   above `maxHourlyPriceUSD` that must lose.
4. Seeding and attach in the provisioning saga. Test: simulation of a miss
   (seeding, then ready), a hit, and a checksum mismatch.
5. Release in the reconcile loop. Test: expiry, replacement, budget,
   `maxCaches: 0`, model change, orphan, failed list, failed delete, a
   failed start while `seeding`.
6. Ledger entry, `estimatedStorageUSD`, budget stop. Test: month-to-date
   spend includes storage; a GPU entry and a storage entry of the same
   `gen` close separately; an open storage entry survives `Rollover`; the
   budget stop releases volumes.
7. Progress text, rental record field, metrics, `vk-llm status`. Test:
   golden outputs.
8. Chart values, the $35 default in the chart, gitops, constitution C-7 and
   `CLAUDE.md`. Test: `make helm-lint helm-template`.

## Tests / acceptance criteria

1. AC1. Simulated miss with `maxCaches: 1`: one volume is recorded, then
   created with the instance, and is `ready` after READY.
2. AC2. Simulated next start, with session costs after step 1 of "Offer
   selection": cached machine $0.40, another $0.35 (12.5 % cheaper): the
   cached machine wins. Another at $0.34 (exactly 15 % cheaper) or $0.33:
   the other wins.
3. AC3. A cached machine priced above `maxHourlyPriceUSD` is never chosen.
   On a miss, an offer that can start a cache at $0.40 wins over one that
   cannot at $0.35 (12.5 % cheaper); one that cannot at $0.34 wins.
4. AC4. 72 h after the destroy, with no use, the volume is deleted within two
   reconcile ticks; a use at 71 h moves the expiry to 72 h after that use.
5. AC5. When the month-to-date spend reaches the budget, every volume not in
   use is deleted within two ticks.
6. AC6. A labelled volume that the document does not know is deleted within
   three ticks; a failed `ListVolumes` deletes nothing.
7. AC7. On the lab (operator go-ahead needed, about $0.50): a second start
   on the cached machine records `cache: "hit"`, and its time to READY is at
   least 60 s shorter than the seeding start.

## Definition of done

- [x] CORE-320 is `DONE` and V1 is true.
- [x] ADR 0014 is `Accepted`.
- [ ] AC1 to AC6 pass in CI; AC7 is recorded under Evidence.
- [ ] `make go-build go-test go-lint helm-lint helm-template specs-check` passes.
- [ ] Status `DONE`, folder renamed `180-D-…`, in the same PR as the code.

## Open questions

| Question | Why it matters | Settled by |
|---|---|---|
| Can a delete fail while another customer rents the machine, or while it is offline? | The release and budget rules need the delete to stop the billing | Observed in production through `vkllm_cache_delete_failures_total` |

## Evidence

2026-11-27, branch `core-180-volume-cache` at `11ec358`: AC1–AC6:
`TestCacheMissSeedsThenReady`, `TestCacheHitLinksVolume`, `TestPickOffer*`,
`TestCacheExpiryDeletes`, `TestCacheBudgetReleasesWithNoGPU`,
`TestCacheOrphanDeletedOnSecondObservation`,
`TestCacheFailedListDeletesNothing`; AC7 pending a lab run (operator
go-ahead).
