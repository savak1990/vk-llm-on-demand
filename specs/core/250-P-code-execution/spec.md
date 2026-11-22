---
id: "CORE-250"
status: "DRAFT"
updated: "2026-11-22"
depends_on: ["CORE-060", "CORE-230"]
adrs: []
---

# 250 — Code execution in the chat front end

**Status note:** DRAFT, problem and requirements only. No design yet.
Written 2026-11-22 as a follow-up to the Open WebUI front end (CORE-230).

## Problem

For calculations, data analysis and checking its own answers, the model is
far more reliable when it can write code, run it and read the result
("code interpreter" in ChatGPT). The model can write code and call tools;
it cannot run anything. Something has to execute the code, and where it
runs decides what it can reach.

## What exists today

- The model handles tool calls through the gateway (verified 2026-11-22:
  OpenCode ran Read/Grep tool calls against it).
- Open WebUI can run Python in the user's browser, and also offers
  server-side execution (a Jupyter-style backend or a terminal). The exact
  engines and their settings are to be confirmed at design time.
- The cluster has no network policy and no Pod Security enforcement
  (2026-11-22): a server-side executor in the cluster could reach Postgres,
  SearXNG and the GPU service's in-cluster address unless the design adds
  isolation.

## Candidate placements (to evaluate)

1. **In the browser** (Python compiled to WebAssembly). No server, nothing
   in the cluster reachable; limited libraries, no network.
2. **A sandboxed executor in the cluster.** Full Python and packages;
   needs strict isolation from everything else in the cluster.
3. **A throwaway container on the rented GPU host.** Isolated from the
   cluster; only available while the GPU runs.

## Requirements

- R1. A user MUST be able to ask a question whose answer needs computation
  and get a result produced by executed code, with the code visible.
- R2. Executed code MUST NOT be able to reach cluster secrets, the
  Kubernetes API, Postgres, or any other in-cluster service.
- R3. Executed code MUST run with CPU, memory and time limits.
- R4. One user's execution MUST NOT see another user's files or variables.
- R5. The design MUST state what network access executed code has (none,
  allow-list, or full) and why.

## Open questions

| Question | Why it matters |
|---|---|
| Is in-browser Python enough for household use? | It removes R2–R4 almost entirely |
| Which packages are needed (pandas, matplotlib, …)? | Decides browser vs. server |
| Does the model need to download data from the internet while running code? | Drives R5 |

## Evidence

None. Problem and requirements only.
