# Decision models: shadow and enforce

This example sends the same two synthetic prompts through D3, OpenJev and
Clef-shaped policy adapters in both router modes. **By default every decision
server is a deterministic keyword fake**. The fake target returns `ok`; no
quality, accuracy, performance or savings comparison is implied. The outcome
is a wiring check: shadow records a recommendation but serves the normal first
eligible target; enforce serves the recommended target. A real decision model
can disagree with the toy classifications and still satisfy the wiring check.

## Run the mock

From the repository root (Go, Python 3, PyYAML). The demo builds `routerctl`
and uses `callers hash` for its private PBKDF2 caller verifier:

```sh
go build -o /tmp/router-decision ./cmd/metrum-ai-router
python3 examples/decision-models/run.py --router /tmp/router-decision
```

Expect 12 successful combinations and structured `recommended_index`,
`served_tier` and `outcome` rows. Fakes are implemented in
`examples/external-routing-policy/fake_d3.py`, `fake_openjev.py` and
`fake_clef.py`. `config.example.json` contains only synthetic prompts and model
names, no keys. `evidence/` contains sanitized execution results.

## Labeled routing probe (mock by default)

A small original hand-authored dataset is in `benchmark_dataset.json` under
this repository's Apache-2.0 license. The cheap/strong labels represent an
illustrative routing policy, **not independently validated ground-truth task
quality**, and the data is too small and artificial for production claims.
Run the same cases in shadow and enforce and retain raw decisions:

```sh
python3 examples/decision-models/run.py --router /tmp/router-decision \
  --benchmark-dataset examples/decision-models/benchmark_dataset.json \
  --policies d3,openjev,clef --repetitions 9 > /tmp/decision-benchmark.json
```

This command uses only fakes and a fake generation backend. It proves harness
wiring, not real accuracy or latency. The report gives two-tier confusion
matrices, policy-label agreement among successful decisions, correct fraction
among all requests, and explicit error counts. Policy-duration p50/p99 uses
successful router telemetry only; p99 is omitted below 100 successful timings,
and repeated examples are correlated. These times are **not** added
end-to-end latency. Raw output retains the original policy outcome. Shadow
serves the default tier; enforce serves the selected tier. Both generation tiers
still point to the same backend, so this cannot measure misroute quality or
savings. No model-selection recommendation follows from this probe.

Select a subset using `--policies d3` (or `openjev`, `clef`, or a comma-separated
combination). In external mode supply URLs for the selected policies only. For
example, to probe an independently served real d3 endpoint on loopback:

```sh
python3 examples/decision-models/run.py --router /tmp/router-decision \
  --benchmark-dataset examples/decision-models/benchmark_dataset.json \
  --policies d3 --repetitions 9 --decisions external \
  --d3-url http://127.0.0.1:18300 --d3-model d3-lite \
  --d3-revision YOUR_PINNED_REVISION --d3-timeout-ms 3500 \
  --state-dir /dev/shm/decision-private-new
```

`--d3-revision` records an operator declaration; the adapter does not verify
server weights. `--clef-model` allows a real compatible server's model ID; the shipped fake
requires its default `clef`. `--d3-timeout-ms` and `--clef-timeout-s` configure
adapter deadlines below the router's 4s policy deadline. A timeout or missing
telemetry is reported as an error, not dropped or labeled correct. External
mode still uses fake generation by default, unless `--backend local` is also
set; private state contains a caller credential and must not be published.
Labeled benchmark mode rejects the paid Fireworks backend; the original bounded
12-request demo remains available separately. Local model/API compatibility, licensing, GPU provisioning and actual costs
must be established separately. The benchmark does not create GPU resources.

## Partial A6000 observation

A [bounded real d3-lite / Clef-flash observation](evidence/a6000-2026-10-29/README.md)
includes raw synthetic decision rows and explicit limitations. It uses different
adapter questions/rules, fake generation and an SSH tunnel; it is not a
representative accuracy ranking, co-located added-latency benchmark, or
misroute quality/cost evaluation. OpenJev and edge results are missing.

## Real endpoints and backends

The adapters send prompt content to the decision endpoints (`include_request`
is true). Use only trusted, isolated services with an approved data policy;
this example is not a privacy boundary. To replace fakes with three running
local services, add:

```sh
--decisions external \
--d3-url http://127.0.0.1:D3PORT \
--openjev-url http://127.0.0.1:JEVPORT \
--clef-url http://127.0.0.1:CLEFPORT/v1/systemone \
--state-dir /absolute/private/new-dir
```

D3 and OpenJev use their existing `/v1/systemone` decision protocol. The Clef
adapter expects a **user-supplied local shim** that accepts a Clef-style
`{model,state,questions}` request and responds with
`{answers:{tier:{choice,confidence}}}`. It is not Cloudflare Workers AI REST
and this repository does not ship a Clef inference server. No actual Clef model
or external decision endpoints were run for this example; model deployment,
protocol compatibility, licensing and decision-server cost are the operator's
responsibility. OpenJev's weights are CC BY-NC 4.0: this demo does not grant
commercial usage rights. See `examples/external-routing-policy/README.md`.

To serve generation from an OpenAI-compatible loopback service instead of the
fake, add `--backend local --local-url http://127.0.0.1:PORT/v1 --local-model
MODEL --state-dir /absolute/private/new-dir`. The tests use a loopback fake,
not a real local model. Both routing tiers point to the **same** supplied model;
provider aliases allow wiring verification, not quality comparison. Token prices
are zero and exclude inference and infrastructure cost.

For a **paid** hosted generation smoke, set `FIREWORKS_API_KEY` privately in the
process environment and use `--backend fireworks --state-dir
/absolute/private/new-dir`. Default decision servers remain fakes; both tiers
use the same hosted model. Twelve bounded requests are made and usage is
printed; decision-service costs, hosting and later adjustments are not included.
Inspect current pricing and availability first. Configured dated list-price
costs derived from reported usage are not provider invoices. The private state
contains router credentials, request logs and the SQLite usage database. Keep
it private, reconcile all attempts including failures, then remove it. For mock
mode only, the script automatically cleans its temporary directory.

Routing decisions are not a safety filter. This example makes no claims about
actual model inference, model quality, calibrated probabilities, independent
reproduction or savings; any production deployment needs its own dataset,
latency, failure-mode, security and cost evaluation.
