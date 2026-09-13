<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Seed example corpus evidence (issue #157)

Status: example N=100 Shadeform evidence pass completed for OpenRouter Qwen
targets. OpenAI portfolio targets were not completed in this pass (see blockers).

This is an example budget (max 100 source traces, N=100 sampled turns), not
statistical sufficiency. No prompts, secrets, or response content are stored
here.

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- SKU memory: 256 GB advertised; host observed about 141 GiB available
- GPUs: 2x NVIDIA L40S
- Work root on instance: `/var/tmp/lrp-seed-157` (mode 700)
- Instance torn down after the run

## Source and sample

- Dataset: `nebius/SWE-rebench-openhands-trajectories`
- Revision: `35455389ab51bf5e2306bfd436ef72d0f98bf882`
- Parquet sha256: `14048dd1fcd22ce094b6e85f8a38f223a9ef1327031aaaad052804870212efa1`
- Streamed extract with `EXAMPLE_MAX_TRACES=100` (no full parquet `to_pandas()`)
- Stratified sample: `SAMPLE_N=100`, seed `42`
- Extracted turns from 100 traces: 6605
- Sampled turns: 100 (all `tool-call`)
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
- `gpt-5.6`: 9.575
- `gpt-5.4-mini`: 1.834

## Paid replay executed

- Scope: OpenRouter Qwen targets only (`qwen/qwen3.5-9b`, `qwen/qwen3.8-27b`)
- OpenRouter-only estimate USD: 0.809
- `execute=True`, concurrency 1, `max_total_cost_usd=100`
- Response rows: 200
- Status counts: ok 95, upstream_error 105
- Upstream errors: all `http_429`
- Actual spend USD (complete): 0.204

Per target:

- `qwen/qwen3.5-9b`: ok 52 / 100, spend USD 0.075
- `qwen/qwen3.8-27b`: ok 43 / 100, spend USD 0.129

## OpenAI blockers (this key / this pass)

- `gpt-5.6` absent from OpenAI `/v1/models`; fallback candidate `gpt-5.5`
- `gpt-5.4-mini` / `gpt-5.4-mini-2026-03-17` absent; fallback candidate `gpt-5-mini`
- Fanout via router previously forced `max_tokens`; GPT-5 chat needs
  `max_completion_tokens` (instance patch used for smoke)
- Large-prompt OpenAI fanout hit sustained `http_429`; excluded from the paid
  evidence pass above

## Operator notes

- Set OpenRouter and OpenAI dashboard spend limits yourself before further paid
  runs. Repo abort thresholds are not a billing hard stop.
- Teacher-forced continuation agreement is not end-to-end task success.
- Example corpus evidence does not transfer to unsampled workloads.
