# Decision models: shadow and enforce

This example sends the same two synthetic prompts through D3, OpenJev and
Clef-shaped policy adapters in both router modes. **By default every decision
server is a deterministic keyword fake**. The fake target returns `ok`; no
quality, accuracy, performance or savings comparison is implied. The outcome
is a wiring check: shadow records a recommendation but serves the normal first
eligible target; enforce serves the recommended target. A real decision model
can disagree with the toy classifications and still satisfy the wiring check.

## Run the mock

From the repository root (Go, Python 3, PyYAML):

```sh
go build -o /tmp/router-decision ./cmd/metrum-ai-router
python3 examples/decision-models/run.py --router /tmp/router-decision
```

Expect 12 successful combinations and structured `recommended_index`,
`served_tier` and `outcome` rows. Fakes are implemented in
`examples/external-routing-policy/fake_d3.py`, `fake_openjev.py` and
`fake_clef.py`. `config.example.json` contains only synthetic prompts and model
names, no keys. `evidence/` contains sanitized execution results.

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
