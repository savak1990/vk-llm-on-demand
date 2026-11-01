# ADR 0014: Model caching on a Vast.ai persistent volume with preferred-host scheduling (P2)

## Status

**Proposed** (2026-11-01), priority P2, not in the initial release. Extends
[ADR 0005](0005-model-storage.md), whose Status carries a pointer here.

## Context

Every provision downloads the 26 GB GGUF (ADR 0005). Vast.ai sells
persistent volumes that are tied to one physical host and cannot move
([research §1.7](../research.md)). A volume on a host that also has a
suitable L40S removes the download from the cold start, at a monthly
storage cost and with no guarantee that the host has a free GPU at an
acceptable price when the user starts.

## Decision (proposed)

1. **Cache lifecycle.** `vk-llm cache init` creates one volume of
   `storage.persistentCache.sizeGB` (50) on a host chosen with the
   production offer filter, rents a GPU there once, downloads and verifies
   the model, records `{volumeId, hostId, machineId, modelURL, sha256,
   createdAt}` in the state document, and destroys the compute. `cache
   list`, `cache status`, `cache delete` manage it. At most one volume per
   installation; the reconcile loop destroys any volume carrying our label
   that the document does not know.
2. **Preferred-host scheduling.** When `storage.preferredHost.enabled`
   is true, the offer search first asks for offers on the cached
   `machineId` that satisfy the filter and cost at most
   `maxHourlyPriceUSD × (1 + maxPricePremiumPercent/100)`. If one exists
   it is chosen and the volume is attached at `/models`; the download
   stage is skipped after a checksum of the cached file. If none exists
   within `maxWait` (30s of search attempts), and `allowFallback` is true,
   the normal search runs and the model is downloaded on the fallback host.
   The cache is a preference, never a dependency.
3. **Integrity.** The cached file's sha256 is verified at every attach;
   a mismatch deletes the file and downloads again.
4. **Cost visibility.** Storage spend is estimated as
   `sizeGB × storage_cost per GB-month` from the offer, reported in
   `GET /api/v1/gpu/cost` as `estimatedStorageUSD`, and counted toward the
   monthly budget.
5. **Configuration (future block, not rendered by the MVP chart):**

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

## Alternatives considered

- **S3 mirror** (ADR 0005). Portable across hosts, no host lock-in, but
  still a full download per provision; stays the fallback for Hugging Face
  throttling and is compatible with this ADR.
- **Keep the instance stopped.** Rejected in ADR 0003 §6.
- **Several cached volumes on several hosts.** More storage cost for a
  marginal availability gain; one volume first.

## Rationale

The download is the largest controllable cold-start stage. A single volume
on a host with the right GPU removes it most of the time, and the fallback
keeps availability unchanged when it does not.

## Consequences

- Spec 180 (new, P2) holds the full design; the MVP chart renders no
  `storage.*` keys; the state document reserves a `cache` field.
- The provider interface gains volume calls (`CreateVolume`,
  `ListVolumes(label)`, `DeleteVolume`, attach via `volume_info` on create,
  research §1.3) when the spec is implemented.
- `vk-llm` gains the `cache` subcommand group (not in MVP).

## Risks

- The cached host disappears or raises prices; mitigated by fallback and
  by `cache status` showing host availability.
- Forgotten volumes bill monthly; mitigated by the one-volume rule, the
  label sweep, and `cache status` in `vk-llm status` output.
- Volume semantics on Vast change; the spec re-verifies research §1.7
  before implementation.
