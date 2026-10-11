# d3 external policy evidence, 2026-10-11 (issue #278)

Copyright 2026 Metrum AI. Collected by the Metrum AI Router team for issue #278.

**Status: live GPU plan complete.** Steps f.1–f.5 ran for **d3-lite** and
**d3-nano**, and f.6 (f.1–f.3) ran for **d3 26B**. Everything ran on one
Shadeform RTX PRO 6000 Blackwell Server Edition. d3 was reached only through an
SSH tunnel to `127.0.0.1`. The earlier CPU and offline evidence is kept at the
end of this page.

The fixed seed set is `../seed_prompts.json`: 204 synthetic prompts, 34 for
each easy/hard × code/math/chat cell. Evidence files contain only seed IDs,
labels, answers, timings, model names and costs. They contain no keys, no
tokens, and no prompt text outside the seed set.

## Recommendation

**Use `vllm-sr/d3` (26B), co-located with the router on a 96 GB (RTX PRO 6000)
or 80 GB card, as the routing model. Use d3-nano when co-location or that card
is not available, or when the policy budget is under about 50 ms. Do not use
d3-lite.**

- **Quality.** On the seed set, d3 26B matched the labels on **204/204** in
  every mode (direct, shadow, enforce). d3-nano scored 0.90–0.91: it never sent
  an easy prompt to the strong tier, but it sent 19–21 of 102 hard prompts to
  the cheap tier. d3-lite scored 0.80–0.84, with only 56–70 of 102 hard prompts
  sent to strong.
- **Latency.** The issue asked whether the quality gain is worth about 4–5×
  the latency. On CUDA it costs **5.4×**: 102 ms vs 19 ms p50 co-located. The
  extra ~85 ms is about 4% of the median end-to-end request here (1.4–2.7 s).
  For routing, the 26B model is worth it. On the request path, the full policy
  hop with a co-located router should stay well under the 300 ms target.
- **d3-lite has no advantage.** On this GPU, d3-lite and d3-nano have the same
  latency (18.9 vs 19.1 ms p50), because both are bound by per-question
  overhead. Nano is clearly better on quality, and lite's decisions also flip
  when the request ends with a short run marker (hard→strong was 70, 62 and 56
  of 102 across three runs).
- **Throughput limit.** `d3_server.py` serves one request at a time. That caps
  one server at about 9–10 policy calls/s for the 26B model and about 50/s for
  nano. Above that, run the batching `vllm-sr serve --engine` or several
  replicas, and raise the adapter's `--max-concurrency` to match.
- **Next step.** Do co-location and batching first. The SSH tunnel added
  140–210 ms per call in these runs, which is more than the 26B model's whole
  inference time. Option B (a native `intelligent` client in the router) would
  only remove the small adapter hop (a few ms on loopback). Defer it until
  co-located shadow data shows the adapter hop matters.

## Setup

| Item | Value |
|---|---|
| Shadeform instance | `metrum-d3-20261011-1`, ID `fe9e00de-0ec6-4512-b9af-54e7a8327b85`, type `RTXPro6000` (massedcompute, kansascity-usa-6), $2.19/h, 16 vCPU, 192 GB RAM |
| GPU / driver | 1× NVIDIA RTX PRO 6000 Blackwell Server Edition, 97,887 MiB; driver 580.126.09; CUDA 13.0 (`ubuntu22.04_cuda13.0_shade_os`) |
| Runtime | Python 3.12.15 venv (uv), torch 2.14.1+cu130, transformers 5.17.0, triton 3.8.0, **flash-linear-attention 0.5.2 installed and working on CUDA**. `causal_conv1d` was not installed, so transformers used its PyTorch reference for that op. |
| Server | Package `d3_server.py --device cuda:0 --host 127.0.0.1 --port 8000` (default kernel warmup), `DECISION_API_KEY` set to a fresh random value, bearer auth on. One model at a time. |
| Models | `vllm-sr/d3-lite` `a8d25a91ef75552803f7009d25b7bd435991abd9`; `vllm-sr/d3-nano` `3aeb1c22a5632aac72353c36b3d0111c40e7dbf4`; `vllm-sr/d3` `cbff3a3cb0b074b6585fe9f747b9f9f5557a0e75`. All three match the issue pins, re-checked on the Hub at the start. |
| Connection | `ssh -N -L 127.0.0.1:18300:127.0.0.1:8000` from the workstation. ICMP RTT was 34 ms. d3 never listened on a public interface. |
| Router | Built from this tree, on `127.0.0.1:18301`. Group `d3-auto` from `scripts/local_pi_instance.py --d3-policy-url http://127.0.0.1:18302/route`: cheap = Fireworks `deepseek-v4p1-flash`, strong = OpenAI `gpt-6-luna`. |
| Adapter | `examples/external-routing-policy/d3_policy.py --port 18302 --url http://127.0.0.1:18300 --timeout-ms 1000`, default questions and rules, `--max-concurrency 1` |
| Timeouts | Router `external_policy.timeout_ms` was **1,500** and the adapter's was **1,000** for every tunnel run. Through `ssh -L`, each HTTP exchange cost about 3 RTTs (about 210 ms), so the issue's 300 ms default would have timed out on the tunnel and not on d3. Co-located, the policy needs far less (see f.1). |

Helper scripts are in `scripts/`:
- `shadeform_d3.py`: types, create, wait, delete and SSH keys. It never prints the API key.
- `remote_setup.sh`: pinned snapshots and venv.
- `remote_serve.sh`: serves a model and measures cold load and GPU memory.
- `tunnel_kill_drill.py`: the f.5 drill.
- `pi_smoke_summarize.py`: turns a `local-router-pi-smoke` run plus the usage DB into the scalar f.4 JSON.

## f.1 d3 alone: latency, memory, cold load, answers (204 prompts)

Each call asks the adapter's three questions (`difficulty` score, `reasoning`
noul, `kind` choice), with 3 warmup calls first. **On-host** means the same
client ran on the GPU host against `127.0.0.1:8000`, which is the co-located
number. **Tunnel** means the client ran on the workstation through the SSH
tunnel.

| Model | Cold load to ready | GPU mem | On-host client p50 / p95 / p99 ms | Server-timing p50 / p95 / p99 ms | Tunnel client p50 / p95 / p99 ms |
|---|---|---|---|---|---|
| d3-lite | 41.2 s first load on a fresh host (includes Triton compile and autotune); 7.0 s warm | 4,189 MiB | **18.9 / 20.0 / 20.3** | 18.4 / 19.5 / 19.9 | 229 / 232 / 235 |
| d3-nano | 9.1 s (warm Triton cache) | 7,877 MiB | **19.1 / 20.2 / 20.9** | 18.6 / 19.6 / 20.3 | 227 / 229 / 231 |
| d3 26B | 73.3 s (52 GB of weights from local disk) | 56,205 MiB | **102.5 / 105.7 / 106.3** | 101.8 / 105.0 / 105.3 | 316 / 326 / 338 |

0 errors and 0 malformed answers for any model. GPU memory was unchanged after
each full run. Weights downloaded from the Hub in 7 s, 10 s and 78 s
(1.7, 4.5 and 52.2 GB).

Answers vs labels (default rules: `difficulty` hard|expert or `reasoning` ≥ 0.6 → strong):

| Model | Tier agreement | Hard → strong | Easy → cheap | `kind` = domain |
|---|---|---|---|---|
| d3-lite | 0.843 | 70/102 | 102/102 | 194/204 |
| d3-nano | 0.907 | 83/102 | 102/102 | 199/204 |
| d3 26B | **1.000** | **102/102** | **102/102** | 201/204 |

Per-cell distributions (`by_cell`) are in `f1_d3_*_rtxpro6000_direct.json` and
`..._onhost.json`. Where nano misses, the hard prompts are math and chat that it
rates `moderate` with reasoning below 0.6. Every hard-code prompt went to
strong. The tunnel and on-host runs gave identical answers, since the server is
deterministic.

## f.2 Shadow through `d3-auto` (mode `shadow`, `on_error: fail_closed`)

Target 0 (cheap) is always served. The recommendation comes from
`request_policy_executions` and `request_routing_signals`.

| Model | HTTP | Policy outcome | Failed due to policy | Policy `duration_ms` p50 / p95 / p99 | Recommended strong / cheap | Hard → strong | Easy → cheap | Agreement |
|---|---|---|---|---|---|---|---|---|
| d3-lite | 204×200 | 204 `shadow_recommended` | 0 | 159 / 165 / 179 | 62 / 142 | 62/102 | 102/102 | 0.804 |
| d3-nano | 203×200, 1 client timeout* | 203 `shadow_recommended` | 0 | 159 / 165 / 185 | 80 / 123 | 80/101 | 102/102 | 0.897 |
| d3 26B | 204×200 | 204 `shadow_recommended` | 0 | 247 / 258 / 267 | 102 / 102 | 102/102 | 102/102 | **1.000** |

\* Seed `hard-chat-17` (nano run): the policy call succeeded (156 ms,
`shadow_recommended`). The Fireworks upstream then took longer than the
eval client's 120 s timeout, and the router recorded status 499. This was not
a policy failure.

The policy `duration_ms` values include the SSH tunnel. Co-located, they would
be close to the on-host f.1 numbers plus a few ms for the adapter.

Notes:
- **Router telemetry quirk.** When the shadow recommendation is target 0 (the
  `model_ref` target `deepseek-v4p1-flash`), the router writes
  `shadow_recommended_candidate` with `candidate_index -1`, so the row cannot be
  joined to a candidate. Recommendations of `gpt-6-luna` resolve correctly. So
  `scripts/d3_live_eval.py` also re-derives the recommended tier from the
  scalar class label (`shadow_by_label_tier`).
  - In the lite and nano runs, the -1 rows were one more than the
    cheap-labelled decisions (143 vs 142, 124 vs 123). That is one decision per
    run whose label rounds reasoning to `r0.60` while the raw value is just
    below 0.6. The label-tier numbers in the table can be off by one there.
  - In the 26B run, the counts match exactly (102 vs 102).
  - Logged as a follow-up.
- Each router run appends a short `[run <marker>]` line to the prompt, to keep
  conversation keys distinct. d3-lite's hard-prompt decisions moved with the
  marker (70 → 62 → 56 of 102 across direct, shadow and enforce). Nano moved
  slightly (83 → 80 → 81). 26B did not move (102 every time).

## f.3 Enforce (mode `enforce`, `on_error: fail_closed`)

| Model | HTTP | Strong share | Hard → strong | Easy → cheap | Policy p50 / p95 / p99 ms | Actual cost | Always-strong est. | Always-cheap est. |
|---|---|---|---|---|---|---|---|---|
| d3-lite | 204×200 | 0.275 (56) | 56/102 | 102/102 | 161 / 164 / 168 | $0.005850 | $0.004192 (actual +39.6%) | $0.006351 (actual −7.9%) |
| d3-nano | 204×200 | 0.397 (81) | 81/102 | 102/102 | 157 / 169 / 188 | $0.005449 | $0.004116 (+32.4%) | $0.006182 (−11.9%) |
| d3 26B | 204×200 | 0.500 (102) | **102/102** | 102/102 | 247 / 253 / 273 | $0.005188 | $0.004090 (+26.8%) | $0.006125 (−15.3%) |

Costs come from `request_usage.total_cost_usd`, with `max_tokens` 32 per
request. The always-X estimates price the same token counts at that target's
list price.

**Cost caveat:** in the issue's suggested pairing, the "strong" target
`gpt-6-luna` ($0.10 / $0.50 per M) is cheaper per token than the "cheap" target
`deepseek-v4p1-flash` ($0.22 / $0.66 per M). So sending more traffic to
"strong" lowers the bill. That makes the delta vs always-strong look like a
loss and the delta vs always-cheap look like a saving. With a strong tier that
really is pricier, the sign flips. What the delta shows is that enforce moves
real traffic in proportion to the d3 decision. It is not a saving claim for
this pairing.

The spot-check confirms that hard prompts reached the strong tier: 26B
102/102, nano 81/102, lite 56/102. No easy prompt reached strong with any
model.

## f.4 Agent continuation with real d3 (AGENTS.md definition of done)

Command: `make local-router-pi-smoke LOCAL_ROUTER_OUT=tmp/test-d3 LOCAL_ROUTER_LISTEN=127.0.0.1:18301 LOCAL_ROUTER_GROUP=d3-auto < /dev/null`.
It ran with `d3-auto` in enforce, `fail_closed`, against the real d3 on the GPU
over the tunnel. pi wrote and then read `proof.txt` with the write and read
tools.

| Model | Result | pi turns routed by d3 | Labels | Served |
|---|---|---|---|---|
| d3-lite | exit 0, `ok: true`, failures [] | 4 + unary + stream, all 200 | `d3:trivial:agentic:r0.13…0.25`, `d3:trivial:chat:r0.02` | Fireworks deepseek-v4p1-flash |
| d3-nano | exit 0, `ok: true`, failures [], no tool errors | 3 + unary + stream, all 200 | `d3:trivial:agentic:r0.09…0.27`, `d3:trivial:chat:r0.00` | Fireworks deepseek-v4p1-flash |

In the lite run, the cheap upstream model ran `read` in parallel with `write`.
It got ENOENT, re-read, and finished. pi reported these as `tool_errors`
(`read`, `bash`). The proof file and the final token still matched. This was
the upstream model's behaviour, not the router's. Evidence:
`f4_pi_continuation_d3_{lite,nano}_rtxpro6000.json`.

## f.5 Tunnel-kill drill

`scripts/tunnel_kill_drill.py`: 4 hard seed prompts with the tunnel up, then
SIGTERM to the `ssh -L` process, then 6 more.

| Model | `on_error` | Tunnel up | After kill | Upstream attempts after kill | Result |
|---|---|---|---|---|---|
| d3-lite | fail_closed | 4×200 `selected` | 6× **502 `routing-policy-error`**, policy `error` (adapter `d3_unreachable`) | **0** | pass |
| d3-lite | fallback | 4×200 `selected` | 6×200 policy `fallback`, served by **target 0** (deepseek-v4p1-flash) | 1 each | pass |
| d3-nano | fail_closed | 4×200 | 6×502 `routing-policy-error` | 0 | pass |
| d3-nano | fallback | 4×200 | 6×200 via target 0 | 1 each | pass |

### Unplanned drill: full 204-prompt runs with d3 unreachable

By operator error, one nano shadow run and one nano enforce run started with
the tunnel down. They are kept as extra evidence, not as f.2/f.3:

- `extra_shadow_d3_unreachable_204.json`: shadow with d3 unreachable. 204×200,
  all `shadow_error`, served by target 0. Shadow mode never fails a request.
- `extra_enforce_fail_closed_d3_unreachable_204.json`: enforce fail_closed with
  d3 unreachable. 204×502, 0 served.

## Not run

**`vllm-sr serve --engine --platform cuda` comparison (optional):** not run.
It needs the Docker-based vllm-sr install on the host. The GPU was kept on the
`d3_server.py` path for f.1–f.6, and then held for a demo. The batching
engine's throughput on CUDA is still the open question for loads above about
10 req/s with 26B.

## GPU spend

| Instance | Type | $/h | Created (UTC) | Delete requested (UTC) | Gone from `GET /v1/instances` | Billed span | Cost |
|---|---|---|---|---|---|---|---|
| `fe9e00de-0ec6-4512-b9af-54e7a8327b85` (`metrum-d3-20261011-1`) | RTXPro6000 | 2.19 | 15:10:15 | 16:49:46 | 16:50:15 | 1 h 39 m 31 s (1.659 h) | **$3.63** |

Total GPU spend: **$3.63**, against the $100 cap (the issue's original working cap
was $40). The figure counts from create, not from active (15:12:32), so it
errs on the high side. It includes a coordinator-requested demo hold from 16:38
to 16:49 with d3-lite served.

## Teardown

- 16:49:46Z: `shadeform_d3.py delete --id fe9e00de-…` returned HTTP 200. At
  16:50:15Z, `GET /v1/instances` returned `{"instances": []}`, so no
  `metrum-d3-*` instance remains.
- The SSH key added for this run (`metrum-d3-live-20261011`) was deleted
  (HTTP 200). `GET /v1/sshkeys` no longer lists it.
- The SSH tunnel, local router and adapter were stopped, and ports
  18300–18302 are free. The local out dir (keys, router state, usage DB) was
  deleted. The model weights lived only on the deleted instance.

## Commands (workstation; keys are read from ignored files and never printed)

```bash
python3 docs/evidence/d3/2026-10-11/scripts/shadeform_d3.py types
python3 docs/evidence/d3/2026-10-11/scripts/shadeform_d3.py sshkey-add --name metrum-d3-live-20261011 --public-key-file tmp/test-d3/id_d3.pub
python3 docs/evidence/d3/2026-10-11/scripts/shadeform_d3.py create --cloud massedcompute --region kansascity-usa-6 \
  --type RTXPro6000 --os ubuntu22.04_cuda13.0_shade_os --name metrum-d3-20261011-1 --ssh-key-id <key-id>
python3 docs/evidence/d3/2026-10-11/scripts/shadeform_d3.py wait --id <instance-id>
# on the host
bash remote_setup.sh "d3-lite d3-nano d3"
bash remote_serve.sh d3-lite a8d25a91ef75552803f7009d25b7bd435991abd9 8000   # then d3-nano, d3
# workstation
ssh -N -L 127.0.0.1:18300:127.0.0.1:8000 shadeform@<host>
D3_API_KEY=... python3 scripts/d3_live_eval.py direct --url http://127.0.0.1:18300 --model-label d3-lite-rtxpro6000 \
  --revision a8d25a91ef75552803f7009d25b7bd435991abd9 --out docs/evidence/d3/2026-10-11/f1_d3_lite_rtxpro6000_direct.json
uv run --with pyyaml python scripts/local_pi_instance.py --env-json <main>/env.json --out-dir tmp/test-d3 \
  --listen 127.0.0.1:18301 --d3-policy-url http://127.0.0.1:18302/route --d3-policy-mode shadow \
  --d3-policy-on-error fail_closed --d3-policy-timeout-ms 1500
D3_API_KEY=... python3 examples/external-routing-policy/d3_policy.py --port 18302 --url http://127.0.0.1:18300 \
  --api-key-env D3_API_KEY --timeout-ms 1000 --model d3-lite --revision a8d25a91ef75552803f7009d25b7bd435991abd9
python3 scripts/d3_live_eval.py router --base-url http://127.0.0.1:18301/v1 --token-file tmp/test-d3/METRUM_API_KEY \
  --usage-db tmp/test-d3/usage.sqlite --mode-label shadow --run-marker lite-shadow \
  --out docs/evidence/d3/2026-10-11/f2_d3_lite_rtxpro6000_shadow.json
# enforce: same with --d3-policy-mode enforce and --mode-label enforce
make local-router-pi-smoke LOCAL_ROUTER_OUT=tmp/test-d3 LOCAL_ROUTER_LISTEN=127.0.0.1:18301 LOCAL_ROUTER_GROUP=d3-auto < /dev/null
python3 docs/evidence/d3/2026-10-11/scripts/tunnel_kill_drill.py --base-url http://127.0.0.1:18301/v1 \
  --token-file tmp/test-d3/METRUM_API_KEY --usage-db tmp/test-d3/usage.sqlite --tunnel-pid <ssh-pid> \
  --on-error fail_closed --out docs/evidence/d3/2026-10-11/f5_tunnel_kill_fail_closed_d3_lite.json
```

# Earlier evidence: CPU and offline (before the Shadeform key worked)

The first d3 stream got HTTP 401 from the Shadeform API. Since then the user
has replaced the key. The shell environment also exports an older
`SHADEFORM_API_KEY` that still returns 401, so the helper reads `ops.env.json`
first. The evidence below was collected without a GPU.

### CPU setup

| Item | Value |
|---|---|
| Model | `vllm-sr/d3-lite`, revision `a8d25a91ef75552803f7009d25b7bd435991abd9` (matches issue pin) |
| Server | package `d3_server.py`, `--device cpu --no-warmup`, bearer auth on, 127.0.0.1 |
| Runtime | Python 3.12 venv, torch 2.14.1, transformers 5.17.0; no flash-linear-attention / causal-conv1d (PyTorch reference kernels) |
| Host | 16 vCPU, 30 GB RAM, `OMP_NUM_THREADS=8`, no GPU |
| Cold load | 20.4 s to `/health` ready with `--no-warmup`. The default warmup, which runs every batch size, had not finished after more than 15 minutes on CPU and was stopped |
| Questions | adapter defaults: `difficulty` (score), `reasoning` (noul), `kind` (choice) |

### f.1 (CPU, stratified subset): d3-lite answers vs labels

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

### Adapter against real d3-lite

`adapter_vs_real_d3_lite_cpu.json`: `d3_policy.py --timeout-ms 60000` against
the CPU server, with two seed prompts outside the measured subset:

| Seed | classLabel | targetIndex | rule |
|---|---|---|---|
| easy-chat-04 | `d3:trivial:chat:r0.02` | 0 (cheap) | cheap-default |
| hard-code-04 | `d3:hard:code:r0.75` | 1 (strong) | strong-hard-or-reasoning |

### Failure drill with the real model (router, fail_closed)

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

### Agent continuation (offline, fake d3, real upstreams)

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
