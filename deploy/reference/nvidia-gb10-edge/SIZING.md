<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Sizing: nvidia-gb10-edge

Hardware: 1× NVIDIA GB10 system, 128 GB unified memory, arm64 CPU. The GPU and
the CPU share one memory pool, so the numbers below are about one 128 GB budget,
not a separate VRAM figure.

## Method

1. **Memory bound (arithmetic, this file).** Per model, the KV-cache pool is
   `gpu_memory_utilization × capacity − weights − framework overhead`, where
   capacity is the unified-memory size and the utilization is what is left for
   the process. `requests_at_context = kv_pool_bytes / (kv_bytes_per_token × context_tokens)`
   with

   ```
   kv_bytes_per_token = 2 (K and V) × layers × kv_heads × head_dim × bytes_per_element
   ```

   The cache is not quantized here, so `bytes_per_element` is 2.

2. **Rate bound (measurement, `results/`).** Unified memory bandwidth is the
   binding constraint on this class of box, and it cannot be derived from a
   datasheet. It is measured on the commit that serves the run:

   ```sh
   deploy/reference/harness/concurrency_sweep.py nvidia-gb10-edge
   ```

   **Sustained concurrency** is the highest worker count at which every planned
   request completed inside the target latency — not the highest count that
   produced some answers.

## Target latency

| Metric | Target for this tier |
|---|---|
| p95 end-to-end, 256 output tokens, 2 000-token prompt | ≤ 8 s |
| Failed requests | 0 |
| Quality | Both local groups answer the functional suite |

An edge box is expected to sustain a handful of agents, not dozens; the run
reports the number it reached, including zero if that is the answer.

## Memory plan

| Slot | Model | Weights | Layers | KV heads | Head dim | KV bytes/token |
|---|---|---|---|---|---|---|
| `local-tiny` | Qwen3-1.7B (BF16) | 3.4 GB | 28 | 8 | 128 | 112 KiB |
| `local-primary` | Qwen3-30B-A3B-Instruct-2507 (BF16) | 61 GB | 48 | 4 | 128 | 96 KiB |

| Slot | `gpu_memory_utilization` | Budget | − weights | − 2 GB overhead | KV pool | Context | Requests at that context |
|---|---|---|---|---|---|---|---|
| `local-tiny` | 0.05 | 6.4 GB | 3.0 GB | 1.0 GB | 1.0 GB | 8 192 | 1 |
| `local-primary` | 0.80 | 102.4 GB | 41.4 GB | 39.4 GB | 39.4 GB | 8 192 | 52 |

0.85 of the unified pool is allocated to the two slots; the remaining ~19 GB is
left to the operating system and the router.

## Expected, not measured

| Claim | Value | Basis |
|---|---|---|
| Concurrent 8K-context agents on `local-chat` | **estimate: memory allows 52; bandwidth bound is unknown and lower** | the table above; the rate bound is the number that matters on this box |
| Concurrent agents at 2K context | **estimate: memory allows 200+; bandwidth bound is lower** | 39.4 GB ÷ 0.19 GB per 2K request |
| Decode throughput | unknown | the sparse 3.3B-active path is what makes an edge MoE usable; it must be measured |

These rows are arithmetic. `results/README.md` records what was measured, on
which commit; the tier README carries the number that replaced them.

## Limits of this method

- Unified memory is shared with the CPU, the container runtime and page cache.
  A box that runs other work will see a lower ceiling than this table.
- Weights are BF16. FP8 would free ~30 GB and roughly double the KV pool, but
  RDNA/Blackwell FP8 kernel support for this GB10 part is not verified by this
  repository; it is not enabled by default.
- KV arithmetic assumes no prefix sharing. Agent traffic with a repeated system
  prompt will do better.
- One 30B-class model fits beside the tiny slot at this memory size. A second
  30B-class slot does not; moving `local-chat` to a larger model needs the
  datacenter tier.
