---
id: "CORE-290"
status: "DRAFT"
updated: "2026-11-27"
depends_on: ["CORE-070", "CORE-210"]
adrs: ["0002", "0005", "0018", "0021"]
---

# 290 — Spike: larger models on multi-GPU packs or with experts in CPU RAM

**Status note:** DRAFT, problem statement and spike plan only. No design,
no decision yet. Written 2026-11-24 after the first on-demand session on
the lab showed what one rented offer carries besides the GPU (an RTX 6000
Ada offer with 48 CPU threads, 120.7 GB RAM and a 60 GB disk; the CPU was
0.04 % busy while the GPU served the model). Widened 2026-11-27 to
multi-GPU packs: the operator wants larger models later as a separate
installation (ADR 0021), not as a change to the current one, and ranked
this spike after [CORE-330](../330-P-interruptible-bid-rentals/spec.md)
and [CORE-340](../340-P-prewarm-on-client-open/spec.md).

## Problem

The model is limited by VRAM. Today the worker runs Qwen3.8-27B Q8_0
(28.6 GB) fully on a 48 GB GPU at about 29 tokens per second. A larger
model would answer better, but a dense model above about 45 GB of weights
does not fit in VRAM, and a dense model split between GPU and CPU is very
slow, because every token reads the CPU part from RAM.

Mixture-of-experts (MoE) models are the exception. Each token uses only a
few experts, so llama.cpp can keep attention and shared layers on the GPU
and the experts in CPU RAM (`--n-cpu-moe N` or `-ot`). An MoE model of
about 100 to 120B total parameters with a small active set (about 5 to
12B) is about 60 to 65 GB at Q4. It could run on the same offer class at
an estimated 15 to 30 tokens per second. These figures are estimates from
public reports, not measurements on our offers.

The operator wants to try this: better quality at the cost of slower
turns and longer cold starts, on the same GPU class.

Multi-GPU packs are the other way to get more memory: Vast.ai offers 2×
and 4× GPU machines, and llama.cpp spreads one model across all visible
GPUs. The spike compares the two ways for the same target model size.

## Multi-GPU packs (verified 2026-11-27)

- **Offers.** One offer is one machine; all `num_gpus` GPUs go to one
  container. `dph_total` and `min_bid` are the price of the whole pack.
  `gpu_ram` is per GPU and `gpu_total_ram` is the sum, both in MB in the
  raw API. `gpu_frac` below 1 means a slice of a larger machine. Some
  "RTX 4090" offers are modified 48 GB cards, so filter on `gpu_ram`, not
  only on `gpu_name`.
- **Interconnect.** `pcie_bw` (5.7 to 25.4 GB/s in samples), `gpu_lanes`
  (8 or 16), `pci_gen`; `bw_nvlink` was 0 in every sample.
- **Today.** The filter sends `num_gpus eq 1`
  (`config.provider.filter.numGpus`), so packs are never offered.
- **Live prices, 2026-11-27, lab filters (verified, reliability ≥ 0.95,
  inet ≥ 500 Mb/s):**

  | Pack | Offers | On-demand min / median $/h | Bid floor min / median $/h |
  |---|---|---|---|
  | 2×24 GB (48 GB) | 53 | 0.27 / 0.54 | 0.20 / 0.48 |
  | 4×24 GB (96 GB) | 27 | 0.62 / 1.26 | 0.43 / 1.07 |
  | 2×48 GB (96 GB) | 64 | 0.89 / 3.16 | 0.32 / 2.20 |

- **llama.cpp.** The default `--split-mode layer` uses every visible GPU
  and needs no flag change. It adds memory, not single-user speed, because
  the GPUs run one after another. `--split-mode tensor` (experimental)
  was 1.64× faster on 2×3090 for a dense 70B in one public benchmark
  (October 2026), but it needs an unquantized KV cache (the lab uses
  q8_0), NCCL peer access, and does not support MoE models.
- **Fit.** 96 GB holds a dense 70B at Q8_0 (75 GB) or Q6_K (58 GB) with a
  long context; 48 GB holds a dense 70B Q4_K_M (42.5 GB) with a short one.
- **Cold start.** At about 100 MB/s, 75 GB adds about 12 min to every
  start without a CORE-180 cache hit; a cache volume of that size costs
  about $15 to $30 per month at $0.20 to $0.40/GB-month.

## What exists today (verified 2026-11-24)

- **Offer filter.** `config.provider.filter.minCpuCores` (8),
  `minCpuRamGB` (32) and `minDiskGB` (60) are fixed chart values. The
  Vast.ai client sends `cpu_ram >= minCpuRamGB * 1000` and
  `disk_space >= minDiskGB` (`internal/provider/vastai/filter.go:23-27`).
  Nothing derives them from the model, so an offer with 32 GB RAM would be
  chosen for a model that needs 70 GB of RAM.
- **Disk cap.** CORE-210 caps the rented disk at
  `config.lifecycle.safety.maxDiskGB` (200) and ranks offers by estimated
  session cost, including the download (ADR 0018).
- **Model size.** CORE-210 resolves the model file size automatically
  (`internal/modelsize`), and it is stored on the chosen offer
  (`Offer.Estimate.ModelBytes`).
- **One model file.** The worker downloads one URL and checks one
  `sha256` (`internal/worker/onstart.sh.tmpl`). Hugging Face splits large
  GGUF models into several files of about 50 GB
  (`…-00001-of-00002.gguf`), so a 60 GB model is usually two files.
- **Fixed launch flags.** `llama-server` runs with `-ngl all`,
  `-c $CTX`, `-np $PARALLEL`, `-fa on` and no expert offload flag
  (`onstart.sh.tmpl:62-65`).
- **Cold start.** About 3 to 6 min today for 28.6 GB. Downloads from
  Hugging Face ran at about 70 to 100 MB/s, so 65 GB adds about 7 to
  12 min, and 130 GB about 25 min, to every cold start until CORE-180
  (cached model volume) exists.

## Questions the spike answers

| Question | How |
|---|---|
| Which MoE models of about 100 to 120B fit one offer with experts in RAM, and at which quantization? | List 2 or 3 candidates from the official GGUF repositories, with file count, total size and active parameters. |
| Tokens per second for decode and prefill (1k and 30k token prompts) with `--n-cpu-moe` at several values of N | One rented offer, `llama-bench` and `llama-server`, numbers recorded under Evidence. |
| How much RAM and disk each candidate needs in practice (peak RSS, file size, KV cache at our context size) | Measured on the same run. |
| Does answer quality justify the slower turns? | A fixed set of 10 prompts from real sessions (coding and chat), answered by today's model and by the best candidate, compared side by side by the operator. |
| Is the CPU share of the offer the bottleneck (threads, RAM bandwidth)? | Repeat one benchmark with fewer threads (`-t`) and record the change. |
| Decode and prefill tokens per second for one candidate on a 2× or 4× pack with layer split, and with tensor split where the model allows it | One rented pack, same prompts; record `pcie_bw` and `gpu_lanes` of the host. |
| For the same quality target, which is cheaper per session: a multi-GPU pack, or one GPU with experts in RAM? | Session cost from CORE-210's model with the measured speeds and download sizes. |

## Requirements for the design that follows the spike

- R1. The offer filter MUST guarantee enough RAM and disk for the
  configured model: RAM at least the CPU-resident part of the model plus
  a margin, disk at least the model files plus a margin. Fixed
  `minCpuRamGB`/`minDiskGB` values that ignore the model MUST NOT be the
  only guard.
- R2. The worker MUST accept a model made of several files, each with
  its own checksum, and MUST verify every file before it starts
  `llama-server`.
- R3. The expert-offload setting (for example `--n-cpu-moe N`) MUST be a
  model setting, not a code change per model.
- R4. Every cost and safety cap MUST still apply: `maxHourlyPriceUSD`,
  the monthly budget, CORE-210's download, storage and disk caps, and the
  attempts-per-hour cap.
- R5. The on-demand progress stream (CORE-220) MUST still report the
  download percentage across several files.
- R6. A change of model MUST stay a deliberate operator action (chart
  value today; per-start parameter once CORE-200 exists), never automatic.
- R7. A larger model MUST run as its own installation (ADR 0021), with its
  own budget entry and model alias; the current installation MUST NOT
  change.
- R8. For a multi-GPU offer, every price check MUST use the price of the
  whole pack, and the filter MUST bound total VRAM (`gpu_total_ram`), not
  only the GPU count.

## Spike plan

1. Pick the candidates from primary sources (official GGUF repositories),
   with sizes and file lists. About 30 min, no spend.
2. Rent one offer by hand with at least 128 GB RAM and 200 GB disk
   (`vk-llm` is not needed; the Vast.ai console or CLI is enough), run the
   benchmarks for the best candidate, and destroy the instance. About
   1 hour of rental, about $1. Repeat on one 2× or 4× pack for the
   multi-GPU questions, about 1 hour and $1 to $2.
3. Record the numbers and the quality comparison under Evidence, then
   write the design (filter derivation, multi-file download, offload
   setting) in this spec or split it.

## Out of scope

Dense models that fit neither one GPU nor one pack. Speculative decoding
(a separate idea, GPU only). Making the model choosable per start
(CORE-200). Changing the model of the current installation.

## Evidence

None yet.
