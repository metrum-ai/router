<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# MI325X bootstrap and toolkit amendment (before gradients/outcomes)

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
`37f324714098e5322e22fa0450b218f7fb61387d38a35a85e6b29ebeab1d2711`.
Any implementation corrections must be recorded before measurement. Actual
training/serving throughput, quality and cost parity are not yet measured.
