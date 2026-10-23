<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Qwen3.8-27B run 2 — preregistered, NOT an outcome

Issue #355; Refs #326. Maintainer authorized a second run with a **$150 GPU cap**
and accepted PR #354 as the truthful rejected-run workflow. Run 2 starts on
`feat/355-ft-run2`, created from `origin/main` (`e2be7692`), then fast-forwarded
onto merged main. #354 merged through the orchestrator at2026-10-23T12:07:55Z,
commit`0ea1e6f38826bd3a4961ccb1b5045e3394ea42c2`, retaining **Fixes #326**.
This accepts the workflow, NOT the rejected candidate; base remains default.

**No run-2 GPU is rented, no run-2 held-out calls have run, and no success or
promotion is claimed.** The freeze is
`examples/fine-tune-qwen3p8-27b/run2/preregistration.json`, published before GPU
rental/training/new heldout inference. This page records preparation; model
outcomes and actual hardware/cache/format checks remain pending.

## Verified no-spend findings

- Same ungated Apache-2.0 base:
  `Qwen/Qwen3.8-27B@1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`.
- Additional general-instruction source license checked through the pinned HF
  card and repository LICENSE: **OpenAssistant/OASST1, Apache-2.0**, revision
  `fdf72ae0827c1cda404aff25b6603abec9e3399b`. Attribution: Köpf et al.,
  *OpenAssistant Conversations — Democratizing Large Language Model Alignment*
  (2023), arXiv:2304.07327; LAION/OpenAssistant contributors. Ready-for-export
  message archive SHA256
  `286a6e9a5a413b3272ae9c0b5a20d327983dea1c24342ae28cb244a6da65185c`;
  LICENSE SHA256`c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4`.
  Raw corpus/metadata remain private. Use reviewed English rank0, nondeleted,
  nonsynthetic assistant replies; keep complete root-prompt context.
- Exploratory **format-only** audit of consumed run-1 MMLU responses: base
 191/200 strict single-letter outputs versus29/200 onprem,30/200 Fireworks,
 31/200 weighted FT. This supports a major output-format failure. It does NOT
  re-score accuracy, establish retained knowledge, change run-1's failed gate,
  or consume any run-2 test data (`run1-format-diagnostic.json`).
- The old recipe already requested `tokenizer_default` and
  `enable_thinking:false`. Do not invent a template root cause. Compare the
  actual base-template generation prefix, rendered training transcript and
  supervised tokens on a small synthetic development pre-check.

## Frozen corrections — one candidate, no heldout-driven retuning

- Preserve the pinned base chat template byte-for-byte, special tokens, empty
  non-thinking prefix and answer/end-of-turn format. Verify prefix/token parity
  and basic letter/JSON/native-tool outputs **before training**, on synthetic
  development cases excluded from train/validation/held-out cohorts.
- Lower LR/epochs and LoRA capacity: one epoch, LR1e-5, rank8/alpha16;
  q_proj,v_proj,linear_attn.in_proj_qkv,linear_attn.out_proj only. No MLP/gate/
  vision/lm_head updates. Architecture/BF16 unchanged. Source hashes frozen.
- Mix licensed general instructions with native Glaive tool examples and
  explicit JSON-text task rehearsal. Add deterministic Metrum-authored simple
  multiple-choice format rehearsal, not benchmark answers or teacher rescoring.
  Frozen train5,667:1,000 native Glaive,667 JSON-text same-source variants,
  3,000 reviewed OASST roots,1,000 deterministic letter-format examples.
  Validation500:200 native Glaive,100 JSON variants,200 separate OASST roots.
  Canonical and opaque-JSON-tools Axolotl-view hashes are both checked.
- Fresh Glaive300-case heldout (200tool/100text) plus fresh200IFEval/200MMLU-Pro.
  Exclude all old canonical train/val/test prompts, old evaluated standard
  items, and cross-source exact/normalized overlaps. Also exclude160 public
  calibration user prompts and12 synthetic development probes. Assign source
  groups before within-train/val augmentation; never move heldout examples.
  `dataset-manifest.json` records zero old/cross-split prompt overlap.
  Eight generated files replay byte-identically; CPU Arrow wire-view parity
  passes (`data-reproducibility-precheck.json`). Actual Axolotl cache parity is
  still a required remote pre-training check, NOT yet observed here.
- Keep the same prompts/scorer, pinned judge and numerical promotion gate:
  exact gain≥10pp, standard regressions≤2pp, no higher error rate, p95 latency
  ratio≤1.25. Same-item paired bootstrap CIs and sampling repeats. No relaxing
  the gate or post-hoc answer normalization. Individual FT arms must also be
  reported; do not route standard benchmarks to base to hide regressions.

## Resource and publication controls

Read-only inventory/authentication before renting; cheapest fitting approved
Shadeform or cheaper authenticated Vast offer, fresh key,≥500GB disk,
CUDA≥12.8 and requested≥1Gbps network. Host-loopback vLLM over SSH tunnel only.
For the two immutable official images, require a Docker-capable VM, not a
nested-container workaround. The recorded snapshot's cheapest fitting Shade
candidate is1×RTX PRO6000,$2.19/h,750GB; the lowest VM-capable/not-VM-deverified
Vast hardware quote is$2.648148/h (cheaper container-only/deverified offers do
not qualify). Recheck stock/quote before creation; hardware fit/driver/network
must be measured at boot. See`hardware-selection-preflight.json`.

Independent cost-bounded cleanup watchdog before training; API-verify instance,
SSH-key and any dedicated Fireworks teardown. Operational stop$120, hard GPU
cap$150; budget any vendor serving reservation conservatively as well. Avoid
idle rentals. Authentication failure: **authenticate first, then retry**.

Raw data, responses, signed URLs, secrets and weights stay outside Git. Only
scripts, synthetic fixtures, hashes and scalar results will be published.
No further run-1 training/threshold tuning; the new protocol uses fresh data.
