# ADR 0022: Inkling 2 Mini replaces Qwen as the served model

## Status

Accepted (2026-11-27). Supersedes decision 2 (the model), the alias in
decision 5, and decision 6 (reasoning control) of ADR 0002. ADR 0002's
runtime, arguments, readiness contract and the gateway's 404 for any other
`model` stay.

## Context

The lab served `Qwen3.8-27B` Q8_0 (28.6 GB) since #38, a values change that
left ADR 0002 naming Qwen3.6. Inkling 2 Mini (Thinking Machines, 2026-11-20)
is a 26.4B MoE with 3.4B active parameters and leads Qwen3.8-27B on 32 of 48
published benchmarks. Its Q8_0 GGUF is 27.8 GB, one file, and fits the 48 GB
RTX 6000 Ada ([research §9](../research.md)).

## Decision

1. **Model:** `ggml-org/Inkling-2-Mini-GGUF`, file
   `Inkling-2-Mini-Q8_0.gguf`, pinned by URL and sha256 in
   `gitops/values.yaml`. Text only: `--no-mmproj` stays.
2. **Alias:** `inkling-2-mini`. Open WebUI's `DEFAULT_MODELS` and
   `TASK_MODEL_EXTERNAL` repeat it (ADR 0020).
3. **Runtime floor:** a `server-cuda` build that contains llama.cpp commit
   `2a335259c` (CUDA flash attention for Inkling 2), pinned by tag and
   digest. The first pin is `server-cuda-b12808`.
4. **Reasoning:** `reasoning_content` passes through as before
   (`--reasoning-format deepseek`). Clients control thinking with the request
   field `reasoning_effort`, not `chat_template_kwargs.enable_thinking`.
   Reasoning cannot be fully disabled; `gateway.maxTokensCap` bounds a loop.

## Alternatives considered

- **Keep Qwen3.8-27B.** No benchmark gain; no work.
- **v1.1.0 release image.** Loads Mini but runs dense attention on CUDA;
  4 × 131072 tokens will not fit 48 GB.
- **IQ4_NL or NVFP4 (17.6 GB).** Frees 10 GB for context; Q8_0 is
  near-lossless and fits, so quality wins.
- **Vision via mmproj.** Needs worker script changes; out of scope.

## Consequences

- A nightly image is less tested than a release. Move the pin to the first
  semver release that contains `2a335259c`.
- The KV-cache size is an estimate until the first prod start measures it;
  `contextSize` and `parallel` may need to drop.
- Open WebUI's title and search-query tasks use the same model; reasoning
  makes them slower and they share the 4 slots.
- Clients that name `qwen3.8-27b` get `404 model_not_found` until edited.
