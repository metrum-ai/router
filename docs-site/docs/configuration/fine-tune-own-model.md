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
- Disable content capture and test that usage storage/logs contain no prompt
  content. Synthetic mock tests prove integration, not trained-model quality.
- Public human-judgment calibration measures judge agreement on that corpus,
  not correctness on the customer's task. Report confidence intervals and weak
  agreement honestly.

See [Self-hosted upstreams](self-hosted-upstreams.md) and
[Provider catalog](provider-catalog.md) for the underlying endpoint contract.
