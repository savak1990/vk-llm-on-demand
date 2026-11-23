---
id: "CORE-200"
status: "DRAFT"
updated: "2026-11-22"
depends_on: ["CORE-055", "CORE-130", "CORE-150", "CORE-190"]
adrs: ["0002", "0004", "0011", "0012", "0015"]
---

# 200 — Per-start parameters: model, quantization, GPU, idle timeout, price cap

**Status note:** DRAFT, problem statement only. No design, no decision
yet. Written 2026-11-22 after the first real installation (CORE-130)
showed that every value below is fixed at deploy time.

## Problem

An operator who runs `vk-llm start` cannot choose what to start. The
model, its quantization, the GPU to rent, the idle timeout and the maximum
hourly price are chart values. To change any of them, the operator edits
`gitops/values.yaml`, opens a pull request, merges it, waits for Argo CD to
redeploy the controller, and only then starts. That is a 10 to 20 minute
loop for a one-line change, and it changes the installation for everyone,
not for one session.

The same five values are the ones that move most often in practice:

| Value | Chart key today | Default | Why it moves |
|---|---|---|---|
| Model file | `config.model.url` + `config.model.sha256` | Qwen3.8-27B Q8_0 | trying another model or a smaller/larger quantization of the same model |
| Model id clients send | `config.model.alias` | `qwen3.8-27b` | follows the model |
| GPU | `config.provider.filter.gpuName` | `RTX 6000Ada` | price and availability change hour to hour; a bigger model needs more VRAM |
| Idle timeout | `config.lifecycle.idleShutdown.timeout` | `30m` | a long session wants 60m; a quick test wants 10m |
| Max price per hour | `config.lifecycle.safety.maxHourlyPriceUSD` | `0.80` | a bigger GPU costs more; a quick test tolerates less |

The precedent for a runtime-mutable value exists: CORE-190 moved the
monthly budget into the state document with a fixed Helm ceiling
(ADR 0015). This spec will decide whether and how the five values above
follow the same path, as parameters of `POST /api/v1/gpu/start` and of
`vk-llm start`.

## What the code does today (verified 2026-11-22)

- Every value is read once at process start into static structs
  (`cmd/gpu-on-demand/main.go`, `internal/config/*`) and copied into the
  controller's `Deps.Config`, `Deps.ModelAlias` and the worker adapter.
- `POST /api/v1/gpu/start` accepts an empty body or `{}` and rejects any
  field with 400 (`internal/mgmt/handlers.go`, `decodeStrict`). The start
  event carries no payload; it only sets `desired = RUNNING`.
- On restart, or on every reconcile tick while `desired = RUNNING`, the
  controller re-searches offers from config alone (`internal/controller/decide.go`).
  A value carried only in the start event would be lost.
- Concurrent starts are coalesced into one call; a later caller gets the
  first caller's result (`internal/mgmt/coalesce.go`). A start while a GPU
  already runs is a no-op that returns `already_running`.
- The state document (`internal/store/document.go`) records `Desired`,
  `Generation` and an `Instance` with the chosen offer (`MachineID`, `GPU`,
  `DPH`), but no model and no parameters.
- The worker is a generic llama.cpp image. The model reaches it as
  environment variables (`MODEL_URL`, `MODEL_SHA256`, `MODEL_ALIAS`, `CTX`,
  `PARALLEL`, `DEADMAN_SECONDS`) built per create
  (`internal/worker/create.go`). No image is baked per model.
  Quantization is only which GGUF file the URL names.
- The worker's dead-man timeout is computed once at boot as
  `idleShutdown.timeout + 10m` (`internal/config/worker.go`).
- `REASONING_FORMAT` is hard-coded to `deepseek` (`internal/worker/create.go`).
- The budget checker keeps its own copy of the max hourly price and uses it
  as a balance floor (`internal/budget/checker.go`).
- The gateway accepts only the configured alias in `/v1/chat/completions`
  (404 otherwise) and lists only that alias in `/v1/models`, with no GPU
  and no store read (`internal/gateway/chat.go`, `models.go`). There is no
  hot reload.
- The worker adapter panics on an invalid model value because validation
  happens once at boot (`cmd/gpu-on-demand/worker_adapter.go`).
- The CLI defines its flags twice (`cmd/vk-llm/commands.go` and
  `config.go`); precedence is flag > environment > file > default, and the
  config file rejects unknown keys.
- `scripts/gpu-control.sh` sends `start` with no body; the workflow's inputs
  are `action` and `monthly_usd`.

## Invariants that must survive any design

- No offer above a configured ceiling is ever created (FR-2.2, FR-6.1). A
  per-start price can lower the cap, never raise it past the Helm value.
- The idle timeout cannot be disabled without `maxLifetime` (FR-3.12); a
  per-start timeout needs a minimum and a maximum, and `0` must not mean
  "off".
- A repeated `start` must not extend the idle deadline (FR-3.4).
- The reconcile loop must destroy any instance it does not recognize
  (ADR 0004); per-generation parameters must be in the document, not in
  memory.
- `GET /v1/models` must answer with no GPU (FR-1.2).
- Secrets never enter a request body or a log line.
- `docs/requirements.md` §13 lists "dynamic model selection" as out of
  scope; this spec amends that line or narrows it to "one model at a time,
  chosen per start".

## Decisions to make (none made)

1. **Public model id.** Does `/v1/models` list a stable id that never
   changes (clients configure once), the id of the model that is running
   (clients re-configure per model), or does the gateway accept any id?
2. **Start with different parameters while a GPU runs.** Reject with 409,
   replace (stop, then start), or ignore and return `already_running`? The
   coalescer must then key on parameters or reject a mismatch.
3. **Where the ceilings live.** Which of the five values get a Helm ceiling
   (price, idle timeout, allowed GPU list?) and which are free? Follow
   ADR 0015: "Helm values become defaults and ceilings; per-start values
   live in the document."
4. **How a model is named in a request.** A raw `{url, sha256, alias}`
   triple per request, or a catalogue of named presets (in the chart, in
   the document, or both) with the triple behind each name? A raw URL
   means the API accepts arbitrary download targets; a catalogue means a
   commit per new model but a safe request surface.
5. **Which GPU names are allowed.** A free string (any Vast.ai `gpu_name`)
   or an allow-list in the chart? A free string plus a price ceiling may be
   enough.
6. **Where the parameters are recorded.** A `StartParams` block written
   with `desired = RUNNING` and copied onto `Instance` at offer choice
   (generation increment), so restart recovery, probes, the idle timer,
   the dead-man value and the status document all read one place.
7. **Status document.** `model.alias`/`contextSize` today come from static
   config; they should come from the instance. Which new fields does the
   document expose (`params`, `instance.gpu` already exists)?
8. **CLI and workflow surface.** Flag names (`--model`, `--gpu`,
   `--idle-timeout`, `--max-price`), `defaults.*` keys in `config.yaml`,
   and `gpu-control.yml` inputs. JSON must be built with `jq --arg`, never
   by string interpolation.
9. **Disk.** The worker disk equals `provider.filter.minDiskGB` (60). A
   larger quantization or a BF16 file needs more; does disk become a
   derived value (file size × 1.5) or another parameter?
10. **Interaction with CORE-180.** The persistent-volume cache (ADR 0014)
    assumes one model per installation. Per-start models either bypass the
    cache when the file differs, or the cache keys by sha256.

## Known obstacles, from the exploration

1. Static config everywhere; fixable because Facts are rebuilt per event
   (`internal/controller/loop.go`).
2. Re-search on restart or tick reads config; parameters must be persisted.
3. The start coalescer and the T3 no-op merge or ignore differing
   parameters.
4. The gateway alias check and `/v1/models` read static config.
5. The dead-man timeout is boot-time; `REASONING_FORMAT` is hard-coded and
   model-specific; the worker disk equals the filter minimum.
6. The budget balance floor reads its own copy of the max hourly price.
7. The worker adapter's panic path must become an error once input is no
   longer validated at boot.
8. CORE-210 resolves the model size at runtime per URL and sizes the disk
   from it; a per-start model only changes those inputs. A per-start price
   cap can only lower the Helm caps (`maxHourlyPriceUSD`,
   `maxDownloadPriceUSDPerGB`, `maxStoragePriceUSDPerGBMonth`).

## Out of scope for this spec

More than one worker at a time, more than one model loaded at once,
providers other than Vast.ai, on-demand provisioning mode (ADR 0010 §4).

## References

- [CORE-190](../190-D-runtime-budget-reconfiguration/spec.md) — the
  runtime-mutable budget, the precedent.
- [CORE-130](../130-D-platform-integration/spec.md) — the installation this
  applies to.
- [ADR 0015](../../../docs/adr/0015-runtime-mutable-budget-cap.md) — document
  value plus fixed Helm ceiling.
- [ADR 0012](../../../docs/adr/0012-management-api-cli-and-github-actions.md) —
  the `start` contract this spec will extend.
- [ADR 0011](../../../docs/adr/0011-rolling-inactivity-timeout.md) — the idle
  timeout rules a per-start value must keep.
- [ADR 0014](../../../docs/adr/0014-persistent-volume-cache-and-preferred-host.md) —
  the cache that assumes one model.
- `docs/requirements.md` §13 — the out-of-scope line to amend.

## Evidence

None. Problem statement only.
