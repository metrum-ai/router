---
title: Fine-tune and route your own model
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Fine-tune and route your own model

Metrum AI Router can route an operator's fine-tuned OpenAI-compatible endpoint
using the same model-group contract as other upstreams. Training and deployment
remain operator responsibilities; the router does not run a GPU trainer.

The source example is
[`examples/fine-tune-qwen3p8-27b/`](https://github.com/metrum-ai/router/tree/main/examples/fine-tune-qwen3p8-27b).
Run 1 is a **rejected candidate**, not a quality promotion: training and serving
passed, but the frozen promotion gate failed, so the base remains default.
The measured host was one RTX PRO 6000 Blackwell, not a B200; provider keys
are routing identifiers, not evidence of the hardware used. Consult the
[dated evidence](https://github.com/metrum-ai/router/blob/main/docs/evidence/finetune/2026-10-23/README.md)
for measured results, costs, exclusions and verified teardown before quoting
quality, latency or economics. A passing nested scalar check alone is not
promotion approval.

## Router-centric loop

A public corpus is not the customer's work, successful training is not safe
production, and cost per token is not cost per successful task. The router loop
is: teacher serve → governed opt-in capture for one caller key/project → harness
outcome labels → audited canonical JSONL and manifest export with frozen time
splits → external fine-tune → route back inside the same `support-agent` group
with a key-scoped canary → replay captured initial fixtures through the router
→ preregistered quality/regression gate decides. Preserve failed-task denominators.

Choose a teacher whose **model license and provider terms permit training on
outputs**; verify rights before enabling training export. The example's scoped
maintainer authorization for synthetic Acme technical-test traffic is not a
permission for customer-data training.

The source example's `router-loop/` provides two-key collection, deterministic
mock tools, `content:training` admin authorization, durable export audit, privacy
filters, hashed manifests and same-group caller-bound routing. Only explicitly
captured encrypted, complete/unexpired text/chat-tool requests with harness
labels are eligible. Train/val additionally require a complete final response;
frozen test retains eligible labelled teacher failures and missing/incomplete
responses as provenance, not successful training answers. Export does not restore PII; copies need their own access/retention
policy. See [Usage reporting](../operations/usage-reporting.md) and
[Privacy](../privacy.md). Replay new model calls from initial fixtures, never
recorded teacher answers. Freeze backend/checker and gate before held-out calls.

The initial tiny teacher sample and synthetic candidate stand-in are historical
integration evidence, superseded for training by the governed run-2 export
(1,186 train / 221 validation / 300 held-out tasks). Run 1, NVIDIA run 2 and AMD
all **FAIL their original promotion gates**. Do not promote. Run 1 retained its
base-default rollback; run-2 non-canary `support-agent` callers remain on the
teacher. Neither is permission to alter unrelated routing groups.

## Implemented Export Contract

`POST /admin/training/outcomes/{request_id}` accepts harness attestations using
`router-outcome-v1`; it does not infer task success from HTTP 200. The initial
allowlisted backend is `acme-support-v1`, not arbitrary uploaded checker code.
`POST /admin/training/export` returns a ZIP with `manifest.json`, `train.jsonl`,
`val.jsonl`, and `test.jsonl` in `metrum-router-ft-export-v1` format. Filters
include public token ID, project, client, group and a half-open UTC time range;
validation/test boundaries are explicit and ordered before training. Success and
quality filters apply to train/val, never to the held-out population.

These are separately authorized deployment-wide `content:training` actions
`label` and `export`, not ordinary report-export permissions. Both require a
durable security audit before privileged access. Missing capture consent cannot
be replaced by uploading a transcript or outcome label. Cross-split source-task
conflicts and ambiguous capture/labels fail closed. The exporter scans at most
10,000 matching usage records and limits JSONL payload to 64 MiB; narrow filters
rather than bypassing limits.

The [router-loop recipe](https://github.com/metrum-ai/router/tree/main/examples/fine-tune-qwen3p8-27b/router-loop)
provides small-sample and explicit full-cohort modes, a versioned mock backend,
privacy/split/hash evidence and same-group caller/project canary routing.
The [export reference](https://github.com/metrum-ai/router/blob/main/docs/training-trace-export-design.md)
documents API errors and authorization. Existing captures without canonical
chat/tool history are ineligible; this is not an automatic historical backfill.

An earlier small-sample canary hit a router-side budget and is infrastructure
integration evidence only, not valid model-quality or latency evidence. Rerun
entire evaluations with reviewed high local limits; do not selectively refill
failures or relax the outcome gate. The measured NVIDIA and AMD results below
must be assessed from their own evidence, not inferred from this API.

## Workflow

1. Verify model and dataset licenses, pin artifacts, deduplicate canonical chat
   JSONL, and keep real data outside Git. Freeze a held-out test split and hash it.
2. Check managed-provider tunability. If only LoRA upload is supported, train
   externally and upload that adapter; do not claim independent managed training.
3. On an ephemeral approved GPU host, run the Axolotl two-step smoke before the
   full text-only LoRA job. The example includes YAML and an FSDP2 overlay.
4. Serve using the official vLLM image with the base and named LoRA adapter.
   Publish only a host-loopback port, then use an SSH tunnel to the router.
5. Configure the FT group with weighted dedicated-provider and private-vLLM
   targets, and keep a separate base control group. Roll back by removing the
   FT target or setting its route weight to zero where supported.
6. Evaluate through a localhost router, not directly against providers. Use
   pinned promptfoo, type-strict tool scorers, a pinned calibrated rubric judge,
   standard regressions and the preregistered outcome gate.
7. Delete every rented host and dedicated deployment at step end and verify via
   its API. Hourly deployments incur costs even with no requests.

## Measured Outcomes: No Promotion

- **Run 1 — FAIL:** strict JSON-text exact tools improved 4.5% → 32%, but
  IFEval fell 81.5% → 63.5% and MMLU-Pro 60.5% → 13%. Those regression gates
  reject the candidate; a nested gain check is not promotion approval.
- **NVIDIA run 2 — FAIL:** on one RTX PRO 6000 Blackwell Server Edition 96 GB,
  base → LoRA JSON-text exact tools were 4 → 5 of 200 (+0.5 percentage points,
  below the required +10). IFEval was 164 → 161 of 200; MMLU-Pro 123 → 123.
  Native replay 258 → 265 of 300 is a different endpoint and cannot rescue it.
- **AMD — FAIL:** MI325X VF 256 GB, approved Transformers/PEFT ROCm fallback;
  JSON-text exact tools 7/200 versus base 9/200, below the required gain.
  Its merged serving path is measured; unmerged LoRA serving did not pass.

Both accelerator arms used the hash-bound router export. The supplemental
BF16 merged comparison reports NVIDIA/AMD native mock-tool success 180/200
for each, but this is **not semantic parity**, hardware causality, or a
replacement for either failed original gate. Original grader infrastructure
failures and authorised whole-step repair remain separately archived.

NVIDIA training wrapper time was 892.894 seconds versus AMD 1,214.947 seconds,
with different kernels/toolkits/timing scope. All final c1/8/32 serving profiles
had zero errors, but these small synthetic profiles are not sustained capacity.
Quoted whole-300 native-window cost per harness success was about $0.001421
and $0.001297 respectively; this excludes lifecycle costs and does not establish
semantic resolution, an invoice or quality-equivalent ROI. Missing self-hosted
price completeness remains unknown, not free. NVIDIA GPU/cloud key and dedicated
vendor resources were API-deleted; AMD droplet/firewall/cloud key deletion was
also verified. See the dated receipts before quoting spend or teardown.

## AMD Instinct And Comparison Limits

The additional [NVIDIA/AMD merged comparison](https://github.com/metrum-ai/router/blob/main/docs/evidence/finetune/2026-10-23-run2/NVIDIA-AMD-COMPARISON.md)
now includes quality, training wall/throughput, serving c1/8/32 and scoped cost
per output token/native-harness success. Authorized CPU-only infrastructure repair
completed all800semantic assertions on exact recorded outputs (400per merged arm),
unchanged grader/rubric/prompts and no model regeneration. Original failures stay
archived; original NVIDIA LoRA and AMD FAIL verdicts remain unchanged. This aligns
BF16 merged representation, not pure hardware causality, production approval or
quality-equivalent ROI. Refs #359.

The example's [AMD runbook](https://github.com/metrum-ai/router/tree/main/examples/fine-tune-qwen3p8-27b/amd)
prepares MI325X training with official `rocm/pytorch` and serving with official
`rocm/vllm`. MI325X training and merged-serving smoke/load checks have run;
frozen AMD/base qualification completed with **failed original quality gates**:
JSON-text exact3.5% (7/200) vs base4.5% (9/200): −1.0pp,
paired95%CI[−2.5,0]pp (required+10pp); overall80/300vs86/300.
Native tool180/200both cannot replace these gates. **Do not promote.**
Pinned Axolotl bootstrap failed on the tested ROCm torch version; the documented
Transformers/PEFT fallback passed its two-step smoke and fixed one-epoch training
on the router-exported split. Require a two-step model/toolkit smoke on the actual GPU, a pinned
host-compatible stack and the governed router-exported dataset from #358.
Serve on host loopback over SSH through a localhost router; replay newly executed
tool continuations and keep failures in evaluation denominators. The AMD evidence
and VIDEO-FACTS under `docs/evidence/finetune-amd/2026-10-23/` explicitly leave
measured training/load evidence separate from quality verdicts and economics.
Orchestrator verified droplet API404 and firewall/cloud-key deletion19:23Z.
GPU quote~6.08USD plus judge~2.874USD is not a settled invoice; failed charges
remain unknown. The RPM-throttled teacher run remains invalid infrastructure evidence; a
whole high-limit teacher replacement completed 300 tasks with zero transport
errors. It does not change either original gate. The merged NVIDIA comparison
is measured, but not a hardware-only causal claim. Unmerged LoRA serving failed;
the measured AMD serving path is BF16 merged, not unmerged.

The initial26local concurrency429s were self-inflicted infrastructure throttling,
not model failures. The entire initial load attempt is excluded from all reported
benchmark denominators; all three profiles reran whole before qualification.
Historical failures/costs remain in the lifecycle ledger only. Per #370, local/test
caller keys and effective project/group profiles must have high limits; only
explicitly labelled intentional limit tests may throttle. Final outcomes:
[AMD evidence](https://github.com/metrum-ai/router/tree/main/docs/evidence/finetune-amd/2026-10-23).

The maintainer authorized a Fireworks-hosted open-weight teacher specifically for
this synthetic Acme/mock-tool technical test, not general customer outputs. The
manifest and evidence retain `teacher_terms_decision` and the pinned terms hash.
Users must choose a teacher whose model license and provider terms allow training
on outputs, and verify that permission before enabling export for training.

## Governance caveats

- Generate a caller with a narrow model allow-list, rate/concurrency quotas and
  request/token budgets. Do not commit caller keys.
- For hourly GPUs/deployments, zero token-price metadata does **not** mean free
  inference. Dollar-budget enforcement needs realistic amortized prices; track
  rental charges independently and maintain a hard stop/watchdog.
- Router external-policy `shadow` records routing recommendations; it is not
  automatic duplicate inference against both models. Use paired offline replay
  through the router for quality comparisons, and opt into request access only
  inside the trust boundary.
- Keep content capture disabled except for explicit operator opt-in on the
  selected key/project. Test that uncaptured callers produce no content export;
  operational usage reports/logs remain metadata-only. Secure and expire exported
  copies separately. Synthetic mock tests prove integration, not model quality.
- Public human-judgment calibration measures judge agreement on that corpus,
  not correctness on the customer's task. Report confidence intervals and weak
  agreement honestly.

See [Self-hosted upstreams](self-hosted-upstreams.md) and
[Provider catalog](provider-catalog.md) for the underlying endpoint contract.
