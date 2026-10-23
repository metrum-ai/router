<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# MI325X bootstrap and toolkit amendment (before gradients/outcomes)

## Outcome (final) — 2026-10-23, after19:23Z teardown

The dated bootstrap record below is historical, not the current run status.
One-epoch Transformers/PEFT training and BF16-merged serving completed;
Axolotl resolution and unmerged ROCm LoRA loading failed as documented.
Original quality gates **FAIL / not promoted**: exact3.5%vsbase4.5%, −1.0pp,
paired95%CI[−2.5,0]pp. See [gate-verdict.json](gate-verdict.json) and
[economics-and-teardown.json](economics-and-teardown.json): GPU quote≈6.08USD,
failed upstream charges unknown, droplet deletion API-verified19:23Z.
No healthy-teacher savings or hardware-only parity claim follows.

Initial26local concurrency429s were self-inflicted infrastructure throttling;
the entire initial load attempt is excluded from all reported benchmark
denominators. All three load profiles reran whole before qualification.
Historical costs remain in the lifecycle ledger. Local/test limits are now high
per #370; intentional quota tests are separately labelled.

## Historical bootstrap record — 2026-10-23, before full training

DigitalOcean droplet611994722, source-restricted SSH firewall, $3.80/h; remote
UTC17:48Z on2026-10-23. Image advertises ROCm6.4.1 but actual host reports
6.4.0-47, driver6.12.12, kernel6.8.0-59-generic. MI325X VF gfx942,261824MB,
304CUs,697GB filesystem. Raw unique hardware IDs remain private.

`cloud-init status --wait`, `ss -tulpn` and `amd/first-boot.sh` PASS: only TCP22
public, DNS53 loopback. Marketplace001_onboot absent; disabled-copy path supplied
by orchestrator also absent, so no claim the copy exists. Caddy inactive; Docker
initially not installed, no public rocm container. Installed Docker29.1.3.

Official `rocm/pytorch` immutable digest
`sha256:4449f856653602317e4101a76fce599c7fcd58ccec2e539951fce5f73083179e`
(ROCm7.2.4/PyTorch2.10) ACTUAL BF16 tensor/matmul/synchronize smoke PASS on gfx942.
PyTorch reports generic AMD Radeon Graphics but properties match gfx942/261824MB/
304CUs; `rocminfo`/amd-smi identify MI325X VF. This is not a training smoke.

## Axolotl tried first: dependency bootstrap fails

Pinned source `777990d36ab92dcdc8b62d5ad07dc30b9618ff25` was cloned inside the
official ROCm image. `pip install --dry-run -c /data/torch-constraint.txt
/opt/axolotl` exited1, ResolutionImpossible: source requires torch>=2.13,<2.15
but this image supplies torch2.10.0+rocm7.2.4. The constraint prevents silently
installing CUDA torch/replacing the tested ROCm build. No Axolotl optimizer steps
were run or claimed. Switching to a newer ROCm10 image without a compatible
host-driver proof is not assumed safe.

## Documented ROCm PyTorch / Transformers / PEFT alternative

Keep the official AMD PyTorch container, Transformers5.20.1, PEFT0.21.2,
Accelerate1.15.0, Datasets5.0.0, jsonschema4.26.0. These packages install/import
successfully without replacing ROCm torch. Exact resolved inventory retained
privately in `/workspace/amd359/env/peft-package-inventory.txt` on the GPU host.
This follows the official ROCm PyTorch container + Hugging Face PEFT low-rank
adapter API path; it is not a claimed AMD-specific Axolotl success. Documentation:
https://huggingface.co/docs/peft/main/en/developer_guides/lora and
https://github.com/ROCm/pytorch . No Unsloth/llama.cpp/ollama/community image.

`amd/train_peft.py` is the explicit alternative trainer. It preserves unchanged
HF full-input template token sequences, parses wire JSON function arguments only
for HF rendering, masks input and all assistant header prefixes, supervises ALL
assistant turns through im_end but not trailing newline (Axolotl policy). It
checks every prefix exactly before training. Canonical data/views and shared
BF16/r8/alpha16/four-target/LR1e-5/one-epoch/batch16/seed355/SDPA settings stay
unchanged; no heldout inputs/gold are used for training. Template5/5 checks pass.
A full token/supervision audit, base-generation/native proofs and **two completed
optimizer steps** are still mandatory before full training.

Frozen alternative trainer SHA256 before gradients:
Initial source `37f324714098e5322e22fa0450b218f7fb61387d38a35a85e6b29ebeab1d2711`.
First smoke launch stopped before gradients because Transformers5.20 removed the
`warmup_ratio` TrainingArguments keyword. Corrected API transport to explicit
ceil(0.03*total_optimizer_steps), preserving the same recipe:1warmup smoke step,
3warmup full-run steps. Corrected source SHA256 before full training:
`7aa76c011654313a7c9d0e20d45cec503709d2a951785de90437ee4ae12dbc4f`.

## Actual pre-training checks and two-step result

- Template5/5 PASS, official ROCm vLLM base strict letter FORMAT12/12 PASS
  (arithmetic correctness3/12 is separate, not a knowledge claim).
- Real base native tool call through a working-tree localhost router wrote a
  temporary file, received NEW tool result, and completed. This used development
  fixtures only, no qualification tasks. vLLM stopped before training smoke.
- Full consumed train/val views:1186/221rows, max2180/2544tokens,
  input792690/154901tokens, supervised581388/114102tokens,
  native405/74rows. Full input template prefixes unchanged, supervised totals
  match the NVIDIA actual audit. No hidden empty labels or dropped earlier calls.
- **ROCm PEFT smoke PASS**: two completed optimizer steps,11.435s wrapper,
 11.209s trainer, peak allocated55,673,092,608bytes/reserved55,840,866,304bytes,
 13,238,272trainable parameters, BF16 rank8/alpha16. Adapter separate from the
 candidate, losses0.1874/0.3845 (not heldout quality). Scalar runtime/audit attached.
- ROCm uses correct but slower PyTorch reference causal-conv1d and gated-delta
  kernels because CUDA-specific optimized extras are not installed. Record this
  major throughput caveat versus NVIDIA; do not silently alter the model/recipe.

Full training, serving throughput, quality and cost parity remain unmeasured.
