<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# AMD Instinct video facts — actual training, quality pending

- DigitalOcean MI325X VF/gfx942,261824MB HBM,697GB disk. Cloud firewall SSH-only;
  actual host listener check only22public. Serve loopback over SSH through a
  working-tree localhost Metrum AI Router. Droplet still active; API-verified
  destruction is PENDING, never claim cleanup from an SSH disconnect.
- Both accelerators use EXACT same governed #358 export:1186train/221val/300test,
 98historical teacher failures retained in test. Teacher synthetic Acme/mock-tool
  authorization is recorded as `teacher_terms_decision`, not general customer
  output-training permission. Users must verify model and provider rights.
- Official ROCm PyTorch tensor smoke passes. Axolotl pinned dependency resolution
  FAILED (Torch>=2.13 required vs tested official ROCm Torch2.10). The documented
  permitted Transformers/PEFT fallback passes: unchanged template5/5, strict
  letter FORMAT12/12 (arithmetic3/12, not knowledge quality), real file/new-tool
  result continuation, full-input audit with NVIDIA-matching supervision, and
  two actual optimizer steps. Do not narrate Axolotl ROCm success.
- Fixed full one-epoch AMD run PASS:75steps/1214.947s/652.448training-inputtok/s,
 63.34GB peak Torch allocated,13,238,272text-only LoRA parameters. Validation NLL
 .6176 is teacher-label likelihood, NOT held-out correctness/quality. No tuning
  or checkpoint selection on test outcomes. Reference ROCm conv/delta kernels and
  toolkit/version/timing-scope differences confound hardware-only speed claims.
- Official ROCm vLLM **unmerged LoRA failed to load** (NoneType.shape). Same final
  adapter was safe-merged CPU/BF16 with the shared merge.py/PEFT procedure;
 12shard portable weight-manifest SHAe38db84e1e2a1afa28236330e9f23fab467808d1d4ea7dcaa3cd3f4308daec6f.
  Merged actual chat/SSE/strictJSON/file+new-tool-result continuation PASS.
- Correctly provisioned localhost-router load:32requests/profile,0errors at1/8/32,
 70.533/395.608/1400.613 outputtok/s. Fully utilized quote estimate atc32
 $0.753638/M outputtokens, NOT invoice/productionSLO/quality-equivalent ROI.
 128token output cap, GPUmemoryfraction.42, one active merged engine. Initial
  invalid load hit router caller concurrency8; retained privately/costs retained,
  not relabeled as upstream throughput. Caller capacity corrected BEFORE any
  heldout inference; no model/scorer/gate change.
- Frozen candidate held-out native/primary/regression/sampling measurements are
  RUNNING; base/teacher/like-for-like NVIDIA merged comparison and full costs
  remain pending. Native completion is not semantic or JSON-text exact success.
- Do not narrate quality gain, successful promotion, production rollout, pure
  hardware causality, final all-in economics, or completed/verified teardown.
