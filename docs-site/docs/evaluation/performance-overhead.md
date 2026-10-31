---
title: Performance and overhead
doc_type: explanation
---

# Performance and overhead

**Partial experimental observation, not a general performance claim.** One
2026-10-30 run compared direct and routed requests against the same warmed
vLLM/Qwen2.5-0.5B backend on a shared A100-80GB machine. Three repeated
phases used a fixed 43-token prompt and 32 output tokens. The numbers below are
means of within-repetition router-minus-direct percentiles, in milliseconds:

| Clients | Non-stream p50 | Non-stream p99 | Stream first-content p50 | Stream first-content p99 |
|---:|---:|---:|---:|---:|
| 1 | 0.596 | -1.398 | 0.468 | 1.064 |
| 8 | 2.890 | 3.208 | 2.072 | 3.184 |
| 32 | 6.565 | 12.430 | 7.913 | 13.960 |
| 128 | 17.215 | 104.675 | 43.284 | 52.508 |

The concurrency-one p99 uses only eight requests per phase; negative deltas
reflect variation, not negative physical overhead. First content is a streamed
event, not necessarily a token. This single shared-host result is not an edge
or small-VM benchmark, repeatability guarantee, production SLO, or universal
router overhead. Edge: **pending, amd-halo offline**; edge measurement is
deferred beyond v4.5.

Two 32-client decision-path profiles were captured but their CPU samples were
sparse. Plain profiles failed capture; decision latency and 128-client profiles
had policy errors. CPU/memory per 1,000 requests/s and maximum sustained
throughput are missing. The [full partial results and limitations](https://github.com/metrum-ai/router/tree/main/benchmarks/overhead/evidence/a100-2026-10-30)
include raw samples, profiles, hardware, method and variance. Do not treat the
failed phases as measurements. [#422](https://github.com/metrum-ai/router/issues/422)
remains open.

## d3 decision-path observation

A later A100-SXM4 run compared plain, real d3 shadow and enforce paths to the
same backend. The values below are mean router-minus-direct per-repetition
percentile deltas in milliseconds, not universal overhead guarantees. Each
concurrency-one p99 is based on eight requests per phase; streamed first
content is an event rather than necessarily a token.

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

The router, load generator, backend and four serialized d3 replicas shared the
host. Each decision mode recorded 9,126 successful decisions in its latency
phase. This synthetic workload and sequential phase ordering do not establish
production SLOs or isolate a single cause for the large high-concurrency
deltas. Captured profiles used an exploratory 90% occupancy floor, not strict
occupancy; enforce/32 failed capture. See [raw samples, variance, profiles,
method and limitations](https://github.com/metrum-ai/router/tree/main/benchmarks/overhead/evidence/a100-sxm-2026-10-31).
Edge: **pending, amd-halo offline**, deferred beyond v4.5.
