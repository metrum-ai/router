<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# CPU tier sizing evidence

## Measured load point

The case study used `Qwen/Qwen2.5-0.5B-Instruct` on an Intel Xeon Platinum
8488C with 8 physical cores (16 vCPUs). For each concurrency step, the harness
sent two short synthetic classification requests per concurrent agent. These
are measured load points, not a capacity guarantee.

| Concurrent agents | Agents per physical core | Requests | Requests/s | Latency p50 | Latency p95 |
|---:|---:|---:|---:|---:|---:|
| 1 | 0.125 | 2 | 4.582 | 67 ms | 67 ms |
| 2 | 0.25 | 4 | 8.581 | 88 ms | 97 ms |
| 4 | 0.5 | 8 | 13.892 | 123 ms | 452 ms |
| 8 | 1.0 | 16 | 25.782 | 162 ms | 450 ms |
| 16 | 2.0 | 32 | 45.291 | 237 ms | 344 ms |

At the one-agent step, p95 is the maximum of two samples. The 16-agent step had
the highest observed throughput, so this sweep did not reach saturation. The
small sample count and non-monotonic latency do not establish capacity behavior.
Use this table to size a pilot only; repeat with the deployment's prompts,
output lengths, latency budget, and quality floor before setting a production
capacity.

## Sizing method

Record physical cores available to the serving process, concurrent agent
requests, successful completions, TTFT and total latency percentiles, error and
timeout rates, and a workload quality score at each concurrency step. Compute
observed agents per physical core as:

```text
concurrent agent requests / physical cores available to inference
```

Increase concurrency until the latency budget, quality floor, or reliability
target fails. Use the highest prior passing step as a measured operating point,
then leave headroom for framework work and workload variation. The existing
16-vCPU latency run does not supply these saturation or quality inputs.
