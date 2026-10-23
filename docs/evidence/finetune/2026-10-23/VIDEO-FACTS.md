<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Video facts — work in progress

## Current caveat — read before using any headline

**05:01 UTC:** the first complete two-epoch run is **superseded**. Pre-outcome
dataset QA found Python-literal function arguments were being rejected, leaving
zero tool targets in the original test cohort. No held-out model outcomes had
been collected. Safe parsing + mandatory 2:1 stratification now produces
1333/2000 train,333/500 validation,200/300 test tool targets; see corrected
`dataset-manifest.json` and the protocol amendment. Fresh smoke/retraining is
running. First-adapter live native/tool/schema smoke proves plumbing only; it
must not be sold as corrected-task quality improvement. Fireworks first upload
validated, but its still-CREATING 2×B300 deployment was deleted and API-verified;
no successful Fireworks inference yet.

## Measured

- **Axolotl BF16 LoRA hardware smoke passed on 1× RTX PRO 6000 Blackwell 96GB.**
  Source: `docs/evidence/finetune/2026-10-23/hardware-smoke-runtime.json`,
  `exit_code=0`, `steps=2`, `sequence_length=512`, `rank=16`, `alpha=32`.
  Peak allocated **53.11 GiB**, trainer runtime **47.31 seconds**, total wrapper
  wall time **88.746 seconds**. Caveat: tiny smoke with compile/model-load
  overhead; not full training, production throughput, latency or promotion.
  Axolotl 0.21.1.dev0 from an immutable official image supports the architecture;
  Unsloth fallback was not needed.

- **Pinned judge–human agreement: 60%, 95% bootstrap CI 46–74%, n=50.**
  Source: `docs/evidence/finetune/2026-10-23/judge-calibration.json`, fields
  `agreement`, `agreement_95_ci`, `sample_count`, `judge`, `revision`.
  Caveat: one public MT-Bench human preference per distinct question; pairwise
  answer-quality comparison with `gpt-4o-2024-11-20` at temperature 0 through a
  local worktree router. This is weak agreement, not domain-specific validation
  or a fine-tuning quality gain. No in-house human labeling.
- **Calibration token usage: 57,166 input / 98 output. Estimated $0.143895.**
  Source: same JSON, sums of `records[].prompt_tokens` and
  `records[].completion_tokens`; rates $2.50/$10.00 per 1M in
  `examples/fine-tune-qwen3p8-27b/config.qwen3p8-ft.example.yaml`.
  Caveat: estimated API token charges, not invoice; excludes a $0.000045 direct
  availability smoke. Not training or serving economics.

## Quotes and incomplete runs — not quality evidence

- **Shadeform 1× RTX PRO 6000 Blackwell quote: $2.19/hour**, 96GB VRAM,
  750GB disk. Source: `/workspace/router-v430/ft-step0/shade-current.json`
  (massedcompute RTXPro6000), recorded in track report at 03:52 UTC.
  Caveat: tiny BF16 smoke passed. First full training completed but is
  superseded due to the dataset bug above; corrected training is running.
  This hourly quote is not cost per output token.
- **Vast failed-rental estimate: $0.124168 total.** Source:
  `/workspace/router-v430/ft-step0/instance-57316233-deleted.json` and
  `/workspace/router-v430/ft-step0/instance-57316282-deleted.json`, sum of
  `estimated_cost`. Caveat: calculated hourly quote × lifetime, not invoice.
  Both deleted and API absence verified; no training completed on either.

**FT quality deltas, production routing split, model latency, cost per 1M output
and break-even volume: not yet measured.** Synthetic mock routing counts must
not be presented as a real FT traffic split. Do not claim corrected-model
completion, Fireworks serving or promotion until the corresponding live numbers
are added here with exact sources. First-adapter native serving is historical
plumbing evidence only, not evidence for the corrected training cohort.
