<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Sizing: amd-mi350p-datacenter

Hardware: 1× AMD Instinct MI350P, 144 GB HBM3E, PCIe dual-slot (gfx950, CDNA 4).
This is the tier on which all four groups fit: a 32B chat model, a sparse coder
model and the tiny slot share one card.

## Method

1. **Memory bound (arithmetic, this file).** Per model, the KV-cache pool is
   `gpu_memory_utilization × capacity − weights − framework overhead`, and
   `requests_at_context = kv_pool_bytes / (kv_bytes_per_token × context_tokens)`
   with

   ```
   kv_bytes_per_token = 2 (K and V) × layers × kv_heads × head_dim × bytes_per_element
   ```

   The cache is not quantized here, so `bytes_per_element` is 2 even though the
   weights are FP8.

2. **Rate bound (measurement, `results/`).** Measured on the commit that serves
   the run, on this card:

   ```sh
   deploy/reference/harness/concurrency_sweep.py amd-mi350p-datacenter
   ```

   **Sustained concurrency** is the highest worker count at which every planned
   request completed inside the target latency.

## Target latency

| Metric | Target for this tier |
|---|---|
| p95 end-to-end, 256 output tokens, 2 000-token prompt | ≤ 8 s |
| Failed requests | 0 |
| Quality | All three local groups answer the functional suite |

## Memory plan

| Slot | Model | Weights | Layers | KV heads | Head dim | KV bytes/token |
|---|---|---|---|---|---|---|
| `local-tiny` | Qwen3-1.7B (BF16) | 3.4 GB | 28 | 8 | 128 | 112 KiB |
| `local-chat` | Qwen3-32B (FP8) | 33 GB | 64 | 8 | 128 | 256 KiB |
| `local-coder` | Qwen3-Coder-30B-A3B-Instruct (FP8) | 31 GB | 48 | 4 | 128 | 96 KiB |

| Slot | `gpu_memory_utilization` | Budget | − weights | − 2 GB overhead | KV pool | Context | Requests at that context |
|---|---|---|---|---|---|---|---|
| `local-tiny` | 0.05 | 7.2 GB | 3.8 GB | 1.8 GB | 1.8 GB | 8 192 | 2 |
| `local-chat` | 0.50 | 72.0 GB | 39.0 GB | 37.0 GB | 37.0 GB | 8 192 | 18 |
| `local-coder` | 0.43 | 61.9 GB | 30.9 GB | 28.9 GB | 28.9 GB | 8 192 | 38 |

0.98 of the card is allocated across the three slots.

## Expected, not measured

| Claim | Value | Basis |
|---|---|---|
| Concurrent 8K-context agents, `local-chat` | **estimate: 18, memory bound** | table above; the rate bound is not yet measured |
| Concurrent 8K-context agents, `local-coder` | **estimate: 38, memory bound** | the sparse 3B-active path also has a lower per-token cost |
| Concurrent 8K-context agents, both groups | **estimate: bounded by 144 GB, not by a slot** | mixed traffic shares the same HBM |
| Decode throughput | unknown | must be measured; FP8 32B on one MI350P |

These rows are arithmetic. `results/README.md` records what was measured, on
which commit; the tier README carries the number that replaced them.

## Limits of this method

- One card is one failure domain and one bandwidth budget; three slots on it do
  not add bandwidth, only memory. Mixed traffic will not sustain the sum of the
  per-slot estimates.
- KV arithmetic assumes a dense 2-byte cache with no prefix sharing.
- FP8 weights are what make three slots fit. Changing `VLLM_QUANTIZATION` to
  empty (BF16) needs ~62 GB more and does not fit this plan; a BF16 variant is a
  different table, not a footnote.
- PCIe attach matters: MI350P talks to the host over PCIe, not an 8-GPU fabric,
  so this table assumes a single card with no inter-GPU scaling.
