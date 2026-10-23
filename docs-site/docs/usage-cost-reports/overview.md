---
title: Usage, Cost, And Reports
doc_type: explanation
---

# Usage, Cost, And Reports

Metrum AI Router records usage as operational evidence, not just a billing summary. Reports should explain who used the gateway, which model groups and upstream/provider models served requests, how much traffic cost at request time, where savings came from, and which provider/model routes were slow or unreliable.

## What The Router Records

Each completed request stores safe scalar fields such as caller identity, public token ID, requested model group, selected upstream/provider model, optional deployment-declared target region, token counts, cache status, latency, throughput, fallback behavior, terminal status, and request-time cost values. After a successful fallback, the parent usage row attributes the serving target, its region label, and its request-time prices; failed hops remain on attempt rows. Historical cost reports use stored request-time values instead of recalculating old usage from current provider pricing. A region label is diagnostics metadata, not proof that residency policy was enforced.

Diagnostics use relational child rows for attempts, trace events, errors, and decision telemetry. They do not store raw prompts, images, tool outputs, provider keys, raw router tokens, token hashes, or full runtime configuration. Usage and decision-telemetry reports explain past decisions; they do not drive `dynamic_score` selection on the hot path.

## Usage and pricing in v4.3.0

Usage provenance is separate from token count: `reported` means upstream usage,
`partial` means upstream usage before a committed stream failed, `estimated`
means router estimation, and `none` means no inference usage settled. Historical
empty values are unknown, not evidence of reported usage. Request-list filtering
accepts `usage_source`; request details expose `usageSource`. Overview and cost
summaries expose `estimatedRequests`, `estimatedInputTokens`,
`estimatedOutputTokens`, and `estimatedTokenShare` (a percentage).

Missing streamed usage is estimated from observed text, not the requested output
cap. This is not an exact tokenizer for tool arguments or hidden reasoning.
Prometheus `metrum_ai_router_input_tokens_total`,
`metrum_ai_router_output_tokens_total`, and `metrum_ai_router_tokens_total`
include a bounded `usage_source` label; sum across it for aggregate dashboards.

Gemini `thoughtsTokenCount` is included in billable output alongside visible
candidate tokens. Reasoning is a subset of output: never add reasoning tokens
a second time to output, total tokens or cost. A reported zero differs from
missing reasoning evidence.

Anthropic canonical input includes uncached input, cache reads and cache writes.
`cached_input_tokens` is cache reads; `cache_write_input_tokens` records writes.
Optional `cached_input_price_per_million_usd` and
`cache_write_price_per_million_usd` price those components separately; absent
prices fall back to the base input price. Missing write pricing emits
`missing-cache-write-pricing-metadata`. One write price applies to both cache
TTLs; configure pricing appropriate to the actual cache policy.

`cost_list_usd` retains list cost and `cost_billed_usd` is the charged cost.
Router response-cache hits bill zero and clear inherited provider cache evidence;
provider prompt-cache discounts are a different mechanism. Historical reports
use request-time prices, not today's catalog prices.

`/v1/messages/count_tokens` and its `/anthropic` alias proxy native Anthropic
counts. Other transports, including Bedrock, return heuristic estimates.
`X-Metrum-Usage-Source` distinguishes `reported` from `estimated`; native errors
are returned rather than hidden by estimates. Count events appear in request
reports as `requestKind=count_tokens`, with `usage_source=none` and zero tokens
and costs. They are excluded from inference aggregates, quotas and metrics.
Historical count charges cannot be identified reliably and are not rewritten.

For migration contracts, see [Upgrade Guide](../release-notes/upgrade-guide).
Implementation details and reproducible local checks are maintained in the
repository's [usage provenance](https://github.com/metrum-ai/router/blob/main/docs/usage-provenance.md),
[count-token guide](https://github.com/metrum-ai/router/blob/main/docs/count-tokens.md),
and [usage database design](https://github.com/metrum-ai/router/blob/main/docs/USAGE_DB_DESIGN.md).

## Report Questions

| Question | Start with |
|---|---|
| Who used the router and which public token ID was responsible? | Usage by user, caller, project, public token ID, and model group. |
| Which upstreams are most used? | Provider/model mix and model-group usage reports. |
| Which upstreams are slow? | Latency, TTFB, upstream throughput, downstream throughput, and attempt/fallback reports. |
| Where did savings come from? | Savings reports using stored router actual cost and source-dated baseline assumptions. |
| What failed? | Error, fallback, troubleshooting bucket, request evidence/drilldown, and terminal error reports. |
| Are limits sized correctly? | Quota, TPM/RPM, concurrency, traffic-shaping, max-token, and input-token reports. |

## Related Pages

- [Usage Reporting](../operations/usage-reporting)
- [Admin Browser Reports](../operations/admin-browser-reports)
- [Report Examples](../operations/report-examples)
- [Observability](../operations/observability)
- [Cost Governance](../evaluation/cost-governance)
- [Error Reference](../reference/errors)
