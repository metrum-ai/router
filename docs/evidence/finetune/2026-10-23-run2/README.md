<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 (#355) — original gate FAIL; limited merged comparison; resources deleted

**Do not promote.** The governed export was trained and served, but original
NVIDIA base→LoRA JSON-text exact4→5/200 (+0.5pp) fails the required+10pp.
Native tool success is a separate endpoint and cannot rescue that gate.
AMD original FAIL also stands. Non-canary `support-agent` callers remain teacher;
Run1's historical base-default rollback does not authorize changing this group.

- [RESULTS](RESULTS.md): four-part workflow, measured results and limitations.
- [NVIDIA vs AMD merged](NVIDIA-AMD-COMPARISON.md): quality endpoints, training
  wall/throughput, c1/8/32 serving, output-token unit costs and native-harness CPS;
  consumes PR#369 evidence on pinned main. Refs #359, not a closure claim.
- [Machine-readable comparison](nvidia-vs-amd-merged-comparison.json): exact
  source hashes and per-metric caveats. Independently trained BF16 safe merges,
  different CUDA/Axolotl vs ROCm/PEFT stacks: not pure hardware causality.
- [Original gate](nvidia-original-gate.json): full300/200/200 denominators and
  authenticated join audits; frozen SDK grades retained, no new scoring.
- [Merged grader infrastructure audit](nvidia-merged-grader-infrastructure-audit.json):
  **all400 semantic rubric calls rejected pre-route400unsupported_modality**.
  Semantic primary/repeat comparison is UNUSABLE, not model degradation; no
  offline regrade, score borrowing, selective redo or unauthorized rerental.
- [Healthy shared teacher whole-arm rerun](shared-teacher-native-high-rerun.json):
 300episodes once,488authenticated requests,0transport errors; general completion
  is not semantic correctness. Prior227/300RPM429 teacher run remains archived
  as unusable infrastructure evidence. All earlier attempted costs retained.
- [Economics and teardown](economics-and-teardown.json): NVIDIA instance/key
  API-deleted21:25:13Z, earlier vendor deployment/model API-deleted19:44:53Z.
  Known quote/API estimates plus collection upper≈$51.04095, **plus unknown
  upstream charges**; complete total/invoice null. Quotes are not ROI.

Data: full governed1186train/221val/300test, shared pinned base and recipe;
[training receipt](nvidia-fixed-training.json), [safe merge preparation](nvidia-merged-parity-preparation.json),
[workflow/freeze/amendments](../../../../examples/fine-tune-qwen3p8-27b/run2/README.md).
Direct-download preparation remains superseded, never training labels. All labels
are NEW scoped captured teacher outputs; source answers are hidden checker gold.
Teacher-output rights are cleared only for synthetic Acme/mock-tool technical
work, not general customer traffic. Preserve `teacher_terms_decision`; users must
verify both model license and provider output-training rights. Consumer hashes
and attestations are not independent privacy/consent proof. Raw prompts/answers,
request joins, weights, credentials, signed URLs and infrastructure stay outside
Git. [VIDEO-FACTS](VIDEO-FACTS.md) states what may and may not be claimed.
