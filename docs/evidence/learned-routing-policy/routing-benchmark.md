<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Routing benchmark (issue #159)

Status: live A/B/C seed_replay and Harbor overhead complete on Shadeform with
real ONNX CUDA BGE int8 embedder. Configuration D remains on issue #162.

Throughput cells used a deterministic local upstream sink and are marked
`synthetic_upstream`. Paid portfolio replay for effectiveness/cost ran under
the $100 abort threshold (estimate $12.22; actual ~$0.269).

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- SKU memory: 256 GB advertised; observed about 141 GiB
- GPUs: 2x NVIDIA L40S
- Router commit: `0706c576e050bf04721deac033913750d560d2fa`
- Bundle fingerprint: `043b62fac5a1c4f3539997d25852dcd67e74c7cb72fbaabd5a15c091bd6ae7ef`
- Embedding kind for B/C sidecar: `onnx` (CUDAExecutionProvider active)
- ORT providers: `['CUDAExecutionProvider', 'CPUExecutionProvider']`
- Sidecar `deadline_ms` / `max_decision_ms`: 200; router `external_policy.timeout_ms`: 500
- Harbor task set: `aider/polyglot_python_two-bucket` revision `42aefbe4f3dd34776278b37f78f7188aaacda1435a3362937a28f380a03daa7c`
- Instance left running after the run (not torn down)

## Router-added latency

- Definition: Monotonic milliseconds from router receive to outbound httptrace.WroteRequest.
- Receive event: `router_receive` at `internal/router/service.go:1318-1352`
- WroteRequest event: `router_upstream_wrote_request` at `internal/router/service.go:2209-2230`
- Join: request_trace_events rows where event=router_upstream_wrote_request; duration_ms is receive_to_wrote_request.

## Overhead medians (seed_replay, synthetic_upstream, real ONNX CUDA)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms | timeout rate |
|---|---:|---:|---:|---:|---:|
| A | 1 | 10.78 | 1.0 | n/a | 0.000 |
| A | 8 | 10.56 | 1.0 | n/a | 0.000 |
| A | 32 | 9.77 | 3.0 | n/a | 0.000 |
| A | 128 | 8.99 | 13.0 | n/a | 0.000 |
| B | 1 | 10.24 | 9.0 | 8.0 | 0.000 |
| B | 8 | 9.92 | 10.0 | 8.0 | 0.000 |
| B | 32 | 8.97 | 12.0 | 8.5 | 0.000 |
| B | 128 | 9.34 | 13.0 | 3.5 | 0.000 |
| C | 1 | 10.09 | 9.0 | 8.0 | 0.000 |
| C | 8 | 8.71 | 10.0 | 8.0 | 0.000 |
| C | 32 | 11.38 | 11.0 | 6.5 | 0.000 |
| C | 128 | 11.73 | 22.0 | 3.0 | 0.000 |

### B/C deltas versus A (p50 router-added ms; not a cost claim)

- Config B @ concurrency 1: router_added_latency_delta=8.0, lrp_decisions_per_sec_delta=-0.5399160869758717
- Config B @ concurrency 8: router_added_latency_delta=9.0, lrp_decisions_per_sec_delta=-0.6453538854722183
- Config B @ concurrency 32: router_added_latency_delta=8.5, lrp_decisions_per_sec_delta=-0.7949129068587624
- Config B @ concurrency 128: router_added_latency_delta=3.5, lrp_decisions_per_sec_delta=0.348507788605092
- Config C @ concurrency 1: router_added_latency_delta=9.0, lrp_decisions_per_sec_delta=-0.6913440353541489
- Config C @ concurrency 8: router_added_latency_delta=9.0, lrp_decisions_per_sec_delta=-1.8500036761915482
- Config C @ concurrency 32: router_added_latency_delta=8.5, lrp_decisions_per_sec_delta=1.6186452493668888
- Config C @ concurrency 128: router_added_latency_delta=21.5, lrp_decisions_per_sec_delta=2.7403262219917934

## Harbor load shape (instruction replay through router; pass rate is load shape only)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms |
|---|---:|---:|---:|---:|
| A | 1 | 10.14 | 1.0 | n/a |
| A | 4 | 8.30 | 1.0 | n/a |
| A | 16 | 11.59 | 1.0 | n/a |
| B | 1 | 6.12 | 76.0 | 74.0 |
| B | 4 | 8.12 | 73.0 | 71.0 |
| B | 16 | 9.75 | 73.0 | 72.0 |
| C | 1 | 6.77 | 72.5 | 71.0 |
| C | 4 | 11.01 | 74.5 | 73.0 |
| C | 16 | 10.88 | 73.0 | 71.5 |

## Effectiveness and cost

Paid portfolio replay on the same host (N=100 sample, estimate under $100):

- Estimate USD: 12.218124976000002
- Actual spend USD: 0.2686863619999999
- Response rows: 400
- Status counts: {'ok': 108, 'upstream_error': 292}
- Models: ['qwen/qwen3.5-9b', 'qwen/qwen3.8-27b', 'gpt-5.6-sol', 'gpt-5-mini']
- OpenAI mini fallback: requested `gpt-5.4-mini`, used `gpt-5-mini` (requested id unavailable on key)
- GPT-5.6-only absolute USD baseline: unavailable this pass (OpenAI targets returned only http_429/http_502)

No savings percentages.


## Drivers

- Seed replay concurrency: 1, 8, 32, 128 (3 runs each; median and spread in JSON)
- Harbor concurrency: 1, 4, 16 (3 runs each; Harbor instruction load shape)

## Teardown

- Shadeform instance kept running after the run.
- Instance id: `9963d127-f312-4a37-b72d-91b42607cf8e`
