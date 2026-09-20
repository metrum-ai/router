<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Seed example corpus evidence (issue #157)

Status: N=100 Shadeform paid portfolio after pivot off OpenAI.
Targets: OpenRouter Qwen 3.5-9b and 3.8-27b, OpenRouter MiniMax-M3,
Fireworks kimi-k2p7-code, Baseten GLM-5.2. No OpenAI endpoints.

This is an example budget (max 100 source traces, N=100 sampled turns), not
statistical sufficiency. No prompts, secrets, or response content are stored
here.

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- SKU memory: 256 GB advertised; host observed about 141 GiB
- GPUs: 2x NVIDIA L40S

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
- Estimate USD: 7.105
- Abort threshold USD: 100
- Paid run authorized because estimate was at or under 100

Per target estimate USD:

- `qwen/qwen3.5-9b`: 0.219
- `qwen/qwen3.8-27b`: 0.582
- `minimax/minimax-m3`: 0.695
- `accounts/fireworks/models/kimi-k2p7-code`: 2.211
- `zai-org/GLM-5.2`: 3.398

## Paid replay executed

- Scope: OpenRouter Qwen + OpenRouter MiniMax-M3 + Fireworks kimi + Baseten GLM
- `execute=True`, concurrency 1, `max_total_cost_usd=100`
- Response rows: 500
- Status counts: ok 349, upstream_error 151
- Upstream errors: http_429 151
- Actual spend USD (complete): 3.261576
- Primary absolute USD baseline: `baseten-glm-5-2`
- OpenAI used: false

Per target:

- `qwen/qwen3.5-9b`: ok 75 / 100, spend USD 0.149358
- `qwen/qwen3.8-27b`: ok 73 / 100, spend USD 0.282625
- `minimax/minimax-m3`: ok 67 / 100, spend USD 0.241138
- `accounts/fireworks/models/kimi-k2p7-code`: ok 67 / 100, spend USD 1.009130
- `zai-org/GLM-5.2`: ok 67 / 100, spend USD 1.579324

## Operator notes

- Provider keys loaded from `env.json` (`OPENROUTER_API_KEY`, `FIREWORKS_API_KEY`,
  `BASETEN_API_KEY`). Set dashboard spend limits yourself before further paid
  runs. Repo abort thresholds are not a billing hard stop.
- Teacher-forced continuation agreement is not end-to-end task success.
- Example corpus evidence does not transfer to unsampled workloads.
- Host lifecycle status for the evidence GPU SKU is `operator_private`.
