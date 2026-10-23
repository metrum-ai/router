<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 video facts — preregistered, no model outcome yet

Issue #355, Refs #326. **Run 2 is in preparation. No GPU rented, no new
held-out inference, no quality improvement/promotion or ROI claim.** Run 1's
rejected-result evidence remains under `2026-10-23/`; do not relabel it run 2.

## Provider/hardware attribution

The example and referenced public evidence use hardware-neutral`vllm_onprem_ft`.
Run 1 actually trained on **1× RTX PRO 6000 Blackwell Server Edition,96GB**,
not B200. Historical evidence labels are normalized, with original receipts and
hashes preserved and all non-label JSON data unchanged; see
`provider-alias-publication-audit.json`. Run-2 hardware is not yet rented or
measured. Do not infer hardware from a provider ID or reuse run-1 metrics as run 2.

## Measured diagnostic, not run-2 quality

Source:`run1-format-diagnostic.json`. In the consumed run-1 MMLU cohort,
strict single-letter output conformance was **191/200 base**, **29/200 onpremFT**,
**30/200 FireworksFT**, **31/200 weightedFT**. Format-only analysis; no accuracy
rescoring, retained-knowledge claim or changed run-1 gate. This motivates a
small disjoint synthetic template/answer-format pre-check, not benchmark tuning.

## License/provenance preflight

Same pinned Apache-2.0 Qwen base. New proposed general instruction source:
OpenAssistant/OASST1, Apache-2.0, pinned
`fdf72ae0827c1cda404aff25b6603abec9e3399b`; see README for attribution and hashes.
No new in-house human labels. Glaive remains pinned Apache-2.0.

## Frozen recipe and fresh data (not measured model quality)

- `run2/preregistration.json` freezes one epoch, LR1e-5, rank8/alpha16;
  q_proj,v_proj,linear_attn.in_proj_qkv,linear_attn.out_proj only. BF16 base and
  base chat template unchanged; no MLP/gate/vision/lm_head updates.
- Train5,667 rows:1,000 native Glaive (667tool),667 JSON-text variants from the
  same training sources,3,000 licensed OASST roots,1,000 computed letter-format
  examples. Val500. Within-split variants are not extra independent sources.
- Fresh300-case heldout (200tools/100text), SHA256
  `db1c9521e7042f116c529d065de126cd1f3c8b020c5f62a6662feb993f082d57`.
  Fresh200IFEval/200MMLU-Pro exclude the old400; no overlap with old2,800 canonical
  prompts. Public calibration questions and12 synthetic probes are excluded.
- Eight generated files replay byte-identically. Opaque-JSON-tools training
  views survive CPU Arrow roundtrip without changing canonical tool definitions.
  This is serialization proof, NOT actual trainer-cache or model correctness.
- Hardware-neutral-name localhost router **mock** continuation/file creation,
  weighted routing (21Fireworks/24onprem observed counters), quota/shadow/privacy
  checks passed. Eight historical JSON evidence files differ only in provider
  labels; all non-label JSON data and original receipt/join hashes preserved.
- Recorded cheapest fitting Docker-VM plan:1×RTX PRO6000,750GB,$2.19/h Shadeform;
  cheapest non-VM-deverified VM-capable Vast hardware quote$2.648148/h. Cheaper
  container/deverified offers do not qualify. These are quotes, not actual run-2
  hardware observations or invoices. Recheck before rental.

## Still to measure

Actual base-template/token parity,
base/candidate pre-check, training runtime/loss/memory, quality deltas/paired CIs,
IFEval/MMLU regressions, native continuation/load/controls, cost and API-verified
teardown. GPU cap **$150**, conservative operational stop$120. Paid spend so far$0.

Do not claim a template bug is proven: run 1 already selected tokenizer_default
and explicit non-thinking mode. Do not soften the frozen strict scorer or route
general regression tests to base to manufacture a pass. Report honestly pass/fail.
