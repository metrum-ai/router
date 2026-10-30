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

## Synthetic labeled measurement fixture

`benchmark_run.py` adds 12 hand-authored arithmetic cases, fixed reference
answers, and explicit synthetic `complexity`, `sensitive` and latency labels.
It uses known case IDs and exact prompt matching to choose a tier; it is **not**
a complexity or sensitivity classifier and must not be used with real data.
A latency label is a routing input, not a measured SLA. The sensitive cases
are invented arithmetic, not private data; the frontier-only comparison also
sends those synthetic cases to the frontier. Use neither this fixture nor its
markers to make production privacy decisions.

The default is fake-only (all answers scripted, no network or paid calls):

```sh
uv run --with pyyaml python examples/escalation-continuum/benchmark_run.py \
  --router /tmp/escalation-router
```

For an explicitly approved mixed run, first provision the two independently
protected local and GPU services, expose them only on loopback, set the OpenAI
credential, and add `--backend mixed --allow-paid`, the four endpoint/model
flags shown above and a **new** private absolute `--state-dir`. Unlike the
smoke runner, the benchmark's loopback relay forwards only the fixture's exact
synthetic inputs to a fixed GPT-6.1 Sol Responses endpoint, rejects additional
payload fields, limits output to 32 tokens, and counts at most 15 upstream
attempts (including ambiguous failures). Stop and reconcile after any failure;
these bounds are not an invoice or a guarantee against rate changes. It does
not provision, watch or tear down GPU services. Never publish private state.

Each case runs both `frontier-only` and `escalation` through the router. Raw
records show exact-match correctness, selected tier, request time, token usage
and usage-derived cost; routing rows must match the fixture reason. The sample
is far too small and narrow for general quality claims. Local and GPU cost
excludes hardware and energy. Provider costs are configured token-price
estimates, not reconciled billing. Network/tunnel topology is part of timing;
do not treat it as co-located overhead. AMD and NVIDIA must be independently
measured before claiming vendor-neutral results. A single
[synthetic MI300X measurement](evidence/mi300x-2026-10-30/README.md)
records CPU, AMD GPU and hosted frontier routing and the illustrative latency
target it missed; it does not establish provider billing or production parity.

The isolated fake Responses integration can also be exercised without any
network traffic or real key by setting `ROUTER_TEST_BINARY=/tmp/escalation-router`
when running the tests below.

## What you should see

The output identifies the selected tier and model for each prompt, using the
stored request-usage rows. The runner fails if a route, status, retry count or
reported usage does not match its expectations. Run the boundary controls with:

```sh
uv run --with pyyaml python -m unittest discover -s examples/escalation-continuum -p 'test_*.py' -v
```

Metrum AI
