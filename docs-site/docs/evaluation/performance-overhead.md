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
