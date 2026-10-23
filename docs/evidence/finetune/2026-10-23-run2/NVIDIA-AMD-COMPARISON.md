<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# NVIDIA merged vs AMD merged — measured comparison, whole semantic step repaired

Refs #359. **Do not promote.** The original NVIDIA LoRA gate FAIL and AMD FAIL
stand. This supplemental merged comparison cannot replace either gate.
**Semantic step repaired CPU-only under explicit orchestrator authority:** signed
amendment `cfbdb3f1a4` preceded all800items (whole400 per merged arm). Original
400NVIDIA rubric failures were infrastructure, not quality; retained separately.
Explicit legacy `modalities: []` overrode text capability after CLI serialization;
fixed grader target to text, proved one authenticated successful grader request,
then executed the unchanged SDK rubric on exact recorded output strings/prompts.
No protocol requirement for live regeneration at this grading step; no evaluated
model calls, GPU, borrowing, normalization or selective redo. All800HTTP200 with
valid rubric responses and authenticated frozen `gpt-4o-2024-11-20` joins.
[Repair receipt](semantic-step-repair.json): new API estimate$2.881055 including
proof; CPU router/token removed. Old failed attempt remains archived/disclosed.

AMD source: merged PR#369 evidence at
[`docs/evidence/finetune-amd/2026-10-23/`](https://github.com/metrum-ai/router/tree/666aa4c55320c78275a32e5a77855a7c8702e802/docs/evidence/finetune-amd/2026-10-23),
pinned main revision `666aa4c55320c78275a32e5a77855a7c8702e802`.
File hashes, raw scalar precision, source identities, units and caveats are in
[nvidia-vs-amd-merged-comparison.json](nvidia-vs-amd-merged-comparison.json).

## Aligned representation, not pure hardware causality

Both use the same pinned Qwen base, governed1186/221/300 export and one-epoch75step
rank8/alpha16 recipe. Both serve a **BF16 CPU safe merge** using byte-identical
`standalone/merge.py` (`5167c168…`),12shards,5GB shard cap. NVIDIA adapter31c622a1…
and AMD adapter9d78ff96… were trained independently. NVIDIA RTX PRO6000 Blackwell
96GB uses Axolotl/CUDA optimized kernels; AMD MI325X VF256GB used the approved
Transformers/PEFT ROCm reference fallback. Torch2.14.1 vs2.10, runtime/platform,
cache/warmup, CPU/network, timing scope and price quotes differ. **No pure GPU
causal speed/quality claim or presumed near-equivalence.** NVIDIA and AMD shard
manifest hashes also use different canonicalization formats; do not compare
those hash strings as a weight-equivalence test.

NVIDIA's infrastructure-only6f8d210b qualification amendment preserves e411's
cases/backend/scorers/gates/dispatch. Caller capacities differ but exceed offered
load: NVIDIA128concurrent/10kRPM/1bTPM; AMD64/20kRPM/100mTPM. Both final c1/8/32
profiles had zero errors. Old self-inflicted throttled runs were excluded WHOLE,
archived with costs, not selectively filtered.

## Quality endpoints

| Frozen endpoint | NVIDIA merged | AMD merged | NVIDIA − AMD |
| --- | ---: | ---: | ---: |
| Original JSON-text exact tools,200 |5/200 (2.5%)|7/200 (3.5%)|−1.0pp |
| IFEval fresh200 |170/200 (85%)|164/200 (82%)|+3.0pp |
| MMLU-Pro fresh200 |124/200 (62%)|124/200 (62%)|0pp |
| Native mock-tool success,200 |180/200 (90%)|180/200 (90%)|0pp |
| All300 native harness success |264/300 (88%)|262/300 (87.33%)|+0.67pp |
| Primary overall (tools + semantic) |83/300|81/300|+0.67pp; paired95%CI[−1.67,+3]pp |
| Primary semantic subset |78/100|74/100|+4pp |
| Non-tool sampling seeds326/327/328 |77/76/80 of100|81/79/73 of100|−4/−3/+7pp |

Native all300 includes75general **completion**, not semantic correctness, and
25letter cases. NVIDIA has70/75general completions and14/25letters; AMD70/75 and
12/25. Frozen letter backend compares the expected letter after surrounding
whitespace strip; this is not a new raw-format scorer. Source declaration failure1
remains in every full denominator without dispatch. Native does not substitute
for the original JSON-text endpoint. Sampling repeats the SAME100cases, not300
independent observations.

Original AMD publication supplies aggregates: no paired uncertainty is invented
for those objective/native endpoints. For the newly authorized semantic step,
private recorded exports provide exact prompt.raw+vars+assertion hash pairs,
100/100 per stage (export completion order differs). Paired bootstrap10000seed326
repeat delta95%CIs: [−12,+4]/[−11,+5]/[−1,+15]pp. These include zero and are not
hardware causality or semantic equivalence. Old primary5/300 and repeats0/100 are
retained as unusable infrastructure assertions in the original
[grader audit](nvidia-merged-grader-infrastructure-audit.json), never model failure.
New whole-step results supersede semantic-only fields, not original gate verdicts.

## Training wall time and throughput

| Fixed75step training | NVIDIA | AMD |
| --- | ---: | ---: |
| Wrapper wall seconds |892.8943|1214.9466 |
| Input tokens/second over wrapper |887.7758|652.4484 |
| Trainer runtime seconds |852.4 (rounded log)|1214.7243 |

Each processed792,690input tokens and581,388supervised tokens. NVIDIA input count
is **derived** from its exact full-template input audit against byte-identical
canonical files and AMD's token audit, not a separately logged NVIDIA throughput
counter. Wrapper ratio is1.3607x, difference−322.0523s. Wrapper/evaluation/model
construction scope and optimized-vs-reference kernels differ; do not headline
this as isolated hardware speedup. Download/bootstrap/idle/serving lifecycle is
separate. Validation NLL is teacher-label likelihood, not heldout correctness.

## Serving throughput and cost per1M output tokens

| Offered concurrency | NVIDIA outputtok/s | AMD outputtok/s | NVIDIA quoted USD/M output | AMD quoted USD/M output |
| ---: | ---: | ---: | ---: | ---: |
|1|25.8738|70.5330|23.51155|14.96542|
|8|194.1444|395.6078|3.13341|2.66819|
|32|658.4734|1400.6127|0.92385|0.75364|

Each profile is32requests, the same integer-list prompt,128output-token cap,
SSE through the localhost router, zero errors. These are end-to-end observations
at different hosts/times, not sustained production capacity or inference-kernel
causality. Unit cost = hourly quote×1e6/(observed outputtok/s×3600), assuming full
utilization at2.19USD/h NVIDIA vs3.80USD/h AMD. **Output tokens only**, not combined
input/output. Excludes training/startup/idle/grading and is not an invoice or
quality-equivalent ROI. Raw scalar profiles: [NVIDIA merged](load-nvidia-merged.json),
[NVIDIA LoRA](load-nvidia-lora.json), pinned AMD `load-amd-merged.json`.

## Cost per successful task — narrow, explicit allocation

| Whole300 native batch | NVIDIA merged | AMD merged |
| --- | ---: | ---: |
| Entire batch serving-window GPU quote |$0.375136|$0.339684|
| Harness successes |264|262|
| Window quote per harness success |$0.00142097|$0.00129650|

The numerator includes failed/input-invalid/idle time for the entire sequential
batch; the denominator is the same mixed300-task harness endpoint. It is **not**
semantic customer resolution, tool-only allocated cost, full lifecycle cost,
healthy-teacher savings or an invoice. Selfhosted router model token prices are
unpriced: router-cost completeness and full lifecycle semantic CPS remain
**unknown/null**, never zero/free. AMD lacks per-task cost vectors, so no invented
class-level allocation or paired economic uncertainty. All historical attempted
costs remain in [economics-and-teardown.json](economics-and-teardown.json).

## NVIDIA LoRA versus merged, controls and teardown

Exact JSON tools5/200 both; IFEval161→170/200; MMLU123→124/200. Native265→264/300,
paired delta−0.33pp,95%CI[−1.67,+1.0]pp; native tools180/200both. Raw final-content
bytes match223/299comparable nonempty/nonerror pairs (74.58%), with1missing/schema
case not counted as agreement. This diagnostic never removes quality cases and
is not semantic equivalence. Authorized repaired merged semantic primary78/100
versus original LoRA83/100 (combined83vs88/300) is descriptive, not a replacement
LoRA gate; original LoRA grades remain untouched.
Serving c1/8/32 LoRA24.2412/185.1950/624.4912 vs merged25.8738/194.1444/658.4734
outputtok/s. Separate original gate remains **FAIL**: base4→LoRA5/200 exact,
+0.5pp below+10pp. Controls are not fully passed: strictJSON502 and rollback502
are retained; merged error classes include `no-eligible-target` and
`routing-policy-error`, with no proven GPU/model root cause. A separate merged
engineering client created a real file, continued with a newly generated nonce
receipt, and authenticated both requests to the merged target: **PASS**. It does
not rescue the failed controls or grade semantics.

NVIDIA instance AND fresh cloud SSH key API-deleted at
**2026-10-23T21:25:13.669113Z**, after4.10355h; rental lifecycle quote$8.98677.
The authorized extension was used only briefly, not idled until00:19Z. Earlier
Fireworks deployment/model were API-deleted19:44:53Z, lifecycle quote$32.39026;
no recreation. Local routers/policy/tunnel stopped and caller tokens removed.
Known pre-repair quotes/configured API estimates plus collection upper≈$51.04095,
plus new whole-step/proof API estimate$2.881055 gives≈$53.92200 **plus unknown
failed-upstream charges**; complete total/invoice remains null. NVIDIA neural
weights stayed on the remote GPU host. CPU grader router/token also removed.
Refs #359; measured comparison is delivered for review, no automatic closure.
