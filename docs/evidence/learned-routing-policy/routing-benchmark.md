<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Routing benchmark (issue #159)

Status: live A/B/C seed_replay overhead complete on Shadeform. Harbor skipped.
Configuration D deferred to issue #162.

Throughput cells used a deterministic local upstream sink and are marked
`synthetic_upstream`. No new paid upstream API spend for #159.
Effectiveness/cost reuse #157 OpenRouter seed scalars (see blockers there).

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- SKU memory: 256 GB advertised; observed about 141 GiB
- GPUs: 2x NVIDIA L40S
- Router commit: `5b810a15912bb1e4c54394f0cbab46aa9632d666`
- Bundle fingerprint: `138848dfa1a32f4002a945dc8fca10b661488a835f9f933cf63b54259aaedc13`
- Embedding kind for B/C sidecar: `synthetic` (not real-ONNX evidence; ONNX/GPU is #162)
- Sidecar `deadline_ms` / `max_decision_ms`: 200; router `external_policy.timeout_ms`: 500

## Router-added latency

- Definition: Monotonic milliseconds from router receive to outbound httptrace.WroteRequest.
- Receive event: `router_receive` at `internal/router/service.go:1318-1352`
- WroteRequest event: `router_upstream_wrote_request` at `internal/router/service.go:2209-2230`
- Join: request_trace_events rows where event=router_upstream_wrote_request; duration_ms is receive_to_wrote_request.

## Overhead medians (seed_replay, synthetic_upstream)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms | timeout rate |
|---|---:|---:|---:|---:|---:|
| A | 1 | 12.35 | 1.0 | n/a | 0.000 |
| A | 8 | 12.22 | 1.0 | n/a | 0.000 |
| A | 32 | 11.95 | 3.0 | n/a | 0.000 |
| A | 128 | 11.52 | 7.0 | n/a | 0.000 |
| B | 1 | 11.80 | 4.0 | 3.0 | 0.000 |
| B | 8 | 11.76 | 4.0 | 3.0 | 0.000 |
| B | 32 | 11.50 | 18.0 | 13.0 | 0.000 |
| B | 128 | 11.22 | 43.5 | 35.0 | 0.000 |
| C | 1 | 11.51 | 5.0 | 4.0 | 0.000 |
| C | 8 | 11.64 | 5.0 | 4.0 | 0.000 |
| C | 32 | 9.62 | 29.5 | 17.0 | 0.000 |
| C | 128 | 11.31 | 37.5 | 31.0 | 0.000 |

### B/C deltas versus A (p50 router-added ms; not a cost claim)

- Config B @ concurrency 1: router_added_latency_delta=3.0, lrp_decisions_per_sec_delta=-0.5451827933934936
- Config B @ concurrency 8: router_added_latency_delta=3.0, lrp_decisions_per_sec_delta=-0.46382280607327964
- Config B @ concurrency 32: router_added_latency_delta=15.0, lrp_decisions_per_sec_delta=-0.440934141975017
- Config B @ concurrency 128: router_added_latency_delta=36.5, lrp_decisions_per_sec_delta=-0.2921659783390762
- Config C @ concurrency 1: router_added_latency_delta=4.0, lrp_decisions_per_sec_delta=-0.8420491693702061
- Config C @ concurrency 8: router_added_latency_delta=4.0, lrp_decisions_per_sec_delta=-0.5843851223467809
- Config C @ concurrency 32: router_added_latency_delta=26.5, lrp_decisions_per_sec_delta=-2.3293955982845738
- Config C @ concurrency 128: router_added_latency_delta=30.5, lrp_decisions_per_sec_delta=-0.205240869100896

## Effectiveness and cost

Reused from #157 seed example corpus evidence (no new paid spend):

- OpenRouter response rows: 200
- Status counts: ok 95, upstream_error 105
- Actual spend USD: 0.203992
- Full portfolio estimate USD (reused): 12.218 (abort threshold 100)
- GPT-5.6-only absolute USD baseline: unavailable this pass (see #157 OpenAI blockers)

## Drivers

- Seed replay concurrency: 1, 8, 32, 128 (3 runs each; median and spread in JSON)
- Harbor concurrency: skipped (Harbor not installed on host)

No savings percentages.


## Teardown

- Shadeform instance terminated after the run; instances list count confirmed 0.
