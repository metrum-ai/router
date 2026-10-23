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
It is a **work-in-progress operator recipe**, not a published promotion result.
Consult its evidence before quoting quality, latency or economics.

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
captured complete/unexpired text/chat-tool traces with harness labels are
eligible. Export does not restore PII; copies need their own access/retention
policy. See [Usage reporting](../operations/usage-reporting.md) and
[Privacy](../privacy.md). Replay new model calls from initial fixtures, never
recorded teacher answers. Freeze backend/checker and gate before held-out calls.

Measured integration is a tiny teacher sample and synthetic candidate stand-in,
not a trained-model canary or promotion. Run 1's narrow JSON-text improvement
failed general regressions; base remains default. A useful governed training
cohort, actual fine-tuned-target replay and cost-per-success comparison remain
pending. The steps below describe the historical external training machinery,
not permission to bypass router capture for the router-centric run.

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

## Router-captured traces: run 2 (in progress, #358)

Run2 (#355) is re-scoped to a governed router export, not direct downloaded
training rows. **No GPU rental until the #358 export exists and a new protocol
is frozen.** The previous direct-download preparation is superseded.

The intended loop is licensed task seeds → teacher agent → deterministic tool
execution/result continuation through the router → opted-in project/key capture
→ audited admin export with filters, redaction/PII policy, provenance hashes and
time-based heldout split → fine-tune → caller-scoped canary in the same
`support-agent` group. Other callers remain on teacher and must not be exported.
General/answer-format anchors also come through the governed export.

Replay heldout captured tasks through the router for candidate, teacher and base,
executing each arm's NEW calls against the same reset backend, never providing
recorded teacher answers as candidate continuations. Report deterministic task
success, unchanged quality/general-regression gates, latency and total attempted
routed model cost per successful task (failures included; unknown cost/zero
successes explicit). Native success is not JSON-text exact accuracy; endpoints
must be preregistered, not silently interchanged. Teacher output rights must
permit training and must be checked before enabling training export. The maintainer
cleared Fireworks-hosted gpt-oss-120b for this synthetic Acme technical test only
(issue358 comment6366186842); the scoped `teacher_terms_decision` and pinned terms
hash are recorded in manifest/evidence. This is not customer-data permission.
PR361 has a consumer-compatible real-teacher engineering sample, not a useful
training cohort. Full export/shared NVIDIA-AMD freeze/student evidence remain
pending; this is not a claim the full loop has shipped.

See the source example's `run2/README.md` and dated `VIDEO-FACTS.md` for current
blocking dependencies and validated versus still-unmeasured facts.

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
- Keep content capture off by default. For intentional training collection,
  explicitly opt in only the authorized key/project with redaction/PII policy
  and retention; prove other callers remain uncaptured/unexported. For ordinary
  evaluation, keep capture off and prove logs/usage contain no prompt content.
  Secure and expire exported copies separately. Synthetic mock tests prove
  integration, not trained-model quality.
- Public human-judgment calibration measures judge agreement on that corpus,
  not correctness on the customer's task. Report confidence intervals and weak
  agreement honestly.

See [Self-hosted upstreams](self-hosted-upstreams.md) and
[Provider catalog](provider-catalog.md) for the underlying endpoint contract.
