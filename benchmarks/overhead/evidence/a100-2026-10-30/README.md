<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Partial A100 router overhead evidence — 2026-10-30

This run is **not #422 acceptance**. Plain direct and routed latency completed
on the same warmed vLLM backend; decision-model latency and 128 profiles had
policy errors, while both plain profiles failed their guard. The valid
shadow/32 and enforce/32 profiles below are separate runs, not matched plain
profiles. The full private, bounded run, failures and teardown are retained in
`router / v45-proof / 422`, snapshot `61f9259b`.

## Setup

- Dedicated Massed Compute A100-SXM4-80GB, NVIDIA driver 580.126.09, 14
  visible vCPUs (Intel Xeon Platinum 8462Y+), 105,470,996,480 bytes memory;
  router, loader, decision replicas and vLLM shared this host. This is neither
  a small CPU VM nor an edge box.
- Router and profiler built from `fc48a3b53d9cfd8a92bbad92c73382327d794720`,
  Go 1.27.3, `dev_no_license` build tag. Official `vllm/vllm-openai` image
  digest `sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34`;
  Qwen2.5-0.5B-Instruct revision `7ae557604adf67be50417f59c2c2f167def9a775`.
- Plain phase: identical fixed 43-token prompt and 32 output tokens, one
  connection warmup, eight timed requests per worker, three alternating-order
  repetitions at concurrency 1, 8, 32, 128. The synthetic prompt hash and
  workload settings are in `plain-latency/manifest.json`. Uncompressed raw
  JSONL SHA-256 is
  `e34ea94ed7bfa3d29eeb7a6be04fa0b166c5f43f0e0f7aebad90d6e6937a1b9e`.
- Decision profiles: four real d3-lite replicas at revision
  `03560c26f5199c3c04129a2ccb38960c913635e0` and the repository adapter. Each
  32-client profile had 2,048 timed requests plus 32 warmups, and 2,080
  successful telemetry decisions. The path label is an operator declaration;
  the telemetry counts are in `policy-summary.json`.

## Plain latency and time to first content

Values are the mean of three per-repetition (router percentile minus direct
percentile) differences in milliseconds; negative values are measurement
variation, not negative physical overhead. `p99` at concurrency 1 uses only
8 timed requests per phase and is not a robust tail estimate. Streaming
measures the first nonempty content event, not guaranteed tokenizer TTFT;
content-event gaps in the raw summary are not guaranteed inter-token gaps.

| Clients | Non-stream p50 | Non-stream p99 | Stream first-content p50 | Stream first-content p99 |
|---:|---:|---:|---:|---:|
| 1 | 0.596 | -1.398 | 0.468 | 1.064 |
| 8 | 2.890 | 3.208 | 2.072 | 3.184 |
| 32 | 6.565 | 12.430 | 7.913 | 13.960 |
| 128 | 17.215 | 104.675 | 43.284 | 52.508 |

See the committed compressed raw samples, all 24 paired phase summaries and
per-repetition variance in `plain-latency/`. Direct and routed phases are
sequential and alternate order, not simultaneous; these host-specific figures
are not a general SLA. Resource use per 1k requests/s and maximum sustained
throughput were not measured.

## Decision-path profiles

Both 32-client CPU/heap captures passed their stop guard; the observed 8-second
window was polled at a floor of 29/32 active handlers, and handler exits were
tracked at a floor of 32 during CPU stop and heap capture. This does **not**
prove continuous 32/32 occupancy throughout CPU sampling. CPU samples total
only 730 ms in shadow and 750 ms in enforce. In these sparse samples, the
largest flat entry is Linux `Syscall6` (130 ms shadow, 210 ms enforce), with
SQLite binding/execution and runtime GC/locking also present. These samples do
not establish a stable hotspot ranking or quantify the added latency. The heap
profiles are forced-GC snapshots, not steady-state memory per 1k requests/s.

Both plain profiles and both 128 decision-path comparisons were invalid or had
policy errors and are deliberately not committed as valid results. Missing:
clean matched plain/decision profiles at 32 and 128, decision-path latency,
small-VM/edge hosts, representative resource measurements and reproducibility.
Provider cost estimate is not reconciled billing. #422 remains open.
