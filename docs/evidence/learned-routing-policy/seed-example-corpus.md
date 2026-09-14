<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Seed example corpus evidence (issue #157)

Status: OpenAI gpt-5.4-mini retake on Shadeform for full iteration-1 portfolio
(gpt-5.6-sol, gpt-5.4-mini, OpenRouter Qwen). Mini was enabled on the key and
used without gpt-5-mini fallback. OpenAI targets still completed with 0 ok rows
(see blockers).

This is an example budget (max 100 source traces, N=100 sampled turns), not
statistical sufficiency. No prompts, secrets, or response content are stored
here.

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- Instance id (left running): `9963d127-f312-4a37-b72d-91b42607cf8e`
- SKU memory: 256 GB advertised; host observed about 141 GiB
- GPUs: 2x NVIDIA L40S
- Work root on instance: `/var/tmp/lrp-retake` (mode 700)
- Router commit on host working tree: `0706c576e050bf04721deac033913750d560d2fa` (config/script retake edits; evidence PR from main)

## Source and sample

- Dataset: `nebius/SWE-rebench-openhands-trajectories`
- Revision: `35455389ab51bf5e2306bfd436ef72d0f98bf882`
- Streamed extract with `EXAMPLE_MAX_TRACES=100`
- Stratified sample: `SAMPLE_N=100`, seed `42`
- Extracted turns: 6605
- Sampled turns: 100
- Split counts: train 76, valid 19, test 5
- Prompt tokens (approx cl100k metadata): mean 21377.34, min 3954, max 83535

## Spend estimate (full iteration-1 portfolio)

- Assumed output tokens: 512
- Estimate USD: 12.218
- Abort threshold USD: 100
- Paid run authorized because estimate was at or under 100

Per target estimate USD:

- `qwen/qwen3.5-9b`: 0.221
- `qwen/qwen3.8-27b`: 0.588
- `gpt-5.6-sol`: 9.575
- `gpt-5.4-mini`: 1.834

## Paid replay executed

- Scope: full portfolio via router (OpenRouter Qwen + OpenAI gpt-5.6-sol + gpt-5.4-mini)
- `execute=True`, concurrency 1, `max_total_cost_usd=100`
- Response rows: 400
- Status counts: ok 90, upstream_error 95, ineligible 215
- Upstream/error classes: http_502 85, http_429 10, spend_evidence_unavailable 215
- Actual spend USD: incomplete (`spend_complete=false`); known computable spend about 0.329

Per target:

- `qwen/qwen3.5-9b`: ok 45 / 100, known spend USD 0.105214
- `qwen/qwen3.8-27b`: ok 45 / 100, known spend USD 0.224216
- `gpt-5.6-sol`: ok 0 / 100, spend USD 0.000000
- `gpt-5.4-mini`: ok 0 / 100, spend USD 0.000000

## OpenAI notes (this key / this pass)

- `gpt-5.6-sol` present on OpenAI `/v1/models`
- `gpt-5.4-mini` present; `gpt-5.4-mini-2026-03-17` absent; used `gpt-5.4-mini` (no fallback)
- Fanout honored `max_completion_tokens` via instance evidence patch
- OpenAI rows ended as upstream_error (http_502 / http_429) then ineligible (`spend_evidence_unavailable`); no durable OpenAI ok outcomes

## Operator notes

- Set OpenRouter and OpenAI dashboard spend limits yourself before further paid
  runs. Repo abort thresholds are not a billing hard stop.
- Teacher-forced continuation agreement is not end-to-end task success.
- Example corpus evidence does not transfer to unsampled workloads.
- Shadeform instance left running for resume.
