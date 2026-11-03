<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# GB10 functional check environment

| Item | Value |
|---|---|
| Tier | nvidia-gb10-edge |
| Host | Vast GB10 container (machine 153485), 174.21.169.13:21178 |
| Instance | 58899537 created 2026-11-03T01:11:08Z, destroyed after the check |
| Image | vllm/vllm-openai:v0.33.0 (arm64 member of the pinned manifest list) |
| Model | Qwen/Qwen3-1.7B @ 70d244cc86ccca08cf5af4e1e306ecf908b1ad5e, served as local-tiny |
| Router | built from commit 24a6bbfe, run on the operator host (not on the GB10 box) |
| GPU | GB10, unified memory; vLLM loaded with FlashAttention 2 and captured CUDA graphs |

## What this proves

The pinned arm64 image runs vLLM 0.33.0 on GB10, resolves the model revision,
serves `/health`, `/v1/models` and completions with exact token accounting, and
the tier's `local-tiny` group answers through the router with caller auth
enforced.

## What it does not prove

- **No co-located deployment.** Vast GPU instances are unprivileged containers, so
  the tier's compose file cannot run there: the router was on a different host and
  the model-server base URL is the only difference from the committed config.
- **No concurrency or latency claim.** The load driver requires the direct lane on
  loopback, and there is no loopback path across the internet; the sweep was not run
  and any latency here includes the WAN hop.
- **One slot only.** The tier's `local-chat` slot (Qwen3-30B-A3B) was not served
  in this run; a 30B attempt on another GB10 host downloaded the checkpoint but
  never finished starting within 15 minutes, so nothing is claimed about it.

## Hosts tried

| Instance | Host | Result |
|---|---|---|
| 58896532 | Poland, PL | vLLM loaded the image and Qwen3-30B-A3B engine; API server never came up in 15 min |
| 58897127 | Zhejiang, CN | no outbound egress: Hugging Face unreachable |
| 58897188 | Sweden, SE | launch did not produce a running server |
| 58899308 | Washington, US | **served local-tiny; functional suite 6 passed, 0 failed, 3 skipped** |

Cost is recorded in the issue close-out from the provider's billing API.
