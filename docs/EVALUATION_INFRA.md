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

### Pinned baseline and current-stable canary (AGENT-01)

AGENT-01 requires the supported pinned baseline and, separately reported, a
current-stable canary. A cell declares which one it is in provenance:

```json
"provenance": {"baseline_kind": "pinned", "client_versions": {"openai": "7.45.1"}}
```

```json
"provenance": {"baseline_kind": "current-stable", "baseline_of": "<pinned cell_id>", "client_versions": {"openai": "7.46.0"}}
```

`baseline_kind` accepts `pinned` or `current-stable`. A current-stable cell must
name its pinned baseline in `baseline_of`, and the validator rejects a report
where that cell is missing, where the referenced cell is not itself
`baseline_kind: pinned`, or where the canary is present without its baseline.
The pinned baseline is therefore never replaced by the canary; both are reported
side by side.

Missing controls or insufficient samples are reported as violations; they
cannot silently become certification.

## Direct-provider differential canary (ARCH-05)

The last ARCH-05 layer compares the router against the provider itself. The same
fixed probes go to the exact provider/model (`canary-direct`) and to the same
model pinned through the router (`canary-router-pinned`); a divergence needs the
outside reference, so router-only tests cannot produce it.

- Probes and their pure local checks live in
  `evaluators/eval-infra/canary-probes.json`. A nonce is generated per run and
  injected into the prompts, so a canned answer cannot pass. No model grades
  another model.
- The report uses the same schema as the other cells, so the preregistered
  latency and cost gates apply unchanged. Both canary cells are preregistered in
  the matrix with `allowed_error_rate: 0.10`.
- The hard gate is a **router regression**: a probe the direct call passed and
  the router call failed. A probe only the direct call failed is provider noise
  and is reported as such, never counted against the router.

```sh
# offline: probes, classification, report and gates against local servers
python3 scripts/direct_provider_canary_test.py

# live, paid, opt-in (never part of test-fast)
CANARY_DIRECT_BASE_URL=... CANARY_DIRECT_API_KEY=... CANARY_DIRECT_MODEL=... \
CANARY_ROUTER_BASE_URL=... CANARY_ROUTER_API_KEY=... CANARY_ROUTER_MODEL_GROUP=... \
make eval-canary CANARY_REPORT=canary.json
make eval-canary-validate CANARY_REPORT=canary.json
```

`CANARY_MAX_COST_USD` (default `0.50`) bounds the run: the direct side stops the
run before the router side if it is already over budget. Exit codes: `0` pass,
`1` gate violation, `2` configuration error.

### Pinned baseline and current-stable canary rows (AGENT-01)

The canary runs twice and merges the two reports, so the pinned baseline and the
current-stable canary are reported side by side and the canary can never replace
its baseline:

```sh
# pinned: the exact dated model on both sides
... CANARY_DIRECT_MODEL=gpt-4o-mini-2024-07-18 ... \
  python3 scripts/direct_provider_canary.py run --baseline-kind pinned --report-out pinned.json

# current-stable: the alias that currently resolves to it
... CANARY_DIRECT_MODEL=gpt-4o-mini ... \
  python3 scripts/direct_provider_canary.py run --baseline-kind current-stable --report-out stable.json

python3 scripts/direct_provider_canary.py merge --pinned pinned.json --current-stable stable.json \
  --report-out canary.json
```

`merge` refuses runs whose cells or probe sets differ, refuses a report whose
pinned cells do not declare `baseline_kind: pinned`, and sets each
`<cell>-current-stable` row's `provenance.baseline_of` to its pinned cell before
validating the merged report with the same schema and gates.

## What still needs paid runs (tracked on #330)

- Nightly repeated trials of the `nightly-live` cells (EVAL-01/03/05) —
  real provider calls.
- Live runs of the direct-provider differential canary (ARCH-05): the layer is
  implemented and offline-tested; a recorded live run needs bounded spend.
- Real-client runs with images and reasoning (AGENT-06).
- Live Harbor coverage beyond HARBOR-01 (HARBOR-02…HARBOR-18).

Each is skipped explicitly until its bounded-spend approval lands; skipped
cells are never counted as certification.

Metrum AI
