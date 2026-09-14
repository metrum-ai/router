<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Routing benchmark (issue #159)

Status: retake complete. Live A/B/C seed_replay and Harbor load cells on Shadeform
with real CUDA ONNX int8 BGE (no synthetic embedder).
Configuration D deferred to issue #162.

Throughput cells used a deterministic local upstream sink and are marked
`synthetic_upstream`. Paid #157 portfolio ran with estimate first (12.218 USD,
abort 100). Actual paid spend about 0.269 USD.

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- Instance id (left running): `9963d127-f312-4a37-b72d-91b42607cf8e`
- SKU memory: 256 GB advertised; observed about 141 GiB
- GPUs: 2x NVIDIA L40S
- Router commit: `0706c576e050bf04721deac033913750d560d2fa`
- Bundle fingerprint: `043b62fac5a1c4f3539997d25852dcd67e74c7cb72fbaabd5a15c091bd6ae7ef`
- Embedding kind for B/C sidecar: `onnx` (CUDAExecutionProvider active; ORT 1.22.0)
- Session providers: `['CUDAExecutionProvider', 'CPUExecutionProvider']`
- Sidecar `deadline_ms` / `max_decision_ms`: 200; router `external_policy.timeout_ms`: 500
- Harbor task: `aider/polyglot_python_two-bucket` revision `42aefbe4f3dd34776278b37f78f7188aaacda1435a3362937a28f380a03daa7c`

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
- Config B @ concurrency 32: router_added_latency_delta=9.0, lrp_decisions_per_sec_delta=-0.7949129068587624
- Config B @ concurrency 128: router_added_latency_delta=0.0, lrp_decisions_per_sec_delta=0.348507788605092
- Config C @ concurrency 1: router_added_latency_delta=8.0, lrp_decisions_per_sec_delta=-0.6913440353541489
- Config C @ concurrency 8: router_added_latency_delta=9.0, lrp_decisions_per_sec_delta=-1.8500036761915482
- Config C @ concurrency 32: router_added_latency_delta=8.0, lrp_decisions_per_sec_delta=1.6186452493668888
- Config C @ concurrency 128: router_added_latency_delta=9.0, lrp_decisions_per_sec_delta=2.7403262219917934

## Harbor load-shape medians (synthetic_upstream sink)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms | timeout rate |
|---|---:|---:|---:|---:|---:|
| A | 1 | 10.14 | 1.0 | n/a | 0.000 |
| A | 4 | 8.30 | 1.0 | n/a | 0.000 |
| A | 16 | 11.59 | 1.0 | n/a | 0.000 |
| B | 1 | 6.12 | 76.0 | 74.0 | 0.000 |
| B | 4 | 8.12 | 73.0 | 71.0 | 0.000 |
| B | 16 | 9.75 | 73.0 | 72.0 | 0.000 |
| C | 1 | 6.77 | 72.5 | 71.0 | 0.000 |
| C | 4 | 11.01 | 74.5 | 73.0 | 0.000 |
| C | 16 | 10.88 | 73.0 | 71.5 | 0.000 |

## Effectiveness and cost (paid #157 retake)

- Estimate USD (printed first): 12.218 (abort 100)
- Actual spend USD: 0.268686
- Response rows: 400
- Status counts: ok 108, upstream_error 292
- Error counts: http_429 195, http_502 97
- Per target ok/spend:
  - `qwen/qwen3.5-9b`: ok 56 / 100, spend USD 0.097948
  - `qwen/qwen3.8-27b`: ok 52 / 100, spend USD 0.170738
  - `gpt-5.6-sol`: ok 0 / 100, spend USD 0.000000
  - `gpt-5-mini`: ok 0 / 100, spend USD 0.000000
- OpenAI probe: gpt-5.6-sol present=True; gpt-5.4-mini present=False; mini fallback=`gpt-5-mini`
- GPT-5.6-sol-only absolute USD baseline: unavailable this pass (OpenAI ok count 0)

## Drivers

- Seed replay concurrency: 1, 8, 32, 128 (3 runs each; median and spread in JSON)
- Harbor concurrency: 1, 4, 16 (3 runs each); task `aider/polyglot_python_two-bucket`

No savings percentages.

## Shadeform

- Instance left running per operator instruction (not torn down).
- Resume SSH: `ssh -i ~/.ssh/id_ed25519 shadeform@64.247.196.20`
- Work root: `/var/tmp/lrp-retake`

