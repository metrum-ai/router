<!--
Copyright 2026 Metrum AI
SPDX-License-Identifier: Apache-2.0
-->

# Seed example corpus evidence (issue #157)

Status: example N=100 Shadeform retake with full portfolio attempt
(`gpt-5.6-sol`, `gpt-5-mini` fallback, Qwen OpenRouter). Estimate under $100.

This is an example budget (max 100 source traces, N=100 sampled turns), not
statistical sufficiency. No prompts, secrets, or response content are stored
here.

## Host

- Shadeform SKU: `L40Sx2` (massedcompute, kansascity-usa-1)
- Router commit: `0706c576e050bf04721deac033913750d560d2fa`
- Instance id: `9963d127-f312-4a37-b72d-91b42607cf8e` (kept running)

## Source and sample

- Dataset: `nebius/SWE-rebench-openhands-trajectories`
- Revision: `35455389ab51bf5e2306bfd436ef72d0f98bf882`
- Streamed extract with `EXAMPLE_MAX_TRACES=100`
- Stratified sample: `SAMPLE_N=100`, seed `42`
- Extracted turns: 6605
- Sampled turns: 100
- Split counts: {'train': 76, 'valid': 19, 'test': 5}
- Turn kinds: {'tool-call': 100}

## Spend estimate (full iteration-1 portfolio)

- Estimate USD: 12.218124976000002
- Abort threshold USD: 100.0
- Paid run authorized because estimate was at or under 100

## Paid replay executed

- Models: ['qwen/qwen3.5-9b', 'qwen/qwen3.8-27b', 'gpt-5.6-sol', 'gpt-5-mini']
- OpenAI mini: requested `gpt-5.4-mini`, used `gpt-5-mini`
- Response rows: 400
- Status counts: {'ok': 108, 'upstream_error': 292}
- Error counts: {'http_502': 97, 'http_429': 195}
- Actual spend USD: 0.2686863619999999
- Per target ok/spend: {'qwen/qwen3.5-9b': {'ok': 56, 'spend_usd': 0.09794824999999999, 'errors': {'http_429': 44}}, 'qwen/qwen3.8-27b': {'ok': 52, 'spend_usd': 0.17073811199999997, 'errors': {'http_429': 48}}, 'gpt-5.6-sol': {'ok': 0, 'spend_usd': 0.0, 'errors': {'http_502': 48, 'http_429': 52}}, 'gpt-5-mini': {'ok': 0, 'spend_usd': 0.0, 'errors': {'http_502': 49, 'http_429': 51}}}

OpenAI targets returned only http_429/http_502 this pass. Qwen produced ok rows.
No savings percentages.
