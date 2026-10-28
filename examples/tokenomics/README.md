<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Tokenomics: caller limits and cost attribution

## What it shows

Two synthetic callers in one project each send a request through a local
router. The first caller exhausts its daily **request-count quota** and is
rejected on the next request. Request-time usage rows attribute tokens and
configured-price cost to owner, public key ID, project and model group. This
is not a monetary budget enforcement mechanism: token/request quotas do not
cap dollars when prices and output lengths vary. Usage-row cost is not an
independently reconciled provider invoice.

## How to run

Go is required: the demo builds `routerctl` locally and uses `callers hash`
to create PBKDF2 token verifiers in private state.

Prerequisites: Go, Python 3 and `uv`. From the repository root:

```sh
go build -o /tmp/tokenomics-router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/tokenomics/run.py --router /tmp/tokenomics-router
```

The default uses a loopback mock, no credentials and no paid requests. It
creates and removes a temporary router instance, a mock server and private
caller keys. Synthetic prices and quota settings are in
[`config.example.json`](config.example.json).

Optional hosted Fireworks variant (incurs cost): set `FIREWORKS_API_KEY` in
an approved secret environment; do not paste it into a command or config.
Review the current provider price and the budget first, then run with a fresh,
private tmpfs state directory. State is retained for reconciliation even if the
run fails; stop and reconcile before retrying. Remove that state after review.

```sh
uv run --with pyyaml python examples/tokenomics/run.py --router /tmp/tokenomics-router \
  --backend fireworks --state-dir /dev/shm/tokenomics-run
```

For a self-hosted OpenAI-compatible backend already listening on loopback:

```sh
uv run --with pyyaml python examples/tokenomics/run.py --router /tmp/tokenomics-router \
  --backend local --local-url http://127.0.0.1:8000/v1 \
  --local-model YOUR_MODEL --state-dir /dev/shm/tokenomics-local
```

The local variant's zero token price excludes GPU and other infrastructure
costs; it must not be interpreted as free inference. It does not provision or
start that server. For non-mock variants the backend must report usage tokens;
a request error, retry or fallback requires investigation before reuse.

## What you should see

The [mock output](evidence/mock-output.json) contains two successful requests,
then a quota rejection, with separate owner/key rows. Its cost values use
fictional configured prices and are **not** provider charges. HTTP status,
token and cost values are machine-checked by the runner. The CI smoke invokes
this same mock path and does not send requests to a hosted model.

A [hosted Fireworks run](evidence/hosted-output.json) on 2026-10-28 produced
those same HTTP statuses. Its [sanitized usage rows](evidence/hosted-usage.json)
show that the rejected request made no upstream attempt. Total usage-derived
cost for the two successful requests was $0.0000678 at the dated configured
rates. Provider invoices are not attributable to this run. The hosted output
was captured before the runner was updated to print the rejection-attempt count;
the count was separately checked in the stored usage rows.

Metrum AI
