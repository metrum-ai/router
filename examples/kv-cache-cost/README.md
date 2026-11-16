<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# KV-cache cost and routing

This example compares one `dynamic_score` group and target set with session
affinity disabled and enabled. The disabled arm alternates four short requests
across two deployments of the same model, then the enabled arm pins a session
to its first successful target. Each arm uses its own synthetic prefix to
isolate cache state. Both arms include a 32,000-token request to exercise the
tiered target above the short-request 120 KB bound.

The harness reads actual usage rows for reported input and cached tokens,
configured-price request costs, and streaming TTFB. It reads only response
usage fields; it never captures prompts. The results are a small run with five
requests per arm. Endpoint prices differ, cache state is provider-controlled,
and observed latency or cost differences should not be generalized as causal
production effects.

## Mock check

```sh
GOFLAGS=-p=2 go build -tags dev_no_license -o /tmp/kv-router ./cmd/metrum-ai-router
ROUTER_TEST_BINARY=/tmp/kv-router uv run --with pyyaml python examples/kv-cache-cost/test_run.py -v
uv run --with pyyaml python examples/kv-cache-cost/run.py --router /tmp/kv-router --backend mock
```

The committed [mock output](evidence/mock-output.json) verifies the off-arm
short requests route A/B/A/B and the affinity-on requests route A/A/A/A. Mock
usage, cache behavior and latency are simulated; these are not provider
measurements.

## Hosted same-model comparison

Pair mode uses Fireworks and OpenRouter deployments of DeepSeek V4.1 Flash
1017. The endpoint rates and sources are dated in
[`config.example.json`](config.example.json). Set both `FIREWORKS_API_KEY` and
`OPENROUTER_API_KEY` in the environment and use a fresh private tmpfs state
directory:

```sh
uv run --with pyyaml python examples/kv-cache-cost/run.py --router /tmp/kv-router \
  --backend pair --state-dir /dev/shm/kv-cache-pair
```

The sanitized [2026-11-16 result](evidence/hosted-affinity-pair-2026-11-16.json)
shows the off arm split short traffic 2/2 across providers and the affinity-on
arm route all four short requests to Fireworks. Short-request cache hit rates were 25% off and 50% on. Short-request
streaming TTFB p50/p95 was 651.5/1,176 ms off and 916.5/1,151 ms on.
Short-request usage-row costs were $0.010171232 off and $0.013525032 on.
Including the tiered request, total usage-row costs were $0.024605432 off and
$0.027958932 on. The tiered requests reported 48,050 and 48,049 input tokens,
respectively, and both reported zero cached input tokens. This single small
run showed more short-request cache hits with affinity, higher median TTFB and
higher usage-row cost on. Provider mix differs between the arms, and the sample
is not a causal production estimate.

Costs are calculated from usage rows at the configured endpoint prices, not
provider invoice reconciliations. A missing cached-token field means unknown,
not zero. If a run fails, retain private state long enough to reconcile any
charged attempts, then remove it. Never publish keys, router state or raw
provider logs.

## Cache usage reporting

Cached-token metering is recorded only when a response reports it. OpenAI
compatible responses can provide `prompt_tokens_details.cached_tokens` or
`input_tokens_details.cached_tokens`; Anthropic Messages responses can provide
`cache_read_input_tokens`. Support varies by engine and model. The hit rate is
derived from cached-token and reported-request counters; requests without a
reported cached-token field are excluded.

Metrum AI
