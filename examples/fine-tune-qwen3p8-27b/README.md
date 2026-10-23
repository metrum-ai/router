<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Fine-tune Qwen3.8-27B and govern it with Metrum AI Router

Work in progress for #326. Do not infer training completion or promotion from
this runbook. Actual results live in `docs/evidence/finetune/2026-10-23/`.

## Pins and rights

- Base: `Qwen/Qwen3.8-27B@1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0`,
  Apache-2.0 (verified Hub LICENSE), public and ungated; see MODEL_LICENSES.md.
- Training: Glaive AI `glaiveai/glaive-function-calling-v2` revision
  `e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac`, Apache-2.0, ungated.
  Retain upstream attribution/NOTICE and record modifications. No real rows in Git.
- Judge calibration: LMSYS `lmsys/mt_bench_human_judgments` revision
  `f7d2896d2cc5d80f8b55c2bbc722613555233c25`, CC-BY-4.0 per pinned dataset card.
  Attribute Zheng et al., *Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena*
  (2023), arXiv:2306.05685. Use the **human** split, not `gpt4_pair`.
  Public human labels satisfy the maintainer's calibration decision; do not
  relabel AI-generated judgments as human. Pairwise agreement on this public
  corpus is not a domain-specific correctness guarantee.

## Prepare and test without spending

Install `jsonschema` in a local venv. Download the pinned Glaive JSON outside Git
using `huggingface_hub` (read `HF_TOKEN` from the environment, never argv).

```sh
python3 prepare_data.py --input /secure/glaive.json --out /secure/canonical --train-size 2000
python3 -m unittest -v
python3 standalone/cloud.py vast offers
python3 standalone/cloud.py shadeform offers
```

The first economical run uses 2,000 training rows, 500 validation rows and 300
held-out test rows. Canonical chat JSONL hashes are recorded in `manifest.json`.
Normalization is NFKC + casefold + whitespace folding; reject test leakage.
Only first turns with valid declared tools are retained. This conservative
conversion is a dataset modification, not the upstream's full benchmark.

## Train and serve

Fireworks reports `tunable=false`, `supportsLora=true`: use the issue's fallback
**standalone train → upload LoRA → dedicated Fireworks deployment**. Do not claim
independent managed training. Both serving stacks use the same adapter.

Provision only once the dataset, pinned environment and commands are ready.
Use Shadeform first and also check Vast for B200s. Approved GPUs are RTX PRO
6000 Blackwell, H100, H200 and B200, up to eight GPUs. Pick the cheapest
configuration that actually fits; verify with the tiny hardware smoke before
training. Require CUDA ≥12.8, ≥500 GB disk and ≥1 Gbps network. Report advertised
network separately from measurements.
`standalone/cloud.py` supports both marketplaces; mutations default to dry-run.
Use a fresh per-instance SSH key, attach only its public key, keep runtime state
outside Git, and verify deletion through the API even after failures.

Primary toolkit: **Axolotl**, whose upstream includes an explicit Qwen3.8-27B
recipe (inspected at commit `777990d36ab92dcdc8b62d5ad07dc30b9618ff25`).
`standalone/axolotl.yaml` is BF16 LoRA, not QLoRA. `standalone/fsdp2.yaml` is
merged by `standalone/render.py` for 2/4/8 GPUs, keeping effective batch 16.
The launcher pins the official Axolotl image by digest and saves its actual
package inventory in `requirements.lock`. This inventory includes the image's
Axolotl 0.21.1.dev0 and is not a pip-installable PyPI lock. Use the pinned image
to reproduce it. `requirements.in` names the released 0.21.0 alternative for
operators, but that alternative is not claimed to be hardware-tested.
If the real hardware smoke shows unsupported architecture, use Unsloth only
after verifying its exact Qwen3.8 recipe/version; record why, never silently
change quantization or training parameters.

Download the exact base revision on the remote host. Pass `HF_TOKEN` over
encrypted stdin into the single `standalone/download_model.py` process; do not
persist tokens or copy credentials. Mount host `/workspace` at container `/data`:
mounting over the image's `/workspace` would hide Axolotl's bundled venv.

```sh
# On the GPU host after copying this example to /workspace/standalone:
DRY_RUN=1 bash /workspace/standalone/run.sh smoke
DRY_RUN=0 GPUS=1 bash /workspace/standalone/run.sh smoke
# Only after the 2-step, 512-token smoke succeeds:
DRY_RUN=0 GPUS=1 bash /workspace/standalone/run.sh train
MODEL_DIR=/workspace/base ADAPTER_DIR=/workspace/adapter DRY_RUN=1 bash serve/run.sh
# After reviewing, DRY_RUN=0 launches the pinned official vLLM image.
```

Training uses text-only BF16 LoRA, rank 16, alpha 32, two epochs, learning rate
1e-4, cosine decay, effective batch 16, max sequence 8192, gradient checkpointing.
The vision tower and LM head remain frozen and are checked before training.
The byte-identical canonical training file is the upload input if managed SFT
becomes available later; token rendering is a trainer-only transformation.

Reach vLLM from gyrados using `ssh -N -L 127.0.0.1:18526:127.0.0.1:8000 ...`.
Never publish an unauthenticated model endpoint. The Docker recipe publishes
only loopback on the host. Native serving inside an existing GPU container must
also use `--host 127.0.0.1`.

## Router groups, caller budgets and rollback

Copy `config.qwen3p8-ft.example.yaml` outside Git and replace the Fireworks account
and deployment endpoint as appropriate. `vllm_b200_ft` is a logical provider name
retained from the issue, not a claim that the current host is a B200: this run
uses RTX PRO 6000 Blackwell. The default FT group splits traffic 50/50. The static
`-onprem` and `-fireworks` FT groups isolate serving stacks for paired evaluation.
The optional `-shadow` group uses `python3 eval/shadow_policy.py`: it records the
alternate recommendation with decision telemetry enabled while serving configured
baseline order. It does not run a second model inference.

Generate a fresh narrow caller (never pass a raw key on argv):

```sh
go run ./cmd/metrum-ai-routerctl callers generate \
  --owner-user local-dev --project example-project --env dev \
  --allow qwen3p8-27b-ft,qwen3p8-27b-base,qwen3p8-27b-ft-onprem,qwen3p8-27b-ft-fireworks,frontier-ref \
  --token-out /secure/router.token --config /secure/ft.yaml --write
```

On that generated caller, set `rate.concurrent`, `rate.rpm`, `rate.tpm` and
`quota.day`/`quota.month` request/token budgets. For example, 5,000 requests and
10,000,000 tokens/day are bounded dev-evaluation budgets, not production defaults.
The mock e2e proves an intentionally tiny request budget is enforced.
`spend_ceiling.max_estimated_cost_usd` is a **per-request** estimated-cost ceiling,
not an hourly GPU spending cap. Zero list-token prices in this template cannot
protect a rental budget; use the separate hard GPU lease watchdog and track real
hourly charges. Replace placeholder amortized prices before cost-based routing.

Rollback: remove the candidate target (or set its route weight to zero where
supported) and route callers to the base group. Disable content capture; do not
publish promptfoo raw outputs. `eval/run_eval.py` keeps raw configs/results in a
private output location and reads the caller key only from its token file into
the child environment. `eval/summarize.py` publishes scalars and paired CIs.

## Budget and release safety

Latest maintainer decision: **$1,000 on-prem GPU allowance, stop at $800 GPU**,
separate from Fireworks. Keep Fireworks modest (original allowance $300; stop
and report before $240). Track OpenAI judge costs separately too.
Record every paid step and wall-clock rental cost in the track report. Dedicated
Fireworks deployments bill while idle. Delete deployments and instances at step
end and verify via APIs; remove per-instance SSH keys. Full SFT on 8× B200 needs
explicit maintainer approval and is not authorized by this example.

Quality and tool-continuation evidence must use a localhost router built from
the worktree. Promotion requires preregistration before the first eval, +10 point
tool exact match improvement, ≤2 point regression, no higher error rate and p95
latency ratio ≤1.25. Report means, paired comparisons and 95% bootstrap CIs.
No results or gate pass are implied until those checks actually run.
