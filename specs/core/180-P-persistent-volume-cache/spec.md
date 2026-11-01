---
id: "CORE-180"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-020", "CORE-050", "CORE-150"]
adrs: ["0014", "0005", "0001", "0003"]
---

# 180 — Persistent-volume cache and preferred-host scheduling

**Status note:** DRAFT, written 2026-11-01 before any code exists. **Priority P2 — not in the initial release** (`docs/requirements.md` §13; ADR 0014 is Proposed, not Accepted). No FR ids exist for this feature yet. Implemented only after [CORE-140](../140-P-e2e-acceptance/spec.md) is `DONE` and ADR 0014 is Accepted (see Definition of done).

**Complexity:** M
**Risk:** medium — a wrong attach serves stale weights; a forgotten volume bills every month with nothing using it
**Estimated effort:** one session (4–6 h) once ADR 0014 is Accepted
**Recommended model:** standard — a bounded extension of interfaces CORE-020, CORE-050, CORE-055 and CORE-100 already define
**Depends on:** [CORE-020](../020-P-vastai-provider-client/spec.md), [CORE-050](../050-P-gpu-lifecycle-controller/spec.md), [CORE-150](../150-P-vk-llm-cli/spec.md)

## Outcome

`vk-llm cache init` rents one GPU once, creates a `storage.persistentCache.sizeGB` volume on that host, downloads and verifies the model onto it, records the volume in the state document, and destroys the compute. When `storage.preferredHost.enabled` is true, every `vk-llm start` first searches for an offer on the cached host within a price premium; if one is free it attaches the volume at `/models` and skips the download stage, re-verifying the checksum before `llama-server` starts. If no such offer appears within `maxWait`, the unchanged ADR 0005 download path runs. `cache status`/`list` show the volume and its staleness; `cache delete` removes it. Storage spend appears in `GET /api/v1/gpu/cost` as `estimatedStorageUSD`. At most one volume ever exists per installation; a reconcile sweep destroys any volume carrying our label that the document does not know about.

## Scope

- `Provider` extension: `CreateVolume`, `ListVolumes(labelPrefix)`, `DeleteVolume`, a `VolumeInfo` attach field on `CreateInstance` (research §1.3, §1.7).
- The state document's `cache` field (reserved by ADR 0004): shape, writer, and when it is `null`.
- `vk-llm cache init|list|status|delete` and the `/api/v1/cache*` endpoints they call.
- The preferred-host offer-search algorithm and its fallback.
- The integrity check run on every attach, and the one-volume-per-installation rule with its label sweep.
- Storage cost in the ledger and in `GET /api/v1/gpu/cost`; the `storage.*` config block (not rendered by the MVP chart).
- Failure cases: host offline, volume missing, checksum mismatch, premium exceeded.
- Acceptance criteria, gated to run only after the initial release ships.

Excludes: anything in the MVP chart beyond the already-reserved `cache` field (ADR 0014 Consequences); the S3 mirror (ADR 0005, unaffected); more than one cached volume or host (ADR 0014 "Alternatives"); moving a volume between hosts (impossible, research §1.7); raising `maxInstances` above 1 (cache init and normal serving stay mutually exclusive).

## Design and contracts

### Provider interface extension (research §1.3, §1.7)

```go
// Added to Provider (ADR 0001, CORE-020); the six existing methods are unchanged.
type Provider interface {
    CreateVolume(ctx context.Context, r CreateVolumeRequest) (VolumeID, error)
    ListVolumes(ctx context.Context, labelPrefix string) ([]Volume, error)
    DeleteVolume(ctx context.Context, id VolumeID) error
}

type VolumeID int64

type CreateVolumeRequest struct {
    MachineID int64  // host the volume is pinned to (research §1.7)
    SizeGB    int    // storage.persistentCache.sizeGB; fixed after creation
    Label     string // vkllm:<installationId>:cache
}

type Volume struct {
    ID        VolumeID
    MachineID int64
    Label     string
    SizeGB    int
}

// CreateRequest (CORE-020) gains one field:
//     VolumeInfo *VolumeInfo // nil = no attach, the ADR 0005 ephemeral path
type VolumeInfo struct {
    VolumeID  VolumeID
    MountPath string // intended "/models"; see Open questions, Q40
}
```

The exact `volume_info` JSON shape Vast expects, and whether an attached volume survives `DestroyInstance` on the compute, are not stated in research §1.3/§1.7: this is **Q40**, verified by a probe (mirrors `hack/vastprobe`, CORE-020) before implementation, not guessed here.

### State document `cache` field (ADR 0004, reserved; ADR 0014 §1)

```json
"cache": {
  "volumeId": "987", "hostId": 55012, "machineId": 12345,
  "modelURL": "https://huggingface.co/.../Qwen3.6-35B-A3B-Q5_K_M.gguf",
  "sha256": "…", "sizeGB": 50,
  "createdAt": "2026-11-01T12:00:00Z", "lastVerifiedAt": "2026-11-01T18:30:00Z"
}
```

`null` until `cache init` succeeds, or after `cache delete`. `lastVerifiedAt` updates only on a successful checksum match at attach; a mismatch leaves it at the last good value, so `cache status` shows staleness rather than a false "fresh" reading. Both `hostId` and `machineId` are kept because offers and `ListInstances` key on `machineId` (research §1.4) while Vast also exposes a separate `host_id`; preferred-host search below filters on `machineId`. No storage rate is persisted here; see "Cost".

### Cache init (ADR 0014 §1)

Requires `desired == STOPPED` and no provider instance for this installation (`maxInstances` stays 1). `POST /api/v1/cache/init` in any other GPU state returns 400 `gpu_running`.

1. `cache.volumeId` already set → 200, idempotent (no `force`; `cache delete` first).
2. `SearchOffers` with the normal production filter, no `machine_id` constraint; cheapest offer wins.
3. `CreateVolume(machineId, storage.persistentCache.sizeGB)`.
4. `CreateInstance` with `VolumeInfo{VolumeID, "/models"}`, through the same provisioning saga CORE-050 already runs, plus the ADR 0005 download onstart.
5. `/health` 200 → compute sha256; a mismatch aborts the whole init (destroy instance and volume) rather than leaving `cache` half-written.
6. One CAS writes `cache.{volumeId, hostId, machineId, modelURL, sha256, sizeGB, createdAt, lastVerifiedAt}`.
7. `DestroyInstance` (compute only; Q40 confirms the volume is untouched).

### Preferred-host scheduling (ADR 0014 §2)

Runs only when `storage.preferredHost.enabled` and `cache.volumeId != ""`; otherwise every `start` is the unchanged ADR 0005 path.

```
cap      = maxHourlyPriceUSD × (1 + maxPricePremiumPercent/100)
deadline = now + storage.preferredHost.maxWait   // 30s
loop until deadline:
    offers = SearchOffers(production filter + machine_id == cache.machineId + dph_total <= cap)
    if offers non-empty: attach(offers[0]); return
    sleep(backoff)
allowFallback:  run the unchanged ADR 0005 flow (no machine filter)
!allowFallback: state -> STOPPED, unavailable: "no_offer"  // as FR-2.6
```

`attach(offer)` is `CreateInstance` with `VolumeInfo{cache.volumeId, "/models"}` and no download step; the `download` cold-start stage is recorded as `0s` with `skipped="true"` rather than omitted, so the metric stays comparable to the ephemeral path. A search failure here never fails `start` — "a preference, never a dependency" (ADR 0014 §2).

### Integrity check at attach (ADR 0014 §3)

Every attach, cached or fresh, re-computes the mounted file's sha256 before `llama-server` starts, as the ephemeral path already does.

| Result | Action |
|---|---|
| match | start `llama-server`; `cache.lastVerifiedAt = now` |
| mismatch, or file absent | delete, download once on this instance, leave `lastVerifiedAt` unchanged, `vkllm_cache_checksum_mismatch_total`++ |

### One volume per installation, label sweep (ADR 0014 §1; mirrors ADR 0003 §4)

Volume label `vkllm:<installationId>:cache` — one literal suffix, since at most one ever exists. Each reconcile tick also calls `ListVolumes(labelPrefix)`:

| Provider has | Document has | Action |
|---|---|---|
| labelled volume, id ≠ `cache.volumeId` (or `cache` null) | — | `DeleteVolume`; `vkllm_cache_volumes_swept_total`++ |
| no labelled volume | `cache.volumeId` set | `cache status` → `volume_missing` after two observations; preferred-host search skipped until the next `cache init` |
| exactly `cache.volumeId` | same | no action |

A failed `ListVolumes` never deletes anything (same rule as FR-5.2 for `ListInstances`).

### `vk-llm cache` commands and `/api/v1/cache*` endpoints

Same auth surface as `/api/v1/gpu` (management token, ADR 0012 §3); same error shape (`{"error": {"code","message","reason"?}}`).

| CLI | Endpoint | Success | Failure |
|---|---|---|---|
| `cache init` | `POST /api/v1/cache/init` | 202 cache document, `initializing`; 200 `already_initialized` | 400 `gpu_running`; 503 `gpu_unavailable` |
| `cache status` | `GET /api/v1/cache` | 200 cache document | 404 `not_initialized` |
| `cache list` | `GET /api/v1/cache?scope=provider` | 200 array of provider volumes under our label, each `managed: true/false` | — |
| `cache delete` | `DELETE /api/v1/cache` | 202 destroy in progress; 200 `already_absent` | 400 `gpu_running` |

Cache document: the `cache` field above plus `status` (`uninitialized \| initializing \| ready \| volume_missing`), `hostAvailable` (result of the last preferred-host search, `null` until one has run), and `estimatedStorageUSD`.

### Cost (ADR 0014 §4)

`storage_cost` ($/GB/month, research §1.2, §1.4) is captured from the offer chosen at `cache init`; it is **not** a persisted `cache` field (that list is fixed). Instead it folds into CORE-100's ledger as one more `LedgerEntry` with `Gen: 0` (reserved, never a real generation), `DPH = storage_cost × sizeGB / (24×30)`, `End` always `nil` while `cache.volumeId` is set. `EstimatedMTD` already sums every open entry, so no change to that function; `estimatedStorageUSD` is this entry's `EstUSD`. The rate refreshes opportunistically on the next successful `SearchOffers` for `cache.machineId`; between refreshes the last known rate stands, labelled an estimate like every other cost figure (CORE-100).

### Failure cases

| Case | Detection | Response |
|---|---|---|
| Host offline / no offer on `machineId` | empty `SearchOffers` through `maxWait` | fallback if `allowFallback`, else `no_offer` |
| Volume missing at provider | `ListVolumes` omits it for two ticks | `volume_missing`; search skipped; ephemeral start still works |
| Checksum mismatch at attach | sha256 compare | see "Integrity check"; start is slowed, not failed |
| Premium exceeded | every offer on `machineId` is above `cap` | treated as "no offer"; never pays above the premium |

### Configuration (`storage.*`, ADR 0014 §5; architecture.md §9)

```yaml
storage:
  strategy: ephemeral            # ephemeral | persistentCache
  persistentCache:
    enabled: false
    sizeGB: 50
  preferredHost:
    enabled: false
    allowFallback: true
    maxPricePremiumPercent: 20
    maxWait: 30s
```

This block is reserved and documented but **not rendered by the MVP Helm chart** (R9); the controller reads it only when this spec is implemented.

## Requirements

No FR ids exist for this feature (`docs/requirements.md` §13 lists it as out of scope for the initial release); FR/NFR ids are assigned when ADR 0014 is Accepted.

- R1. `CreateVolume`/`ListVolumes`/`DeleteVolume` use the same `Retryable`/`Terminal`/`Fatal` classification CORE-020 defines (source: CORE-020 "Errors").
- R2. The controller MUST NOT create a second volume while `cache.volumeId` is set (source: ADR 0014 §1).
- R3. A reconcile tick MUST destroy any provider volume under our label prefix that is not `cache.volumeId` (source: ADR 0014 §1; ADR 0003 §4).
- R4. Preferred-host search MUST NOT choose an offer priced above `cap` (source: ADR 0014 §2).
- R5. A preferred-host search failure MUST fall back when `allowFallback` is true and MUST NOT fail `start` (source: ADR 0014 §2).
- R6. Every attach, cached or fresh, MUST verify sha256 before `llama-server` starts (source: ADR 0014 §3; ADR 0005 §1).
- R7. `cache init` and `/api/v1/gpu/start` MUST be mutually exclusive while `maxInstances` is 1 (source: ADR 0003 §2; this spec).
- R8. `GET /api/v1/gpu/cost` MUST include `estimatedStorageUSD` whenever `cache.volumeId` is set, labelled an estimate (source: ADR 0014 §4; CORE-100).
- R9. The MVP Helm chart MUST NOT render any `storage.*` key (source: ADR 0014 §5; architecture.md §9).

## Implementation steps

1. Resolve Q40 with a probe; extend `Provider` (`vastai`, `fake`) with the three volume methods and `CreateRequest.VolumeInfo`. Test: fixture unit tests on `fake`.
2. Add the `cache` document field and its CAS transitions (ADR 0004 §5, schema bump). Test: load/save round trip with and without `cache`.
3. Implement `cache init` as a one-shot saga on CORE-050's existing state machine. Test: fake-provider simulation asserting one `CreateVolume` and a final `DestroyInstance` that leaves the volume alone.
4. Implement preferred-host search and fallback inside `start`. Test: simulation with a cached-host offer present/absent, under/over cap.
5. Implement the integrity check and download-skip on attach. Test: a fixture with a deliberately wrong sha256.
6. Implement the volume reconcile sweep. Test: an unlabelled-by-document volume triggers one `DeleteVolume`.
7. Implement the `Gen: 0` ledger entry and `estimatedStorageUSD`. Test: `EstimatedMTD` includes it; it never closes while `cache.volumeId` is set.
8. Implement the four `/api/v1/cache*` handlers and the `vk-llm cache` subcommands. Test: `httptest` contract tests per endpoint row; CLI golden-output tests.

## Tests / acceptance criteria

Gated: run only after CORE-140 is `DONE` and ADR 0014 is Accepted; none block the initial release.

1. AC1. `cache init` with one qualifying offer: exactly one `CreateVolume`, one `CreateInstance`, one `DestroyInstance`; `cache` populated with a matching sha256.
2. AC2. A second `cache init` with `cache.volumeId` set returns 200 `already_initialized`, zero provider calls.
3. AC3. With a cached-host offer inside the premium, `start` records `vkllm_coldstart_stage_seconds{stage="download",skipped="true"}` as `0`.
4. AC4. With no cached-host offer and `allowFallback: true`, `start` completes through the normal download path.
5. AC5. With no cached-host offer and `allowFallback: false`, `start` returns 503 `gpu_unavailable` `reason: no_offer` (FR-2.6 shape).
6. AC6. A corrupted cached file is detected at attach, re-downloaded, and `start` still reaches `READY`; `vkllm_cache_checksum_mismatch_total` increments by 1.
7. AC7. An unlabelled volume injected into the fake provider is destroyed within two reconcile ticks; `vkllm_cache_volumes_swept_total` increments.
8. AC8. `GET /api/v1/gpu/cost` shows a non-zero `estimatedStorageUSD` after `cache init` and `0` after `cache delete`.
9. AC9. `cache init` while `READY` returns 400 `gpu_running`, zero provider calls.

## Definition of done

- [ ] ADR 0014 status is `Accepted`.
- [ ] Q40 (attach shape; whether destroying the compute preserves the volume) is resolved by a probe, not assumed.
- [ ] CORE-140 is `DONE`.
- [ ] All tests above and AC1–AC9 pass in CI.
- [ ] `make specs-check` passes.
- [ ] Status flips to `DONE`, folder renamed `180-D-…`, evidence filled, in the same PR as the code.
- [ ] Requirements promoted here gain FR ids in `docs/requirements.md` and are removed from its §13 "out of scope" list.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Q40: exact `volume_info` JSON shape; mount-path field name | Attach cannot be coded without it | a probe before implementation | assume `{volume_id, mount_path}`; verify first |
| Does destroying attached compute also destroy/detach the volume? | If so, ADR 0014's cache-then-destroy design is wrong | same probe | assume the volume survives (research §1.7); abort the design if false |
| Is a volume billed while no instance is attached? | Determines whether `estimatedStorageUSD` applies with the GPU `STOPPED` | not in research; Vast support | assume yes, continuous like instance `storage_cost` |
| Maximum volumes per account | Could block `cache init` | not in research | assume ≥ 1; surface the provider error verbatim |
| Does `volume_info.mount_path` override the image's `/models` default? | Affects whether the worker image needs a change | CORE-070 at implementation time | assume caller-controlled; no image change |

## Evidence

None yet.
