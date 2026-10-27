<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Paired case-study evidence controls

This is **partial infrastructure for #423**, not a live case study, billing
receipt, quality certification or savings claim. It summarizes sanitized paired
results for coding-agent, support-agent and document-Q&A workloads. It does not
execute a model or the verifier. An operator must independently validate the
workload, verifier, usage exports and provider billing evidence before making a
claim. Failed attempts must not be removed from the planned denominator.

Run the offline synthetic tests:

```sh
python3 -m unittest discover -s examples/case-studies/common -p 'test_*.py' -v
```

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

Keep raw provider receipts, request IDs and task content private. Publish only
reviewed, sanitized aggregates, configuration without credentials, and
permission-cleared evidence. This partial utility does not yet implement the
three agent workloads, the weekly paid rerun, or an external rerun.

Metrum AI
