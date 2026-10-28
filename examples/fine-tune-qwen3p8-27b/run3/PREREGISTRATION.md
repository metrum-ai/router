<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Run 3: fresh source pool and selection freeze (#425)

This is a **pre-collection source and split registration**, not an executed
training run or a promotion decision. Hashes below were generated before any
run-3 teacher, base, candidate or held-out calls. No weights were trained.
A separate collection/runtime and recipe registration must be committed before
collection/training; the unchanged gate is linked below. This file alone does
not authorize a GPU rental or external provider calls.

Run-1/2 issue comments and PRs #354, #360, #361 and #368 record old private
files under `/workspace/router-v430`, which is absent in this environment.
The shared restic snapshot tags/paths and repository evidence were searched;
we did not find the old selected-task or held-out indexes. Per maintainer
instruction, this run uses a newly generated pool instead. Its synthetic
prompts and checker records are distinct by construction from this generator's
own train/validation/test groups; **overlap with historical task prompts cannot
be proven without the old index**. If recovered, check and disclose any overlap;
do not silently replace cases based on scores.

## Source and selection

`build_pool.py` produces 4,200 deterministic Apache-2.0 synthetic source tasks:
1,500 single-tool tasks (support, operations and inventory personas), 2,000
templated general text tasks and 700 A–J-format tasks. The inherited class name
`reviewed_general` is a compatibility label, not a claim of independent human
review. Tasks are repetitive, artificial and publicly reproducible, not customer
traces or evidence of general agent quality. Generated reference assistant
messages are checker-only. **Training answers must come from new governed
router captures**, never these source references. Agent personas are not proof
that distinct caller keys or agent processes have been exercised; collection
must document those separately.

`freeze.py` reproduces the old fixed seed355 grouping algorithm, taking a
predeclared 3,000 train / 500 validation / 300 held-out attempted groups. The
matrix is 1000/1500/500, 167/250/83 and 200/75/25 native/general/letter,
respectively; letters are balanced A–J. Hash and normalized prompt uniqueness
are verified across the three groups. Selection is independent of model
outcomes; do not refill or discard held-out failures based on quality.

| File | SHA-256 |
|---|---|
| `seed-canonical.jsonl` | `5dad6cf400661d747275e7409c358ff1efbab37ca76009b7e8c67476351d3495` |
| `class-by-normalized-user-sha256.json` | `679abc785fab6f01cb85dee490500f389e6fe6c19382542be19cc4161befcb81` |
| `manifest.json` | `45b9b0f0c69f48288608bb9afe37972d0d6141a2801fe7459793f529ac0287fc` |
| `selected-plan.json` | `3310f9cb2d983f72e20cd6a9b3c5489b36758468b30ac00f3b7a0f8411bb1a55` |
| `heldout-gold.json` | `00e09198e5572487764a768619d48c6b7083e48a69e98d992501d861b4362483` |

Reproduce locally, using new private output directories:

```sh
umask 077
uv run --with bcrypt --with jsonschema python examples/fine-tune-qwen3p8-27b/run3/build_pool.py --out /secure/run3-pool
uv run --with bcrypt --with jsonschema python examples/fine-tune-qwen3p8-27b/run3/freeze.py --pool /secure/run3-pool --out /secure/run3-plan
```

Do not run the old `router-loop/full.py` collection entry point against this
pool: it pins a different seed and has run-2-specific source and cost metadata.
A run-3 collector must verify these hashes, record accurate source attribution,
use scoped capture and authorized audited export, persist failure denominators,
retain held-out failures, and freeze its code/budget before any paid call.

## Gate and remaining preflight

The original [`eval/PREREGISTRATION.md`](../eval/PREREGISTRATION.md) numerical
gate remains unchanged: +10 percentage points strict tool exact match; at most
2 percentage points regression each on IFEval and MMLU-Pro; no error-rate
increase; p95 latency ratio ≤1.25. All other original coverage, scoring,
sampling and calibration conditions apply. Native API replay and simple
synthetic general-completion checks are separate, not substitutes for this
gate. Freeze the exact standard regression sets, grader, model/adapter,
training recipe, evaluated interfaces and data/view hashes before training or
held-out inference. A missing or incomplete gate is not a pass.

Run 2 used rank8/alpha16, LR 1e-5, one epoch and a 75-step fixed training run;
see [`run2/README.md`](../run2/README.md). Run 3 must publish its actual recipe
diff and evaluation before claiming completion. Model selection or thresholds
must not be adjusted after viewing held-out results. GPU item cap is $150; report
actual spend separately from quotes and usage-derived provider model costs.
