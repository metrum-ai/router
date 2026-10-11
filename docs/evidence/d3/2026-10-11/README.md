# d3 external policy evidence, 2026-10-11 (issue #278)

Copyright 2026 Metrum AI. Collected by the Metrum AI Router team for issue #278.

**Status: partial.** The Shadeform GPU plan (steps f.1–f.5 on L40S, RTX PRO 6000
or H100) has not run yet. The Shadeform API key returned HTTP 401 on
`GET /v1/instances` and `GET /v1/sshkeys` (checked 2026-10-11 14:16Z), so no
instance was created and no GPU spend was incurred. This directory holds the
evidence that could be collected without a GPU:

1. Real `vllm-sr/d3-lite` on **CPU**, which checks the wire format and gives a
   first quality signal. Latency here is not representative of GPU serving.
2. The adapter against the real d3-lite server, including a router
   fail-closed drill.
3. A pi tool-continuation run through a `d3-auto` group with real upstreams and
   the scripted fake d3. It meets the AGENTS.md request-path definition of done
   for the adapter, but it is not d3 model evidence.

The fixed seed set is `../seed_prompts.json`: 204 synthetic prompts,
34 each for easy/hard × code/math/chat. Evidence files contain only prompt IDs,
labels, answers, timings and model names. They contain no keys or tokens.

## Setup

| Item | Value |
|---|---|
| Model | `vllm-sr/d3-lite`, revision `a8d25a91ef75552803f7009d25b7bd435991abd9` (matches issue pin) |
| Server | package `d3_server.py`, `--device cpu --no-warmup`, bearer auth on, 127.0.0.1 |
| Runtime | Python 3.12 venv, torch 2.14.1, transformers 5.17.0; no flash-linear-attention / causal-conv1d (PyTorch reference kernels) |
| Host | 16 vCPU, 30 GB RAM, `OMP_NUM_THREADS=8`, no GPU |
| Cold load | 20.4 s to `/health` ready with `--no-warmup`. The default warmup, which runs every batch size, had not finished after more than 15 minutes on CPU and was stopped |
| Questions | adapter defaults: `difficulty` (score), `reasoning` (noul), `kind` (choice) |

## f.1 (CPU, stratified subset): d3-lite answers vs labels

`f1_d3_lite_cpu_direct.json`: the first 4 prompts of each of the 6 cells
(24 prompts). Command:
`python3 scripts/d3_live_eval.py direct --url http://127.0.0.1:18300 --per-cell 4 --warmup 0 ...`

| Cell | d3 difficulty | d3 kind | Tier from default rules |
|---|---|---|---|
| easy-code | trivial ×4 | code ×4 | cheap ×4 |
| easy-math | trivial ×4 | math ×4 | cheap ×4 |
| easy-chat | trivial ×4 | chat ×4 | cheap ×4 |
| hard-code | hard ×2, expert ×1, moderate ×1 | code ×4 | strong ×3, cheap ×1 |
| hard-math | moderate ×4 (reasoning 0.62–0.76) | math ×4 | strong ×4 (via reasoning ≥ 0.6) |
| hard-chat | hard ×2, expert ×1, moderate ×1 | chat ×4 | strong ×3, cheap ×1 |

- Tier vs label agreement: **22/24 (0.917)**. Easy → cheap 12/12; hard → strong 10/12.
- `kind` matched the labelled domain on **24/24**.
- 0 errors and 0 malformed answers. The adapter's parser accepted every real
  d3_server response (score answers keyed `"0".."3"`, with `score` as the
  expected index).

CPU latency, per call with 3 questions (client and `server-timing` agree within
about 6 ms):

| | p50 | p95 | p99 | max |
|---|---|---|---|---|
| client ms | 26,439 | 56,269 | 58,951 | 58,951 |
| server-timing ms | 26,432 | 56,262 | 58,946 | 58,946 |

The p95/p99 tail came from the first prompts, which overlapped other CPU load on
the host. CPU d3 is far too slow for the request path. GPU numbers are still
needed.

## Adapter against real d3-lite

`adapter_vs_real_d3_lite_cpu.json`: `d3_policy.py --timeout-ms 60000` against
the CPU server, with two seed prompts outside the measured subset:

| Seed | classLabel | targetIndex | rule |
|---|---|---|---|
| easy-chat-04 | `d3:trivial:chat:r0.02` | 0 (cheap) | cheap-default |
| hard-code-04 | `d3:hard:code:r0.75` | 1 (strong) | strong-hard-or-reasoning |

## Failure drill with the real model (router, fail_closed)

The router was built from this tree on 127.0.0.1:18301, with group `d3-auto`
(`mode: enforce`, `on_error: fail_closed`, router `timeout_ms: 300`). The
adapter ran with `--timeout-ms 250` against the slow CPU d3:

- HTTP **502** `routing-policy-error` in 0.44 s.
- Adapter log: `{"errorClass": "d3_timeout", "outcome": "error"}`.
- `request_policy_executions`: outcome `error`, `external-policy-http-error`,
  254 ms.
- `request_usage`: status 502, empty target, **0 attempts**, so no upstream
  call was made.

The `fallback` and d3-down variants are covered by `make d3-policy-e2e`
(D04–D06). The live tunnel-kill drill (f.5) is still to run.

## Agent continuation (offline, fake d3, real upstreams)

`pi_continuation_offline_fake_d3.json`:
`make local-router-pi-smoke LOCAL_ROUTER_OUT=tmp/test-d3 LOCAL_ROUTER_LISTEN=127.0.0.1:18301 LOCAL_ROUTER_GROUP=d3-auto < /dev/null`
ran with `d3-auto` in `enforce`. pi wrote and then read `proof.txt` through
the write/read tools and replied `DONE <token>`. Result `ok: true`, no failures.
Every turn was routed by the adapter:

| Request | classLabel | Policy ms | Served |
|---|---|---|---|
| pi turn 1–3 (streaming) | `d3:hard:agentic:r0.82` | 2 / 9 / 16 | openai `gpt-6-luna` (strong) |
| unary chat | `d3:trivial:chat:r0.18` | 8 | fireworks `deepseek-v4p1-flash` (cheap) |
| streaming chat | `d3:trivial:chat:r0.18` | 2 | fireworks `deepseek-v4p1-flash` (cheap) |

The fake d3 is a keyword heuristic, so these labels show wiring only.

## Still to do (needs a working Shadeform key)

- f.1 on GPU for d3-lite and d3-nano (full 204 prompts): p50/p95/p99,
  `server-timing`, GPU memory, cold load, flash-linear-attention on CUDA.
- f.2 shadow agreement and policy `duration_ms` through `d3-auto` over the SSH tunnel.
- f.3 enforce tier split and cost vs always-strong.
- f.4 pi continuation with the real d3 over the tunnel.
- f.5 tunnel-kill drill (`fail_closed` 502, `fallback` target 0).
