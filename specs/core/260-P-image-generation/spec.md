---
id: "CORE-260"
status: "DRAFT"
updated: "2026-11-22"
depends_on: ["CORE-050", "CORE-060", "CORE-230"]
adrs: ["0002"]
---

# 260 — Image generation (and image understanding)

**Status note:** DRAFT, problem and requirements only. No design yet.
Written 2026-11-22 as a follow-up to the Open WebUI front end (CORE-230).

## Problem

The model behind this service, Qwen3.8-27B, is a text model. It cannot
create images, and in the current setup it cannot read them either
(no vision projector is loaded). Two separate capabilities are missing:

- **Generation:** "draw me a lighthouse" needs a diffusion model (for
  example FLUX or SDXL), usually served by ComfyUI or an OpenAI-compatible
  `/v1/images/generations` endpoint.
- **Understanding:** "what is in this photo" needs a vision-language model
  or a vision projector next to the text model.

## What exists today

- The gateway serves only `/v1/models` and `/v1/chat/completions`;
  `/v1/images/*` returns 404. The request body limit is 1 MiB, which a
  base64 image quickly exceeds.
- The worker runs one llama-server with one model on one rented GPU. At
  262k context the 48 GB card has about 8 GiB of VRAM spare.
- Open WebUI can call several image engines (OpenAI image models, Gemini,
  ComfyUI and others). The exact list is to be confirmed at design time.
- A diffusion model is a second multi-GB download at every cold start
  (see CORE-210 on bandwidth cost and CORE-180 on caching).

## Candidate placements (to evaluate)

1. **Same GPU, smaller LLM context.** Run a diffusion server next to
   llama-server and give it the VRAM freed by a shorter context.
2. **A second GPU instance on demand,** started only for image requests.
3. **A hosted image API** through Open WebUI. Simplest; prompts leave the
   household's infrastructure and cost per image.

## Requirements

- R1. A user MUST be able to request an image from a text prompt in the
  chat front end and receive it in the conversation.
- R2. The design MUST state the VRAM, download size and cost per cold start
  it adds, and its effect on the LLM's context size.
- R3. Image generation MUST be subject to the same budget and idle controls
  as the LLM; a stopped GPU MUST give a clear message, not a broken image.
- R4. Image understanding (uploading a photo and asking about it) MUST be
  decided explicitly: in scope with a named model, or out of scope.
- R5. Prompts and images MUST stay on the household's infrastructure unless
  the design names the third party and the user accepts it.

## Open questions

| Question | Why it matters |
|---|---|
| Which diffusion model, and at what quality/VRAM? | Decides placement 1 vs 2 |
| Is image understanding wanted more than generation? | A vision model may be the bigger win |
| Does CORE-200 (per-start model choice) make "start an image GPU" a start parameter? | Avoids a second control path |

## Evidence

None. Problem and requirements only.
