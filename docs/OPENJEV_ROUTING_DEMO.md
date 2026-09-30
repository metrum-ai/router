# OpenJev external routing demo (Shadeform)

**Research / non-commercial weights.** [OpenJev](https://huggingface.co/openjev/openjev)
is CC BY-NC 4.0. Do not use the weights for commercial marketing without
permission from the authors. This repository's policy/shim glue is Apache-2.0.
OpenJev is independent of TypeSafe's hosted Jev product.

## What it demonstrates

Metrum AI Router `strategy: external` calls a trusted policy that asks OpenJev
for a **task class** (`simple` / `medium` / `advanced`), then maps that class
onto priced OpenAI targets:

| task | intent | live-key default | published $/1M in/out |
|---|---|---|---|
| simple | summarization, extract, classify | `gpt-5.6-luna` | 0.20 / 1.20 |
| medium | bounded coding / analysis | `gpt-5.6-sol` | 4.00 / 20.00 |
| advanced | hard reasoning / research | `gpt-6-astra` | 10.00 / 50.00 |

On 2026-09-25 the configured OpenAI key listed `gpt-6-astra` but not
`gpt-6-luna` / `gpt-6-sol`, so the example uses the GPT-5.6 substitutes above.
Prefer GPT-6 Luna/Sol when the key lists them. `gpt-5.4-nano` remains a cheap
fallback hint inside the policy.

## Synthetic (CI / laptop)

```bash
python3 scripts/openjev_policy_test.py
python3 scripts/run_openjev_routing_demo.py
# or: make openjev-routing-demo
python3 scripts/openjev_routing_benchmark.py --out docs/evidence/openjev-routing
# optional tiny live OpenAI cost sample:
python3 scripts/openjev_routing_benchmark.py --live-openai --live-limit 6
```

## Shadeform OpenJev (operator)

1. Discover GPUs (never prints the API key):

```bash
python3 scripts/openjev_shadeform.py discover
```

Prefer available `RTXPro6000` (Blackwell Server Edition). Record cloud, region,
SKU, and hourly price.

2. Create (requires a pre-registered Shadeform SSH key id):

```bash
python3 scripts/openjev_shadeform.py create \
  --cloud massedcompute --region <region> --sku RTXPro6000 \
  --ssh-key-id <id> --name openjev-routing-demo
python3 scripts/openjev_shadeform.py wait --instance-id <id>
```

3. Print and run the remote bootstrap (localhost-only vLLM + shim):

```bash
python3 scripts/openjev_shadeform.py serve-script > /tmp/openjev-serve.sh
# scp + ssh, then bash openjev-serve.sh on the GPU host
```

4. Tunnel and run the policy on the router host:

```bash
ssh -N -L 3000:127.0.0.1:3000 <user>@<ip>
OPENJEV_URL=http://127.0.0.1:3000 \
  python3 examples/external-routing-policy/openjev_policy.py --port 18093
```

5. Point the router at [`examples/external-routing-policy/config.openjev.example.yaml`](../examples/external-routing-policy/config.openjev.example.yaml).
Start `external_policy.mode: shadow`, compare recommendations, then `enforce`.
Shut down the Shadeform instance when finished; capture hourly cost in your operator journal.

## Security

- Do not expose vLLM (`:8000`) or the OpenJev shim (`:3000`) publicly.
- Keep `OPENAI_API_KEY` on the router host only.
- `include_request: true` is required for content classification; treat the
  policy process as trusted infrastructure.
- Fail closed on OpenJev errors (`on_error: fail_closed`).

## Live evaluation (real OpenJev)

After the shim is reachable on `OPENJEV_URL` (SSH tunnel recommended):

```bash
OPENJEV_URL=http://127.0.0.1:3000 python3 scripts/openjev_routing_live_eval.py \
  --live-openai --openai-per-tier 5 --shuffle-repeat 2
```

Observed live evidence: [`docs/evidence/openjev-routing/benchmark.live.json`](../evidence/openjev-routing/benchmark.live.json).

For a separate animation agent, use the measured handoff prompt:
[`docs/evidence/openjev-routing/ANIMATION_AGENT_PROMPT.md`](../evidence/openjev-routing/ANIMATION_AGENT_PROMPT.md).

Operational notes from the live run:

- Install `cuda-nvcc-13-0` and set `CUDA_HOME=/usr/local/cuda-13.0` before vLLM.
- Set `VLLM_USE_FLASHINFER_SAMPLER=0` if flashinfer JIT fails without a full toolkit.
- Start the helper with `python3 openjev/helper/shim.py` (not `python`).
- Delete the Shadeform instance when finished (`scripts/openjev_shadeform.py delete`).
