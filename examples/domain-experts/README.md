<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Domain experts

## What it shows

A single model group routes explicitly tagged synthetic requests to four
model targets and routes untagged or ambiguously tagged requests to a general
target. This example is **not a classifier**, does not prove that a model is
an expert in a domain, and must not be used for access control or privacy:
callers can write or forge the tags. The mock targets are not actual expert
models, and the example does not establish quality or savings. The separate
GPU comparison in #426 is not represented by this smoke.

The external policy receives request text (`include_request: true`), so it
must be inside the same trusted boundary as the router. Do not use real
sensitive data. Policy errors fail closed; review fallback behavior separately
before deploying with real data.

## How to run

Go is required: the demo builds `routerctl` locally and uses `callers hash`
to create a PBKDF2 token verifier in private state.

Requires Go, Python 3 and `uv`. From the repository root:

```sh
go build -o /tmp/domain-router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/domain-experts/run.py --router /tmp/domain-router
```

The default uses five mock target identities on one loopback fake upstream and
makes no paid requests. It starts and stops an isolated router and policy. The
key-free [`config.example.json`](config.example.json) defines mock model IDs
and loopback self-hosted addresses.

For actual self-hosted experts, copy the JSON to a local file, replace each
model ID and each local URL with your deployed OpenAI-compatible server or
SSH tunnel, and use a fresh private tmpfs state directory:

```sh
uv run --with pyyaml python examples/domain-experts/run.py --router /tmp/domain-router \
  --backend local --config /path/to/local-config.json --state-dir /dev/shm/domain-run
```

The script does not start those servers or provision GPUs. Zero configured
local token cost excludes infrastructure and must not be interpreted as free.

For hosted Fireworks models, copy the JSON and set five **distinct** valid
`accounts/<account>/models/<model>` IDs. Add a `pricing` object with one
entry per domain containing `input_per_million_usd`,
`cached_input_per_million_usd`, `output_per_million_usd`, an HTTPS `source`,
and ISO `date`. Verify every price against the provider, set
`FIREWORKS_API_KEY` in an approved secret environment, and use a fresh private
state directory:

```sh
uv run --with pyyaml python examples/domain-experts/run.py --router /tmp/domain-router \
  --backend fireworks --config /path/to/hosted-config.json --state-dir /dev/shm/domain-hosted
```

This makes paid requests. The preflight is a conservative token-price
reservation, not an invoice or a guarantee about retries, provider policy or
unpriced services. Monitor spend, and reconcile any errors before retrying.
Delete private state after the run. Do not place credentials in the config.

A [2026-10-30 CPU local smoke](evidence/local-cpu-summary-2026-10-30.json)
ran five loopback inference servers with distinct pinned GGUF weights listed in
the [manifest](evidence/local-cpu-manifest-2026-10-30.json). The domain runner
verified all five routes returned HTTP 200 in one attempt without fallback.
Separate direct greeting checks returned nonempty responses with `stop` finish
reasons on all five servers; these were not the routed requests. The runner did
not inspect routed response bodies or finish reasons, and three routed calls
reported the full 20-token limit. The small, narrow models and synthetic tags
show local routing and transport only—not classification, specialist quality,
independent external reproduction or production readiness. No GPU or hosted
calls were used; local CPU and infrastructure costs were not measured. The
sanitized result and procedure are in snapshot `4533b232`
(`router/v45-proof/424-domain-cpu`).

A [2026-10-29 hosted smoke](evidence/hosted-summary-2026-10-29.json)
selected five distinct Fireworks model IDs with 5/5 HTTP 200 responses, each
with a single attempt and no fallback. This is tag-routing and transport
coverage, not classification or domain-expertise evidence: the chosen models
were not validated as specialists. All five reported 20 output tokens, equal
to the configured limit; the runner did not inspect response bodies or finish
reasons, so complete or useful answers are not established. The provider-reported token counts and
published prices give a usage-derived total of $0.0001122, **not an invoice**;
actual attributable charges remain unknown. The [sanitized usage rows](evidence/hosted-usage-2026-10-29.json)
and [dated configuration](evidence/hosted-config-2026-10-29.json) record the
model IDs and [price source](https://docs.fireworks.ai/serverless/pricing).
The sanitized run and dated pricing page are retained in snapshot `81525a7e`
(`router/v45-proof/424-domain-hosted`). Refresh prices before reproducing;
these historical rates are not live quotes.

## What you should see

The [mock evidence](evidence/mock-output.json) shows the four explicit domain
routes and general fallback. CI runs the router against the mock and tests
unknown and ambiguous labels. No number in this mock is a quality, latency or
cost comparison.

Metrum AI
