<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Routing benchmark (issue #159 / #162 GPU)

Status: CPU ephemeral A/B/C seed_replay + GPU cuda:0 A/B/C seed_replay and Harbor
complete on Shadeform. Explicit `--device` / `compute.device` after #156/#180.
Throughput cells use synthetic_upstream. Paid effectiveness/cost reused from
NON-OpenAI portfolio (#179); no new paid spend.

## Hosts

### CPU (ephemeral; torn down after run)

- Shadeform SKU: `cpu_small` (massedcompute, desmoines-usa-1)
- vCPUs / memory: 14 / 40 GB
- Device: `cpu` (`strict_device=true`); ORT providers: `CPUExecutionProvider`
- ORT version: 1.22.0; embedding: ONNX int8 BGE
- Router commit: `505b85d191292f732aa20a590be52b156f1996ab`
- Bundle fingerprint: `18860595b99b5a75a46488c075375579e76713441a8389a43b79425aae759b27`
- Harbor: skipped on CPU SKU

### GPU (ephemeral evidence host)

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- GPUs: 2x NVIDIA L40S; observed memory ~141 GiB
- Device: `cuda:0` (`strict_device=true`); providers: `CUDAExecutionProvider`, `CPUExecutionProvider`
- ORT version: 1.22.0 (onnxruntime-gpu); training_seconds ~4.42
- Router commit: `505b85d191292f732aa20a590be52b156f1996ab`
- Bundle fingerprint: `3393a3f5fe237d6304129a616f5c28f3bd342fb3db4c8201d24377796fb0edae`
- Harbor task: `aider/polyglot_python_two-bucket` revision `42aefbe4f3dd34776278b37f78f7188aaacda1435a3362937a28f380a03daa7c`

## Router-added latency

- Definition: Monotonic milliseconds from router receive to outbound httptrace.WroteRequest.
- Receive event: `router_receive` at `internal/router/service.go:1318-1352`
- WroteRequest event: `router_upstream_wrote_request` at `internal/router/service.go:2209-2230`

## CPU overhead medians (seed_replay, synthetic_upstream, device=cpu)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms | timeout rate |
|---|---:|---:|---:|---:|---:|
| A | 1 | 11.01 | 1.0 | n/a | 0.000 |
| A | 8 | 10.99 | 1.0 | n/a | 0.000 |
| A | 32 | 9.90 | 2.0 | n/a | 0.000 |
| A | 128 | 9.93 | 4.0 | n/a | 0.000 |
| B | 1 | 9.70 | 12.0 | 11.0 | 0.000 |
| B | 8 | 9.84 | 12.0 | 11.0 | 0.000 |
| B | 32 | 9.37 | 12.0 | 8.0 | 0.000 |
| B | 128 | 9.65 | 7.0 | 3.0 | 0.000 |
| C | 1 | 9.94 | 12.0 | 11.0 | 0.000 |
| C | 8 | 9.33 | 12.0 | 11.0 | 0.000 |
| C | 32 | 9.21 | 12.0 | 7.0 | 0.000 |
| C | 128 | 9.94 | 7.0 | 3.0 | 0.000 |

## GPU overhead medians (seed_replay, synthetic_upstream, device=cuda:0)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms | timeout rate |
|---|---:|---:|---:|---:|---:|
| A | 1 | 10.92 | 1.0 | n/a | 0.000 |
| A | 8 | 11.71 | 1.0 | n/a | 0.000 |
| A | 32 | 11.84 | 3.0 | n/a | 0.000 |
| A | 128 | 10.04 | 6.0 | n/a | 0.000 |
| B | 1 | 9.98 | 10.0 | 9.0 | 0.000 |
| B | 8 | 12.03 | 12.0 | 10.0 | 0.000 |
| B | 32 | 11.94 | 9.5 | 5.5 | 0.000 |
| B | 128 | 9.64 | 9.0 | 3.0 | 0.000 |
| C | 1 | 10.17 | 10.0 | 9.0 | 0.000 |
| C | 8 | 10.82 | 10.0 | 8.0 | 0.000 |
| C | 32 | 11.15 | 10.0 | 5.5 | 0.000 |
| C | 128 | 10.84 | 7.0 | 2.5 | 0.000 |

## GPU vs CPU config C (seed_replay)

| Conc | CPU C dec/s | GPU C dec/s | CPU C sidecar p50 | GPU C sidecar p50 | CPU C router p50 | GPU C router p50 |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 9.94 | 10.17 | 11.0 | 9.0 | 12.0 | 10.0 |
| 8 | 9.33 | 10.82 | 11.0 | 8.0 | 12.0 | 10.0 |
| 32 | 9.21 | 11.15 | 7.0 | 5.5 | 12.0 | 10.0 |
| 128 | 9.94 | 10.84 | 3.0 | 2.5 | 7.0 | 7.0 |

## GPU Harbor load-shape medians (synthetic_upstream)

| Config | Conc | decisions/s | router-added p50 ms | sidecar p50 ms | timeout rate |
|---|---:|---:|---:|---:|---:|
| A | 1 | 12.41 | 1.0 | n/a | 0.000 |
| A | 4 | 11.21 | 1.5 | n/a | 0.000 |
| A | 16 | 12.11 | 1.0 | n/a | 0.000 |
| B | 1 | 6.27 | 73.0 | 71.0 | 0.000 |
| B | 4 | 10.69 | 77.5 | 76.0 | 0.000 |
| B | 16 | 10.91 | 77.0 | 75.0 | 0.000 |
| C | 1 | 5.99 | 74.0 | 72.0 | 0.000 |
| C | 4 | 12.16 | 76.0 | 74.5 | 0.000 |
| C | 16 | 10.43 | 76.5 | 75.0 | 0.000 |

## Effectiveness and cost (reused paid #157/#179 portfolio)

- Estimate USD (printed first): 7.105 (abort 100)
- Actual spend USD: 3.261576
- Response rows: 500
- Status counts: ok 349, upstream_error 151
- Error counts: http_429 151
- OpenAI used: false
- Primary absolute USD baseline: `baseten-glm-5-2`
- Per target ok/spend:
  - `qwen/qwen3.5-9b`: ok 75 / 100, spend USD 0.149358
  - `qwen/qwen3.8-27b`: ok 73 / 100, spend USD 0.282625
  - `minimax/minimax-m3`: ok 67 / 100, spend USD 0.241138
  - `accounts/fireworks/models/kimi-k2p7-code`: ok 67 / 100, spend USD 1.009130
  - `zai-org/GLM-5.2`: ok 67 / 100, spend USD 1.579324
- Quality/mix framing: same reused candidate table for static A / cheap / LRP C / glm baseline; no new paid outcomes.

## Drivers

- Seed replay concurrency: 1, 8, 32, 128 (3 runs each; median and spread in JSON)
- Harbor concurrency (GPU only): 1, 4, 16 (3 runs each)

No savings percentages.

## Host lifecycle

- Ephemeral CPU instance was torn down after evidence collection.
- GPU evidence host lifecycle status: `operator_private` (reachability and teardown recorded outside the public tree).
