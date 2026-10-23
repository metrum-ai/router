<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Run 2 (#355): preserve base formatting, test a fresh cohort

Refs #326; run 1's rejected-workflow PR #354 is merged. This is a separate
experiment, not a relabeling/re-score of run 1. Results/current filming facts
belong in `docs/evidence/finetune/2026-10-23-run2/`. Do not promote without the
unchanged full gate, and do not infer production validation from public benchmarks.

## Before spending or model calls

Prepare outside Git with the CPU requirements in `../eval/requirements.txt`.
Do not install the training stack or load neural weights on the coding-agent
machine. All loading, training and merging occurs on the remote GPU host.

Sources are pinned Glaive plus **OpenAssistant/OASST1, Apache-2.0**, revision
`fdf72ae0827c1cda404aff25b6603abec9e3399b`. Download the latter's
`2023-04-12_oasst_ready.messages.jsonl.gz` outside Git; retain its LICENSE/card.
Credit Köpf et al., *OpenAssistant Conversations — Democratizing Large Language
Model Alignment* (2023), arXiv:2304.07327, and LAION/OpenAssistant contributors.
Only reviewed English rank0 nonsynthetic/nondeleted root pairs are eligible;
known PII/spam flags and control-token literals are filtered, metadata dropped.
This is licensed public training data, not new in-house human evaluation labels.

Reconstruct run-1 exclusions with the unchanged `../prepare_data.py` (2,000 train,
500 val,300 test, seed326) and `../eval/prepare_regressions.py` (200+200, seed326).
Their original published hashes are checked, not trusted from arbitrary inputs.
The pinned MT-Bench human parquet is used only for conservative question exclusion
(and separately judge calibration), never as new training targets.

```sh
python run2/data.py --glaive /secure/glaive.json \
  --oasst /secure/oasst-ready.messages.jsonl.gz \
  --mtbench /secure/mtbench.parquet --old-canonical /secure/run1-canonical \
  --regressions /secure/run1-regressions --out /secure/run2-canonical
```

Run from the example directory. Source-group split precedes augmentation. Seed355
selects fresh300 heldout (200tool/100text), excluding all2,800 old canonical
prompts,400 old standard items and160 public calibration user prompts. Fresh
standard cohorts contain200IFEval/200MMLU-Pro, excluding the old items and
MMLU question stems. Exact/NFKC-casefold-whitespace overlap fails. The12 synthetic
MC development probes are excluded, including their stems; training arithmetic
uses operands1..99, probes201..999.

Training5,667 rows:1,000 native Glaive (667tool),667 same-source JSON-text rehearsal,
3,000 OASST roots,1,000 Metrum-authored arithmetic letter-format examples.
Validation500:200 native Glaive,100 JSON-text variants,200 separate OASST roots.
Within-split augmentation is not extra independent source data. No heldout item
is augmented into training. The JSON-text prompt is the unchanged public task
contract, not copied heldout answers.

Canonical files retain array-valued tools and schema-valid messages. Separate
`train-axolotl.jsonl`/`val-axolotl.jsonl` views encode tools as opaque JSON strings,
which Axolotl decodes before rendering. This prevents Arrow nested-struct
null-fill/key-order changes, without changing tool schemas, the chat template,
references or source groups. Canonical and view hashes are both frozen/checked.
Do not attribute run-1 regressions to this mechanism without actual evidence.

Publish signed `preregistration.json` and its referenced manifests/hashes before
training/heldout inference. No hyperparameter/checkpoint search: final adapter
from one epoch only. Rank8/alpha16, LR1e-5, no MLP/gate/vision/lm_head updates;
only q_proj,v_proj,linear_attn.in_proj_qkv,linear_attn.out_proj. Same BF16 base,
immutable Axolotl image, default base template and explicit non-thinking mode.

## Pre-check and training on the GPU host

Rent only after read-only auth/inventory and freeze. Require a Docker-capable VM,
≥500GB disk, compatible CUDA (image requires CUDA13-capable driver), requested
≥1Gbps network, fresh SSH key and independent deletion watchdog. Compare approved
Shadeform and Vast quotes; cheap non-VM or VM-deverified offers do not qualify
for this two-official-image workflow. **GPU hard cap$150, operational stop$120.**
Budget dedicated serving conservatively too. Authentication failure:
**authenticate first, then retry**, not another idle rental.

Upload source/frozen registration and train/val canonical+views+manifest only;
keep heldout data on the evaluation machine, not the training host.

1. Download the pinned base on the GPU host. Run `run2/template_check.py` inside
   the pinned Axolotl image, with `--remote-gpu-host --base /data/base
   --preregistration /data/run2/preregistration.json --out /data/base-template-check.json`.
   Five synthetic cases verify template bytes, non-thinking/answer/EOT strings
   and generation/training token-prefix parity; this loads tokenizer metadata,
   not neural weights.
2. Start official vLLM in `MODE=base` with host-loopback publishing and SSH
   tunnel. Build/start the localhost router from the working tree, with fresh
   caller/ID-rewrite keys and hardware-neutral provider`vllm_onprem_ft`.
   Run `run2/precheck.py` through the router against`qwen3p8-27b-base` with
   `--execute --token-file ... --preregistration ... --out ... --raw-out ...`.
   Twelve disjoint synthetic letter probes must all conform before training;
   record correctness separately. Run actual native file/tool-result smoke too.
   Copy scalar base-format proof to`/workspace/base-format-precheck.json`.
3. Stop vLLM to free VRAM. `RECIPE=run2 DRY_RUN=0 bash standalone/run.sh preprocess`
   creates the full training/validation cache without gradients. Inside the
   Axolotl image run`run2/audit_prepared.py --remote-gpu-host --workspace /data
   --split train --cache /data/prepared/... --out /data/prepared-check-train.json`
   and likewise`val`. Select actual dataset directories by their dataset_info.json,
   not guesses. Full input-token multisets must match the unmodified base HF
   template; native supervised marker coverage must match canonical targets.
4. `RECIPE=run2 DRY_RUN=0 bash standalone/run.sh smoke`, then`train`. Readiness
   checks registration/source/data/view/base-proof hashes; training additionally
   requires both real cache audits and a matching hardware/recipe/data smoke.
   Dry-run remains the default. Save scalar loss/memory/time and adapter SHA256;
   loss is NLL, not tool accuracy. Compare remote/local artifact hashes.

## Serving, quality and teardown

Serve this adapter via official vLLM over SSH tunnel and Fireworks dedicated
upload/live merge (rank8 supported); not independently managed Fireworks SFT.
Provider base-weight byte equivalence remains unverified. Use the same logical
model groups/explicit non-thinking sampling settings as run1. Repeat synthetic
format probes/native continuation on each FT arm; do not retune on these results.

Run fresh primary/regression/sampling inputs through the router using the existing
SDK runner, redaction/privacy safeguards, scorers and aggregate-only publication.
Do not regenerate JSON-text prompts differently or postprocess letter answers.
Use the run-2 scalar identity with the unchanged numerical rules:

```sh
python eval/outcome.py --quality /secure/quality-summary.json \
  --ifeval /secure/ifeval-summary.json --mmlu /secure/mmlu-summary.json \
  --base openai:chat:qwen3p8-27b-base --candidate openai:chat:qwen3p8-27b-ft \
  --preregistration run2/scalar-preregistration.json --out /secure/outcome.json
```

Report all three FT groups, paired CIs, all200 tool denominators, absent responses
as failures,≥10pp exact gain,≤2pp standard regressions, no lower overall pass rate,
no higher errors, p95 ratio≤1.25. Sampling repeats are the same100 new non-tool
cases at nominal seeds326/327/328, not300 independent cases. Historical public
judge agreement60%(CI46–74%) is weak, not domain validation. Public benchmarks
may already occur in base pretraining; fresh here means new to this experiment.

Run native continuation, loads/controls, then API-verify exact instance/key and
vendor deployment/model deletion. Stop watchdogs only after verification. Publish
all failures/limitations and lifecycle quote/usage estimates, not invoice totals,
production safety or quality-equivalent ROI. Run-2 PR: **Refs #326 / Fixes #355**.
