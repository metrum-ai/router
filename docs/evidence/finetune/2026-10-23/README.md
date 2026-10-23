<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Qwen3.8-27B fine-tuning evidence — candidate rejected

Track FT, issue #326, Metrum AI Router. Corrected training and both-stack native
serving passed. **The frozen promotion gate FAILS. Base remains default.**
See [RESULTS.md](RESULTS.md) for the complete300-case quality and200+200 standard
regression results. Sampling, all three serving-lane loads, remote BF16 merge, actual rollback/budget
controls and API-verified paid-resource teardown are complete. Draft
[PR354](https://github.com/metrum-ai/router/pull/354) awaits product acceptance of
a rejected-candidate workflow example; no production or merged-issue claim.

## Result and claim boundaries

Strict **JSON-text** exact match on200 tool targets:4.5% base versus32.0% weighted
FT, paired gain+27.5pp (95% bootstrapCI+20–35pp). This is format-sensitive, NOT
native API tool-selection accuracy. Wrapper JSON containment validity worsened
99.5%→42.5%; absolute accuracy remains low. Onprem FT29.0%, FireworksFT32.5%.

General regressions reject promotion: weighted IFEval81.5%→63.5% and
MMLU-Pro60.5%→13.0%, exceeding the≤2pp limits. All three full wrappers fail;
**the nested scalar gate alone is not promotion approval**. No threshold relaxed.
Same adapter artifact across stacks, not independently trained models; vendor
base packaging is not independently byte-verified against HF.

## Current artifacts

| Evidence | Source |
|---|---|
| Pinned licensed data, corrected hashes |`dataset-manifest.json`, `regression-manifest.json`|
| Actual prepared tool supervision |`prepared-tokenization-v2.json`|
| Corrected250-step BF16 training, peak memory, adapter hash |`training-runtime-v2.json`|
| Public human-label judge calibration |`judge-calibration.json`|
| Actual localhost worktree-router chat/SSE/schema/file/tool continuation |`live-smoke-base.json`, `live-smoke-onprem.json`, `live-smoke-fireworks.json`|
| Real synthetic canary/shadow, telemetry and marker absence |`live-governance.json`|
| Unmerged SSE load1/8/32 |`load-onprem-lora.json`|
| Complete quality, all1200 response-ID joins |`quality-summary.json`|
|200IFEval +200MMLU-Pro per arm,1600 joins |`ifeval-summary.json`, `mmlu-summary.json`|
| Authoritative rejection |`outcome-weighted.json`, `outcome-onprem.json`, `outcome-fireworks.json`|
| Final-source loads and remote merge |`load-onprem-lora-final-source.json`, `load-onprem-merged.json`, `load-fireworks.json`, `merged-runtime.json`|
| Sampling spread, same100 cases/seed |`sampling-spread.json`, `sampling-326-summary.json`, `sampling-327-summary.json`, `sampling-328-summary.json`|
| Actual rollback, quota, spend-ceiling and permission denial |`live-rollback-budget.json`|
| Complete4000unique joins, full per-request rows private |`join-publication-audit.json`|
| Final hypothetical capacity math, NOT quality-equivalent ROI |`economics-final-*-c*.json`|
| Verified paid teardown and known lifecycle estimates |`cleanup-spend.json`, `fireworks-lifecycle.json`|
|100MB public HF Range transfer, not sustained/link SLA |`network-benchmark.json`|

Only scalar/allowlisted telemetry is published. Licensed raw data, test YAML,
responses, full logs, keys, signed URLs and weights remain outside Git. Full
per-request joined rows are private; public summaries contain aggregates and
hashes, with all provider/paired numerical metrics verified unchanged. A few
synthetic native/governance request IDs are opaque router IDs, not credentials.

## Training and serving facts

- Base:`Qwen/Qwen3.8-27B@1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`, Apache-2.0.
  Data:`glaiveai/glaive-function-calling-v2@e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac`,
  Apache-2.0. License details/attribution in root `MODEL_LICENSES.md` and example.
- Axolotl0.21.1.dev0 immutable image, no Unsloth fallback.1×RTX PRO6000 Blackwell
  Server Edition96GB,192GB RAM,750GB disk, driver580.126.09, quote$2.19/h.
  Cheapest qualifying advertised approved candidate in the recorded search.
  Base download~240s. Remote100MB pinned public HF Range transfer measured
  0.483s/1.656Gbps (Cloudflare probe403). Not a sustained/upload/link guarantee.
- Corrected2000/500/300 train/val/test; tool targets1333/333/200. Prepared-cache
  inspection proves supervised native tool markers were retained. Max observed
  train example3929tokens;8192 is a configured cap, not an observed example.
- Two epochs/250steps,2211s wrapper;66.17GiB peak allocated, driver72091MiB.
  BF16 compute, F32 adapter storage;800 tensors/108,789,760 parameters, text
  decoder only. Validation loss (negative log-likelihood)0.383500→0.155748 is not tool accuracy.
- Adapter SHA256 local+remote:
  `1f1ce60dac65f01ba54601c557d821c490ff954a969fbb563425c7728d86bb2e`.
- Official digest-pinned vLLM, host-loopback through SSH tunnel. Fireworks base is
  READY but `tunable=false`; approved fallback is standalone training + upload.
  Adapter validated0warnings; dedicated live merge,2×B300 BF16 default matched
  shape, quote$30/h. No managed Fireworks SFT or independent training claim.
- Public human calibration: MT-Bench `human` split,50 distinct questions,
  revision`f7d2896d2cc5d80f8b55c2bbc722613555233c25`, CC-BY-4.0 per pinned card.
  Attribution:Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot
  Arena* (2023), arXiv:2306.05685. Agreement60%,95%CI46–74%; weak, not domain
  validation. Judge`gpt-4o-2024-11-20`,temperature0. No in-house labeling.

## Commands actually run

From repository root (private paths shown as this run's operator paths):

- `make ft-example-test`:PASS,26Python +2Node privacy tests +2/2promptfoo mocks
  and planted scalar gate tests. `make docs-qa`, source headers, `make secret-check`:
  PASS. Full `make test-fast`:PASS after an initial240s harness timeout/retry.
- `python3 examples/fine-tune-qwen3p8-27b/eval/router_e2e.py --router-bin /workspace/router-v430/ft-step0/router`:
  synthetic mock PASS, weighted/tool continuation/quota denial/base-serving shadow.
- `eval/live_smoke.py --token-file tmp/ft-router/router.token --model <base|FT-onprem|FT-fireworks> --out <scalar-path>`:
  actual trained-model chat/SSE/strictJSON/file creation/native continuation PASS.
- `eval/governance.py --token-file tmp/ft-router/router.token --usage-db tmp/ft-router/usage.sqlite --router-log /workspace/router-v430/ft-step0/router.log --out <scalar-path>`:
  PASS; configured90/5/5 canary observed96base/1onpremFT/3FireworksFT in100,
  base-serving recommendation-only shadow; marker absent. Not production traffic.
- `eval/load.py --token-file tmp/ft-router/router.token --model qwen3p8-27b-ft-onprem --hourly-usd 2.19 --requests 32 --out <scalar-path>`:
  PASS,0errors at1/8/32;23.83/181.13/599.42 outputtokens/s. Synthetic short-prompt
  128-token cap, not productionSLO; see exact source/caveats in VIDEO-FACTS.
- `eval/run_eval.py --tests /workspace/router-v430/ft-step0/held-out-v2.yaml --token-file tmp/ft-router/router.token --out /workspace/router-v430/ft-step0/outcomes/quality.json`:
  complete1200results, exit100 means assertion failures, not missing cases.
  Same runner with `regressions/tests.yaml`:complete1600results. Default16
  concurrency,2048cap, non-thinking; temp0 objective, top_p.95/top_k20, seed326.
- `eval/summarize.py --input <private-result> --journal <safe-ID-journal> --usage-db tmp/ft-router/usage.sqlite --out <scalar-summary>`:
  complete joins; `--case-prefix ifeval|mmlu-pro` for regressions.
- `eval/outcome.py --quality <quality-summary> --ifeval <ifeval-summary> --mmlu <mmlu-summary> --base openai:chat:qwen3p8-27b-base --candidate <FT-arm> --out <wrapper>`:
  all three exit1, expected **promotion rejection**, not permission to promote.

Commands prefixed `eval/` above run via the private Python venv from the example
directory (or repository-root example path); no real caller token appears in argv.
Raw command logs and exact generated configs remain private. Full executable
setup/cleanup instructions are in the example README; these are run evidence.

## Preregistration, excluded attempts and accounting

Original pre-outcome verified commit:[a5a60109](https://github.com/metrum-ai/router/commit/a5a60109d20e3eaa5c4275efc4905cd4a7d2c920).
Corrected cohort:[55834c5a](https://github.com/metrum-ai/router/commit/55834c5ad4999aeb12160a9835d21ce87f3aa5f0).
Final settings:[50a9b5c9](https://github.com/metrum-ai/router/commit/50a9b5c9586a46f6c33081581ff4c9bf2d796620).
Typed transport correction:[3757b2d2](https://github.com/metrum-ai/router/commit/3757b2d2669c5ccfc1c6ae8c9b2e05c9dc7d621d).

The first adapter was superseded before any held-out calls: JSON-only argument
parsing left zero original test tool targets. Safe literal parsing +2:1
stratification fixed this; old smoke `live-smoke-onprem-superseded-data.json`
is historical plumbing, never corrected-task quality. First Fireworks deployment
was deleted/API-verified04:56:23UTC; first model GET404 verified05:00:52UTC.

A separate synthetic integration exposed SDK array expansion; initial06:02→06:06
attempt aborted before any held-out result review, raw partials excluded. The
complete run uses the transport guard. SDK skips25 absent-output callbacks;
unchanged frozen scorer assigns those zero, all objective denominators200.
Original SDK scores are retained because exported references may be redacted.
See `RESULTS.md` and the protocol; no new inference or threshold changes.

## Final known lifecycle estimates and verified cleanup

| Component | Quote/usage estimate | Scope |
|---|---:|---|
| Corrected training wrapper |$1.345025|Included in whole GPU lifetime below, NOT added twice|
| Whole GPU lifetime incl both failed Vast rentals |$7.894820|Creation-to-observed-deletion quote; all training/idle/serving/validation|
| Both Fireworks lifetimes |$45.672298|Conservative upper quotes, includes advertised unbilled startup|
| Recorded OpenAI calibration/grading |$4.841545|Router token usage/list rates; not invoice|
| Combined known experiment estimate |$58.408663|Not invoice/complete-fee guarantee or quality-equivalent ROI|

See `cleanup-spend.json` and `fireworks-lifecycle.json`. Shade instance deletion
API-verified07:23:47UTC, owned provider SSH key absent and local private key gone;
Fireworks exact deployment GET DELETED and uploaded model GET404 verified.
Recent-traffic guard required explicit`ignore_checks=true` on our disposable
owned deployment; public helper now exposes a flag, checks GET before mutation
and polls GET rather than repeatedly deleting tombstones. Independent watchdogs
stopped only after verified deletion; owned router/policy stop sent.

Main integrated via verified GitHub merge`ef6c974a`, rebuilt from the worktree:
all three native smokes repeated (`*-final-source.json`). Restored base/onpremFT
additionally passed ID rewrite with a fresh private64byte key
(`*-id-rewrite.json`); Fireworks native smoke used the local/dev passthrough
starter, not an asserted rewrite test. Main-updated `make test-fast`:exit0,
including379s router tests. Native merged smoke and all final1/8/32 loads:PASS.
Actual rollback/default, request quota, dynamic_score spend ceiling and caller
FT-permission revocation:PASS (`live-rollback-budget.json`). These are synthetic
controls; no promotion occurred. Weighted ignores spend_ceiling; hourly charges
require the separate lease/watchdog, not illustrative token prices.

Final c32 outputtokens/s:616.09 onpremLoRA /660.44 merged /3190.34 Fireworks,
0errors/32requests per profile. Fully-utilized quote$/million outputs:
0.98741 /0.92111 /2.61205, respectively. Final720h scenarios include the full
$58.408663 known experiment upfront, reference$2.50/$10 input/output and assumed
1:1 token mix:break-even capacity fractions8.192% /7.642% /20.953%. Batching
at scaled duty-cycle/changed mix is unvalidated; reference quality equivalence
is unproven. Older initial-load scenarios include only the corrected wrapper
upfront. Zero router token prices are placeholders, NOT free hosting.

Sampling temp.7 over the same100 cases, seeds326/327/328:base74/76/69%, weighted
54/53/53%, onprem58/58/57%, Fireworks51/52/54%. Weak rubric calibration, not300
independent cases or domain validation; no promotion or positive business ROI.
