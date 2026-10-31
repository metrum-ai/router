<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# A100-SXM4 partial overhead observation — 2026-10-31

This is one real, shared-host measurement, **not full #422 acceptance**.
The underlying bounded run and teardown evidence is `router / v45-proof / 422`,
snapshot `8ea2013d`. The GPU was deleted; the post-deletion provider estimate
was $0.6235102405, not a reconciled charge. Edge: **pending, amd-halo offline**,
deferred beyond v4.5.

## Method and scope

- One dedicated NVIDIA A100-SXM4-80GB, driver in the archived runtime metadata,
  14 visible Intel Xeon Platinum 8462Y+ vCPUs, 105,571,483,648 bytes RAM.
  Router, loader, vLLM and four real d3-lite replicas shared this machine;
  this is not a small CPU VM or an edge benchmark.
- Router and profiling binary built from clean revision
  `f5851e3498b4d60eb840c7a2ba49c0d595c6fb06`, Go 1.27.3,
  `dev_no_license`. The generation backend used official
  `vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34`,
  Qwen2.5-0.5B-Instruct revision `7ae557604adf67be50417f59c2c2f167def9a775`,
  and d3-lite revision `03560c26f5199c3c04129a2ccb38960c913635e0`.
- Direct and routed requests used the same warmed backend and fixed 43-token
  prompt with 32 output tokens. Direct/router phases ran sequentially, with
  alternating order over three repetitions; each worker sent one warmup and
  eight timed requests. The qualification and each shadow/enforce phase
  recorded only successful policy outcomes, including 9,126 per full latency
  phase. See `policy-summary.json` and `phase-status.json`. The representative
  `router-config-redacted.json` replaces the private state paths and synthetic
  caller-token hash; it is illustrative and must be adapted to rerun.
- These are measurements from one setup. Router resource use per 1,000 req/s,
  maximum sustainable throughput, strict concurrency and reproducibility on a
  clean independent host are not established.

## Unprofiled latency deltas

Each cell is the mean of the three within-repetition (router percentile minus
matching direct percentile) deltas in ms. Streaming is first nonempty content
**event**, not guaranteed tokenizer TTFT. At concurrency 1, each percentile
uses only eight timed requests; p99 is not a robust tail estimate. Negative
values reflect variation, not negative physical overhead. Exact repetitions,
variance and compressed JSONL samples are in the three `*-latency` directories.

| Mode | Clients | Non-stream p50 | Non-stream p99 | First-content p50 | First-content p99 |
|---|---:|---:|---:|---:|---:|
| plain | 1 | 0.858 | -1.078 | 0.974 | 0.902 |
| plain | 8 | 2.908 | 3.708 | 1.940 | 3.484 |
| plain | 32 | 5.163 | 14.552 | 8.093 | 18.564 |
| plain | 128 | 25.954 | 97.861 | 57.991 | 74.920 |
| shadow | 1 | 45.871 | 48.025 | 44.560 | 51.094 |
| shadow | 8 | 263.906 | 319.003 | 232.610 | 272.490 |
| shadow | 32 | 1036.018 | 1903.148 | 425.929 | 1070.439 |
| shadow | 128 | 4207.342 | 5684.103 | 2582.442 | 3996.518 |
| enforce | 1 | 44.275 | 47.401 | 43.830 | 49.218 |
| enforce | 8 | 264.596 | 319.348 | 232.937 | 267.879 |
| enforce | 32 | 1034.861 | 1870.445 | 395.727 | 1067.097 |
| enforce | 128 | 4208.133 | 5499.529 | 2557.923 | 3955.310 |

Per-repetition content-event gap percentile differences, averaged over three
repetitions, are shown below (ms). Events need not map one-to-one to tokens,
so these do **not** establish inter-token latency. Negative gaps are variation.

| Mode | Clients | Event gap p50 delta | Event gap p99 delta |
|---|---:|---:|---:|
| plain | 1 | -0.004 | -0.054 |
| plain | 8 | -0.001 | 0.528 |
| plain | 32 | 0.035 | -0.635 |
| plain | 128 | 2.099 | -6.058 |
| shadow | 1 | -0.007 | 0.081 |
| shadow | 8 | 0.024 | 19.570 |
| shadow | 32 | 6.657 | 62.255 |
| shadow | 128 | 51.627 | 43.299 |
| enforce | 1 | -0.003 | -0.053 |
| enforce | 8 | 0.020 | 16.147 |
| enforce | 32 | 9.760 | 62.130 |
| enforce | 128 | 50.919 | 44.686 |

Direct phases occur within each mode rather than being a single shared
baseline; differences between modes are not a controlled model comparison.

## Exploratory profiles

Plain/32, plain/128, shadow/32, shadow/128 and enforce/128 captures passed the
**90%** sampled and capture floor, not the strict full-concurrency guard. The
poll and stop floors are 29/32 or 116/128; polling does not prove continuity.
Enforce/32 failed guarded capture: its profile is invalid and deliberately
omitted. There is no matched valid full 32/128 plain/decision set.

In these five individual captures, plain/32 and plain/128 CPU samples totaled
6.55s and 14.38s; their largest flat entries were SQLite bind (1.31s/5.15s)
and Linux `Syscall6` (1.30s/2.09s). Decision samples are much sparser (under
one CPU-second per capture), and `Syscall6` was the largest flat entry in
shadow/128 and enforce/128. These are host-specific samples, not proof of
stable rankings or causal attribution. Heap files are forced-GC snapshots,
not memory per 1,000 req/s. `cpu-top.txt`, raw CPU and heap profiles and
capture metadata are in each successful profile directory.
