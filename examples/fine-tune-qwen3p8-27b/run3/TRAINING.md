<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Run 3 recipe and evaluation freeze (#425)

This registration is **after teacher collection but before any run-3 training,
base/candidate held-out inference or outcome results**. The source-pool
registration said that a collection/runtime and recipe registration must be
committed before collection/training. The source split and collector were
committed before collection, but this separate recipe was not. Treat that as a
timing ambiguity/deviation, not as a fully pre-collection recipe freeze. Do not
change this recipe or choose another adapter based on held-out outcomes.
Training is not authorized by this document alone: prove the reviewed
capture/export, recipe, data, gate, infrastructure smoke, scoped credentials,
independent pre-armed teardown and budget first. No weights have been trained.

The 4,200-task public synthetic pool, its splits and gold were frozen before
collection in [PREREGISTRATION.md](PREREGISTRATION.md). These examples are
repetitive, artificial, and not representative of customer traffic. Five
separate caller keys used the same scripted agent loop, not five agent
frameworks. The old private task indexes remain unavailable, so historical
prompt overlap is unverified. The collector completed 3,800 attempted groups:
2,987 train and 474 validation captures survived the predeclared success filter;
all 300 held-out tasks remain, including 84 teacher failures. Provider 503s
and unknown charges are documented in artifact snapshot `d1c806eb`. There are
no score-based refills. Training labels come only from the approved, audited
router export, not generator reference answers. Export and preparation checks
validate structural privacy attestations, not independent proof of legal rights
or absence of sensitive content.

## Frozen inputs

| Input | SHA-256 |
|---|---|
| Router dataset manifest | `d1d70782b7bed89c4d9c1fa76eb42ac4a10a27fa7b350785487c2b0b07dab849` |
| Export train / validation | `c0e3e5fb76e5d7eb3a95a4ece7490ac09b6b8e14af30e3ce4eaee9add980087c` / `23bc6d65bd4c0357cd6677b4d070795e9dd3882c729e747dbba4ae529a681b6e` |
| Axolotl train / validation views | `546f1fe7443a3bbb901d7f03a1e0db1b4693e2fb7451820ff466290c561a4904` / `b334cb07d41f2bc08ed36a23966b774772038b05ef77eacd343bcb09eb073572` |
| Held-out source gold | `00e09198e5572487764a768619d48c6b7083e48a69e98d992501d861b4362483` |
| First-turn gate YAML | `4e0451e539de91ac6411c67ad8947f26e52ec95ef9dcac6616fed50d0d55e9b6` |
| Original fixed regression YAML | `446eb6e57278a38fb8b8baa7312cc9bdbf81378176a483915d923e73cf9d515c` |
| Recipe `axolotl.yaml` | `c546981d61015d3ff06f7d47455ae9c335997d60cf3c4ad90e38cf92d68c679d` |
| `build_gate.py` / type-strict scorer | `898071ecc124b2fa3a0bf20ce08d1e23a782412c1387c846f5be5e30e37a77f3` / `d70d15cd14ffeb8a923473ee9392783fd326155a03d3612c60dbd2debd1c35c6` |

The gate generator validates both pre-collection selected-plan and gold hashes,
then builds the 200/75/25 held-out mix using gold only. It never consumes
teacher response text, including failed traces. It retains exactly 200
first-turn JSON tool-extraction tasks and 100 non-tool rubric tasks. Native
API multi-turn replay is supplementary and cannot replace the original gate.
The public fixed seed-326 200 IFEval and 200 MMLU-Pro regression manifest and
YAML are byte-identical to the original 2026-10-23 published cohort; they are
not newly unseen benchmarks. Licensed public inputs and verifiers are snapshot
`0ba00305` (`router/v45-proof/425`). Benchmark contamination remains possible.

## Training recipe

- Base `Qwen/Qwen3.8-27B`, revision `1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`; use the base tokenizer's unmodified template, thinking disabled.
- Official Axolotl `axolotlai/axolotl@sha256:26a660139e62125256473dce660087786d68e23411c7013339deffa4141a8631`; serving uses official `vllm/vllm-openai@sha256:befccf0e7e4089aa23926bb6f887a85c0d46c2a99e6de5c5ffdf5cb7957c5b34`.
- Same run-2 recipe parameters: BF16, no quantization or packing; max sequence 8,192; LoRA rank 8, alpha 16, dropout 0; `q_proj`, `v_proj`, `linear_attn.in_proj_qkv`, `linear_attn.out_proj` only. Verify actual module names and trainable parameter inventory before training.
- One epoch over the 2,987 captured training views; microbatch 1, gradient accumulation 16, LR 1e-5, AdamW, cosine schedule, warmup ratio 0.03, seed 355, SDPA, gradient checkpointing and TF32 disabled. Record actual steps rather than assuming the earlier run's fixed 75 steps. Do not add direct-download rehearsal labels or tune on test data.
- Run a tiny non-held-out smoke and verify exact data, image, model, GPU, LoRA inventory and matching rendered recipe before full training. Stop on smoke failure; a smoke is not a quality result. `smoke_config.py` renders the one-step configuration and retains the real nonempty validation split. Offline CPU schema checks use Axolotl source revision `777990d36ab92dcdc8b62d5ad07dc30b9618ff25` from the pinned image; they do not replace the GPU smoke.

## Unchanged outcome gate

The [original preregistration](../eval/PREREGISTRATION.md) numerical gate and
scorer remain unchanged: strict tool exact-match improvement at least 10
percentage points; at most 2-point regression independently on IFEval and
MMLU-Pro; no increase in error rate; p95 end-to-end latency ratio at most 1.25;
and the existing scalar no-regression gate. Require complete paired coverage,
not just the scalar-gate pass. Retain all failures and report denominators.
Both arms use the same interfaces and identical cases, objective temperature 0,
max 2,048 output tokens, top_p 0.95, top_k 20, concurrency 16, thinking disabled.
The 100 non-tool cases also use the three preregistered temperature-0.7 seeds
326/327/328. Report paired bootstrap 95% CIs, 10,000 resamples, seed 326.

Judge is `gpt-4o-2024-11-20`, temperature 0, max 256 output tokens. The
existing 50-question public MT-Bench calibration (`judge-calibration.json` SHA
`11d9a4cb9fcc8b0a0d13aa96a2d9c960314d321f1dd4c43be1977cd90f6774cd`)
reported 60% agreement, 95% CI 46–74%; it is old, weak, and not in-domain
validation. Any judge/provider failure remains visible and its charges unknown.

The combined run-3 GPU authorization is $150. Set a conservative stop/reserve
below it, pre-arm an independent Herdr teardown watcher before any new cloud
resource, and verify instance and key deletion. Quotes, reservations and
usage-derived list prices are not actual bills. Reconcile and report actual GPU
charges separately; incomplete/missing evaluations or billing are not a pass.
