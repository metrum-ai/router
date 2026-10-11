# d3 with a real price tier: gpt-6.1-sol as the strong target (2026-10-11)

Live evidence for issue #287 ("real price tiers before quoting savings" and "co-located end-to-end policy latency") from the Metrum AI Router team at Metrum AI. It follows the d3 GPU evaluation in [`../2026-10-11/`](../2026-10-11/README.md) (PR #288). That run paired a "strong" target that was cheaper per token than the "cheap" one. This run uses a strong target that really costs more.

Copyright 2026 Metrum AI.

## Summary

- **Tiers.** Cheap: Fireworks `deepseek-v4p1-flash`, $0.22 in / $0.66 out per 1M tokens. Strong: OpenAI `gpt-6.1-sol`, $2.00 in / $0.10 cached in / $10.00 out per 1M tokens. That is 9.1× the input price and 15.2× the output price.
- **d3.** `vllm-sr/d3` (26B) at revision `cbff3a3cb0b074b6585fe9f747b9f9f5557a0e75` on one RTX PRO 6000 Blackwell SE, reached over an SSH tunnel. Policy mode `enforce`, `on_error: fail_closed`, default adapter rules.
- **Routing on the 204-prompt seed set.** All 204 requests returned HTTP 200 and every policy call succeeded. Hard → strong 102/102, easy → cheap 102/102, so the strong share was 0.500.
- **Cost on the seed set:**
  - enforce $1.856, always-strong $1.882, always-cheap $0.257;
  - the saving against always-strong is $0.026 (**1.4%**);
  - the extra cost against always-cheap is $1.599 (**7.2×**).
- **Why the saving is small here.** 97% of always-strong's spend is on the hard half, which enforce also sends to the strong tier.
  - The seed prompts are short one-liners: 27–51 input tokens per prompt on average, depending on the tokenizer.
  - gpt-6.1-sol answered the easy half with 4,571 output tokens in total. deepseek-v4p1-flash used 27,529, most of them reasoning tokens.
  - So routing easy prompts to cheap saved only $0.031 in this run.
- **Quality signal (cheap to measure, not a benchmark).** Rate of hard prompts that hit the 4,096-token output cap:
  - always-cheap 73/102;
  - enforce 9/102;
  - always-strong 7/102.

  Easy prompts never hit the cap.
- **Coding-agent take (the demo video, `sol-real-2`).** pi ran 8 turns through `d3-auto`: 3 cheap, then 5 strong.
  - The take cost $0.0228. The same turns at gpt-6.1-sol prices are estimated at $0.0304, so the take cost **25% less**.
  - Agent turns carry 1,867–3,335 input tokens of context each, mostly cached after the first turn of each step, so the turns kept on the cheap tier save more than the short seed prompts do.
- **Policy latency:**
  - over the SSH tunnel, the router's `duration_ms` was p50 245 / p95 249 ms;
  - co-located (router, adapter and d3 on the GPU host, mock upstream), it was **p50 104 / p95 107 ms**, of which d3 itself was p50 103 ms.
- **pi continuation through `d3-auto`** with real d3 26B and gpt-6.1-sol as the strong tier: pass. Also through `d3-auto-strong` (pure gpt-6.1-sol): pass.

## Pricing sources

| Model | Input | Cached input | Output (per 1M tokens) | Source (read 2026-10-11) |
|---|---|---|---|---|
| `gpt-6.1-sol` (OpenAI) | $2.00 | $0.10 | $10.00 | https://developers.openai.com/api/docs/models/gpt-6.1-sol |
| `gpt-5.6-sol` (OpenAI, first fallback, not used) | $4.00 | $0.40 | $20.00 | https://developers.openai.com/api/docs/models/gpt-5.6-sol |
| `glm-5p3` (Fireworks, second fallback, not used) | $1.40 | $0.26 | $4.40 | https://fireworks.ai/models/fireworks/glm-5p3 |
| `deepseek-v4p1-flash` (Fireworks, cheap) | $0.22 | n/a | $0.66 | catalog entry already on main (`pricing_updated_at` 2026-10-01) |

More detail on gpt-6.1-sol:
- Prompts over 272K input tokens are charged 2× for input and 1.5× for output.
- Cache writes are charged at 1.25× the input rate.
- Tool calling needs the Responses API. The router reaches it through the `chat_to_responses` bridge.
- The default reasoning effort is `medium`.

gpt-6.1-sol passed a single tool-call smoke and both pi continuations, so neither fallback was needed.

## Setup

- **GPU:** Shadeform `RTXPro6000` (1× NVIDIA RTX PRO 6000 Blackwell Server Edition, 97,887 MiB), massedcompute kansascity-usa-6, $2.19/h. Driver 580.126.09, CUDA 13.0.
- **d3 server:** d3 26B served by the package's `d3_server.py --device cuda:0` on the host's 127.0.0.1:8000 with bearer auth.
  - Software: torch 2.14.1+cu130, transformers 5.17.0, flash-linear-attention 0.5.2.
  - Download 77 s (52.2 GB). Cold load 92.5 s. GPU memory after load 55,499 MiB.
- **Network:** reached only through `ssh -N -L 127.0.0.1:18350:127.0.0.1:8000`. The host's public listeners were only sshd and rpcbind.
- **Helper scripts:** host preparation and serving used `../2026-10-11/scripts/remote_setup.sh` and `remote_serve.sh`; the Shadeform API used `shadeform_d3.py` (all from PR #288).
- **Router:** built from this tree on 127.0.0.1:18351. Config from `scripts/local_pi_instance.py --d3-policy-url http://127.0.0.1:18352/route --d3-policy-mode enforce --d3-policy-timeout-ms 1500 --d3-strong-model gpt-6.1-sol --d3-tier-groups`.
- **Adapter:** `d3_policy.py --port 18352 --url http://127.0.0.1:18350 --timeout-ms 1000 --model d3`, with default rules.
- **Requests:** `max_tokens` 4,096, sequential, one pass per mode. The pilot showed that a 1,024-token cap truncated most hard answers.

## Cost on the 204-prompt seed set

Every run is a separate live pass over the same prompts, and all costs are `request_usage.total_cost_usd`. Output lengths differ between passes, so this is measured spend, not the same tokens repriced.

| Run (group) | Served | Cost | Per request | Output tokens (reasoning) | Hard prompts at the 4,096 cap |
|---|---|---|---|---|---|
| enforce (`d3-auto`) | 102 deepseek / 102 sol | **$1.8558** | $0.00910 | 210,568 (62,050) | 9/102 |
| always-strong (`d3-auto-strong`) | 204 sol | **$1.8815** | $0.00922 | 187,034 (39,290) | 7/102 |
| always-cheap (`d3-auto-cheap`) | 204 deepseek | **$0.2570** | $0.00126 | 385,831 (354,284) | 73/102 |

| Comparison | Value |
|---|---|
| Saving of enforce against always-strong | $0.0257 (1.4%) |
| Extra cost of enforce against always-cheap | $1.5988 (7.22×) |
| Strong share (enforce) | 0.500 (hard → strong 102/102, easy → cheap 102/102) |
| Same-token estimate (the enforce run's tokens repriced) | all-strong $2.1209, all-cheap $0.1407. This is biased because deepseek's outputs are mostly reasoning tokens; it is shown only for completeness |

Cost split by half:

| Run | Easy half | Hard half |
|---|---|---|
| enforce | $0.0192 (deepseek) | $1.8366 (sol) |
| always-strong | $0.0503 | $1.8313 |
| always-cheap | $0.0160 | $0.2409 |

## Latency

| Run | Policy `duration_ms` p50 / p95 / p99 | End to end p50 / p95 | Easy p50 | Hard p50 |
|---|---|---|---|---|
| enforce (tunnel) | 245 / 249 / 252 ms | 8.4 / 67.8 s | 2.7 s | 37.1 s |
| always-strong | (no policy) | 4.3 / 67.0 s | 2.0 s | 36.6 s |
| always-cheap | (no policy) | 7.6 / 49.5 s | 2.7 s | 39.3 s |
| co-located, mock upstream (`colocated_policy_latency_d3_26b_204.json`) | **104 / 107 / 108 ms** (adapter → d3: 103 / 106 / 108 ms) | 147 / 157 ms | | |

End-to-end latency is dominated by upstream generation. The co-located run put the router (static linux/amd64 build of this tree), the adapter, d3 and a loopback mock upstream (from `scripts/run_d3_policy_e2e.py`) on the GPU host. No provider keys were used. It also reproduced hard → strong 102/102 and easy → cheap 102/102.

In the agent take, the adapter → d3 time over the tunnel rose with conversation length, from 261 ms to 758 ms (`demo_take_sol-real-2_rows.json`; adapter log). d3 26B's latency grows with the length of the state it reads. The seed-set numbers are for short prompts.

## pi tool continuation (AGENTS.md definition of done)

| Check | Result | File |
|---|---|---|
| `make local-router-pi-smoke LOCAL_ROUTER_OUT=tmp/test-d3-sol LOCAL_ROUTER_LISTEN=127.0.0.1:18351 LOCAL_ROUTER_GROUP=d3-auto < /dev/null`, real d3 26B | exit 0, `ok: true`, tools write/write/read, no tool errors; unary and stream 200 | `pi_continuation_d3_auto_d3_26b_gpt61sol.json` |
| same with `LOCAL_ROUTER_GROUP=d3-auto-strong` (every turn on gpt-6.1-sol) | exit 0, `ok: true`, write/read, no tool errors | `pi_continuation_d3_auto_strong_gpt61sol.json` |
| one `add(a, b)` tool call to gpt-6.1-sol, then the tool result | 200 `tool_calls` `{"a":2,"b":3}`, then 200 `stop`; usage cost $0.00032 + $0.000294 | `tool_call_smoke_gpt61sol.json` |

## Demo take

The demo video takes are in `demo_take_sol-real-2_*` and `demo_prompt_probe_d3_26b.json`.

- **Hard step.** We reused v1's hard prompt unchanged. d3 26B rated it `hard·agentic·r0.99`.
- **Easy step.** d3 26B rated v1's easy prompt `trivial·agentic·r0.80`. That is P(reasoning) of 0.6 or more, so it routes to the strong tier.
  - Seven easy wordings were probed directly against d3. The demo uses one that d3 rated `r0.37` (cheap): v1's wording without "Run the tests."
  - The routing rules and thresholds were not changed.
- **Routing in the take.** It is per turn. In this take, d3 26B kept the hard task's follow-up turns on the strong tier (`hard·agentic·r0.94–0.98`), because the hard request stays in the conversation.
- **Takes recorded:**
  - one rehearsal with fake d3;
  - two real takes. `sol-real-1` was rejected for an on-screen account name and a footer quoting bug; it had the same routing pattern and the tests passed.

## Files

| File | What it holds |
|---|---|
| `enforce_d3_26b_gpt61sol_204.json` | enforce run through `d3-auto` |
| `always_strong_gpt61sol_204.json` | always-strong run through `d3-auto-strong` |
| `always_cheap_deepseek_204.json` | always-cheap run through `d3-auto-cheap` |
| `compare_enforce_vs_baselines.json` | `d3_live_eval.py compare` output |
| `colocated_policy_latency_d3_26b_204.json` | co-located policy latency run |
| `pi_continuation_*.json`, `tool_call_smoke_gpt61sol.json` | pi continuations and the single tool-call smoke |
| `demo_prompt_probe_d3_26b.json`, `demo_take_sol-real-2_rows.json`, `demo_take_sol-real-2_panel.txt` | demo prompt probes and the final take |
| `scripts/colocated_policy_latency.py` | harness for the co-located run (Metrum AI) |

The run outputs hold only scalar data: seed IDs, labels, tiers, timings, token counts, costs and finish reasons. No prompt text appears except the public demo prompts, and no keys or host addresses appear.

## Commands

```bash
# config (local), router, adapter
uv run --with pyyaml python scripts/local_pi_instance.py --env-json <main>/env.json --out-dir tmp/test-d3-sol \
  --listen 127.0.0.1:18351 --d3-policy-url http://127.0.0.1:18352/route --d3-policy-mode enforce \
  --d3-policy-timeout-ms 1500 --d3-strong-model gpt-6.1-sol --d3-tier-groups
# 204-prompt runs (enforce needs d3; the two baselines do not call the policy)
python3 scripts/d3_live_eval.py router --base-url http://127.0.0.1:18351/v1 --token-file tmp/test-d3-sol/METRUM_API_KEY \
  --usage-db tmp/test-d3-sol/usage.sqlite --group d3-auto --mode-label enforce --strong-model gpt-6.1-sol \
  --strong-input-price 2 --strong-output-price 10 --max-tokens 4096 --timeout 300 --out enforce.json
#   ... --group d3-auto-strong --mode-label always-strong / --group d3-auto-cheap --mode-label always-cheap
python3 scripts/d3_live_eval.py compare --enforce enforce.json --always-strong always-strong.json \
  --always-cheap always-cheap.json --out compare.json
# co-located (on the GPU host)
python colocated_policy_latency.py --repo ~/colo --router-binary ~/metrum-ai-router-linux \
  --d3-url http://127.0.0.1:8000 --d3-key-file ~/d3.key --d3-model d3 --out colo.json
```

## Spend and teardown

| Item | Amount |
|---|---|
| GPU: instance `metrum-d3-20261011-sol-1`, created 17:09:54Z, delete requested 18:29:56Z, 1.334 h × $2.19 | $2.92 |
| Upstream (all `request_usage` rows of this stream: pilot, smokes, the three 204-prompt runs, rehearsal and real takes; 673 requests) | $4.12 (gpt-6.1-sol $3.83, deepseek $0.29) |
| **Total** | **$7.04** |

- The instance list was empty at 18:30:07Z.
- The stream's Shadeform SSH key was deleted at 18:29:56Z and no longer appears in the key list.
- The tunnel is closed and local ports 18350–18352 are free.

## Limits

- **Synthetic seed set.** The 204 seed prompts are synthetic and deliberately separable, and they are short. Agreement with their labels is not a quality benchmark.
- **Cost depends on the workload.** It depends on input size and answer length: the seed set shows a 1.4% saving, while the coding-agent take shows 25%. Neither is a forecast for real traffic.
- **Output-cap hits are a coarse signal.** They are not a measure of answer correctness.
- **One GPU, one pass per mode.** We used one GPU type and `d3_server.py`, which serves one request at a time, and ran one pass per mode.
- **Vendor figures.** The d3 model-card benchmarks are vendor-reported.
