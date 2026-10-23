<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# AMD Instinct video facts — measured quality FAIL, teardown API-verified

- DigitalOcean MI325X VF/gfx942,261824MB HBM,697GB disk. Cloud firewall SSH-only;
  actual host listener check only22public. Serve loopback over SSH through a
  working-tree localhost Metrum AI Router. GPU work ended19:21Z; orchestrator
  confirmed19:23Z API404, tagempty, cloudfirewall/SSHkeyabsent. This is operator
  API-verification attestation, not an inference from SSH disconnect.
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
  load's26local concurrency429s were self-inflicted infrastructure throttling,
  not model failures. The ENTIRE initial attempt is excluded from ALL reported
  benchmark denominators; all3profiles reran whole. Historical failures/costs
  remain only in the lifecycle ledger. Caller capacity corrected BEFORE any
  heldout inference; no model/scorer/gate change. Per #370, local/test limits
  are high; only explicitly labelled intentional limit tests may throttle.
- All AMD+same-stack base frozen cohorts complete: original JSON-text exact
  AMD7/200vsbase9/200 (−1pp; needed+10pp), overall80/300vs86/300: **FAIL,
  DO NOT PROMOTE**. Native tool180/200both cannot rescue it. IFEval164vs161/200,
  MMLU124/200both, primaryp95 34.487svs34.302s. Non-tool100 repeated across3seeds
  is NOT300independent cases. All denominators/failures retained.
- Current e411 qualification has no pre-route429s. Known duplicate-declaration
  no-dispatch case remains in full300/200denominators; prior96partial294 is
  excluded/non-comparable with costs retained. Finalseed328 initialSDKlaunch
  made0requests; signed availability receipt precedes full unstarted100cohort.
- Sharedteacher e411 run degraded by227router RPM429episodes; teacher quality
  comparison and cost/success are unusable. FT-owned authorized whole300rerun
  pending; do not imply AMD beats a healthy teacher. Matched NVIDIA merged
  results separately owned, no hardware-only causal headline.
- Lifecycle GPU quote~6.08USD (conservative6.2067), judge800calls~2.8738975USD,
  engineeringteacher~0.0004917USD; failed-upstream charges unknown, no invoice.
  All aborted/idle/invalid-load time retained. No final all-in/ROI assertion.
- Real-file/newreceipt same-group base/candidate/teacher/otherproject PASS;
  provider-agnostic teacher rollback PASS. InitialGPU-specific rollback payload
  upstream400 retained. External strategy spend_ceiling NOT enforced; do not
  claim it caps rental spend. Physical deadline/API cleanup is separate.
- Do not narrate quality gain, successful promotion, production rollout, pure
  hardware causality or final quality-equivalent economics.
