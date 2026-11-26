---
id: "CORE-310"
status: "IN_PROGRESS"
updated: "2026-11-26"
depends_on: ["CORE-040", "CORE-050", "CORE-055", "CORE-090", "CORE-150", "CORE-210"]
adrs: ["0004", "0005", "0018"]
---

# 310 — Cold-start recording: rental history and real stage durations

**Status note:** DRAFT, written 2026-11-26 after a design session with the
operator. It is the first of three specs for faster cold starts: this one
(records), [CORE-320](../320-P-vast-volume-spike/spec.md) (a spike on
Vast.ai volumes) and [CORE-180](../180-P-persistent-volume-cache/spec.md)
(the model cache on a volume). It has no dependency on the other two.

**Complexity:** S
**Risk:** low — it adds records and metrics; no decision about a rental changes
**Estimated effort:** 3 to 4 h
**Recommended model:** standard
**Depends on:** [CORE-040](../040-D-state-store/spec.md), [CORE-050](../050-D-gpu-lifecycle-controller/spec.md), [CORE-090](../090-D-observability/spec.md), [CORE-210](../210-D-offer-cost-model/spec.md)

## Problem

The operator saw rentals in South Korea and slow cold starts, and asked if
the host location or a repeat of the same host makes a start faster. No
record can answer this today (verified 2026-11-26 on the lab):

- The offer's machine id, geolocation and advertised `inet_down` are in
  `instance.offer` only while the GPU runs. After destroy, `instance` is
  `null` and the data is lost.
- `vkllm_coldstart_stage_seconds` pre-registers 8 stages, but only
  `first_token` gets values (`internal/controller/metrics.go:71`). The worker
  writes a timestamp for each of its stages, and `ParseProgress` discards the
  timestamps (`internal/worker/download.go:28`).
- The lab keeps logs and metrics for 7 days (`lokiRetention: 168h`,
  `prometheusRetention: 7d` in `vk-lab-platform`), so logs alone cannot
  hold two weeks of data.

Data from the last 10 starts (2026-11-24 to 11-25, from the ledger and
`vkllm_gpu_state`): a normal start took 2:16 to 3:11; two starts took 5:36
and 6:32. The model phase (download, sha256, load) was the largest phase in
8 of 9 good starts, at an estimated 95 to 270 MB/s.

## Outcome

Each rental, good or failed, leaves one record in a bounded `rentals` list in
the state document: the host (machine id, geolocation, GPU, $/h, advertised
`inet_down`), the duration of each cold-start stage, the measured download
speed, the outcome and the cost. `vk-llm rentals` prints the list as a table.
`vkllm_coldstart_stage_seconds` gets a value for every stage, and one
`rental_ready` log line holds the same data as the record. After about two
weeks the operator can decide from data if a location preference is useful.

## Scope

- The `rentals` field in the state document, its writer and its bound.
- Stage durations from controller and worker timestamps.
- `GET /api/v1/gpu/rentals` and `vk-llm rentals`.
- One `rental_ready` log line and the stage histogram values.

Excludes: any change to offer selection (the operator decided on
2026-11-26: no location preference, no last-machine preference); the cache
hit and miss counter (CORE-180 adds it, because nothing produces a hit
before CORE-180); Postgres or any other new store (the operator decided on
2026-11-26: the state ConfigMap); a dashboard panel.

## Design and contracts

### Rental record

```json
"rentals": [
  {
    "gen": 10,
    "origin": "demand",
    "startedAt": "2026-11-25T09:37:40Z",
    "readyAt": "2026-11-25T09:44:12Z",
    "endedAt": "2026-11-25T09:47:55Z",
    "outcome": "ready",
    "machineId": 12345,
    "geo": "Quebec, CA",
    "gpu": "L40S",
    "dph": 0.643,
    "inetDownMbps": 2402,
    "estDownloadSeconds": 96,
    "modelBytes": 28595763840,
    "stages": {"search": 1, "create": 1, "boot": 105, "link": 6,
               "download": 285, "load": 9, "ready": 296},
    "downloadMBps": 100.3,
    "costUSD": 0.18
  }
]
```

| Field | Source |
|---|---|
| `gen`, `origin`, `startedAt` | the start transition (`generation`, `startOrigin`) |
| `machineId`, `geo`, `gpu`, `dph`, `inetDownMbps`, `estDownloadSeconds`, `modelBytes` | `instance.offer` and `instance.offer.estimate` of the chosen offer |
| `readyAt` | the transition to READY |
| `outcome` | `ready`, or the provision outcome label of `vkllm_provision_attempts_total` (`failed_status`, `deadline_exceeded`, `cancelled`, …) |
| `stages`, `downloadMBps` | see "Stage durations" |
| `endedAt`, `costUSD` | the destroy transition; `costUSD` is the ledger entry's `estUsd` for this `gen` |

The list keeps the last 50 records, oldest first; `Encode` trims it the same
way it trims `ops`. 50 records are about 15 KB, well inside the 256 KiB
document limit. The field is additive with `omitempty`, so no schema bump: an
older binary ignores it and drops it on its next write, the same rule as
`budgetMonthUsd` (ADR 0015).

A record is written in the same CAS transition that already runs at each
point (READY, failure, destroy), never in a separate write.

### Stage durations

Each duration comes from two timestamps of one clock, never one controller
and one worker timestamp, because the two clocks can differ by seconds.

| Stage | From | To | Clock |
|---|---|---|---|
| `search` | start accepted | offer chosen | controller |
| `create` | create sent | provider returns the instance id | controller |
| `boot` | instance id | provider reports `running` with an SSH endpoint | controller |
| `link` | `running` | link up (`EvLinkUp`) | controller |
| `download` | `download_begin` | `verified` (fetch plus sha256) | worker |
| `load` | `server_spawn` | `server_healthy` | worker |
| `ready` | link up | READY (probe passed) | controller |

`ready` is the whole model phase as the controller sees it, so it is at
least `download + load` when the download starts after link up. The worker
starts the download at container start, so on a fast boot part of
`download` runs before link up; the two worker stages show that split.

`downloadMBps` is `modelBytes / 1e6 / (download_end − download_begin)`, so
sha256 time is not in the speed. If the worker wrote `download_skipped`, the
`download` stage is `0` and `downloadMBps` is absent.

After the READY transition, the controller reads the worker stage log once
in the background, over the existing link, with `worker.ProgressCommand`.
`ParseStages` already returns the timestamps
(`internal/worker/stages.go:17`). The values stay in memory, the
`rental_ready` line is logged after the read, and the destroy transition
writes them into the record. A failed read leaves the worker stages out of
the record and never delays READY. A controller restart between READY and
destroy also loses them; the controller stages and the offer data are
already in the record from the READY transition.

Each known stage value goes to `vkllm_coldstart_stage_seconds{stage}`. The
label set does not change.

### Log line

At READY the controller logs one line, `msg=rental_ready`, with the record's
fields flattened (`gen`, `machine_id`, `geo`, `gpu`, `dph`,
`inet_down_mbps`, `download_mbps`, `stage_<name>_s`, `total_s`). No key, no
token and no prompt is in the line.

### API and CLI

`GET /api/v1/gpu/rentals` returns `{"rentals": [...]}`, newest first. It uses
the management token, as the other `/api/v1/gpu/*` routes (ADR 0012).

`vk-llm rentals` prints one row per rental:

```
GEN  STARTED           WHERE       $/H    BOOT  DOWNLOAD      LOAD  TOTAL  OUTCOME
10   11-25 09:37 UTC   Quebec, CA  0.643  1:45  4:45 100MB/s  0:09  6:32   ready
```

## Requirements

- R1. Every start that reaches the create step MUST leave one record in
  `rentals`, with outcome `ready` or the failure outcome (source: this spec).
- R2. `rentals` MUST hold at most 50 records; `Encode` MUST drop the oldest
  (source: ADR 0004, document size limit).
- R3. A record MUST be written inside a transition that already writes the
  document; the feature MUST NOT add a CAS write (source: ADR 0004).
- R4. A stage duration MUST use two timestamps from the same clock (source:
  this spec).
- R5. A failed read of the worker stage log MUST NOT delay or fail READY
  (source: CORE-050).
- R6. The record, the log line and the endpoint MUST NOT contain a key, a
  token, an SSH host or a prompt (source: constitution, nothing sensitive).
- R7. Offer selection MUST NOT change (source: operator decision 2026-11-26).

## Implementation steps

1. Add `Rental` and `Document.Rentals` with the 50-record trim in `Encode`.
   Test: encode 60 records, decode 50, oldest dropped.
2. Record controller timestamps for `search`, `create`, `boot`, `link` and
   `ready`. Test: fake provider and fake clock, assert the five values.
3. Read the worker stage log at READY and compute `download`, `load` and
   `downloadMBps`. Test: fixture stage log, including `download_skipped` and
   a failed read.
4. Write the record at READY, at a failure and at destroy; send the stage
   values to the histogram; log `rental_ready`. Test: simulation of one good
   and one failed start, assert two records and the histogram counts.
5. Add `GET /api/v1/gpu/rentals` and `vk-llm rentals`. Test: `httptest`
   contract test; CLI golden output.

## Tests / acceptance criteria

1. AC1. After one simulated good start and stop, `rentals` holds one record
   with all seven stages, `downloadMBps > 0`, `endedAt` and `costUSD` set.
2. AC2. A simulated start that ends in `failed_status` holds one record with
   that outcome and no `readyAt`.
3. AC3. With 60 simulated rentals the document holds 50. The full document
   (100 ledger entries, 20 ops, 10 denylist entries, 50 rentals) stays
   below 64 KiB; AC10 of CORE-040 is unchanged.
4. AC4. `/metrics` shows `vkllm_coldstart_stage_seconds_count` ≥ 1 for each
   of the seven start stages after one good simulated start.
5. AC5. On the lab, after one real start (operator go-ahead needed, about
   $0.20): `vk-llm rentals` shows the row, and its `download` and `boot`
   values are within 15 s of the `vkllm_gpu_state` timeline.

## Definition of done

- [ ] AC1 to AC4 pass in CI; AC5 is recorded under Evidence.
- [ ] `make go-build go-test go-lint helm-lint helm-template specs-check` passes.
- [ ] Status `DONE`, folder renamed `310-D-…`, in the same PR as the code.

## Open questions

None.

## Evidence

None yet.
