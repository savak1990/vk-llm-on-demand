---
id: "CORE-300"
status: "IN_PROGRESS"
updated: "2026-11-24"
depends_on: ["CORE-070", "CORE-080", "CORE-130"]
adrs: ["0002", "0008"]
---

# 300 — Concurrent sessions: KV cache type and per-session context

**Status note:** IN_PROGRESS (2026-11-24). Design agreed with the user in
chat on 2026-11-24 after the first on-demand sessions: two OpenCode
sessions (and an agent's subagents) queued behind each other on one
`llama-server` slot. Implemented on branch `worker-kv-cache-config`.

**Complexity:** S — two new chart values, one changed meaning, one worker
flag pair, a gitops pass-through.
**Risk:** medium — a wrong combination of `parallel`, `contextSize` and
cache type does not fit in VRAM and the worker never becomes ready.
**Estimated effort:** one session (2–3 h with the live check)
**Recommended model:** standard
**Depends on:** [CORE-070](../070-D-worker-runtime/spec.md) (worker
launch), [CORE-080](../080-D-helm-chart/spec.md) (chart values),
[CORE-130](../130-D-platform-integration/spec.md) (gitops).

## Outcome

The operator chooses how many sessions the GPU serves at the same time,
how much context each one gets, and how compactly the KV cache is stored,
with chart values only. The lab runs 4 concurrent sessions of 131,072
tokens each with an 8-bit KV cache on a 48 GB GPU. Two OpenCode sessions,
or an agent's subagents, answer at the same time instead of queuing.

## Problem

`llama-server` runs with `-np 1` today. Every request beyond the first
waits in its queue until the first answer is complete. On 2026-11-24 the
user ran two OpenCode sessions at once, and agents that start several
subagents "in parallel" effectively run them one after another. A queued
request also spends part of the gateway's 10-minute generation timeout
waiting.

Measured on the lab (RTX 6000 Ada, 48 GB, Qwen3.8-27B Q8_0, 28.6 GB file,
`-c 262144 -np 1`, f16 KV cache): **42.9 GB VRAM in use**. About 14 GB of
that is the KV cache for 262,144 tokens plus working buffers.

Two facts of llama.cpp shape the design:

- `-c` is the **total** context, divided between the `-np` slots. With
  `-c 262144 -np 2` each session gets 131,072 tokens, which surprises
  anyone who reads `contextSize` as "context per session".
- The KV cache can be stored in 8 or 4 bits (`--cache-type-k`,
  `--cache-type-v`; a quantized V cache needs flash attention, which the
  worker already enables). Relative to f16, `q8_0` takes about 53 % and
  `q4_0` about 28 % of the memory. A 4-bit K cache costs the most
  quality on long contexts.

## Scope

Chart values `config.model.kCacheType` and `config.model.vCacheType`; the
meaning of `config.model.contextSize` (per session); the worker's
`llama-server` flags; the gitops pass-through of `parallel`,
`kCacheType`, `vCacheType`; docs.

Excludes: an automatic VRAM fit check (the operator chooses values that
fit; the README gives the rule of thumb); a different inference server
(vLLM's paged KV cache would remove the fixed split, but it means a new
ADR replacing ADR 0002, a different model format and longer cold starts);
per-request or per-key slot reservation; changing the chart defaults.

## Design and contracts

### Values

| Chart value | Env | Default | Meaning |
|---|---|---|---|
| `config.model.parallel` | `VKLLM_MODEL_PARALLEL` | `1` | sessions served at the same time (`-np`) — unchanged |
| `config.model.contextSize` | (existing) | `32768` | context **per session**; the worker reserves `contextSize × parallel` tokens (`-c`) |
| `config.model.kCacheType` | `VKLLM_MODEL_K_CACHE_TYPE` | `f16` | `f16`, `q8_0` or `q4_0` (`--cache-type-k`) |
| `config.model.vCacheType` | `VKLLM_MODEL_V_CACHE_TYPE` | `f16` | `f16`, `q8_0` or `q4_0` (`--cache-type-v`) |

With `parallel: 1` and f16, the worker's flags are exactly today's.

### Worker

`worker.CreateParams` gains `KCacheType`, `VCacheType`. `BuildCreate`
rejects any other value, sets env `K_CACHE_TYPE`, `V_CACHE_TYPE`, and sets
`CTX` = `Ctx × Parallel`. The worker script requires both new variables
and passes `--cache-type-k "$K_CACHE_TYPE" --cache-type-v "$V_CACHE_TYPE"`.
The values reach a worker when it is created; a running instance keeps
its flags until the next start.

### Lab values (promotion after the release)

`parallel: 4`, `contextSize: 131072`, `kCacheType: q8_0`,
`vCacheType: q8_0`. Estimate: model about 28.6 GB + KV cache for
524,288 tokens at q8_0 (about 13 GB) + buffers ≈ **43–44 GB of 48 GB**.
To be confirmed by the live check.

### VRAM rule of thumb (README)

VRAM ≈ model file + KV cache + about 1.5 GB of buffers. For the lab model,
an f16 KV cache takes about 12 GB per 262,144 tokens of total context;
`q8_0` about 53 % of that, `q4_0` about 28 %. Set an OpenCode model's
`limit.context` to `contextSize`, so OpenCode compacts a session before
the server refuses it.

## Requirements

- R1. `kCacheType` and `vCacheType` MUST accept exactly `f16`, `q8_0`,
  `q4_0`; the schema, the config loader and `BuildCreate` MUST reject
  anything else.
- R2. The worker MUST pass `-c` = `contextSize × parallel` and the two
  cache-type flags to `llama-server`.
- R3. With chart defaults, the rendered worker script and the create
  request MUST be identical to today's except for the two new env
  variables (f16) and the matching flags.
- R4. The gitops chart MUST pass `parallel`, `kCacheType` and
  `vCacheType` through to the chart.
- R5. The docs MUST state that `contextSize` is per session and give the
  VRAM rule of thumb.

## Implementation steps

1. Config, chart schema and helpers. Check: config tests; `make helm-lint
   helm-template`; schema rejects `fp8`.
2. Worker create request and script. Check: create tests (`CTX` = 524288
   for 131072 × 4); golden script.
3. Adapter and gitops pass-through. Check: adapter test; `make
   gitops-check`.
4. Docs: spec 080 values table, README note.
5. Release, then a promotion PR with the lab values; live check.

## Tests / acceptance criteria

- AC1. Config defaults `f16`/`f16`; `q8_0` accepted; `fp8` rejected.
- AC2. `BuildCreate` with `Ctx 131072, Parallel 4, q8_0/q8_0` sets
  `CTX=524288`, `K_CACHE_TYPE=q8_0`, `V_CACHE_TYPE=q8_0`; an unknown type
  is rejected.
- AC3. The golden worker script contains `--cache-type-k "$K_CACHE_TYPE"
  --cache-type-v "$V_CACHE_TYPE"` and requires both variables.
- AC4. `make gitops-check` passes; the rendered Application carries the
  three values.
- AC5. Live, lab values: the worker becomes ready, VRAM in the Vast
  console is below 48 GB (record the number), and two OpenCode sessions
  stream answers at the same time (record tok/s for each).

## Definition of done

- [ ] AC1–AC4 pass; `make specs-check go-build go-test go-lint helm-lint
      helm-template gitops-check` exit 0.
- [ ] Release published; lab promoted with the lab values.
- [ ] AC5 recorded under Evidence.
- [ ] Status DONE, folder `300-D-...`.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Real VRAM with 4 slots and q8_0 | the estimate may be off by the size of the per-slot buffers | AC5 | lower `parallel` to 3 if it does not fit |
| Per-stream speed with 4 concurrent streams | user-visible latency | AC5 | accept if each stream stays above 15 tok/s |
| Quality of q8_0 KV on long agent sessions | silent degradation | a long OpenCode session after AC5 | switch V only to f16 if a regression shows |
| Does this llama.cpp build split `-c` per slot or use one unified KV pool? | a unified pool lets one session exceed `contextSize` while others are short | `GET /slots` on the worker during AC5 | the docs describe the split; amend them if the pool is unified |

## Evidence

None yet.
