<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Harbor phase-latency protocol (W2)

Status: **live Harbor publish blocked pending GPU host and operator assets.**
Instrumentation for per-phase sidecar histograms is on branch `w2-lrp-latency`
(PR #216). This file records the reproducible measurement protocol only. It does
**not** publish absolute milliseconds, ceiling fractions, or enforce-viability
claims.

`host_lifecycle_status`: `operator_private`

## Blockers (this environment)

Checked before attempting a live re-run:

| Check | Result |
|---|---|
| Worktree `env.json` | absent |
| `$LRP_DATA_DIR` | unset; no ONNX/bundle assets discovered for a fresh Harbor cell |
| Local NVIDIA GPU (`nvidia-smi`) | unavailable |
| Shadeform API inventory | reachable with operator credentials; **zero** running instances |
| Shadeform / host CLI | not present on PATH |

Do not invent phase timings or savings percentages from prior uninstrumented
Harbor cells. Prior aggregate sidecar p50 rows in `routing-benchmark.md` remain
whole-route evidence only and are **not** phase-attributed.

## Required host scalars (publish when measured)

Record only these public scalars after a successful GPU Harbor cell. Keep
reachability, SSH, instance identifiers, work-root paths, and teardown
bookkeeping outside the public tree (`host_lifecycle_status: operator_private`).

- Calendar date (UTC) of the run
- Router commit SHA and LRP sidecar commit SHA (instrumentation branch)
- Bundle fingerprint / model hashes (embedder + LightGBM)
- Embedding kind and ORT providers (for example `CUDAExecutionProvider`)
- `compute.device` and `strict_device`
- Harbor task set + pinned task revision
- Config cells: A / B / C
- Concurrency cells: 1, 4, 16 (3 runs each; median and spread)
- Synthetic upstream mark for throughput cells
- External policy timeout / sidecar deadline / max decision ms
- Token-bucket histogram label distribution (counts only)
- Tokenizer saturation counter rate (`lrp_tokenizer_saturation_total` / routes)
- Absolute phase latencies in milliseconds (see metric names below)
- Absolute parent route latency (`lrp_route_latency_seconds`) for join

## Phase metric names

Parent histogram (unchanged):

- `lrp_route_latency_seconds`

Per-phase histogram (bounded labels only; never prompt text):

- `lrp_route_phase_latency_seconds{phase,token_bucket}`

Closed `phase` label set:

- `receive`
- `tokenize`
- `embed`
- `featurize`
- `predict`
- `select`
- `respond`

Tokenizer ceiling counter:

- `lrp_tokenizer_saturation_total` (left-truncation hit at `max_seq_len=512`)

## Token buckets

Feature-frame token estimate maps to a closed `token_bucket` label set:

| Label | Estimated tokens |
|---|---|
| `0-31` | `[0, 32)` |
| `32-127` | `[32, 128)` |
| `128-511` | `[128, 512)` |
| `512-2047` | `[512, 2048)` |
| `2048+` | `>= 2048` |
| `unknown` | missing / non-finite / negative |

Harbor load-shape evidence should report absolute phase p50/p95/p99 **ms** per
`(config, concurrency, token_bucket)` where sample size is adequate. Prefer the
Harbor-dominant buckets actually observed; do not extrapolate empty buckets.

## Reproducible command (operator GPU host)

Offline harness validation (no GPU required; does not publish live timings):

```bash
python3 scripts/lrp_routing_benchmark_test.py -q
python3 scripts/lrp_routing_benchmark.py validate \
  --document docs/evidence/learned-routing-policy/routing-benchmark.json \
  --schema docs/evidence/learned-routing-policy/routing-benchmark.schema.json
```

Live Harbor-shaped load with phase instrumentation (GPU host only; assets via
`$LRP_DATA_DIR`):

1. Provision an ephemeral remote GPU host; leave lifecycle private.
2. Install CUDA ONNX Runtime and load the signed/operator bundle from
   `$LRP_DATA_DIR` (never commit bundles or secrets).
3. Checkout the instrumentation commit; start router + LRP sidecar with
   `compute.device=cuda:0` and `strict_device=true` for GPU claims.
4. Drive Harbor load shape for configs A/B/C at concurrency **1 / 4 / 16**,
   three runs per cell, task set `aider/polyglot_python_two-bucket` at the
   pinned revision recorded in `routing-benchmark-manifest.json`.
5. Use a deterministic local synthetic upstream for throughput cells so Harbor
   load does not burn paid model budget. Harbor pass rate remains load-shape
   metadata only.
6. Scrape sidecar `/metrics` after steady state and export
   `lrp_route_phase_latency_seconds`, `lrp_route_latency_seconds`, and
   `lrp_tokenizer_saturation_total` as absolute milliseconds / counts.
7. Tear the host down. Publish scrubbed JSON/Markdown under
   `docs/evidence/learned-routing-policy/` with
   `host_lifecycle_status: operator_private`.

Public artifacts must omit SSH invocations, public IPs, instance UUIDs,
operator work-root paths, and long-lived host retention flags.

## Stage-09 interactive budget (template)

Fill only after the instrumented Harbor re-run. Until then every measured cell
is `TBD pending Harbor re-run`.

| Field | Value |
|---|---|
| Stage id | stage-09 (interactive / agentic Harbor load) |
| Measurement status | TBD pending Harbor re-run |
| Enforce viable for agentic traffic? | TBD pending Harbor re-run |
| Dominant token bucket(s) | TBD pending Harbor re-run |
| Tokenizer saturation fraction | TBD pending Harbor re-run |
| Parent sidecar p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `receive` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `tokenize` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `embed` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `featurize` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `predict` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `select` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Phase `respond` p50 / p95 / p99 (ms) | TBD pending Harbor re-run |
| Interactive budget decision | TBD pending Harbor re-run |

No savings percentages. Report absolute milliseconds and token categories only
when measured.

## Mitigation candidates (no winner assumed)

Evaluate only after stage-09 measurements implicate input length / embed cost.
If the instrumented run shows input length is **not** implicated, publish that
negative result and stop before adopting a mitigation.

Candidates (unordered; no assumed winner):

1. **Prefix-only embedding** — embed a bounded prefix instead of the full
   Harbor-scale prompt body.
2. **Prefix-hash cache** — cache embeddings by a prefix hash for repeated agent
   turns with shared heads.
3. **Smaller / quantized long-input embedder** — alternate ONNX int8 (or smaller)
   embedder for long token buckets only.
4. **Length-threshold abstention** — abstain to a static/anchor path when the
   feature-frame token estimate exceeds a measured threshold.

Selection requires measured phase attribution (especially `tokenize` / `embed`
versus the rest) plus ceiling-fraction evidence. Do not pick a winner from this
protocol document alone.
