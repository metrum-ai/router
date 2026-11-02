<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Sizing: amd-strix-halo-edge

Hardware: 1× AMD Ryzen AI MAX+ 395 (Strix Halo, gfx1151, RDNA 3.5) with 128 GB
unified LPDDR5X and ~256 GB/s of shared bandwidth. The CPU and the GPU draw from
one pool, so the numbers below are one 128 GB budget, not a VRAM figure.

## Method

1. **Memory bound (arithmetic, this file).** Per model the KV-cache pool is
   `gpu_memory_utilization × capacity − weights − framework overhead`, and
   `requests_at_context = kv_pool_bytes / (kv_bytes_per_token × context_tokens)`
   with

   ```
   kv_bytes_per_token = 2 (K and V) × layers × kv_heads × head_dim × bytes_per_element
   ```

   Cache and weights are BF16 here: gfx1151 has no FP8 datapath in this
   configuration, so `bytes_per_element` is 2 for both.

2. **Rate bound (measurement, `results/`).** Shared LPDDR5X bandwidth is the
   binding constraint on this box and no datasheet predicts it. It is measured on
   the commit that serves the run:

   ```sh
   deploy/reference/harness/concurrency_sweep.py amd-strix-halo-edge
   ```

   **Sustained concurrency** is the highest worker count at which every planned
   request completed inside the target latency.

## Target latency

| Metric | Target for this tier |
|---|---|
| p95 end-to-end, 256 output tokens, 2 000-token prompt | ≤ 8 s |
| Failed requests | 0 |
| Quality | Both local groups answer the functional suite |

## Memory plan

| Slot | Model | Weights | Layers | KV heads | Head dim | KV bytes/token |
|---|---|---|---|---|---|---|
| `local-tiny` | Qwen3-1.7B (BF16) | 3.4 GB | 28 | 8 | 128 | 112 KiB |
| `local-primary` | Qwen3-30B-A3B-Instruct-2507 (BF16) | 61 GB | 48 | 4 | 128 | 96 KiB |

| Slot | `gpu_memory_utilization` | Budget | − weights | − 2 GB overhead | KV pool | Context | Requests at that context |
|---|---|---|---|---|---|---|---|
| `local-tiny` | 0.05 | 6.4 GB | 3.0 GB | 1.0 GB | 1.0 GB | 8 192 | 1 |
| `local-primary` | 0.80 | 102.4 GB | 41.4 GB | 39.4 GB | 39.4 GB | 8 192 | 52 |

0.85 of the unified pool is allocated to the two slots; the rest stays with the
operating system, the desktop session and the router.

## Prerequisite: the GPU must be able to map the pool

Strix Halo's GPU reaches the unified pool through the GTT window, and a default
kernel configuration often exposes far less than 100 GB. Before trusting this
table, confirm the GPU can address it:

```sh
journalctl -k | grep -i -E 'amdgpu.*(VRAM|GTT)'
rocminfo | grep -E 'Name|Size'   # inside the container
```

If the window is too small, raise it with the `amdgpu.gttsize` kernel parameter
(MB) in the boot loader and reboot. Sizing that parameter is host setup, not part
of this configuration; the README records what the box was set to for the run.

## Expected, not measured

| Claim | Value | Basis |
|---|---|---|
| Concurrent 8K-context agents on `local-chat` | **estimate: memory allows 52; bandwidth bound is much lower** | table above; 256 GB/s shared with the CPU is the real limit |
| Concurrent agents at 2K context | **estimate: memory allows 200+; bandwidth bound is lower** | 39.4 GB ÷ 0.19 GB per 2K request |
| Decode throughput | unknown | a 3.3B-active MoE on LPDDR5X must be measured, not extrapolated |

These rows are arithmetic. `results/README.md` records what was measured, on
which commit; the tier README carries the number that replaced them.

## Limits of this method

- Unified memory is shared with CPU work, the desktop and page cache. A box also
  running a browser or a build has a lower ceiling than this table.
- The GPU's share is bounded by the GTT window, not only by 128 GB installed.
- Weights and cache are BF16. FP8 would halve the weights and roughly double the
  KV pool but is not part of this configuration for gfx1151.
- KV arithmetic assumes a dense 2-byte cache with no prefix sharing.
- One 30B-class model fits beside the tiny slot; two do not, which is why this
  tier has no `local-coder` group.
