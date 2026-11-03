<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Sizing: nvidia-datacenter

Hardware: 1× NVIDIA 80 GB datacenter GPU (H100 80 GB class), 1 host with ≥ 32 vCPU,
≥ 256 GB RAM and ≥ 500 GB free disk for weights.

## Method

Sizing answers one question: **how many concurrent agents does this box sustain
at a stated latency and quality?** It is answered in two parts, and only the
second part is a measurement.

1. **Memory bound (arithmetic, this file).** Per model, the KV-cache pool is
   `gpu_memory_utilization × capacity − weights − framework overhead`. Every
   request holds one KV slot per simultaneously active sequence, so
   `requests_at_context = kv_pool_bytes / (kv_bytes_per_token × context_tokens)`.
   Bytes per token for a decoder with grouped-query attention:

   ```
   kv_bytes_per_token = 2 (K and V) × layers × kv_heads × head_dim × bytes_per_element
   ```

   The cache is not quantized in this configuration, so `bytes_per_element` is 2.

2. **Rate bound (measurement, `results/`).** Prefill and decode throughput decide
   the number that is actually usable. It is measured with the repository's load
   driver on the same commit that serves the run:

   ```sh
   deploy/reference/harness/concurrency_sweep.sh nvidia-datacenter
   ```

   The driver runs 1, 8 and 32 concurrent workers against the router with an
   identical request body, three repetitions, and compares the routed lane with
   the direct vLLM lane. **Sustained concurrency** is the highest worker count at
   which every completed request stayed inside the target latency and no request
   failed — not the highest count that produced some answers.

## Target latency

| Metric | Target for this tier |
|---|---|
| p95 end-to-end, 256 output tokens, 2 000-token prompt | ≤ 8 s |
| Failed requests | 0 |
| Quality | Chat/coder group answers the functional suite (`harness/functional_smoke.sh`) |

A run that misses the p95 target at 8 workers reports the count it did sustain;
the tier README records it and does not round up.

## Memory plan

Model facts are pinned in `images.lock` (Hugging Face revisions). KV arithmetic:

| Slot | Model | Weights | Layers | KV heads | Head dim | KV bytes/token |
|---|---|---|---|---|---|---|
| `local-tiny` | Qwen3-1.7B (BF16) | 3.4 GB | 28 | 8 | 128 | 112 KiB |
| `local-chat` | Qwen3-32B (FP8) | 33 GB | 64 | 8 | 128 | 256 KiB |

| Slot | `gpu_memory_utilization` | Budget | − weights | − 2 GB overhead | KV pool | Context | Requests at that context |
|---|---|---|---|---|---|---|---|
| `local-tiny` | 0.08 | 6.4 GB | 3.0 GB | 1.0 GB | 1.0 GB | 8 192 | 1 |
| `local-chat` | 0.88 | 70.4 GB | 37.4 GB | 35.4 GB | 35.4 GB | 8 192 | 17 |

The two slots share one GPU (0.96 of it allocated; the remaining 4 % is left for
driver and NCCL buffers).

## Expected, not measured

| Claim | Value | Basis |
|---|---|---|
| Concurrent 8K-context chat agents | **estimate: 12–17, memory bound** | the table above; the rate bound is not yet measured |
| Concurrent agents without the tail | **estimate: 40+ at 2K context** | 35.4 GB ÷ 0.5 GB per 2K request |
| Output throughput on `local-chat` | unknown | must be measured; FP8 32B on a 80 GB class card |

These rows are arithmetic, not results. `results/README.md` records what was
actually measured, on which commit, and the tier README carries the number that
replaced them.

## Disk versus memory

`--quantization fp8` is a **load-time** quantization: vLLM downloads the published
BF16 checkpoint and converts it while loading. Resident memory follows the FP8
column above; disk follows the BF16 repository size (Qwen3-32B is ~64 GB), plus a
second copy for any model the same box also serves. A sizing plan that budgets
disk from the FP8 number will fill the disk on first boot.

## Limits of this method

- KV bytes assume a dense 2-byte cache and no prefix sharing. vLLM may enable
  prefix caching, which raises real concurrency for agent traffic with repeated
  system prompts; the estimate stays below that.
- Framework overhead grows with `--max-model-len` and CUDA-graph capture; 2 GB is
  a planning allowance, not a vLLM promise.
- A single 80 GB card serving two slots cannot serve a 32B-class model at full
  BF16 precision. Moving `local-chat` to a larger model, or adding a coder slot,
  needs the 144 GB tier or a change to `VLLM_QUANTIZATION`/model ID — both are
  configuration changes, both change this table.
