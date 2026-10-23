<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 video facts — RE-SCOPED, blocked on router trace export #358

## Current facts (not model results)

- Maintainer requires run2 (#355) to train on **governed router-captured agent
  traces exported by #358**, not directly downloaded training data.
- **No FT GPU rented/student trained or qualification-heldout replay; FT paid
  spend$0.** Producer has real-teacher engineering traffic, not a trained-model
  result. Do not show a completed run2 demo yet.
- Direct-download preparation/preregistration at`2bfd8de018` is **superseded**.
  Its5667/500/300 counts, hashes, CPU replay and Arrow checks are historical
  preparation ONLY, not the new run2 training/evaluation cohort.
- Current `run2/preregistration.json` explicitly BLOCKS training. No rental until
  useful-sized #358 export and new shared NVIDIA/AMD signed freeze. Teacher
  rights are CLEARED, not a remaining blocker.
- Keep recipe plan: one epoch, LR1e-5, rank8/alpha16, text-attention projections
  only; BF16 base/template unchanged. General/letter-format anchors must also
  be router-captured/exported. Actual mix/counts await the export.
- Five synthetic template/token fixtures, twelve disjoint letter probes and
  full actual Axolotl-cache parity remain required remote pre-training checks;
  these have NOT run against a real run2 model/export.
- GPU cap$150, operational stop$120. Quotes are not rentals/invoices.

## Router-centric story to prove (pending)

1. Licensed task seeds → teacher agent → real deterministic mock tool execution
   → tool-result continuation **through router**, under several callers.
2. Capture only opted-in support caller/project, with redaction/PII policy and
   retention. Prove other callers are not captured/exported.
3. Audited admin export: filters, dedupe/provenance hashes, canonical chat/tool
   JSONL + manifest, frozen temporal/source-group heldout split.
4. Train the export, route back to the SAME`support-agent` group, canary only
   the selected support caller/project; others stay teacher.
5. Replay captured heldout TASKS through router for candidate vs teacher vs base.
   Execute NEW calls/results; never feed recorded teacher answers to candidate.
6. Report deterministic task success, quality/general gates, latency and all
   attempted routed cost / successful tasks (including failures). Unknown cost
   or zero successes is not a fake free-cost/savings claim. Native success is
   not strict JSON-text exact accuracy; freeze endpoints before inference.
7. Accept/reject honestly; API-verify instance/key/vendor teardown.

Teacher: Fireworks`accounts/fireworks/models/gpt-oss-120b`, Apache-2.0. Maintainer
approved this synthetic Acme/mock-tools technical test at comment6366186842;
`teacher_terms_decision` and pinned model/provider-terms hashes are recorded in
manifest/evidence. NOT general customer-data permission; users must independently
choose/check a teacher whose terms permit output training. Collection cap~$20.
PR361 engineering export1train/3val/3test is consumer-compatible, includes historical
teacher failures, and is NOT a useful training cohort. Shared NVIDIA/AMD recipe
now uses TF32=false, same BF16/SDPA/rank8/LR1e-5/one epoch. Full cohort/freeze and
student measurements are still pending; do not imply the full loop is done.

## Provider-name correction / Run 1 facts that must remain true

- Current provider ID **`vllm_onprem_ft`** is hardware-neutral throughout example
  and referenced public evidence. Eight historical JSON publications changed
  provider labels only; numerical results and original receipt/join hashes stay
  unchanged (`provider-alias-publication-audit.json`). Not a rerun.
- Run1 trained on **1×RTX PRO6000 Blackwell Server Edition,96GB**, not B200.
  Two failed Vast startup rentals on B200 offers were deleted without training;
  later availability recheck created no rental. Actual run2 hardware UNKNOWN.
- PR354 merged as`0ea1e6f38826bd3a4961ccb1b5045e3394ea42c2`, retaining Fixes#326:
  acceptance of reproducible REJECTED workflow, not promotion/production rollout.
- Run1 exact-text tool gain passed, standard regressions failed; **base remains
  default, DO NOT PROMOTE**. Do not present run1 metrics as run2 metrics.
- Historical public judge agreement60%(CI46–74%) is weak, not domain validation.
  Provider base-byte equivalence remains unverified. No retained-knowledge,
  production-safety, quality-equivalent ROI or invoice-total claims.
