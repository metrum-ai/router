<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# AMD Instinct #359 — training/merged serving measured, qualification pending

Initial preparation was free. The orchestrator subsequently created an MI325X
for a bounded paid run. Actual training and merged serving measurements are now
recorded in `training-summary.json`, `peft-train-runtime.json`,
`load-amd-merged.json` and `bootstrap-amendment.md`. Qualification is running and
API-verified destruction is pending; live spend/status is in the track report.
The #358 seven-row mixed engineering export and trusted replay backend have
been delivered and the engineering export passes the unmodified FT consumer;
the useful-sized final cohort is now delivered and verified (1186/221/300).
The exact shared protocol/data/recipe freeze is `shared-freeze.json`; AMD has
ACKed the NVIDIA shared plan and is requesting orchestrator-owned hardware.
The fixed one-epoch training has completed; frozen qualification is now running.
No positive quality or promotion result is claimed. Direct-download training data is not an acceptable fallback.

## Teacher decision cleared; shared freeze coordination

The maintainer authorized Fireworks-hosted `accounts/fireworks/models/gpt-oss-120b`
for the synthetic Acme/mock-tool technical test at
https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842 . This is
not general customer-output permission. The actual engineering export manifest
records `teacher_terms_decision`, pinned public terms SHA256
`78c0dfb857eb860d15cfc2dc7296f3b19950d3145b0e5a40bc46e28cfbd19d30` and
model Apache-2.0 license SHA256
`58d1e17ffe5109a7ae296caafcadfdbe6a7d176f0bc4ab01e12a689b0499d8bd`.
Users must independently choose a teacher whose license and provider terms permit
training on outputs before enabling export for training.

Actual CPU validation on 2026-10-23: FT `run2/router_export.py` against
`/workspace/router-v430/traces-e2e-ft-handoff/dataset/` PASS:1train/3val/3test,
`training_authorized:false`, dataset hash
`e7ef76753b30f7030b4b4db6963cfacaf85c5ff5ee10ba1250d9d333fa62674d`.
No raw rows or secrets published; no new teacher/model calls made by AMD.
This small sample is never the training/quality cohort. The shared proposal is
`examples/fine-tune-qwen3p8-27b/amd/freeze-proposal.json`, explicitly not signed
preregistration/authorization. NVIDIA recipe including seed355 is accepted;
only TF32=false/runtime differs. Full counts/mix, source-group isolation,
native-vs-original gate mapping and exact artifact hashes await FT/traces
agreement. Coordination: https://github.com/metrum-ai/router/pull/360#issuecomment-6368393610 .
NVIDIA results from a different cohort cannot establish AMD parity.

Prepared tooling: `examples/fine-tune-qwen3p8-27b/amd/` contains an official-image
command planner, provisional BF16 Axolotl renderer, first-boot security script,
hardware/image evidence capture, tests and router/tunnel evaluation runbook.
Commands are planning aids, not automatic permission to train. Hardware/runtime inventory, documented Axolotl bootstrap failure, permitted PEFT
alternative and actual two-step/full training evidence are recorded below and
in the bootstrap amendment. Frozen held-out replay/gates remain pending.

## Research (2026-10-23, unauthenticated registry / GitHub API)

- Axolotl `docs/amd_hpc.qmd` at
  `777990d36ab92dcdc8b62d5ad07dc30b9618ff25` documents ROCm installation, but
  uses ROCm5.7/gfx90a and Llama. Its xformers workaround is not validation for
  Qwen3.8 on MI325X. Prefer SDPA and verify actual kernels/model on gfx942.
- ROCm/vllm `docker/Dockerfile.rocm` at
  `eb9d4de9eb7649bdf36b2d0e4832fcaab8465153` confirms the ROCm build path;
  it does not prove any released image supports this model/LoRA combination.
- Docker Hub `v2/repositories/rocm/{pytorch,vllm}/tags?page_size=8` returned
  official tags/digests. One CDNA candidate was
  `rocm/vllm:rocm7.14.1_cdna_ubuntu24.04_py3.14_pytorch_2.11_vllm_0.23.0`,
  digest `sha256:04b7c83e7fec4ea0e6d38fd77025aa39b5f7d0a61ecd49a977f1a520616fdbab`.
  PyTorch latest resolved to
  `sha256:099f59a3bee9c436f5cc57db6bc6076fdeda579c57e9e22ae40f856d5905b3e4`
  (tag `rocm10.1.0_ubuntu24.04_py3.12_pytorch_release_2.14.0`).
  **Neither is selected/approved**: versions returned by a registry must be
  matched to the actual host driver and model package support before use. Do
  not blindly combine these different ROCm generations or use mutable latest.
- Four attempted public rendered documentation URLs returned HTTP errors;
  compatibility was not inferred from inaccessible pages. Upstream source was
  retrieved via `gh api` instead. Source snapshots are private preparation files
  in `/workspace/router-v430/amd-research/`, not redistributed third-party code.

## Pending evidence

| Measurement | AMD | NVIDIA parity |
| --- | --- | --- |
| Driver/ROCm/gfx942/image/package inventory | Captured; official image BF16 PASS | Different CUDA/ROCm stacks |
| Axolotl two-step smoke / alternative reason | Dependency conflict; PEFT two-step PASS | Explicit toolkit/kernel confound |
| Export hash / native tool-supervision parity | Exact bytes and supervised counts match | Same frozen cohort |
| Train tok/s, wall time, peak memory, cost | 652.448inputtok/s;1214.947s;63.34GB allocated | NVIDIA892.894s; different timer/engine caveats |
| Serve concurrency 1/8/32 and cost/M output tokens | 0errors;70.533/395.608/1400.613tok/s | Matched NVIDIA merged arm pending |
| Held-out success / general regressions / cost per success | Not measured | Not comparable |
| Droplet teardown API receipt | Pending orchestrator destruction | No completion claim |

## Local validation actually run

- `python3 -m unittest discover -s examples/fine-tune-qwen3p8-27b/amd -v`:
  PASS, four tests; shell `bash -n` checks PASS for both AMD scripts.
- `make ft-example-test`: PASS, including AMD tests, existing example tests,
  telemetry privacy tests, promptfoo mocks and deterministic gate self-tests.
- `make test-fast`: initial 600-second command timed out; subsequent complete
  rerun with 1200-second bound exited 0.
- `make docs-qa`, `make secret-check`,
  `python3 scripts/check_source_headers.py`, `git diff --check`: PASS.
- `go build -o /workspace/router-v430/amd-local/router ./cmd/metrum-ai-router`
  then `python3 examples/fine-tune-qwen3p8-27b/eval/router_e2e.py --router-bin
  /workspace/router-v430/amd-local/router`: PASS; real temporary-file creation
  and tool-result continuation against synthetic upstreams, both weighted
  targets observed (24/21), quota/privacy/shadow checks PASS. Local processes
  torn down by the harness. **Synthetic only, not AMD model evidence.**
- `amd/plan.py serve` with the candidate official vLLM digest printed a command
  without execution; `amd/render.py --smoke` rendered a two-step YAML using an
  explicitly synthetic 64-character data identity. No dataset validation claim.

Full command outcomes/logs are recorded in the track report; no mock result will
be relabeled as a trained-model result. Current work: frozen qualification, routed controls, full cost reconciliation,
NVIDIA matched-merge comparison and orchestrator destruction/API verification.
Fresh per-droplet SSH key was generated only at the actual request gate.
