---
title: Admin Browser Reports
doc_type: howto
---

# Admin Browser Reports

Admin browser reports are an authenticated operational surface for usage, performance, cost, cache, fallback, and diagnostic drilldown. They are disabled by default and served separately from public `/docs/`. The browser dashboard uses Metrum branding, local embedded assets, a dark operational theme, and an admin-only build-version chip for authorized administrators.

For evaluations, this surface lets operators inspect whether the router reduced
cost, preserved workload outcomes, isolated access, explained upstream choices,
and produced enough sanitized evidence for cost allocation and triage.

For health checks, metrics, log dimensions, and alerting, see [Observability](./observability). For incident triage by request ID, see [Request Troubleshooting](../troubleshooting/requests).

## Access Model

Browser identity can be HTTP Basic under `server.admin_auth.basic` or OIDC sessions under `server.admin_auth.oidc`. Authorization is configured under `server.admin_auth.authorization`; every `/admin/reports/*` page, API, export, and drilldown route requires an allow decision for object `admin:reports`. Aggregate pages/APIs use action `read`, Markdown export uses `export`, and request detail/evidence uses `drilldown`. Security access report APIs additionally require `admin:security_reports` so access metadata can be restricted more tightly than cost and performance reports.

```yaml
server:
  admin_auth:
    basic:
      enabled: true
      users:
        - username: admin
          password_hash_env: SMART_ROUTER_ADMIN_PASSWORD_HASH
          subject: basic:admin
          domain: example/prod
    authorization:
      enabled: true
      source: static
      policy:
        - g, basic:admin, reports_admin, example/prod
        - g, user:alice@example.com, reports_admin, example/prod
        - p, reports_admin, example/prod, admin:reports, read|export|drilldown
        - p, reports_admin, example/prod, admin:security_reports, read|export
  admin_reports:
    enabled: true
    path_prefix: /admin/reports
    default_since: 24h
    max_range: 31d
    max_rows: 500
    export_markdown: true
    baselines:
      - id: gpt-5.5
        name: GPT-5.5
        input_price_per_million_usd: 5.00
        output_price_per_million_usd: 30.00
        notes: Keep pricing source and update-date evidence in config.example.yaml.
      - id: claude-opus-4.8
        name: Claude Opus 4.8
        input_price_per_million_usd: 5.00
        output_price_per_million_usd: 25.00
        notes: Keep pricing source and update-date evidence in config.example.yaml.
    security:
      enabled: true
      retention_days: 90
  client_ip:
    trusted_proxy_cidrs:
      - 10.0.0.0/8
    header_order:
      - X-Forwarded-For
      - X-Real-IP
    store_ip: true
```

`path_prefix` must be `/admin/reports` or a child path such as
`/admin/reports/team`. The router redirects the bare prefix to the same path
with a trailing slash, so validation rejects any prefix that could point off
the router: protocol-relative (`//host`) or backslash (`/\host`) forms, and
any value with a backslash, `%`, `?`, `#`, `:` or a control character.

Ordinary router caller tokens receive `403 reports-forbidden`. Missing or invalid Basic credentials or missing/invalid OIDC sessions receive `401`.

## What It Shows

### Data Migrations

The **Operations / Data migrations** view is a read-only, authenticated view of the checked-in migration contract and its safe scalar ledger state. It shows scope, schema/data version, compatibility, effective pending/applied/failed/running/in-progress state, release, execution/maintenance/lock/timeout class, rollback class, bounded duration, data-job key and state, safe progress totals, and safe error class. A bound data job is projected only after its schema ledger is applied: missing, pending, paused, and cancelled jobs remain pending; running jobs are in progress; failed jobs remain failed; and only validated jobs are verified. A failed or running schema ledger remains authoritative even if a job is absent or reports a conflicting state. It deliberately does not expose SQL, DSNs, raw configuration, credentials, tokens, hashes, prompts, images, tool payloads, or database rows.

The browser view has no apply, retry, pause, resume, restore, or package-rollback controls. Operators use the separately authorized non-serving `router-migrate` deployment job and the release-specific backup/recovery procedure. A report administrator needs the existing `admin:reports` `read` grant; ordinary caller tokens still receive `403 reports-forbidden`.

The browser UI displays requests, errors, input tokens, output tokens, total tokens, input cost, output cost, total cost, savings, latency, TTFB, upstream output/total throughput, downstream write output/total throughput, cache hit/miss/bypass, attempts, fallbacks, provider/model/dialect groups, requested model, model-group usage by user, public token IDs, caller ID/user/project/environment, caller IP when stored, quota/caller-token states, traffic-shaping decisions, routing strategy summaries, dynamic-score signal/score/threshold buckets, max-token and input-token buckets, request-shape and translated-shape buckets, admission reasons, policy execution outcomes/errors, fallback transition reasons, contract buckets, target validation buckets, capability usage, troubleshooting buckets, anomaly signals, status codes, expensive requests, client breakdowns, project cost allocation, provider catalog/validation status, retention/rollup status, and recent safe request rows. Request drilldown and evidence bundles join the relational usage, attempt, trace-event, terminal-error, traffic-shaping, request-shape, translation-shape, sanitized upstream-error, cost, and decision-telemetry rows by request ID.

Admin report APIs accept safe request-shape filters such as inbound dialect, stream flag, tool-choice mode, tool-count bucket, request-bytes bucket, estimated-input-token bucket, output-cap bucket, reasoning presence, multimodal presence, request-shape fingerprint, and tool-schema fingerprint. These filters help compare successful and failed requests to the same provider/model/dialect when a small smoke test passes but a real agent request receives an upstream rejection.

Reasoning proof uses the same reports and drilldowns. For each smoke request ID, request evidence should show the selected provider/model/dialect, `fallback_used` false unless fallback was under test, and a translation-shape `translated_reasoning_control` of `reasoning_effort`, `reasoning`, or `thinking` for Chat, Responses, or Anthropic Messages respectively.

Bridge proof should also include `bridge_direction`. Chat-to-Responses reasoning evidence should show inbound Chat, target Responses, `bridge_direction = chat_to_responses`, and `translated_reasoning_control = reasoning`. Responses-to-Chat reasoning should show a bounded filter reason such as `responses-to-chat-reasoning` unless that exact target has validated `responses_to_chat.reasoning`; do not infer bridge reasoning support from same-dialect reasoning metadata alone.

Responses do not include raw router tokens, token hashes, provider keys, raw prompts, raw images, raw tool outputs, full config values, or unsanitized upstream bodies.

## Buyer And Operator Questions

| Question | Report evidence |
|---|---|
| Which teams are driving spend or savings? | Savings by user, project, public token ID, model group, and provider/model; project cost allocation; client breakdown. |
| Which model groups are used by each cohort? | Model groups by user/project/key and usage by requested model group. |
| Which upstream/provider models are actually serving traffic? | Provider/model mix, active target metadata, validation status, attempts, fallbacks, and errors. |
| Are quotas and rate limits sized correctly? | Quotas/budgets, troubleshooting buckets, traffic-shaping buckets, max-token and input-token buckets, TPM/RPM/concurrency signals. |
| Should an operator increase burst, change queueing, slow a client, or route around a provider? | Traffic tuning advisor, traffic-shaping tabs, provider capacity shaping, upstream failures, and request-shape failures. |
| Why was a request expensive or slow? | Expensive requests, request drilldown, downstream user performance, upstream endpoint performance, latency and throughput. |
| Is access governed? | Security access events, ordinary-caller `403 reports-forbidden`, metrics-admin isolation, public token IDs, key state, and caller/project dimensions. |

## Security Access Reports

When `server.admin_reports.security.enabled: true`, the router persists safe scalar access events in the usage database for authorized API calls, missing or invalid caller-token attempts, caller authorization failures, model access denials, metrics/report/content authorization failures, Basic admin auth checks, and admin report reads/exports. The Security tab shows event outcome, reason, surface, safe caller/admin identity, public token ID, client, trusted-proxy-derived IP metadata, and input/output/total token counts where a completed model request reported usage.

Security reports use the same browser shell and chart contract as usage reports, but their API routes require `admin:security_reports` `read` or `export`. CSV export is available at `/admin/reports/security/export.csv` and includes only safe scalar fields with spreadsheet formula-leading values neutralized.

The Overview API also includes security trend charts when `server.admin_reports.security.enabled: true` and the authenticated administrator is authorized for `admin:security_reports` `read`. These charts are omitted, not replaced by an error, for administrators who can read usage reports but cannot read security reports. The trend charts use the selected Overview time window and the same domain scope as security report APIs. They include safe scalar counts only, such as `security_events_over_time` by outcome and `security_denials_over_time` by surface.

Report APIs are scoped to the authenticated admin's policy domain by default. A subject authorized in `example/prod` sees usage rows, request lists, request detail, Markdown export, and security events for caller project `example` and environment `prod`; cross-domain request IDs return `404`. Deployment-wide report administrators require an explicit `*` policy domain.

Configure `server.client_ip.trusted_proxy_cidrs` before relying on IP-based security triage. The router ignores `X-Forwarded-For` and `X-Real-IP` unless the direct remote address is in a trusted proxy CIDR. If no trusted proxy matches, reports use the direct remote address. Set `store_ip: false` only when deployment policy forbids IP storage; the report will then omit IP addresses and keep source/classification metadata best-effort.

## Shared Usability

The browser report shell provides shared controls for every tab:

- grouped left-sidebar navigation on desktop, with a header drawer on narrow screens;
- collapsible navigation groups with preferences stored in browser `localStorage`;
- a two-row header with the Metrum brand, title, and safe version chips on top, followed by a full-width global filter bar with the `Since` time range, Markdown export, mobile `Sections` drawer, and an accessible `Filters` disclosure for global investigation filters;
- global filters for caller ID, caller user, public token ID, caller IP, caller project, caller environment, requested model, resolved group, provider, target model, dialect, and client; these remain active while switching tabs;
- per-tab filter panels for tab-local controls such as baseline, status, cache state, sort, direction, and traffic-shaping bucket/scope; each report shows only the controls that apply to that tab;
- a `Rows` select in the table toolbar for the URL-backed server row limit;
- selected tab, active filters, server sort, direction, limit, and cursor stored in shareable URL query parameters;
- clearly labeled quick filtering across the returned page or returned top-N rows;
- sortable table headers with click-to-sort controls and active ▲/▼ indicators; cursor-paged request and security tabs refetch with server-supported sort keys, while aggregate tabs sort the returned top-N rows;
- bounded server page-size selection for 25, 50, 100, or 250 rows when allowed by the deployment's `max_rows`;
- manual refresh with last-refresh state;
- copy buttons for identifiers such as public token IDs, groups, providers, and request IDs;
- request-ID drilldown from request rows;
- CSV export labeled as current page, top-N rows, or visible rows with spreadsheet formula-leading values neutralized;
- consistent chart, table, loading, empty, and error states.

Every report header includes a short purpose statement and an expandable How to use this report panel. The panel explains the page purpose, data semantics, caveats, common filters, key columns, and related reports. Desktop navigation exposes concise tab descriptions through hover/focus tooltips, while the mobile `Sections` drawer shows descriptions inline so the help is not hover-only.

These controls are presentation helpers over bounded authenticated APIs. They do not expose raw tokens, token hashes, provider keys, prompts, images, tool outputs, raw cookies, OIDC tokens, full config, raw spreadsheet formulas, or unsanitized upstream responses.

Tables use per-tab column schemas instead of first-row key discovery. Column order, labels, and units are stable for each tab, CSV export follows the same visible columns, Markdown export escapes raw HTML and active Markdown table-cell syntax, and duplicate compatibility aliases are suppressed when they carry the same value. For example, usage tabs show `Input tokens`, `Output tokens`, and `Total tokens`; they do not show both `tokens` and `totalTokens` when those fields are equivalent. Cost fields follow the same rule: input, image, output, total, baseline, savings, and upstream-billed values are labeled separately when present.

## API Pagination

Admin report API responses include a `pagination` object. Raw, event-like reports use cursor pagination when the router can page directly from indexed relational rows:

- `/admin/reports/api/requests`
- `/admin/reports/api/expensive-requests`
- `/admin/reports/api/security/events`

Example first page:

```text
/admin/reports/api/requests?since=24h&limit=50&sort=timeUtc&direction=desc
```

Example next page:

```text
/admin/reports/api/requests?since=24h&limit=50&sort=timeUtc&direction=desc&cursor=<next_cursor>
```

The response shape is:

```json
{
  "pagination": {
    "limit": 50,
    "returned": 50,
    "total_count": 1234,
    "has_more": true,
    "next_cursor": "opaque",
    "sort": "timeUtc",
    "direction": "desc",
    "mode": "cursor"
  }
}
```

Request sort keys are `timeUtc`, `costUsd`, `latencyMs`, `status`, and `requestId`. Security-event sort keys are `timeUtc`, `status`, `outcome`, `surface`, and `reason`. `limit` must be positive and no larger than `server.admin_reports.max_rows`; `direction` must be `asc` or `desc`. Cursors are opaque, signed, and bound to the endpoint, sort, and direction. Malformed, tampered, stale, or mismatched cursors return `400 invalid-report-filter`. Cursor pagination is domain-scoped the same way as the first page, so a subject authorized for `example/prod` cannot page into another project/environment.

Aggregate tabs such as overview, usage by key, provider/model mix, savings by user, traffic-shaping summaries, and routing-decision buckets remain ranked top-N summaries. Their metadata uses:

```json
{
  "pagination": {
    "limit": 50,
    "returned": 50,
    "total_count": null,
    "has_more": true,
    "sort": "requests",
    "direction": "desc",
    "mode": "top_n",
    "note": "Aggregate rows are top-N for the selected filters."
  }
}
```

The overview API computes full-window totals, hourly series, top-N token/group/provider/status breakdowns, and latest cache snapshot values in the usage database. It returns only a bounded recent request sample, so the summary cards and charts can cover the whole selected window while the request table remains limited by `limit`. Savings and direct scalar aggregate tabs also compute their grouped rows, ranking, and full-window summary totals in the usage database, then return a bounded top-N response to the browser. For those aggregate reports, browser search and table sorting operate over the returned top-N rows. Use the aggregate tabs to identify a dimension, then drill into `/admin/reports/api/requests` or `/admin/reports/api/security/events` with matching filters when you need stable page-by-page review.

CSV export from the browser exports the currently returned table scope: current cursor page for request/security detail, returned top-N rows for aggregate tabs, or visible rows for legacy unpaged responses. Markdown detail export is a bounded current-filter operational report: it uses the requested `limit` to include the most recent matching request rows and adds an export-scope note when more rows matched the selected window. Markdown summary export is available at `/admin/reports/export.md?mode=summary`; it renders full-window SQL totals and bounded top-N aggregate sections without materializing every matching raw request row.

The browser table footer mirrors this distinction. Cursor-paged reports show ranges such as `Showing 51-100 of 1,234`, expose first/previous/next controls, and keep the opaque `cursor` in the URL for sharing the current page. Previous is available after in-session forward navigation or when the API supplies a previous cursor. Aggregate reports show labels such as `Showing top 50 rows` or `Showing top 50 rows, more available` and do not show cursor navigation controls because they are bounded ranked summaries, not page 1 of every possible provider, key, or bucket.

## Request Evidence

When a caller provides a request ID, use the request evidence API to open a safe diagnostic bundle:

```text
/admin/reports/api/request-evidence?request_id=<request_id>
```

The same bundle is available through path-style drilldown links at `/admin/reports/api/request/<request_id>`. Both endpoints require `admin:reports` `drilldown`, are scoped to the admin's policy domain, and use `Cache-Control: no-store`. Ordinary application caller tokens receive `403 reports-forbidden`.

The request evidence bundle is assembled from relational usage and diagnostic rows. It can include request ID, caller/project/environment/client labels, public token ID, requested model and resolved model group, selected upstream/provider model and dialect, stored request-time token and cost fields, upstream-reported billed cost fields, latency and throughput measurements, quota/caller-token/cache state, traffic-shaping state, target candidate and filter summaries, attempts, sanitized upstream error details, trace events, request-shape and translation-shape buckets, and decision telemetry.

Use `diagnosticCompleteness`, `diagnosticCompletenessScore`, and `evidenceSections` to understand gaps. Sections are marked `present`, `not_applicable`, or `missing`; bundle-level status is `complete`, `partial_expected`, `partial_missing`, or `minimal`. A `missing` section on a failed request usually means diagnostics were disabled for that path or a telemetry regression needs investigation.

Evidence APIs must not return raw prompts, raw responses, raw image URLs or payloads, raw tool schemas, raw tool outputs, provider API keys, router bearer tokens, token hashes, full upstream headers, unsanitized upstream bodies, full config, cookies, OIDC tokens, or spreadsheet-active values.

## Navigation

Desktop report users navigate with a fixed left sidebar labeled `Report sections`. The sidebar groups reports by operator intent: Overview, Usage, Savings, Performance, Traffic shaping, Routing decisions, Provider catalog, Security, Request drilldown, and System status. The report header stacks the brand row above the global filter row so filters use the same full content width as the reports below. Filters, Markdown export, active tab URL, CSV export, and request drilldown stay in the main content area.

Each group header is keyboard-focusable and exposes expanded/collapsed state to assistive technology. Collapsed groups are remembered in browser `localStorage` under a versioned UI key so an administrator's browser keeps the same sidebar density after reloads. The preference is local presentation state only; it is not sent to report APIs, stored in the router, or included in shareable URLs.

On narrow screens the sidebar is hidden by default and opens from the `Sections` button in the header. The drawer uses the same grouped navigation and preserves deep links such as `/admin/reports/?tab=requests&since=24h`. The URL `tab` parameter remains the source of truth for the active report, and global or tab-specific filters continue to use one combined URL such as `/admin/reports/?tab=savings-by-key&since=6d&caller_user=alice&baseline=gpt-5.5`, so bookmarked links, filters, exports, and policy-based authorization behavior are unchanged.

Per-tab panels are mounted above the metrics, charts, and table. Savings tabs expose `Baseline`, `Sort`, and `Direction`; cache/error/performance tabs expose `Status`, `Cache`, `Sort`, and `Direction`; traffic-shaping tabs expose `Shape bucket`, `Shape scope`, `Sort`, and `Direction`; and diagnostic/status tabs expose sorting controls where a natural table sort applies. `Reset filters` clears only the active tab's local filters and never clears global investigation filters such as caller user or provider.

## Report Tabs

The current browser surface includes these grouped reports:

- Overview: SQL-backed full-window usage, cost, baseline/savings when a configured or custom baseline is available, latency/TTFB/upstream/downstream duration, throughput, error/fallback rates, cache/hit-rate, and provider trends, plus a bounded recent request sample.
- Usage: model groups, providers, caller tokens, model groups by user, token usage, caller usage, requested models, provider/model mix, clients, projects, and capability usage. The Provider/model tab reports actual upstream/provider model usage, input/output/total tokens, input/output/total cost, latency, and throughput. It does not include baseline or savings fields by default; use the Savings tabs when a hypothetical baseline comparison is needed.
- Savings: actual request-time cost compared with selected source-dated baseline prices, plus savings by user, caller token, model group, project, and provider/model.
- Performance: latency and throughput, errors and fallbacks, upstream failures, request-shape failures, fallback health, user/client impact, and cache. Upstream failures group provider/model/dialect/status/error-class spikes with sanitized provider error code/param/message categories. Request-shape failures compare successful and failed traffic by safe shape buckets and non-reversible request/tool-schema fingerprints. Fallback health shows whether retry/fallback paths recovered or still ended in caller-visible errors, including older rows that only have `fallback_used` and `attempts`. User impact ranks affected users and clients by error rate and latency.
- Traffic shaping: overview, by user, by key, by client, by model group, provider capacity shaping, adaptive upstream backoff, and Traffic tuning advisor. The shaping tabs distinguish caller/server shaping decisions such as `rejected` and `queued`, show limiting scope/bucket, retry-after, queue wait, estimated input tokens, reserved output tokens, and total reserved tokens. Provider shaping and backoff tabs show provider/model/target admission, skipped targets, cooldown starts, upstream 429/quota backoff reasons, and successful route-around counts. The Traffic tuning advisor combines those signals with upstream attempt status, fallback, latency, and cancellation counts from grouped SQL feature rows, not raw request-row materialization, to recommend `enable_queue`, `increase_queue_depth`, `increase_caller_burst`, `disable_queue_for_latency_sensitive_client`, `investigate_provider_429_capacity`, `route_around_incompatible_target`, or `no_shaping_change_indicated`.
- Routing decisions: routing decisions, dynamic-score enabled signals, score buckets, threshold buckets, max-token buckets, input-token buckets, admission reasons, and troubleshooting buckets for quota, TPM/RPM or rate-limit, concurrency, max-token/context, upstream quota/billing, key-state, cache, fallback, multi-attempt, and HTTP error classes inferred from safe stored request fields.

Example shaping URLs:

```text
/admin/reports/?tab=traffic-shaping-overview&since=24h&traffic_shape_scope=caller
/admin/reports/?tab=provider-capacity-shaping&since=24h&provider=openai
/admin/reports/?tab=adaptive-upstream-backoff&since=24h
/admin/reports/?tab=traffic-tuning-advisor&since=24h&caller_user=alice
```

Use caller shaping tabs when the caller received `429 traffic-shaped` or had queued requests. Use provider capacity shaping when the caller received `503 upstream-capacity-throttled` or when a target was skipped and another target succeeded. Use adaptive backoff when a prior upstream `429` or quota/billing response should temporarily protect that provider/model/target.
Use Traffic tuning advisor before changing production shaping values. A row with `route_around_incompatible_target` means the router admitted the traffic but upstream 400/request-shape failures dominate; increase neither burst nor queue depth until the incompatible target, tool/modality metadata, or request-shape routing is fixed. A row with `enable_queue`, `increase_queue_depth`, or `increase_caller_burst` means router-side shaping evidence exists and the listed config fields should be reviewed. A row with `disable_queue_for_latency_sensitive_client` means queue wait and client cancellations point toward fail-fast behavior. A row with `investigate_provider_429_capacity` means shared provider/model capacity or upstream backoff should be tuned before per-user burst.

- Provider catalog: provider catalog status from safe runtime configuration metadata, target validation, contract buckets, and contract workloads. Catalog status separates `catalog` rows from `active_target` rows so per-group target overrides for modalities, tools, pricing, max-token behavior, OpenAI-compatible encoding metadata, and validation are visible without changing catalog metadata. Active target rows also show effective provider-skin eligibility with `activeEligibilitySkin`, `effectiveToolSupport`, `inactiveToolSupport`, `effectiveStructuredOutputs`, `effectiveReasoning`, and `effectiveImageInput`; use these fields to explain why Chat, Responses, and Anthropic clients can see different eligible pools in the same group. This endpoint does not expose provider API keys, headers, full config, or private deployment files. Contract reports show contract-present/pass/fail, failure reasons, and deployment-defined workload labels without exposing request content.
- Security: security access events for authorized and unauthorized access paths when enabled.
- Request drilldown: expensive requests, recent requests and request-ID drilldown, and deterministic anomalies such as errors, fallbacks, multi-attempt requests, slow requests, expensive requests, non-ok quota states, and abnormal key states such as disabled, revoked, expired, or suspended. This is not machine-learning anomaly detection; normal active key state is not anomalous, and baseline/savings fields are reserved for savings reports.
- System status: quotas/budgets plus retention and rollup status from existing usage DB status tables, including the latest retention job, per-table candidate/held/eligible/blocked/deleted counts, and recent hourly/daily/monthly rollup runs. Retention status is read-only; retention execution and rollup generation remain operator-controlled workflows.

Recent request rows include visible columns for time, request ID, caller ID, caller IP, public token ID, caller user/project/environment, client, requested model, resolved model group, provider, model, dialect, status, cache state, attempts, fallback flag, latency, input/output/total tokens, and stored total cost. Request drilldown also shows sanitized upstream error detail rows and safe request-shape / translated-shape telemetry when those diagnostics are enabled.

Use the related-report links in each help panel as the standard investigation path: errors lead to Requests and Provider/model, burst issues lead to Traffic shaping and Provider shaping, spend issues lead to Savings and Expensive requests, slow UX leads to Latency and Requests, access review leads to Security, and model-group quality review leads to Contracts, Validation, and Catalog status.

## Charts

Report API responses include a `charts` array with stable chart IDs, titles, X/Y axis labels, axis types, units, series names, semantic color keys, scalar points, generation timestamp, selected range when applicable, and active safe filters when applicable. Every browser report tab renders at least one aggregate chart when matching data exists. If a selected range or filter has no aggregate points, the tab shows an explicit no-chart-data state before the table empty state. Money is displayed as USD, token and request counts use compact notation where appropriate, latency uses milliseconds or seconds, throughput uses `tok/s`, and rates use percentages. Tables label totals explicitly as Total tokens and Total cost whenever input/output breakdowns are present.

The Overview tab expands the shared chart contract with stable IDs for requests/success/errors, actual cost, actual-vs-baseline cost and savings, savings rate, latency and request-phase durations, upstream/downstream throughput, errors and fallbacks, error/fallback rates, cache counts, cache hit rate, and provider token volume. Baseline and savings charts appear when the selected baseline or default configured baseline is available; actual spend still comes from stored request-time cost fields. The browser groups these charts for fast operational scanning instead of rendering one long undifferentiated grid: Health charts show request, error, and fallback behavior; Value charts show cost, baseline, and savings signals; Performance charts show latency, upstream duration, TTFB, and throughput; Operations charts show cache and security-event activity when the administrator is authorized to see those series; and Top groups charts show bounded provider, model, caller, status, or key categories. Security trend charts use SQL aggregates from security access events and appear only for administrators allowed to read security reports. Deployment-specific charts that do not match these groups remain visible in an Additional charts section. All Overview chart groups follow the selected filters and time window.

Every chart is backed by the same safe aggregate fields shown in tables and Markdown export. The catalog-status tab charts source, validation, and active-target provider counts. The retention-status tab charts retention eligibility/deletion counts and recent rollup status/type counts. Chart payloads contain scalar aggregate points only; they do not include prompts, image payloads, tool schemas or outputs, tokens, token hashes, provider keys, full config, or raw upstream bodies.

Category charts shorten long bucket labels into compact axis labels and render a Bucket legend beneath the chart that maps each short label back to the full value. Hovering a bar shows the full original label and value in a dark-theme tooltip that dismisses on scroll, blur, resize, outside click, `Escape`, or page visibility changes. The table, CSV export, Markdown export, and JSON API response continue to carry the full labels. The legend is expanded for short bucket lists and collapsible for denser charts.

The shared label encoder applies to every category chart, including Provider/model mix, Savings by user/key/group/project/provider, Caller usage, Requested models, Errors/fallbacks, Routing decisions, Validation, Capability usage, and similar aggregate tabs. Email labels use the local-part prefix, composite report buckets use the final bucket component, provider/model paths use the final path segment, long opaque labels are capped with an ellipsis, and collisions receive a numeric suffix.

Time-series charts use UTC epoch milliseconds for Chart.js spacing while preserving the original RFC3339Nano timestamp in the API payload, table/export paths, and hover tooltip. Axis ticks adapt to the visible range so short windows use hourly `HH:mm` labels, multi-day windows use `MMM d`, multi-month windows use weekly `MMM d`, longer windows use `MMM yyyy`, and multi-year windows use `yyyy`. Hovering a point shows the full timestamp with an explicit `UTC` suffix, while CSV and Markdown export continue to emit the full timestamp.

Product docs may show anonymized Chart.js examples built from safe report fixtures. The `router-usage-report` CLI remains focused on stable Markdown tables, relational rollups, and machine-reviewable metrics unless a deployment explicitly adds a chart export workflow outside the router binary.

## Savings

The Savings tab compares stored actual request cost against a selected hypothetical baseline. Actual cost is always summed from request-time stored cost fields; it is not recalculated from current provider configuration. Baseline cost is calculated from stored input and output token counts:

```text
baseline_cost_usd =
  input_tokens / 1_000_000 * baseline_input_price_per_million_usd +
  output_tokens / 1_000_000 * baseline_output_price_per_million_usd
```

Built-in baselines are configured under `server.admin_reports.baselines` with source URL, source date, input USD/M, output USD/M, and notes. The default built-ins were source-checked on June 25, 2026: GPT-5.5 from OpenAI API pricing at $5.00/M input and $30.00/M output, and Claude Opus 4.8 from Anthropic Claude pricing at $5.00/M input and $25.00/M output. Baseline prices are externally maintained by providers and should be revalidated when producing contractual or customer-facing savings claims.

Administrators can also enter a custom baseline for the current browser session. Custom values are validated as finite nonnegative USD-per-million-token rates and are not persisted by the router.

Savings charts mirror the table semantics. The top-level Savings tab charts actual vs baseline cost over time, savings USD over time, and savings rate over time. The savings breakdown tabs for user, key, model group, project, and provider/model show the same cost comparison, savings USD, and savings-rate charts bucketed by the selected dimension, plus the standard request-count chart. Use the charts to identify the largest savings or negative-savings buckets first, then sort the table by `Savings` or export the matching CSV for exact review.

Savings breakdown tables are savings-first. They show the breakdown dimension, optional secondary dimension such as environment or dialect, requests, input/output/total tokens, actual cost, baseline cost, savings, savings rate, and average cost per request. Latency, throughput, cache, fallback, and detailed provider-mix columns stay on the related Usage and Performance tabs so savings attribution does not require horizontal scanning. A typical workflow is: choose the baseline, read the charts, sort the table by savings, export the visible columns when needed, then drill into Usage, Latency, Errors/fallbacks, or Request drilldown for operational causes.

Negative savings means the selected baseline would have been cheaper than the routed upstream/provider model mix for that bucket. Treat it as a routing, quality, entitlement, or baseline-selection investigation signal; do not infer provider failure from negative savings alone.

Actual router cost is summed from stored request-time input, output, image, calculated, and upstream-reported billed cost fields. Reports must not reprice historical actuals from current config. Older rows that predate a cost field can still be counted for usage, latency, or token volume, but savings and cost-allocation views should label the missing cost coverage instead of treating it as zero spend.

## Version Status

The browser shell calls `/admin/reports/api/version` after load and displays a compact version chip with the router version, build date, and license compile mode when available. The endpoint uses the same admin reports authentication, license, and `admin:reports` `read` authorization as aggregate report APIs. It does not require a usage database and does not expose raw router tokens, token hashes, provider keys, full config, prompts, images, tool outputs, cookies, or OIDC tokens.

Use it as a quick operator check that the loaded browser bundle is talking to the expected router binary. The public `/version` endpoint remains available for deployment health checks; `/admin/reports/api/version` exists so authenticated report users can see build metadata without leaving the admin report surface.

## Embedded Assets

The admin HTML, CSS, JavaScript, Metrum logo, fonts, and local chart bundle are embedded in the router binary. The UI does not depend on external CDNs or runtime access to the Metrum website. Charts are assistive; the same data is available in tables and Markdown export.

Admin pages and APIs send no-store cache headers. Static admin assets may use private cache headers and contain no report data.

The admin report shell uses a dark operational theme aligned with the embedded Metrum assets. Theme presentation does not change server-side reporting data or authorization policy.

## Smoke Test

```bash
curl -i -u admin:replace-with-password \
  "$ROUTER_BASE_URL/admin/reports/api/summary?since=24h"

curl -i -u admin:replace-with-password \
  "$ROUTER_BASE_URL/admin/reports/api/requests?since=24h&limit=50&caller_id=example-caller&provider=openrouter&status=200&cache=miss&sort=timeUtc&direction=desc"

curl -i -u admin:replace-with-password \
  "$ROUTER_BASE_URL/admin/reports/api/provider-catalog-status"

curl -i -u admin:replace-with-password \
  "$ROUTER_BASE_URL/admin/reports/api/retention-status"

curl -i -u admin:replace-with-password \
  "$ROUTER_BASE_URL/admin/reports/api/version"
```

Expected for an authorized subject: `200` JSON with `summary`, `series`, `charts`, grouped tables, request rows where applicable, and `pagination` metadata. Request and security-event APIs return `mode: "cursor"` and `next_cursor` when another page exists. Aggregate APIs return `mode: "top_n"`. The catalog-status response includes `groupSummary` plus active target fields such as `activeEligibilitySkin`, `effectiveToolSupport`, and `inactiveToolSupport`; the catalog-status and retention-status responses also include `charts` so those tabs are not table-only. The version endpoint returns safe build fields such as `version`, `build_date`, runtime platform, and `license_compile_mode`.

For OpenAI-compatible upstream failures, start with Request shape / Upstream failures and filter by status or error class. A provider 403 mentioning `store` usually means the resolved target should leave `force_store_false` unset; a provider 400 mentioning `max_tokens` may mean the target needs `output_token_field: max_completion_tokens`. Then check Provider catalog status for the active target row and confirm `forceStoreFalse`, `outputTokenField`, tool support, effective tool support, inactive tool support, dialect, active groups, and any `request_shape_support` metadata. Use request-size filters such as `request_bytes_bucket` when large agent payloads could be a separate issue.

For provider access failures, filter Upstream failures or request evidence by `upstream_auth_failed`, `upstream_access_denied`, `upstream_entitlement_failed`, or `upstream_model_access_denied`. These rows point to provider credentials, account entitlement, region/project restrictions, policy/privacy blocks, or model access, not caller TPM/RPM. Fallback health shows whether another eligible target recovered the request; a terminal `upstream-access-denied` means every attempted target failed with provider access classes.

For large agent-payload triage, compare successful and failed rows for the same provider, model, dialect, client, and model group. Start with `request_bytes_bucket`, `input_tokens_bucket`, `output_cap_bucket`, `tool_count_bucket`, request-shape fingerprint, tool-schema fingerprint, and upstream status. A pattern where small smokes pass but large Cursor, Codex, Claude Code, opencode, or similar agent sessions fail should be reproduced with a sanitized synthetic fixture. Run that fixture through a deployment-owned smoke route with safe caller access and compare the emitted request IDs in reports. Missing caller or report access is a prerequisite gap to record. If the direct upstream passes and the router path fails, inspect translation fields and target metadata. If both direct and router paths fail at the same shape, set explicit request-shape limits or keep the target in a smoke group until the provider/account supports that workload.

OIDC deployments should first complete `/admin/auth/login`, then call the same report URL with the browser session cookie. A valid OIDC session without matching authorization policy receives `403 reports-forbidden`.

Expected for an ordinary router token:

```bash
curl -i -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/admin/reports/api/summary?since=24h"
```

Response: `403 reports-forbidden`.
