<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Escalation continuum

## What it shows

A local external policy selects one of three targets: a local tier, a
self-hosted datacenter tier, or a frontier API. It uses prompt character count
as a deliberately crude *proxy* for complexity: see
[`config.example.json`](config.example.json). It does not measure task
complexity, sensitivity, model quality, latency or cost, and must not be used
as a privacy or quality guarantee. Operators must validate route quality and
failure/fallback behavior before production use.

The policy receives only the router's derived `textChars` and target metadata,
not raw request content (`include_request: false`). Policy errors are configured
to fail closed. The local policy service is trusted infrastructure.

## How to run

Go is required: the demo builds `routerctl` locally and uses `callers hash`
to create a PBKDF2 token verifier in private state.

Requires Go, Python 3 and `uv`. The mock mode makes no paid calls:

```sh
go build -o /tmp/escalation-router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/escalation-continuum/run.py --router /tmp/escalation-router
```

The three mock targets all run on loopback. CI exercises the real router, the
policy and those mock targets. The [recorded mock output](evidence/mock-output.json)
shows the selected route for each synthetic prompt; zero mock token cost is
not a measured saving. A separate [mixed smoke](evidence/mixed-output.json)
used mock local and datacenter backends and a real OpenAI frontier request.
Its [usage rows](evidence/mixed-usage.json) record $0.000498 in hosted
usage-derived cost; that amount is not an invoice, and the mock tiers say
nothing about GPU costs or performance.

For a mixed deployment, start two OpenAI-compatible servers locally or tunnel
them to loopback, with independently selected local and datacenter models. Set
`OPENAI_API_KEY` in the approved environment for the GPT-6.1 Sol frontier
backend. This makes **paid** hosted requests; inspect current pricing and budget
first. The persistent state directory must be new, private and preferably on
tmpfs, and must be deleted after reconciliation:

```sh
uv run --with pyyaml python examples/escalation-continuum/run.py \
  --router /tmp/escalation-router --backend mixed \
  --local-url http://127.0.0.1:8000/v1 --local-model YOUR_LOCAL_MODEL \
  --datacenter-url http://127.0.0.1:8001/v1 --datacenter-model YOUR_DC_MODEL \
  --state-dir /dev/shm/escalation-run
```

The runner creates neither self-hosted server nor cloud resources. Its zero
configured local/datacenter token rates exclude hardware, energy and other
compute costs. It does not show savings or quality parity. Stop on any error
and reconcile potentially charged requests before retrying; never expose the
state directory, caller token, router config or provider credentials. The
example uses a short-lived router and cleans it up on normal exit.

## What you should see

The output identifies the selected tier and model for each prompt, using the
stored request-usage rows. The runner fails if a route, status, retry count or
reported usage does not match its expectations. Run the boundary controls with:

```sh
uv run --with pyyaml python -m unittest discover -s examples/escalation-continuum -p 'test_*.py' -v
```

Metrum AI
