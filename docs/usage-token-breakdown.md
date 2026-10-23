<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reasoning, prompt caching and cost attribution

Metrum AI Router retains token breakdowns and reporting evidence in request
aggregates, daily rollups, hourly rollups and monthly billing aggregates.

## Tokens and coverage

Aggregate APIs (overview, model/group, provider, caller, project and time series)
include:

- `reasoningTokens`, `reasoningReportedRequests`;
- `cachedInputTokens`, `cachedInputReportedRequests` (provider cache reads);
- `cacheWriteInputTokens`, `cacheWriteReportedRequests`;
- `reasoningAttemptCount`, `reasoningSuccessfulAttemptCount`,
  `reasoningReportedAttemptCount`, preserving the existing per-request attempt
  telemetry definitions.

A non-NULL reported **zero** contributes to coverage. A missing field does not.
The overview shows reasoning/output and cached/input shares alongside the number
of reporting requests out of all requests. Partial coverage is not a complete
breakdown: unallocated input may include unreported caching, and output not
attributed to reasoning may include undisclosed reasoning.

Reasoning is already included in output. Cache reads and writes are already
included in normalized input. **Do not add these subsets to token totals.**
The Anthropic normalization and cache-write rates are supplied by the provider
adapters; this reporting layer does not estimate missing reasoning/cache fields.
These are logical recorded usage profiles, not counts of upstream operations:
a router response-cache hit can reuse a prior usage profile while its billed
cost is zero.

The existing raw-request filter `reasoning_present=true|false` retains its
request-shape semantics: requested reasoning or reasoning control added during
translation. It is **not** a filter for upstream reporting completeness.
`cache_present=true|false` selects reporting evidence, matching cache reads
**or** writes, including reported zero rather than only positive token values.
Both filters are exposed in the web UI. The CLI Markdown report includes token sums, coverage and attempt
counts by model group and provider/model.

## Billed cost and savings

The web cost composition partitions billed token cost into uncached input,
cache reads, cache writes, visible/unattributed output and reasoning output.
Images are separately billed and excluded from this token-cost bar. Reasoning
cost is a partition of output cost, not another charge. Free router response
cache hits contribute no billed cost. `costComponentReportedRequests` records
complete allocation evidence; a historical aggregate without it is shown as
unavailable, not as a measured zero-cost breakdown.

The corresponding fields are `uncachedInputCostUsd`, `cachedInputCostUsd`,
`cacheWriteCostUsd`, `visibleOutputCostUsd` and `reasoningOutputCostUsd`.

Savings reports separate three comparisons over the same token workload:

1. **Routing:** baseline list price minus the chosen model's undiscounted list
   price (`routingSavingsUsd`). A baseline must be selected to interpret this.
2. **Provider prompt cache:** undiscounted list price minus the provider's
   cache-adjusted list price (`promptCacheSavingsUsd`). Cache-write premiums can
   make this negative.
3. **Router response cache:** cache-adjusted provider list price minus billed
   cost (`responseCacheSavingsUsd`).

For complete evidence, these sum to baseline cost minus billed cost. This is a
counterfactual price decomposition: response-cache hits avoid the already
cache-adjusted provider list cost. It does not claim a second upstream cache
operation occurred on a router-cache hit. `savingsAttributionComplete=false`
means the split lacks complete component evidence; total recorded spend remains
available. Historic unknown attribution must not be interpreted as measured
routing/cache savings. Zero configured prices and missing prices retain the
existing report's missing-cost caveats; these are router-computed costs, not an
upstream invoice reconciliation.

## Prometheus

On the existing request label set (including usage provenance), `/metrics`
exports:

- `metrum_ai_router_reasoning_tokens_total`;
- `metrum_ai_router_cached_input_tokens_total`;
- `metrum_ai_router_cache_write_input_tokens_total`;
- `metrum_ai_router_cost_usd_total{component="input|cached_input|cache_write|output|image"}`.

The last notation lists allowed component values; each time series has one
value. `input` excludes cache read/write costs. `output` includes reasoning:
there is no second reasoning cost counter. Cost counters exclude free router
response-cache hits. Reasoning counters use the same persisted attempt-coverage
value as the request database, rather than independently recounting only the
last response.

## Reading retained history

The default admin source remains retained raw request rows. For overview,
model/group, provider, caller/project and compatible scalar aggregate views,
choose **History source: rollup** or append `history_source=rollup` to the API
query. Examples:

```text
/admin/reports/api/summary?since=48h&history_source=rollup
/admin/reports/api/requested-models?since=48h&history_source=rollup
/admin/reports/api/provider-model-mix?since=48h&history_source=rollup
```

This reads **only finalized daily rollups**, never a mixture of raw and rolled
rows. The response advertises `summary.historySource=finalized_daily_rollups`.
Relative windows are aligned to completed UTC days; explicit `from`/`to` must be
whole UTC-day boundaries. Charts have daily, not reconstructed hourly,
resolution. Draft rollups, the current/future day and days only partially
covered by a manually finalized window are excluded (including fractional
midnight boundaries). A run must be authoritatively finalized after its nominal
window closes; prematurely finalized future windows do not later become complete
just because wall time passes.
Authorization's project/environment scope applies identically to both sources.

Stored dimensional filters such as caller, project, environment, model,
provider, client and cache state are supported. Row-level presence, exact status,
IP, fingerprint and other unretained predicates are rejected with HTTP 400,
not silently ignored or approximated. Request drilldown, diagnostic reports,
the primary savings endpoint and server-side Markdown export remain raw-only;
compatible scalar savings-by-dimension endpoints can read finalized history.
Request details and live/latest cache snapshots cannot be reconstructed from
daily aggregates. Client-side table CSV export operates on the selected view.

## Migration and historical evidence

Migration **2026102303** adds sums, coverage, attempt counters and cost partitions
to the three aggregate tables. Existing aggregate rows default to **zero known
evidence**. Their old ordinary token/cost/request totals are preserved. Missing
historical NULL/zero evidence and cache-price attribution cannot be invented
after raw-row retention. The UI marks this lack of coverage, and the reader
never fabricates a negative response-cache saving from missing list-price data.

Draft windows can be regenerated while raw rows remain. Finalized windows stay
immutable; recomputing them requires a separately reviewed maintenance/restore
workflow, not an automatic historical rewrite. Rollback requires restoring a
pre-migration database snapshot.

## Local validation

`TestTokenBreakdownLocalhostE2E` starts a localhost router and deterministic
Chat/Anthropic upstreams, ingests reasoning plus cached-prefix traffic through
the real adapters, finalizes a completed UTC day, and compares raw, rolled and
post-retention API totals. It also checks filters, CLI columns and `/metrics`.
No provider credentials or paid inference are used. A real Claude Code
file/tool-result continuation check is available with:

```sh
USAGE_A_CLAUDE_E2E=1 go test ./internal/router \
  -run '^TestClaudeCodeCacheLocalhostContinuation$' -count=1 -v
```
