<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 RESULTS — original gate FAIL; merged comparison limited; resources deleted

**Do not promote.** Original NVIDIA base→LoRA JSON-text exact4→5/200 (+0.5pp),
not required+10pp. IFEval164→161/200, MMLU123→123/200; native258→265/300 does not
rescue the failed gate. See [original scalar gate](nvidia-original-gate.json).
Additional NVIDIA merged vs AMD merged measurements are in
[NVIDIA-AMD-COMPARISON](NVIDIA-AMD-COMPARISON.md), including quality, train/load
throughput and correctly scoped unit/task costs. **Whole semantic grading repaired
CPU-only under signedcfbdb3f1a4:** exact recorded outputs, unchanged grader/rubric,
800/800authenticatedHTTP200. Primary semantic78vs74/100; combined83vs81/300;
seeds77/76/80vs81/79/73of100. No model generation. Original400unsupported_modality
failures retained as infrastructure, never model degradation; explicit serialized
empty legacy modalities repaired to text after successful one-request regression.
See [whole-step receipt](semantic-step-repair.json), including hashes/cost/cleanup.
StrictJSON/rollback502 controls remain failed/incomplete, not production approval.

Non-canary `support-agent` callers remain teacher; historical Run1 base-default
rollback is not authorization to change this group. Shared CPU-only teacher
whole300 rerun has488authenticated requests/0transport errors/$0.11001480 API
estimate including engineering, not invoice. Degraded227/300pre-route429 teacher
run and all other throttled/aborted attempts remain archived infrastructure
failures with costs; whole high-limit reruns preserve all tasks/scorers/gates.
Training parent96f851a7 and qualification6f8d210b infrastructure amendment remain
distinct; direct-download preparation remains superseded, never training input.

NVIDIA instance AND fresh SSH key API-deleted21:25:13Z after4.10355h, quote$8.98677.
Explicit one-lease extension authorized00:19Z maximum, but cleanup occurred as soon
as model work ended. Earlier vendor deployment/model API-deleted19:44:53Z, full
lifecycle quote$32.39026; no new/recreated dedicated deployment permission.
Known quotes/configured API estimates plus collection upper and new semantic
whole-step/proof$2.881055 sum≈$53.92200 **plus
unknown upstream charges**; complete total/invoice remains null, below120USD
operational stop on observed/reserved bounds. See [economics/teardown](economics-and-teardown.json).
Raw data/gold/answers/joins/keys/weights/infrastructure remain outside Git.

## 1. The industry problem: training is not safe production delivery

A checkpoint alone is not evidence of safe customer-task completion. The gaps this
example addresses are data unlike the customer's work, ungoverned agent traffic,
no controlled path back to callers, evaluation unlike real multi-turn tools, and
cost per token rather than cost per successful task. These are design risks, not
a measured industry-wide failure-rate claim.

## 2. The router-centric solution (qualification limitations explicit)

**Serve → governed opt-in capture for one key/project → outcome labels → audited
export (canonical chat/tool JSONL+manifest, provenance/dedupe, frozen temporal
split) → fine-tune → route back into the same `support-agent` group with a
key-scoped canary/budgets/quotas → replay router-captured tasks THROUGH the router
→ preregistered gate decides.** Other callers stay on teacher and uncaptured.

Task seeds and teacher outputs need applicable training rights. The harness must
execute real multi-turn tool continuations against deterministic reset mock state.
General/letter-format training anchors also come through capture/export, not a
side-loaded training download. Replay candidate/teacher/base from initial task
inputs and newly executed tools, never copied teacher answers. Preserve failed
attempts and compare routed model cost / successful tasks; unknown prices or zero
successes are explicit, not fabricated free inference or savings.

#358 owns capture/export authorization/privacy/audit/selection and collection
backend. Full export validates1186train/221val/300test; all300heldout tasks retain
historical failures98. Both accelerators consume identical hash-bound bytes/views.
Native replay uses EXPORTED masked initial inputs/oracles; auxiliary original
JSON-text/semantic probe uses PRIVATE source gold unchanged. Exactly two tool
oracles differ through output-policy masking; representations/endpoints stay
explicitly distinct, never normalized or restored into training.
Teacher-output rights are CLEARED by scoped maintainer decision6366186842 for
this synthetic technical test, recorded in`teacher-terms-decision.json` and the
manifest. Users still must verify model license/provider output-training terms.
Native success and strict JSON-text exact accuracy are different endpoints: define
both before inference; do not silently relabel metrics or relax the original gates.

## 3. What we did: Run 1 results, rejection and lessons

Run1 workflow PR354 merged at`0ea1e6f38826bd3a4961ccb1b5045e3394ea42c2`, with workflow acceptance under #326. It accepted a reproducible **negative result**, not candidate promotion.

| Measured run1 endpoint | Base | Weighted FT | Decision/context |
|---|---:|---:|---|
| Strict JSON-text tool exact, n200 |4.5%(9/200)|32%(64/200)|+27.5pp, paired95%CI20–35; gain threshold passed |
| IFEval, n200 |81.5%|63.5%|−18pp; fails≤2pp regression limit |
| MMLU-Pro, n200 |60.5%|13%|−47.5pp; fails≤2pp regression limit |

All three FT promotion wrappers rejected; individual onprem/Fireworks results and
error/latency/CIs are in [run1 RESULTS](../2026-10-23/RESULTS.md). Strict JSON-text
exact is not native API tool accuracy. The post-run format audit is exploratory,
not proof of lost/retained knowledge or a proven template root cause. No rescore
or threshold relaxation changes the rejection.

Actual training hardware: **1×RTX PRO6000 Blackwell Server Edition,96GB**, not
B200. The provider label is now neutral`vllm_onprem_ft`; eight historical JSON
publications changed only labels, preserving numerical data and receipt/join
hashes. This is publication normalization, not new inference. Run1 resources were
API-verified deleted; its metrics must not be presented as run2 results.

Lessons retained: keep original base template/thinking/answer format; prove actual
render/token/cache/generation behavior instead of attributing a root cause from
output style alone; reduce LR/epochs/capacity; mix general/format training; evaluate
real governed work, not only a downloaded proxy task. The recipe plan remains one
epoch/LR1e-5/rank8-alpha16/text-attention-only/BF16, with fresh captured cohorts and
unchanged≥10pp exact gain,≤2pp standard regressions, no pass/error regression and
p95 ratio≤1.25. Endpoint mapping/mix/hashes are now frozen before qualification;
additional native no-success-regression guard does not replace the original gate.

## 4. What was accomplished — measured only, placeholders otherwise

| Item | Current evidence/status |
|---|---|
| Run1 reproducible workflow |Merged; model rejected; base default |
| Hardware-neutral provider rename |Done; eight-file alias-only invariance audit; no numeric changes |
| Conservative recipe / format safeguards |Actual unchanged template/cache/full supervised-marker audits and remote prechecks passed; no template root-cause claim |
| Export consumer |Actual full PR361/368 export validates1186/221/300; teacher_terms_decision preserved; consumer attestations not independent consent/privacy proof |
| Replay engine |Actual localhost working-tree router with synthetic upstreams created a file and continued with NEW tool result; recorded teacher-answer sentinel not sent |
| Routed cost accounting |Authenticated shared-teacher estimate and quoted native/load GPU allocations measured; failed/idle attempts included, unknown full costs null, no invoice/quality-equivalent ROI |
| Full #358 training export and mix/cohort |**FROZEN** train405native/523general/258letter; val74/100/47; test200/75/25. Dataset6c437067…; full manifest/row/view/gold/backend hashes in signed registration |
| Teacher identity/output training terms |**CLEARED**: Fireworks gpt-oss-120b, scoped synthetic-test maintainer decision; model/license/provider-terms hashes recorded |
| Actual run2 GPU / training / adapter |Fixed75steps/892.894s, adapter31c622a1…; extra BF16 safe merge85.2s/01c32fb9…; no checkpoint search |
| Candidate vs teacher vs base quality/latency |Original numeric gate FAIL; native is separate. Shared teacher healthy rerun complete; merged semantic whole-step repaired800/800HTTP200, original failures archived |
| Cost per successful captured task |NVIDIA merged whole-native-window quote/success$0.00142097 vs AMD$0.00129650; completion-only mixed harness, not semantic/lifecycle ROI |
| Full quality/general-regression/sampling gates |Original complete frozen cohorts reduced without rescoring: +0.5pp exact fails+10pp. Supplemental objective/native/load comparisons available; semantic comparison invalid |
| Run2 promotion |**REJECT original numeric gate; DO NOT PROMOTE**. Extra merged never rescues it; failed controls and incomplete economics remain |
| Run2 teardown |**API-VERIFIED** NVIDIA instance/key and vendor deployment/model deleted; local router/policy/tunnel stopped and caller tokens removed |

Synthetic plumbing does not validate trained-model quality, uncaptured-caller
export privacy, production safety or quality-equivalent ROI. Historical public
judge agreement60%(CI46–74%) is weak, not in-house domain validation. Provider base
weight byte equivalence remains unverified. Costs are router-recorded estimates,
not invoices. Current filming boundaries: [VIDEO-FACTS](VIDEO-FACTS.md).
