# ADR 0002: llama-server from the official CUDA image serves a pinned Qwen3.6 GGUF

## Status

Accepted (2026-11-01). The model file's sha256 and the exact image digest are
pinned by spec 070 when it is implemented; this ADR fixes the choice, not
the digest.

## Context

The brief targets `Qwen3.6-35B-A3B` at Q5_K_M on one 48 GB L40S, 32K
context first and 64K later, concurrency 1 first, streaming, tool calling,
with a possible move to vLLM later. Research confirmed the model exists
(`Qwen/Qwen3.6-35B-A3B`, Apache-2.0, released 2026-04-15), that there is no
official GGUF, that `bartowski/Qwen_Qwen3.6-35B-A3B-GGUF` ships a 25.9 GB
single-file Q5_K_M, and that llama.cpp has supported the architecture since
b7990 ([research §2–3](../research.md)). Weights plus KV cache at 64K fit in
roughly 26–28 GiB.

## Decision

1. **Runtime:** `llama-server` from `ghcr.io/ggml-org/llama.cpp:server-cuda`,
   pinned by tag and digest to a build ≥ b12032 in chart values. No custom
   image in the MVP.
2. **Model:** `bartowski/Qwen_Qwen3.6-35B-A3B-GGUF`, file
   `Qwen_Qwen3.6-35B-A3B-Q5_K_M.gguf`, pinned by URL and sha256 in chart
   values. The vision projector is not downloaded (`--no-mmproj`): the MVP is
   text-only.
3. **Arguments** (the worker contract, spec 070):

   ```
   llama-server --host 127.0.0.1 --port 8080 --model /models/model.gguf \
     -c 32768 -np 1 -ngl all -fa on --no-mmproj --metrics --jinja \
     --reasoning-format deepseek --api-key "$WORKER_API_KEY" \
     --timeout 600 --alias qwen3.6-35b-a3b
   ```

   Context and parallelism are chart values; raising `-np` to 2 waits on the
   benchmark in spec 140.
4. **Readiness contract** between controller and worker: `GET /health`
   returns 200, then `GET /v1/models` lists the alias. Anything else is "not
   ready". Metrics come from `GET /metrics`.
5. **Model id** seen by clients is the alias `qwen3.6-35b-a3b`, configured in
   the chart. The gateway rejects any other `model` value with an
   OpenAI-shaped 404.
6. **Reasoning:** `reasoning_content` is passed through as llama-server emits
   it. Thinking is on by default as in the model card; clients disable it per
   request with `chat_template_kwargs: {"enable_thinking": false}`.

## Alternatives considered

- **vLLM now.** Needs the full BF16 or FP8 weights (≈ 70 GB or ≈ 35 GB) and a
  far larger image; cold start would be dominated by the download. Kept as
  the designed switch: a different image and argument list behind the same
  readiness contract and the same OpenAI surface.
- **unsloth UD-Q5_K_M.** Comparable; 0.5 GB larger. Either works; bartowski is
  chosen for the plain naming. Switching is a values change.
- **Q4_K_M** to save 5 GB. Not needed at 48 GB; quality matters more for
  coding.
- **A custom image with the model baked in.** 26 GB image pulls are slower
  than a Hugging Face download unless the host has it cached, which we
  cannot control (ADR 0005).

## Rationale

The official image removes a build pipeline and a CUDA compatibility matrix
from the MVP. llama.cpp gives the required OpenAI surface, SSE, cancellation
on disconnect, `/health`, `/metrics` and an API key out of the box.

## Consequences

- The worker exposes nothing but what llama-server offers; any gateway-side
  feature (auth, limits, queueing) lives in the gateway.
- Tool calling for Qwen3.6 in llama.cpp is unverified (research tags it U);
  spec 030 runs a tool-call round trip before the gateway spec is built on it.
- The `server-cuda` image needs a host driver that supports CUDA 12.8; the
  offer filter sets `cuda_max_good >= 12.8`.

## Risks

- llama.cpp moves fast; a pinned old build can miss fixes for this
  architecture. Mitigation: dependabot-style bump of the image digest per
  release, verified by spec 140.
- Hugging Face availability or throttling. Mitigation: ADR 0005 keeps an S3
  mirror as the documented fallback.
