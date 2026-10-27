<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Evaluation infrastructure (#330)

The eval-infra tracker relocated from #271/#94. This document describes the
**offline** infrastructure: the preregistered matrix, the pairwise coverage
generator, and the report schema. Paid nightly/live runs are tracked on #330
with bounded-spend approvals; this repository's CI only validates the
infrastructure deterministically.

## Preregistered matrix (EVAL-01, EVAL-03)

`evaluators/eval-infra/preregistered-matrix.yaml` is the preregistration:

- **Modes** (EVAL-01): `direct` (exact provider/model), `fixed-router`
  (same model pinned through the router), `dynamic-group` (routing policy).
  Identical tasks, client versions, tool implementation, caps, and verifier;
  the seed is recorded, without claiming temperature zero is deterministic.
- **Cells** (EVAL-03): each cell declares its suite, mode, environment,
  `sample_size` (chosen before evaluation), and allowed regression deltas
  (`allowed_score_delta`, `allowed_error_rate`). A single trial of a cell is
  a smoke gate, never broad statistical certification. Regression thresholds
  reference `config/evaluation-policy.example.json`.
- **Task gates** (EVAL-05): `max_time_to_first_event_ms`,
  `max_completion_latency_ms`, `max_tool_turns`, and
  `max_cost_per_verified_task_usd` — cost includes failed attempts.

Validate it (runs in the PR gate):

```sh
python3 scripts/eval_infra_matrix.py            # validate
python3 scripts/eval_infra_matrix.py --emit     # resolved pairwise coverage
```

## Pairwise coverage (ARCH-06)

The supported request-shape matrix is covered by **pairwise combinations plus
mandatory high-risk triples** (stream+tools+reasoning; image+tools+fallback;
continuation+restart+caller-isolation), not by an unbounded Cartesian
product. Excluded combinations are enumerated in the matrix file with
reasons; the validator fails when a pair is neither covered nor explicitly
excluded.

## Report schema (EVAL-03, EVAL-05, AGENT-01)

`scripts/eval_report_schema.py` validates evaluation reports:

- per-cell attempts, passes, optional Wilson 95% pass-rate CI, and paired
  baseline outcomes (wins/losses);
- per-task `time_to_first_event_ms`, `completion_latency_ms`, `tool_turns`,
  `cost_usd` (gated against the matrix task gates; failures count toward
  cost);
- provenance: pinned `client_versions` (an unbounded `latest` is rejected),
  `seed`, and `task_image_digests` (`sha256:` pinned, per AGENT-01).

```sh
python3 scripts/eval_report_schema.py path/to/report.json
```

Missing controls or insufficient samples are reported as violations; they
cannot silently become certification.

## What still needs paid runs (tracked on #330)

- Nightly repeated trials of the `nightly-live` cells (EVAL-01/03/05) —
  real provider calls.
- Direct-provider differential canary layer (ARCH-05 live runs).
- Real-client runs with images and reasoning (AGENT-06).
- Live Harbor coverage beyond HARBOR-01 (HARBOR-02…HARBOR-18).

Each is skipped explicitly until its bounded-spend approval lands; skipped
cells are never counted as certification.

Metrum AI
