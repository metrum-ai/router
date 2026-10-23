<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Frozen outcome protocol — 2026-10-23

Before any base/FT outcome evaluation. Public human-judge calibration is separate
and cannot be used to tune training hyperparameters or the held-out split.
`preregistration.json` uses the existing gate's exact schema (which does not
permit copyright or arbitrary metadata keys); this companion supplies provenance.

- Control `qwen3p8-27b-base`; candidate `qwen3p8-27b-ft`; 300 held-out first turns.
- Test canonical JSONL SHA256:
  `b46d1c6cd90b67e212fe9c2ec55d46a49201a37892f221f5ac087b795f2ef0d2`.
- Train/val/test: 2000/500/300. Dataset and model revision pins are in README.
- Scorer identity: `eval/scorers/tools.py`, type-strict exact tool/argument multiset
  matching, recursive leaf F1; malformed output scores zero.
  Scorer file SHA256 before first live outcome evaluation:
  `d70d15cd14ffeb8a923473ee9392783fd326155a03d3612c60dbd2debd1c35c6`.
  Record the preregistration commit SHA in evidence before the first live outcome
  evaluation. Public human calibration and tiny hardware smoke are not outcome
  evaluation and do not use the held-out test prompts.
- Require tool exact match improvement **at least 0.10 absolute**. Existing
  `fixed_model_outcome_gate.py` only supports nonnegative permitted regression,
  so its no-regression check is necessary but **not sufficient** for promotion.
  `eval/outcome.py` invokes the existing scalar gate and additionally enforces
  +0.10 improvement, complete paired case coverage and both ≤0.02 regression
  conditions. Never call a scalar-gate-only pass a promotion pass.
- Standard regression: fixed seeded 200 IFEval and 200 MMLU-Pro items,
  ≤0.02 absolute regression each; license/revision and exact scoring must be
  verified before downloading those components.
- Error rate must not increase; p95 end-to-end latency ratio ≤1.25.
- Means + 95% percentile bootstrap CIs, 10,000 resamples, seed 326. Paired
  comparisons use the same case IDs. Temperature 0 for objective outcomes;
  sampling-sensitive repeats at temperature .7 with seeds 326/327/328.
- Pin judge `gpt-4o-2024-11-20`, temperature 0; calibrate against 50 distinct
  public MT-Bench human-labeled questions. Report agreement and 95% bootstrap CI;
  do not represent this as in-domain validation or in-house labeling.
- The fallback shares one adapter across Fireworks and B200: comparisons measure
  serving parity, not independent training quality.
- Report actual token-cost telemetry separately from GPU/deployment hourly costs.
  No cost-ratio threshold is preregistered until hourly prices are measured.

Run the scalar gate with `python3 scripts/fixed_model_outcome_gate.py
--preregistration examples/fine-tune-qwen3p8-27b/eval/preregistration.json
--results /secure/results.json`. Exit codes: 0 pass, 1 regression, 2 invalid input.
No live result or promotion is claimed by this preregistration.
