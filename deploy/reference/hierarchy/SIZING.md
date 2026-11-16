<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# CPU tier sizing evidence

## Measured load point

The available vLLM CPU run used `Qwen/Qwen2.5-0.5B-Instruct` on an Intel Xeon
Gold 6548N host allocated 16 vCPUs. It sent 60 requests per lane at concurrency
4. The measured CPU direct-lane TTFT p50 was 268.8 ms and stream-total p50 was
about 2.05 s.

| CPU type and allocation | workload agent slots | measured density | observed TTFT p50 | observed stream-total p50 |
|---|---:|---:|---:|---:|
| Intel Xeon Gold 6548N, 16 vCPUs allocated to the run | 4 concurrent requests | `4 / 16 = 0.25` concurrent requests per allocated vCPU | 268.8 ms | about 2.05 s |

For this measurement, one concurrent request is used as the agent-slot proxy.
The run reports vCPUs, not physical cores, and it was not a saturation sweep;
the ratio is therefore a load point and cannot be converted to a maximum
agents-per-physical-core capacity. This evidence also has no semantic quality
score or production traffic-share measurement. Collect those measurements on
the deployment's CPU model and workload before setting a production capacity.

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
