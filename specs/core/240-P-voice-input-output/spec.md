---
id: "CORE-240"
status: "DRAFT"
updated: "2026-11-22"
depends_on: ["CORE-060", "CORE-230"]
adrs: ["0002"]
---

# 240 — Voice: speech to text and text to speech in the chat front end

**Status note:** DRAFT, problem and requirements only. No design yet.
Written 2026-11-22 as a follow-up to the Open WebUI front end (CORE-230),
which ships with text chat and web search only.

## Problem

Household members want to talk to the assistant on their phones and hear
the answer, the way the ChatGPT app allows. The model behind this service
is text only: it neither hears nor speaks. Voice needs two more components:
speech to text (STT) before the model and text to speech (TTS) after it.

## What exists today

- The gateway serves `GET /v1/models` and `POST /v1/chat/completions` only;
  `/v1/audio/transcriptions` and `/v1/audio/speech` return 404.
- Open WebUI offers speech to text, text to speech and hands-free voice
  calls, with engines that run in its own container, call an
  OpenAI-compatible audio API, or use the browser's built-in speech
  functions. The exact engine list is to be confirmed at design time.
- The cluster's only worker node runs at 88% memory use (2026-11-22). A
  speech model inside the Open WebUI pod adds memory there.
- The rented GPU has spare VRAM at the configured 262k context (about 8 GiB
  free on a 48 GB card).

## Candidate placements (to evaluate)

1. **In the browser or phone** (Web Speech API, Conduit's native speech).
   No server cost; quality and language support vary by device.
2. **In the Open WebUI pod** (CPU speech models). No GPU needed; costs node
   memory and may force a second worker node.
3. **On the GPU worker** next to llama-server, exposed by the gateway as
   `/v1/audio/*`. Fast and private; needs gateway routes and only works
   while the GPU runs.

## Requirements

- R1. A user MUST be able to dictate a message on a phone and on a laptop.
- R2. A user MUST be able to have an answer read aloud.
- R3. Russian, Ukrainian and English MUST be supported for both directions.
- R4. Audio MUST NOT leave the household's own infrastructure unless the
  design states which third party receives it and the user accepts that.
- R5. The design MUST state the memory and cost added on each component it
  uses (worker node, GPU VRAM).
- R6. Voice MUST degrade to text when the GPU is stopped, not break the
  chat.

## Open questions

| Question | Why it matters |
|---|---|
| Which engines does the deployed Open WebUI version offer, and with what memory? | Placement 2 depends on it |
| Is browser or device speech good enough for the three languages? | Placement 1 costs nothing |
| Hands-free voice calls in scope, or dictation and read-aloud only? | Calls need low latency end to end |

## Evidence

None. Problem and requirements only.
