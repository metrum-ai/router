<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Paired case-study evidence controls

This directory contains shared setup, extraction and evidence checks for #423.
`analyze.py` summarizes paired results; it does not execute a model or a
verifier. An operator must independently validate the workload, verifier and
usage exports. Failed trials must not be removed from the denominator.

## Local router setup

Requires Python 3, `uv`, Go and approved provider credentials in environment
variables `OPENAI_API_KEY` and `FIREWORKS_API_KEY`. Model calls cost money.
Verify current prices and set a budget before running. Use a private tmpfs
state directory; do not commit or publish it. From the repository root:

```sh
uv run --with pyyaml python examples/case-studies/common/setup.py \
  --config examples/case-studies/common/config.example.json --state-dir /dev/shm/case423
go build -o /tmp/case423-router ./cmd/metrum-ai-router
/tmp/case423-router --config /dev/shm/case423/config.yaml
```

For an **optional** future billing-attribution experiment, provision approved,
exclusive credentials and set `OPENAI_FRONTIER_API_KEY`,
`OPENAI_ROUTED_API_KEY`, and `FIREWORKS_ROUTED_API_KEY` privately instead.
Add `--isolate-billing-arms` to the setup command. The two OpenAI keys must
be distinct; the generated config contains only environment references. This
separates provider credentials by arm, but it is **not an authorization or
privacy boundary**: the same caller can access both groups, and the router
process holds all three credentials. It does not prove billing attribution:
confirm each key's billing scope is exclusive, include failures and retries,
and reconcile provider charges before claiming billed cost. The scheduled
workflow does not use this option.

In a separate shell, check `curl -fsS http://127.0.0.1:18123/readyz` and follow
the workload README. When done, stop the router and remove the private state.
Never print, publish or copy provider credentials or the generated caller token.
`config.example.json` has no keys; `evidence/router-config-sanitized.yaml` is
the actual run's configuration with caller token fields removed (not runnable).
Rates and source dates in the config must be verified and updated before a new run; `extract.py` checks the router's
recorded costs against the request-time token counts and configured rates.
Its output is a **usage-derived list-price calculation**, not a provider invoice.
Invoices may not be attributable to a particular run. It rejects retries and
fallbacks rather than pretending their costs are captured. The runners execute
the frontier arm first and then the routed arm; they do not randomize order or
control provider drift. The small fixture sets and confidence intervals cannot
establish equivalence or generalize to production.

Run the offline synthetic tests:

```sh
uv run --with pyyaml python -m unittest discover -s examples/case-studies/common -p 'test_*.py' -v
```

The suite imports PyYAML; `uv run --with pyyaml` installs it for the run. A bare
`python3 -m unittest` fails on that import.

To summarize an independently collected, sanitized JSON evidence file:

```sh
python3 examples/case-studies/common/analyze.py /path/to/evidence.json > /path/to/summary.json
```

The input document contains `workload` (`coding-agent`, `support-agent` or
`document-qa`), a `verifier` identity, the predeclared unique `planned_trials`
IDs, `frontier_model`, and an `arms` object containing exactly `routed` and
`frontier`. Each arm has:

- `tasks`: one `{trial_id, passed}` row per planned task, including failures;
- `usage`: every request, with `{trial_id, request_id, model, status,
  latency_ms, router_cost_usd, price_source, price_date}`. Record retries and
  failed requests as separate rows. `router_cost_usd` is a decimal **string**
  copied from the stored request-time router row; price source is an HTTPS URL
  and price date is ISO `YYYY-MM-DD`;
- optional `billing`: `{scope: "exact-request-set", source, currency: "USD",
  billed_usd, request_ids}`. `billed_usd` is a decimal string, and request IDs
  must exactly cover the arm's usage rows. Supply only genuinely attributable
  provider-rated billing evidence; an estimate or the router's list-price
  calculation is not a bill. Do not allocate an account-wide invoice to an arm
  without evidence of the allocation.

The tool rejects missing planned tasks, mismatched pairs, missing usage,
duplicate request IDs, invalid price dates and non-finite amounts. It reports
per-arm counts, Wilson intervals, request-weighted model mix, nearest-rank
p50/p95 request latency and paired wins/losses. Missing billing is `null`, not
zero. A supplied receipt remains **unverified**: the tool cannot establish
its authenticity, that all failed requests were captured, that the declared
frontier model is actually frontier, or that a verifier assesses the intended
quality. Those are review obligations. Provider invoices may be delayed,
aggregated or lack per-request attribution; retain that limitation instead of
fabricating an exact receipt.

## Planning billing attribution for a future run

The current setup and scheduled workflow use one credential per provider for
both arms. Their existing aggregate provider charges therefore cannot, by
that fact alone, be assigned to an arm or request set. Do not retrospectively
allocate a shared invoice or relabel token-price estimates as billed costs.

For a future approved reproduction, an operator can investigate separately
scoped, exclusive billing identities for each arm and provider, while keeping
all keys private. For example, OpenAI's [Costs API](https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs)
provides project-grouped daily cost buckets, and Fireworks documents
[usage and cost breakdowns](https://docs.fireworks.ai/accounts/exporting-usage-and-costs),
including a rated-cost query with API-key grouping. These are aggregated views,
not automatically per-request invoices. Access, granularity, availability,
rounding, retries, adjustments and credits must be verified with the provider.
Use identities exclusively for the relevant arm over the covered period, account
for every attempt, and reconcile rated totals with actual charges before claiming
billed cost. If any of that cannot be established, report billing as unknown.
The current example does not provision identities or perform this reconciliation;
a change to isolate arms would need separate implementation and review.

The 2026-10-28 study runs have since been reconciled after the fact: provider
usage for the study minutes matched the router's usage rows exactly, so the
`gpt-6.1-sol` charges of both arms are billed figures. The Fireworks share of
the routed arms is still a list-rate estimate. Method, per-arm token totals and
limits are in
[`evidence/billing-reconciliation-2026-10-28.md`](evidence/billing-reconciliation-2026-10-28.md).

Keep raw provider receipts, request IDs and task content private. Publish only
reviewed, sanitized aggregates, configuration without credentials, and
permission-cleared evidence. See [WEEKLY.md](WEEKLY.md) for the scheduled rerun and audit procedure. Weekly
paid reruns and the required independent external reproduction must not be
claimed until actually completed.

Metrum AI
