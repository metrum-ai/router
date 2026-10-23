<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Corrected-model outcomes: DO NOT PROMOTE

The complete frozen300-case evaluation and200-case IFEval/200-case MMLU-Pro
subsamples reject this candidate. **Base remains default.** No numerical gate
threshold was relaxed; further training against these consumed held-out outcomes
would require a new research protocol/fresh holdout, not repeated score tuning.
Sampling, all serving-lane loads, remote merge, actual controls and API-verified
teardown are complete. Draft PR354 awaits acceptance of a rejected-candidate
workflow example; no merged-issue or production claim.

## 1. Industry problem

Training a fine-tune is not safely reaching production. Public training data is
not the customer's work; ungoverned traffic lacks consent and labels; models need
a safe caller-specific route back; evaluations must match production tools.
Cost per token alone misses failed tasks: retain failures and report cost per
successful task. No positive production ROI is established here.

## 2. Router-centric solution

Teacher serve → opt-in capture for one key/project → outcome labels → admin-only
audited JSONL/manifest export with frozen time splits → external fine-tune →
same `support-agent` group, key-scoped canary → replay router-captured initial
fixtures through the router → preregistered quality/regression gate decides.
Choose a teacher whose model license and provider terms permit output training;
verify permission before export. The scoped maintainer decision for this
synthetic Acme technical test is not customer-data permission.

## 3. What we did: rejected run 1 and lessons

The run-1 results below use direct public data, not router-captured training
data. Weighted strict JSON-text exact score rose 4.5% → 32.0%, but wrapper validity
fell 99.5% → 42.5%; IFEval fell 81.5% → 63.5%, MMLU-Pro 60.5% → 13.0%.
The preregistered gate rejected the candidate. Base stays default. Training and
faster serving do not override data/task mismatch or general regressions.

## 4. Router-loop accomplishments and evidence gaps

A separate engineering sample collected nine task seeds per key through a local
router and real Fireworks `gpt-oss-120b`: eight actual tool-result continuations
and seven deterministic successes per key. Only support was captured; audited
success-only export contained 2 train / 2 val / 3 test rows, no IT rows.
Same-group key+project canary routing used a **synthetic candidate stand-in**,
not a fine-tuned model; a second support request was denied by its caller budget
(429). This is integration evidence, not run-2 training success. Observed public
list prices × router tokens estimate $0.00210315 per nine-task caller cohort,
$0.00030045 per success including failed-task tokens. Cache discounts are ignored;
these are not invoices, hourly serving economics or production ROI. Scalar
source: `router-loop-sample.json`; debugging plus collection estimate $0.01716105.
The checker measures public expected tool-call arguments plus a completed turn,
not semantic final-answer quality; it is not comparable with run 1's JSON-text
metric. No candidate quality or held-out evaluation claim follows.

Pending evidence: useful-sized governed cohort; actual trained-target canary;
frozen held-out routed teacher/candidate/base comparison; comparable latency
and cost per successful task; preregistered run-2 decision. Keep these placeholders
until FT supplies measured results. Private sample dataset and collection/canary
evidence remain outside Git; see the example's `router-loop/README.md` for the
reproducible command and schema. No prompts or raw outputs are published here.

## Strict JSON-text tool-selection/extraction (200 targets)

| Arm | Exact |95% bootstrap CI| Paired gain vs base (95% CI)|
|---|---:|---:|---:|
| Base, onprem |4.5% (9/200)|2.0–7.5%|—|
| FT weighted |32.0% (64/200)|25.5–38.5%|+27.5pp (+20.0–35.0)|
| FT onprem |29.0% (58/200)|22.5–35.5%|+24.5pp (+17.5–32.0)|
| FT Fireworks |32.5% (65/200)|26.0–39.0%|+28.0pp (+20.5–35.5)|

Source:`quality-summary.json`;300 cases per arm,200 tool targets +100 non-tool
rubric targets, all1200 model-response IDs joined to router usage telemetry.
The FT weighted arm actually served151 requests on Fireworks /149 onprem.

**This is a narrow, formatting-sensitive metric, not native API tool-selection
accuracy or general tool-quality improvement.** The base contains a structurally
valid JSON wrapper in99.5% of tool cases, versus42.5% weighted FT (39.0% onprem,
44.0% Fireworks). A fenced JSON payload can pass the containment schema while
failing the frozen strict raw-JSON parser. The low absolute exact scores and
worse wrapper validity must accompany any gain headline. Native API tools are
separate synthetic continuation smokes, not this200-case metric.

At temperature0, non-tool rubric passes were73/100 base versus56/100 weighted,
54/100 onprem and59/100 Fireworks; the judge's weak60% public-human calibration
is separately disclosed. These are not human-coded domain labels.

## General regressions fail the≤2pp rule

| Arm |IFEval (200)|MMLU-Pro (200)|
|---|---:|---:|
| Base |81.5%|60.5%|
| FT weighted |63.5% (−18.0pp)|13.0% (−47.5pp)|
| FT onprem |62.5% (−19.0pp)|12.0% (−48.5pp)|
| FT Fireworks |63.0% (−18.5pp)|12.0% (−48.5pp)|

Sources:`ifeval-summary.json`, `mmlu-summary.json`, including paired bootstrap
CIs. These are pinned seed326 subsamples with frozen output contracts, not
full-benchmark leaderboard scores. All1600 response IDs joined telemetry.

`outcome-weighted.json`, `outcome-onprem.json`, `outcome-fireworks.json` all
**FAIL**, command exit1. All fail IFEval and MMLU-Pro regression limits; isolated
onprem additionally fails the no-increased-error rule (1/300 reported client
errors versus0/300 base). The nested existing scalar gate alone does not test
these standard regressions: **a scalar-gate pass is NOT promotion approval**.

Observed primary p95 client latency:94.720s base,12.769s weightedFT,18.027s
onpremFT,1.242s FireworksFT. These are end-to-end, mixed workload/interleaved
concurrency16 timings with variable completion lengths capped2048, not intrinsic
decode-speed claims. Onprem shares one GPU for base/LoRA; Fireworks uses2×B300
live merge, versus1×RTX PRO6000 unmerged LoRA. Provider base packaging is not
independently byte-verified against HF; the uploaded adapter artifact is the same.

## Sampling and operational completion do not overturn rejection

`sampling-spread.json`:same100 non-tool cases at temp.7, seeds326/327/328;
rubric passes base74/76/69%, weighted54/53/53%, onprem58/58/57%,
Fireworks51/52/54%. Spans7/1/1/3pp. Not300 independent cases, human-coded domain
quality or a new promotion pass; public human judge agreement60% (CI46–74%).

All three final streaming load lanes passed1/8/32 with32requests/profile,
0errors. Remote110s BF16 safe merge and merged native smoke passed; local
merged quality was not re-tested. Actual synthetic rollback/default-base,
caller quota, dynamic_score spend ceiling and caller FT-access revocation passed.
Weighted ignores spend_ceiling; hourly billing needs a separate lease watchdog.

`cleanup-spend.json`:all paid resources/provider SSH key API-verified deleted;
combined known lifecycle estimate$58.408663 (GPU inclVast$7.894820,
Fireworks upper quotes$45.672298, recorded OpenAI$4.841545), not invoices.
`economics-final-*-c*.json` are capacity scenarios, not quality-equivalent ROI.
See VIDEO-FACTS and evidence README for measured rates/scope. Numerical provider
and paired metrics are unchanged by public join aggregation;4000 unique joined
model-response IDs verified, full per-request rows kept private
(`join-publication-audit.json`).

## Accounting corrections, not score tuning

- Canonical cohort hash, prompts, sampling, frozen `tools.py` hash
  `d70d15cd14ffeb8a923473ee9392783fd326155a03d3612c60dbd2debd1c35c6`
  and numerical thresholds are unchanged. Criteria/settings were published in
  verified50a9b5c; typed SDK-variable transport correction in verified3757b2d.
- Initial06:02→06:06UTC run was aborted before any held-out output/score review
  after a separate synthetic integration exposed array-variable expansion.
  Its partial raw files are excluded and retained privately. See protocol note.
- Promptfoo skipped assertions for25 absent tool-text responses (7 weighted,
  18 onprem). The initial SDK-only summary therefore had incomplete denominators.
  The frozen scorer already specifies malformed/absent output scores zero:
  reconciliation invokes that unchanged scorer only for these absent outputs,
  without new inference, and retains all original SDK scores otherwise. Every
  arm and paired objective denominator is now200; the gate enforces exactly200.
- Exported reference values can be redacted by promptfoo. Do not disable its
  redaction or re-score nonempty output against redacted exported references.
  The SDK's original scored references are authoritative; this reconciliation
  never needs their values for an absent output. Raw inputs/outputs and the
  unreconciled summary remain outside Git. `objective_audit` records the method.
- `refusal` marks parsed empty tool-call lists only; malformed output scores0 in
  that field and is exposed by `valid=0` separately. It is not human safety coding.

No retry reduction, production quality equivalence or positive business ROI has
been established. Token-price zeroes are placeholders, not free hourly serving.
