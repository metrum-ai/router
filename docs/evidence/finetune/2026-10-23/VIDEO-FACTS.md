<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Video facts — measured experiment, rejected candidate

**Do not promote. Base remains default.** [PR354](https://github.com/metrum-ai/router/pull/354)
merged at2026-10-23T12:07:55Z as`0ea1e6f3`: maintainer accepted the reproducible
workflow/negative-result example, NOT this candidate. Run 2 is tracked in #355.
All paid resources and the owned provider SSH key are API-verified deleted.
No production traffic, in-house human labeling, quality-equivalent ROI or invoice claim.

## Router-loop engineering sample (#358), separate from run 1

- Real Fireworks-hosted Apache-2.0 `gpt-oss-120b`, local worktree router,
  nine public task seeds under each of two keys (18 episodes). Eight tool-result
  continuations and seven deterministic expected-call successes per key.
- Only support capture enabled. Audited success-only export: 2 train / 2 val /
  3 test rows; IT absent. This tiny sample is not useful-sized training/evaluation
  evidence or a candidate promotion result.
- Same `support-agent` group, key+project-scoped routing demonstrated with a local
  **synthetic candidate stand-in**; other key retained the real teacher. No claim
  that an actual trained candidate was served in this sample.
- Router-loop checker measures tool calls against public gold plus completion,
  not semantic answer quality or run 1's strict JSON-text metric. Keep failures
  in task denominators. Candidate/base/teacher held-out comparison and production
  cost-per-success remain pending, never replace them with token-price claims.
- Scoped maintainer teacher-terms decision and pinned terms SHA are recorded in
  the export manifest. Users must independently verify that teacher license and
  provider terms permit training on outputs; this test is not customer permission.
- See `examples/fine-tune-qwen3p8-27b/router-loop/README.md`; raw prompts, outputs,
  datasets and credentials remain outside Git. No GPU rented for this sample.

## Training: what actually happened

Source:`training-runtime-v2.json`, `dataset-manifest.json`, `prepared-tokenization-v2.json`.

- Pinned Apache-2.0 Qwen3.8-27B + Glaive function-calling-v2. Axolotl immutable
  image, BF16 compute, F32 adapter storage; no Unsloth fallback or managed Fireworks SFT.
- Cheapest qualifying advertised approved Shadeform candidate in the recorded
  search: **1×RTX PRO6000 Blackwell Server Edition,96GB, $2.19/h**,192GB RAM,
  750GB disk, driver580.126.09. The hardware-neutral provider label is now
  `vllm_onprem_ft`; this is publication-label normalization, not a rerun.
  **No B200 training occurred.** Two brief failed Vast B200 startup rentals were
  deleted without training; a later availability recheck caused no new rental.
  Original receipts/join hashes and all metrics are preserved; see the run-2
  `provider-alias-publication-audit.json` for the label-only update audit.
- Corrected **2000/500/300** train/val/test; **1333/333/200** tool targets. Actual
  prepared-cache inspection proves supervised tool tokens were retained.
  Longest observed train example3929 tokens;8192 is the configured cap only.
- **250steps/two epochs,2211s wrapper**,66.17GiB peak torch allocated,
 68.43GiB reserved,72091MiB1Hz driver peak. Validation loss (negative log-likelihood)
  **0.383500→0.155748**, NOT held-out accuracy.
- **108,789,760 parameters/800 tensors**, text decoder only, no vision/lm_head.
  Same adapter local/remote SHA256:
  `1f1ce60dac65f01ba54601c557d821c490ff954a969fbb563425c7728d86bb2e`.
- Wrapper-only training quote **$1.345025**, NOT all-in training/experiment cost.
  The original flawed-data adapter/training is superseded, not usable evidence.

## Serving and real router controls

Sources:`live-smoke-*-final-source.json`, `live-smoke-*-id-rewrite.json`,
`live-smoke-merged.json`, `live-governance.json`, `live-rollback-budget.json`.

- Actual worktree-built router, official vLLM, SSH tunnel/host loopback; no public
  model port. Corrected base/onpremFT/FireworksFT **chat, SSE, strict JSON,
  real temporary file creation and native tool-result continuation PASS**.
- Main integrated through verified GitHub merge `ef6c974a`; all three live smokes
  repeated against its worktree-built binary. Initial local/dev starter used ID
  passthrough; restored base +onpremFT additionally passed with **ID rewrite and
  a fresh private64byte key**. Do not imply a Fireworks ID-rewrite test was run.
- Fireworks base READY but `tunable=false`: standalone Axolotl +same adapter
  upload,0validation warnings. Dedicated **vendor live merge,2×B300 BF16,
  $30/h** default shape independently matched. Not independent Fireworks SFT;
  vendor base packaging is not independently byte-verified against HF.
- Real synthetic100-request canary configured **90base/5onpremFT/5FireworksFT**,
  observed **96/1/3**. NOT an exact observed10% promise or production traffic.
  Recommendation-only shadow served base and logged FT recommendation. The
  synthetic marker was absent from router logs/DB/WAL; not a universal privacy proof.
- Actual rollback removed FT targets from canary: **10/10 canary +1 omitted-model
  request served base**. Base default unchanged; both FT caller permissions then
  revoked and rejected403 before dispatch. Fireworks was already deleted at this
  final authorization check; earlier live inference is independently recorded.
- Actual daily request quota1 denied before upstream; original quota restored and
  base still served. Actual **dynamic_score dollar spend ceiling** rejected with
  HTTP502/no-eligible-target, candidate filter stage`spend_ceiling`, no upstream
  selected. Illustrative configured$1/$1 per-million rates and$1e-9 ceiling;
  NOT an hourly rental budget. **Weighted ignores spend_ceiling**; lease watchdogs
  separately bound real GPU/deployment charges.
- Remote-host BF16 safe merge **110s**, no new training. Merged native smoke PASS;
  local merged quality was **not** re-evaluated (`merged-runtime.json`).

## Frozen results — show the failure beside the narrow gain

Sources:`quality-summary.json`, `ifeval-summary.json`, `mmlu-summary.json`,
`outcome-*.json`, full interpretation in `RESULTS.md`.

| Measure | Base | WeightedFT | OnpremFT | FireworksFT |
|---|---:|---:|---:|---:|
| Strict JSON-text exact,200 tool cases |4.5%|32.0%|29.0%|32.5%|
| Paired exact gain,95% bootstrapCI |—|+27.5pp [20,35]|+24.5pp [17.5,32]|+28pp [20.5,35.5]|
| Outer-wrapper containment validity |99.5%|42.5%|39.0%|44.0%|
| Non-tool rubric pass,100 cases,temp0 |73%|56%|54%|59%|
| IFEval,200 fixed cases |81.5%|63.5%|62.5%|63.0%|
| MMLU-Pro,200 fixed cases |60.5%|13.0%|12.0%|12.0%|

- Strict JSON-text exact is **format-sensitive, low absolute accuracy, NOT native
  API tool-selection accuracy**. Fenced JSON can pass containment but fail raw
  JSON parsing. Wrapper containment is not per-function argument-schema validity.
  Frozen `refusal` means parsed empty tool-call lists; malformed outputs have
  refusal0/valid0 separately, not human-coded safety refusals.
- The≥10pp narrow-gain criterion passes, but≤2pp IFEval/MMLU regression limits
  emphatically fail. **All full promotion wrappers FAIL**; a nested scalar pass
  is NOT approval. Onprem also fails client error-rate increase:1/300 versus0base.
- Primary weighted serving **151Fireworks/149onprem**, distinct from the canary.
  Complete1200primary +1600regression +1200sampling responses, **4000 unique
  joined router IDs** verified. Public aggregates/hashes only; full per-request
  rows/raw outputs private (`join-publication-audit.json`).
- SDK skipped25 absent-output objective callbacks:7weighted/18onprem. Frozen
  scorer assigns these zero, all tool denominators200. Nonempty original SDK
  scores retained: exported references may be redacted. No new inference or
  changed prompt/model/threshold; scorer hash unchanged.
- Primary p95 client latency **94.720s base /12.769s weightedFT /18.027s onpremFT
  /1.242s FireworksFT**. Mixed/interleaved16-concurrency, variable completions
  capped2048; NOT intrinsic decode speed, productionSLO or training-caused speedup.

## Rubric calibration and sampling are weak evidence, not domain validation

Sources:`judge-calibration.json`, `sampling-spread.json`, `sampling-*-summary.json`.

- Licensed public MT-Bench human judgments,50 distinct questions: **60% agreement,
  bootstrap95%CI46–74%**. CC-BY-4.0, Zheng et al., *Judging LLM-as-a-Judge with
  MT-Bench and Chatbot Arena* (2023), arXiv:2306.05685. Pinned source, not GPT labels.
  Judge`gpt-4o-2024-11-20`,temperature0. No in-house labeling.
- Same100 non-tool cases repeated at generation temp.7, nominal seeds326/327/328:
  rubric passes **base74/76/69%, weighted54/53/53%, onprem58/58/57%,
  Fireworks51/52/54%**. Spans7/1/1/3pp. NOT300 independent cases or a promotion pass;
  spread includes generation/routing/judge variability.

## Load and economics — scenarios only

Sources:`load-onprem-lora-final-source.json`, `load-onprem-merged.json`,
`load-fireworks.json`, `economics-final-*-c*.json`.

| Actual serving lane |Outputtokens/s,c1 /c8 /c32|Hourly quote|Fully-utilized c32$/M output tokens|
|---|---:|---:|---:|
|1×RTX PRO6000,unmerged LoRA |23.89 /181.77 /616.09|$2.19|$0.98741|
|Same GPU,local BF16 merged |25.93 /194.81 /660.44|$2.19|$0.92111|
|Fireworks vendor live merge,2×B300 |248.12 /1302.66 /3190.34|$30|$2.61205|

- 32requests/profile,0errors, short synthetic prompt/128-token cap, end-to-end SSE.
  Different hardware/engine/merge modes; no production workload/SLO or statistical
  significance claim. These direct-lane loads do not measure combined50/50 capacity.
- 720h, reference$2.50/$10 input/output, assumed1:1 token mix, one-off **full known
  experiment estimate$58.408663**: theoretical c32 break-even capacity fractions
  **8.192% onpremLoRA /7.642% merged /20.953% Fireworks**. At c1 all exceed100%
  (infeasible). Assumes measured batching efficiency while scaling average load;
  not validated for continuous low concurrency or a changed input/output mix.
  Reference is NOT quality-equivalent; no retry reduction/business payback.
- Older `economics-onprem-lora-c*.json` uses the initial load and only$1.345025
  corrected wrapper upfront; do not confuse it with the final full-lifecycle scenario.
- Zero router token prices are placeholders, NOT free hosting. Candidate fails
  quality gates, so these price scenarios do NOT justify promoting it.

## Final spend, network and teardown

Sources:`cleanup-spend.json`, `fireworks-lifecycle.json`, `network-benchmark.json`.

- Whole GPU lifetime quote including startup/idle/both trainings/serving/controls
  plus both failed Vast rentals: **$7.894820**. GPU hard stop$800.
- Both Fireworks dedicated lifetimes, conservative creation-to-observed-deletion
  upper quotes including advertised unbilled startup: **$45.672298**. Stop$240.
  Recent-traffic guard required explicit`ignore_checks=true` for our disposable
  deployment; GET DELETED verified, uploaded model GET404 verified. Helper now
  exposes an explicit flag and polls GET rather than repeating DELETE on tombstones.
- Recorded router OpenAI usage estimate **$4.841545** including calibration/grading.
  Combined known-lifecycle estimate **$58.408663**, **NOT invoices** or a guarantee
  that aborted/missing API usage was billed zero; excludes unobserved fees/labor.
- Shade deletion API-verified **07:23:47UTC**; owned provider SSH key absent,
  local private key removed. Fireworks deployment GET DELETED/model GET404;
  watchdogs stopped only after verified deletion. Local router/policy stop sent.
- Actual remote public pinned HF100MB Range transfer: **0.483s,1.656Gbps**.
  Cloudflare probe403, so HF fallback used. Not a sustained/upload/link SLA.

## Chronology and prohibited claims

Data parsing/stratification corrected before any held-out FT calls. Initial
06:02→06:06 transport attempt did make held-out calls, then aborted before any
result review after synthetic integration exposed SDK array expansion; excluded
partials retained privately. Canonical corrected cohort/prompt/scorer/thresholds
unchanged in the complete run. Verified freezes and disclosures in evidence README.

**Never claim** promotion, general tool-quality improvement, native API accuracy
from JSON-text scores, strong domain-calibrated judge, quality-equivalent ROI,
invoice totals, independent provider base-byte equivalence, full8192-token training
examples, actual B200 training, or independent managed Fireworks SFT. Further model
research requires fresh held-out data, not tuning to these consumed results.
