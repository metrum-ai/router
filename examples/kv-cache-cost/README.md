<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# KV-cache cost and routing

## What it shows

This example sends the same long synthetic agent-context prefix repeatedly,
first through dynamic-score prefix affinity to one model, then deliberately
spreads requests across two models. Each arm has a distinct,
equal-length marker at the beginning to avoid deliberately sharing its cache
with the other arm. It reads cached and recomputed input tokens, cache-hit
rate, streaming TTFB, and request-time cost from router usage rows. It does
**not** control provider cache allocation or prove a causal production latency
difference. The two model identities are not interchangeable-quality claims.

The default mock simulates per-model prefix caching with fictional token
counts. The [mock result](evidence/mock-output.json) is a wiring check, not a
provider measurement or a cost saving.

## How to run

Go is required: the demo builds `routerctl` locally and uses `callers hash`
to create a PBKDF2 token verifier in private state.

Requires Go, Python 3 and `uv`:

```sh
go build -o /tmp/kv-router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/kv-cache-cost/run.py --router /tmp/kv-router
```

For a hosted Fireworks run, verify the dated rates in
[`config.example.json`](config.example.json), set `FIREWORKS_API_KEY` in an
approved secret environment, and use a fresh private tmpfs state directory:

```sh
uv run --with pyyaml python examples/kv-cache-cost/run.py --router /tmp/kv-router \
  --backend fireworks --state-dir /dev/shm/kv-cache-run
```

This makes paid requests with a long synthetic prefix. Budget them before
running. Provider cache hits can vary; an absent cached-token field is unknown,
not a zero or a hit. On failure, retain private state long enough to reconcile
potentially charged attempts; remove it after review. Provider invoices may not
be attributable to the run. Never publish keys, router state or raw provider
logs.

For an already running self-hosted OpenAI-compatible server with two model
IDs, use a loopback endpoint or secure tunnel:

```sh
uv run --with pyyaml python examples/kv-cache-cost/run.py --router /tmp/kv-router \
  --backend local --local-url http://127.0.0.1:8000/v1 \
  --local-model-a MODEL_A --local-model-b MODEL_B --state-dir /dev/shm/kv-local
```

The script does not start that server or provision GPUs. Local rates are zero
and exclude infrastructure; they are not total inference cost. Backend usage
must be reported, and cached-input support varies by implementation.

## What you should see

The 2026-11-16 mock run measured 4 requests per arm. Dynamic-score prefix
affinity reported a 75% cache-hit rate, 16,000 recomputed tokens, and
$0.00504 configured-price cost. Deliberate spreading reported 50%, 24,000
recomputed tokens, and $0.007392. Mean TTFB was 6.75 ms vs 2.25 ms; these
mock timings are scheduler noise and do not show a latency improvement. The
cache and cost differences are simulated, not backend measurements.

The 2026-11-16 Fireworks streamed run measured 4 requests per arm. Dynamic-score
prefix affinity reported a 75% hit rate and 16,032 recomputed tokens; deliberate
spreading reported 50% and 31,548. Configured-rate usage cost was 46.9% lower
with affinity. Mean TTFB was 1,762 ms vs 741 ms, so this run showed no latency
improvement. One synthetic run with different model identities cannot establish
production performance or model-quality equivalence.

In the [2026-10-28 hosted result](evidence/hosted-output.json), each arm had
four requests and two reported cache hits. Both had the same usage-derived
cost of $0.009727368; the last pinned request unexpectedly missed the cache.
The [sanitized request-time rows](evidence/hosted-usage.json) show actual token
counts, rates and dates. The total for the run was $0.019454736 at configured
rates, **not** a reconciled provider invoice. This small single run does not
establish that spreading is equal to or better than pinning, nor explain the
unexpected miss. The hosted run preceded the addition of the local-backend
CLI option; its routing and accounting paths are unchanged.

CI runs the mock and exercises the local-backend path against a fake server.

Metrum AI
