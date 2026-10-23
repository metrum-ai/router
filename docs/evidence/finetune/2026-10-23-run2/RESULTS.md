<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 RESULTS — BLOCKED on #358; no trained-model result

**GPU cap$150, operational stop$120. No run2 GPU rental, paid deployment,
training or new model/heldout inference. Paid spend$0. Base remains default.**
The directly downloaded cohort/registration published at`2bfd8de018` is superseded,
not run2 training input. Current registration fails closed. No paid work is
triggered by the demo narrative or by synthetic integration checks.

## 1. The industry problem: training is not safe production delivery

A checkpoint alone is not evidence of safe customer-task completion. The gaps this
example addresses are data unlike the customer's work, ungoverned agent traffic,
no controlled path back to callers, evaluation unlike real multi-turn tools, and
cost per token rather than cost per successful task. These are design risks, not
a measured industry-wide failure-rate claim.

## 2. The router-centric solution (full loop still pending)

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
backend. FT's provisional consumer/replay contract is being coordinated there;
actual export/teacher terms/backend agreement and new signed protocol are pending.
Native success and strict JSON-text exact accuracy are different endpoints: define
both before inference; do not silently relabel metrics or relax the original gates.

## 3. What we did: Run 1 results, rejection and lessons

Run1 workflow PR354 merged at`0ea1e6f38826bd3a4961ccb1b5045e3394ea42c2`, retaining
Fixes#326. It accepted a reproducible **negative result**, not candidate promotion.

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
p95 ratio≤1.25. Actual endpoint mapping/mix/hashes await new preregistration.

## 4. What was accomplished — measured only, placeholders otherwise

| Item | Current evidence/status |
|---|---|
| Run1 reproducible workflow |Merged; model rejected; base default |
| Hardware-neutral provider rename |Done; eight-file alias-only invariance audit; no numeric changes |
| Conservative recipe / format safeguards |Implemented; real remote base/cache checks NOT yet executed for new export |
| Export consumer |Proposed versioned JSONL+manifest adapter; synthetic hash/privacy-attestation/rights/time/source-group tests only; not exporter privacy proof |
| Replay engine |Actual localhost working-tree router with synthetic upstreams created a file and continued with NEW tool result; recorded teacher-answer sentinel not sent |
| Routed cost accounting |Synthetic tests include failed attempts, same task sets, missing/invalid costs and zero-success null handling; NOT measured model economics |
| Real #358 export and mix/cohort |**PENDING**; no direct-download fallback |
| Teacher identity/output training terms |**PENDING decision from traces agent/operator** |
| Actual run2 GPU / training / adapter |**NOT RENTED / NOT RUN / NONE** |
| Candidate vs teacher vs base quality/latency |**NOT MEASURED** |
| Cost per successful captured task |**NOT MEASURED** |
| Full quality/general-regression/sampling gates |**NOT RUN** |
| Run2 promotion |**NO DECISION; DO NOT PROMOTE** |
| Run2 teardown |Not applicable yet: no paid resource created; exact API verification mandatory if rented later |

Synthetic plumbing does not validate trained-model quality, uncaptured-caller
export privacy, production safety or quality-equivalent ROI. Historical public
judge agreement60%(CI46–74%) is weak, not in-house domain validation. Provider base
weight byte equivalence remains unverified. Costs are router-recorded estimates,
not invoices. Current filming boundaries: [VIDEO-FACTS](VIDEO-FACTS.md).
