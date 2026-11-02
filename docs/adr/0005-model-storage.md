# ADR 0005: Model weights are downloaded from Hugging Face on every provision

## Status

Accepted for the MVP (2026-11-01). The P2 persistent-volume cache with
preferred-host scheduling is [ADR 0014](0014-persistent-volume-cache-and-preferred-host.md) (Proposed).

**Amendment, 2026-11-02 (spec [030](../../specs/core/030-D-worker-launch-spike/spec.md), research §7.1).**
Two real runs measured the download stage (fetch plus sha256 check,
25,913,185,920 bytes via `aria2c -x 16 -s 16`, no curl fallback needed):
**403.9 s** on one host, **289.3 s** on another — fetch time alone was
387.2 s and 259.9 s. One of the two samples (403.9 s / 387.2 s) exceeds the
5-minute (300 s) trigger below; the other does not. With only two samples
and real host-to-host variance already this wide, promoting the S3 mirror
to the MVP default from one over-threshold sample is a bigger call than
this spike's mandate covers. The promotion is **not** made here, and the
trigger text below is **not** removed or silently satisfied — the observed
range (289–404 s) is recorded, and whether to promote the mirror is left
to spec 140's repeated real runs, which will have more samples to decide
from.

## Context

The Q5_K_M file is 25.9 GB. Vast.ai storage options are machine-local
volumes that cannot move between hosts, stopped instances that keep paying
storage and may not restart, cloud sync from object storage, and host-side
image caches we do not control ([research §1.3, §1.7](../research.md)).
Instances are destroyed after 20 idle minutes (ADR 0003), so any per-host
cache is lost with the host.

## Decision

1. The worker's `onstart` script downloads the pinned GGUF from Hugging Face
   on every provision into the instance's ephemeral disk, verifies the
   sha256, and only then starts `llama-server` (spec 070).
2. The download uses a parallel, resumable fetcher: `aria2c -x 16 -s 16` when
   the image can install it quickly, otherwise `curl` with ranged parallel
   parts. The script fails fast on checksum mismatch and the controller
   destroys the instance.
3. Offer filters enforce the inputs the strategy depends on:
   `disk_space >= 60` GB, `inet_down` and `disk_bw` at or above thresholds set
   in chart values (initial values 500 MB/s and 500 MB/s, units verified by
   spec 020).
4. Cold-start stages are measured and exported separately
   (`vkllm_coldstart_stage_seconds{stage=…}`): search, create, boot, link,
   download, load, ready, first token.
5. A Hugging Face token is **not** required for the chosen public file and is
   not part of the MVP.

## Alternatives considered

| Option | Monthly fixed cost | Expected prep time | Portability | Why not now |
|---|---|---|---|---|
| Download from Hugging Face each time (**chosen**) | $0 | 26 GB at 200–800 MB/s ≈ 1–3 min | any host | HF throttling is a risk; measured in spec 030 |
| Vast.ai volume | ≈ $0.05–0.20 per GB-month × 30 GB ≈ $1.5–6, plus the volume pins us to one machine that may be rented out | seconds | one host only | defeats marketplace choice; adds a second resource to reconcile and clean |
| Keep the instance stopped | storage billed continuously, often at a higher stopped rate | seconds if the GPU is free | one host | restart may never schedule; contradicts ADR 0003 |
| S3-compatible mirror (Hetzner Object Storage or AWS) | ≈ $0.5–1 for 26 GB plus egress per download | similar to HF, more predictable | any host | an extra bucket and credential; adopt only if HF proves slow or throttled |
| Model baked into a custom image | registry storage; 26 GB pull per provision | slower than HF unless host-cached | any host | pull speed is not better and we lose the official image |

## Rationale

The marketplace gives us a different host each time, so the only portable
cache is the public internet. Two to three minutes of download on a fast
host is within the cold-start budget, costs nothing while idle, and needs no
cleanup because destroying the instance removes everything.

## Consequences

- Cold start is bounded below by the download: on a 200 MB/s host about two
  minutes, on a 1 GB/s host under a minute. `gpu.startupDeadline` (15 min)
  and `gateway.maxWait` (8 min, ADR 0007) are sized with this in mind.
- If the measured download in spec 030 exceeds 5 minutes on hosts that pass
  the filters, the S3 mirror becomes the MVP default by a values change and
  a one-paragraph amendment here. **Measured (2026-11-02, research §7.1):**
  403.9 s on one real host (over the trigger) and 289.3 s on another (under
  it). The trigger fired on one of two samples; see the Status amendment
  above for why the mirror is not promoted from this alone.
- Bandwidth is billed by Vast per byte; 26 GB inbound per provision at the
  snapshot's typical `inet_down_cost` is cents, recorded in the cost model.

## Risks

- Hugging Face rate limiting or outage blocks every cold start. Mitigation:
  the mirror fallback above; the controller surfaces the failure as a
  distinct stage so the alert names the cause.
- A host advertises bandwidth it does not deliver. Mitigation: the startup
  deadline destroys it and the next attempt excludes that `machine_id` for
  an hour.
