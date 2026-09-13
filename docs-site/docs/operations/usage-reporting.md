---
title: Usage Reporting
doc_type: howto
---

# Usage Reporting

Metrum AI Router records durable usage data for cost management, auditability, request troubleshooting, and model-group validation.

`router-usage-report` is an administrative CLI shipped in the release package. It is intended for platform administrators and is run from a secure server console, deployment host shell, or controlled admin workstation with access to the usage database. Deployments may also enable the authenticated browser reporting surface at `/admin/reports/`; it is separate from public `/docs/` and requires browser-admin authentication plus policy-based authorization.

For the broader health, metrics, logs, and request-ID workflow, see [Observability](./observability). For request triage, see [Troubleshooting](../troubleshooting/).

## Choose The Report Task

| Task | Start here | Outcome |
|---|---|---|
| Daily operations review | Generate a 24-hour Markdown report or open `/admin/reports/`. | Usage, errors, latency, throughput, cost, fallback, cache, and provider/model mix. |
| Cost allocation review | Use stored request-time cost fields and optional savings baselines. | User/project/key/provider spend without repricing historical requests from current config. |
| Model-group validation | Filter by model group, caller, client, provider, and workload window. | Outcome evidence for promotion, rollback, or weight changes. |
| One failed request | Use the request ID in request evidence or request drilldown. | Safe joined evidence across usage, attempts, traces, errors, shapes, and fallback. |
| Quota or capacity incident | Use traffic-shaping sections and the traffic tuning advisor. | Separate caller burst limits from upstream provider capacity or incompatible targets. |
| Retention and rollup review | Generate rollups, inspect finalized windows, then run retention status. | Aggregates remain available before raw detail rows become delete-eligible. |

## Generate A Markdown Report

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --since 24h \
  --out usage-24h.md
```

For an explicitly configured PostgreSQL deployment, substitute `--driver postgres --dsn "$ROUTER_USAGE_DB_DSN"`; do not use that connection mode as the generic default.

CLI-generated reports are Markdown files with structured tables for usage, cost, latency, throughput, downstream caller performance, and upstream endpoint performance. Instant values in the `Period UTC` bounds and `Per-Request Throughput` time column use fixed UTC RFC3339 milliseconds (`YYYY-MM-DDTHH:mm:ss.SSSZ`). Hour and day summary labels remain reporting buckets; browser Markdown exports, browser JSON APIs, and CSV exports retain their existing precision. The public docs include graphical Chart.js examples built from the same report dimensions.

## Investigate Traffic Shaping

Provider/model/target shared shaping writes safe scalar `request_upstream_shape_events` rows keyed by `request_id`. Use these rows with `request_usage`, `request_attempts`, and `request_trace_events` to explain why an otherwise eligible target was admitted, skipped, rejected, or placed into adaptive backoff after an upstream `429` or provider quota signal. The rows include scope, provider, model label, dialect, bucket, decision, bounded retry-after milliseconds, estimated input tokens, reserved output tokens, total reserved tokens, and safe backoff reason; they do not store prompts, images, raw upstream bodies, provider keys, router tokens, or token hashes.

## Investigate Request Shape

Request-shape diagnostics write one safe `request_shapes` row per routed request and one `request_translation_shapes` row per upstream attempt. These rows are independent of optional decision telemetry and are intended for upstream rejection triage.

Use these rows when a request works for one client or prompt size but fails for another. The safe evidence includes scalar counts, booleans, buckets, and non-reversible HMAC fingerprints for API shape, stream flag, message and tool counts, structured-output presence, reasoning controls, multimodal presence, request-size buckets, estimated input tokens, requested and translated output caps, translated provider/model/dialect/path, field strip or rewrite counts, and translation warnings. `request_translation_field_events` stores one bounded child row per safe field action using only allowlisted field names or `other`.

Bucket definitions are intentionally coarse: byte buckets are `none`, `1b-1kb`, `1kb-16kb`, `16kb-64kb`, `64kb-256kb`, `256kb-1mb`, and `gt-1mb`; reasoning budget buckets are `none`, `tiny`, `small`, `medium`, `large`, and `xlarge`; max-token and estimated-input-token buckets reuse the router's existing reporting buckets. Fingerprints are for comparing repeated request shapes inside a deployment without storing prompts or tool schemas.

When a caller reports `upstream-failed`, start with the downstream error contract from the [Error Reference](../reference/errors). `error.details.request_id` or `X-Request-Id` joins to reports, while `error.details.error_class`, `X-Router-Error-Class`, `error.details.upstream_status`, and `X-Upstream-Status` separate provider responses from router-side admission errors. `upstream_bad_request` is normally a provider/request-shape compatibility signal, not a reason to increase caller quota. Group the window by provider, model, dialect, upstream status, sanitized upstream code/type/param, request bytes bucket, tool-schema bucket, tool count, tool-choice mode, structured-output flag, reasoning flag, image count, output-cap bucket, request-shape fingerprint, and tool-schema fingerprint before changing route weights.

For large coding-agent payload failures, use only these safe buckets and fingerprints. Reproduce the dominant failed shape with a sanitized synthetic fixture against a staging or smoke model group and, when possible, a direct upstream smoke for the same provider/model/dialect. Do not export raw prompts, repository contents, screenshots, tool schemas, tool outputs, bearer tokens, token hashes, provider keys, raw upstream bodies, headers, or full config.

Traffic-shaping report sections appear when the selected window contains caller shaping or upstream shared-capacity events. They include:

- Traffic Shaping Summary;
- Traffic Shaping By Bucket, User / Project, Key, Client, and Model Group;
- Provider Capacity Shaping and Provider Capacity Shaping By Bucket;
- Adaptive Backoff;
- Before/After Investigation Helpers for upstream 429/quota attempts, fallbacks, and successful route-arounds.

Example filters:

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --since 24h \
  --traffic-shaped-only \
  --caller-user <owner-user>

router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --since 24h \
  --provider <provider-name> \
  --traffic-shape-bucket adaptive_backoff
```

Use `--traffic-shape-scope caller` for caller/server shaping, or scopes such as `provider`, `provider_model`, and `target` for upstream shared-capacity shaping.

Traffic tuning advisor output is available from the same CLI with usage database credentials only; it does not require provider keys or caller tokens:

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --since 24h \
  --traffic-tuning-advisor \
  --caller-user <owner-user>
```

The advisor reads existing usage, caller shaping, provider shaping, adaptive backoff, and upstream attempt data as grouped SQL feature rows. It emits recommendation classes, safe evidence counts, queue wait and retry-after percentiles, upstream 400/429/5xx/timeout counts, fallback rate, affected user/client counts, triggering threshold, and config fields to inspect. It does not change config automatically or materialize raw request rows in the report process.

Interpretation examples:

- User sees errors but all shaping buckets were admitted or absent: `route_around_incompatible_target` points to request-shape/provider compatibility, not burst or queue depth.
- User is queued and cancellations increase: `disable_queue_for_latency_sensitive_client` points to lower `queue.max_wait_ms` or fail-fast behavior.
- Provider 429s appear across users: `investigate_provider_429_capacity` points to provider/model shared shaping, adaptive backoff, route weights, or upstream entitlement.
- Large agent payloads fail on selected upstreams: use the advisor with Request-shape failures and Upstream failures, then adjust target eligibility or route around incompatible targets.

## Generate Usage Rollups

Administrators can generate bounded hourly, daily, or monthly rollups from stored request-time usage rows:

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --from 2026-06-14 \
  --to 2026-06-15 \
  --rollup \
  --rollup-type daily
```

Use `--rollup-type hourly` for recent operational trend reporting, `daily` for customer/project/public-token/provider cost allocation, and `monthly` for customer cost allocation and optional enterprise contract true-up summaries. The command writes relational scalar rows to `usage_rollup_runs`, the selected aggregate table (`usage_rollup_hourly`, `usage_rollup_daily`, or `usage_rollup_monthly_billing`), `usage_rollup_decision_buckets`, and `usage_rollup_audit_events` for the selected UTC `[from,to)` window. Rollup runs preserve source row count, source min/max request timestamp, deterministic source checksum, aggregate row counts, router package version and runtime build metadata, and generation/finalization timestamps.

These rollups support deployment governance and cost allocation. The tables are
not a payment or invoicing ledger.

Aggregate rows retain caller, token, client, model group, upstream provider/model/dialect, status class, stream/cache, image-input, PII-filter, contract, validation-status, and optional baseline dimensions alongside request/success/error counts, input/output/total token, input image count, input image token, request-time cost, upstream-reported cost, optional baseline cost/savings, latency, throughput, cache, fallback, and attempt measures. To persist a savings baseline, pass `--baseline-id`, `--baseline-name`, `--baseline-version`, `--baseline-input-price-per-million-usd`, and `--baseline-output-price-per-million-usd`.

Draft reruns replace the same draft run for that exact type/window. Use `--rollup-finalize` only after review; finalized rollup windows are immutable through the generator, and later rollup runs are rejected if they overlap an existing finalized window of the same type. Retention uses finalized daily rollup metadata to block or allow `usage_detail` delete eligibility and never mutates finalized rollup rows.

## Retention

`server.retention` is disabled by default and defaults to `dry_run: true`. Use status mode first from reviewed router config:

```bash
router-usage-report \
  --retention-status \
  --config /app/config/config.yaml
```

The command initializes scalar `retention_policy_versions` and `retention_policy_rules`, writes a `retention_jobs` row, and records per-table counts in `retention_job_table_results`. It counts candidates in diagnostic child tables, decision-telemetry child tables, `security_access_events`, content-capture rows, and `request_usage`, then subtracts active legal holds by data class, optional request ID, and timestamp range. Status mode never deletes rows.

After operators review counts, legal holds, database backups, and finalized rollup coverage, they can run one batch from the same reviewed config:

```bash
router-usage-report \
  --retention-run \
  --config /app/config/config.yaml
```

With `dry_run: false`, the current implementation deletes at most one configured batch per table for `usage_diagnostics`, governed `content_capture` (including selected header children), and `usage_detail`. Other data classes are counted and recorded as blocked. `usage_detail` candidate rows remain blocked unless finalized daily rollups continuously cover the candidate window. Archive/export, scheduler support, browser write workflows, and generic purge execution for decision telemetry and security events are future slices.

Use retention language carefully in operational reviews:

- raw operational rows are request, attempt, trace, error, decision, security event, and optional governed content-capture records;
- immutable billing/usage rollups are finalized daily aggregate rows generated from stored request-time facts;
- archived exports are customer-controlled artifacts and are not created by the current dry-run foundation;
- legal holds are scalar rows that block dry-run candidate counts by data class and timestamp range;
- purge jobs execute only the explicitly supported bounded classes after dry-run review;
- report and cost-allocation calculations should use stored request-time usage and cost fields, not current provider config repricing.

The usage and reporting schema remains purely relational: scalar columns plus normalized child tables. Do not add JSON/JSONB, array columns, serialized blobs, or packed multi-value text fields for structured reporting data.

## Browser Admin Reports

When `server.admin_reports.enabled: true`, administrators with an authorized Basic Auth subject or OIDC session subject can open `/admin/reports/` to inspect the same operational dimensions through a Metrum-branded browser dashboard. The router serves the HTML, CSS, JavaScript, Metrum logo, fonts, and local chart bundle from the binary; no CDN or external brand-asset host is required. Report pages and APIs use no-store cache headers, conservative CSP, bounded time ranges, and authorization policy checks for every page, API, export, and drilldown route.

The dashboard uses a dark operational theme and shows safe build metadata from `/admin/reports/api/version` for authorized report users.

Example policy shape:

```yaml
server:
  admin_auth:
    authorization:
      enabled: true
      source: static
      policy:
        - g, basic:admin, reports_admin, example/prod
        - g, user:alice@example.com, reports_admin, example/prod
        - p, reports_admin, example/prod, admin:reports, read|export
        - p, reports_admin, example/prod, admin:security_reports, read|export
  admin_reports:
    enabled: true
    default_since: 24h
    max_range: 31d
    max_rows: 500
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
```

Common endpoints:

- `/admin/reports/` renders the browser shell.
- `/admin/reports/api/quota-status` returns live per-caller remaining day/month/lifetime counters and configured limits for reports-admin callers. Filter with `caller_id`, `owner_user`, or `token_id`. It does not use the usage DB; it reads signed in-process quota state. Ordinary caller tokens receive `403`. See [Customer Administrator Guide](./customer-administration).
- `/admin/reports/api/summary?since=24h` and `/admin/reports/api/overview?since=24h` return SQL-backed full-window totals, hourly series, top-N grouped tables, a bounded recent request sample, and a reusable `charts` contract with chart IDs, titles, axis labels/types/units, series names, semantic color keys, scalar points, generation timestamp, range, and active safe filters.
- `/admin/reports/api/savings?since=24h&baseline=gpt-5.5` returns actual cost, selected baseline cost, savings USD, savings percent, time buckets, model-group breakdowns, source-dated baseline metadata, and chart descriptors.
- `/admin/reports/api/savings-by-user`, `/savings-by-key`, `/savings-by-group`, `/savings-by-project`, and `/savings-by-provider-model` return category chart descriptors for requests, actual vs baseline cost, savings USD, and savings rate when a baseline is selected.
- `/admin/reports/api/<report-name>?since=24h` returns shared scalar report rows and chart descriptors for overview, savings by user/key/group/project/provider-model, model groups by user, usage by key/caller/requested-model, provider/model mix, latency/throughput, errors/fallbacks, cache, quotas/budgets, troubleshooting buckets, routing decisions, dynamic signal/score/threshold buckets, max-token buckets, input-token buckets, admission reasons, contract buckets, contract workloads, target validation buckets, expensive requests, client breakdown, project cost allocation, capability usage, and deterministic rule-based anomaly signals. Baseline and savings fields are present only on savings reports.

Savings breakdown browser tables intentionally emphasize attribution fields: dimension, requests, tokens, actual cost, baseline cost, savings, savings rate, and average cost per request. Use usage, latency/throughput, errors/fallbacks, and request drilldown reports for the deferred performance and operational columns.
- `/admin/reports/api/provider-catalog-status` returns safe provider catalog and active-target validation metadata from runtime config. It separates `catalog` rows from `active_target` rows so per-group target overrides for modalities, tools, pricing, max-token behavior, OpenAI-compatible encoding fields such as `forceStoreFalse` and `outputTokenField`, and validation are visible without changing catalog metadata. It does not expose provider keys, headers, or full config.
- `/admin/reports/api/retention-status` returns read-only retention and daily-rollup status from existing usage DB tables, including the latest retention job, per-table candidate/held/eligible/blocked/deleted counts, and recent rollup runs.
- `/admin/reports/api/security/events?since=24h` returns safe scalar access events for authorized calls, unauthorized attempts, forbidden admin/report/metrics access, and Basic admin auth checks when security reports are enabled.
- `/admin/reports/security/export.csv?since=24h` exports the filtered security event table with spreadsheet formula-leading values neutralized and requires `admin:security_reports` `export`.
- `/admin/reports/api/requests?since=24h&limit=100` returns recent safe request rows.
- `/admin/reports/api/request/<request_id>` joins safe usage, attempt, trace, terminal error, shape, sanitized upstream-error, and decision-telemetry rows.
- `/admin/reports/api/request-evidence?request_id=<request_id>` returns the same request-level evidence bundle with `diagnosticCompleteness`, `diagnosticCompletenessScore`, and per-section `present` / `not_applicable` / `missing` states.
- `/admin/reports/export.md?since=24h` returns a bounded detail Markdown report with the same report sections as the CLI, existing-precision timestamps, and recent matching request rows capped by `limit`.
- `/admin/reports/export.md?since=24h&mode=summary` returns a SQL-backed Markdown summary with full-window totals and bounded top-N aggregate sections, without raw request rows.

The embedded browser renderer uses the chart contract for axes, legends, unit-aware tick labels, and hover tooltips. Category charts shorten long bucket labels on the axis and show a collapsible bucket legend that maps each short label to its full value; tables, exports, and JSON responses keep the full label. Chart points are scalar aggregate values only and are backed by the same safe report fields exposed in tables and exports.

Request evidence bundles are assembled from normalized relational tables. They expose safe request ID, caller/project/environment/client labels, public token ID, requested and resolved model group, selected upstream/provider model and dialect, stored request-time token and cost fields, upstream-reported billed cost fields, latency/throughput, quota/caller-token/cache state, traffic-shaping state, candidate/filter summaries, attempts, sanitized upstream error fields, and trace rows where available. Evidence APIs require `admin:reports` `drilldown`, use `Cache-Control: no-store`, and must not expose raw prompts, raw responses, raw image URLs or payloads, raw tool schemas, raw tool outputs, provider API keys, router tokens, token hashes, full upstream headers, unsanitized upstream bodies, cookies, OIDC tokens, or full config.

Report APIs also return `pagination` metadata. Raw request and security-event endpoints use cursor pagination with `limit`, `cursor`, `sort`, and `direction`:

```text
/admin/reports/api/requests?since=24h&limit=50&client=codex-cli&resolved_group=default&sort=timeUtc&direction=desc
/admin/reports/api/requests?since=24h&limit=50&cursor=<next_cursor>
/admin/reports/api/security/events?since=24h&limit=50&sort=timeUtc&direction=desc
```

Cursor-paged metadata includes `mode: "cursor"`, `returned`, `total_count`, `has_more`, `next_cursor` when present, `sort`, and `direction`. Request sort keys are `timeUtc`, `costUsd`, `latencyMs`, `status`, and `requestId`; security-event sort keys are `timeUtc`, `status`, `outcome`, `surface`, and `reason`. Cursors are opaque signed page positions; malformed, tampered, stale, or sort-mismatched cursors return `400 invalid-report-filter`.

The Overview report may include SQL-backed security trend charts when security reports are enabled and the administrator is authorized for `admin:security_reports` `read`. Usage-only administrators still receive the normal Overview usage charts; security charts are omitted instead of causing the Overview request to fail.

Aggregate report endpoints remain top-N summaries when full aggregate pagination would be expensive. Overview, savings, and direct scalar aggregates are grouped, ranked, and limited in the usage database, with separate full-window summary totals when the summary should not be limited to the returned top-N rows. Overview also returns only a bounded recent request sample while its summary, hourly charts, and token/group/provider/status breakdowns cover the full selected window and filters. Their metadata uses `mode: "top_n"`, `total_count: null`, `has_more`, and a note explaining that rows are ranked by the selected report's sort. Browser quick filtering and table-header sorting on these tabs operate over the returned top-N rows. Use aggregate reports to find high-volume users, keys, providers, clients, or shaping buckets, then use the cursor-paged request or security endpoints with matching filters for row-by-row review.

Overview charts include requests/success/errors, actual cost, actual-vs-baseline cost and savings, savings rate when a configured or custom baseline is available, latency/TTFB/upstream/downstream duration, upstream/downstream throughput, errors and fallbacks, error/fallback rates, cache counts, cache hit rate, and provider token volume. Actual cost is always summed from stored request-time cost fields; baseline and savings values are hypothetical comparisons from the selected or default configured baseline.

Savings reports use stored request-time actual cost fields for actual spend. Only the hypothetical baseline cost is calculated at report time from stored input/output token counts and selected baseline prices. Built-in baseline prices are source-dated in `server.admin_reports.baselines`; revalidate provider pricing before using savings figures in contractual or customer-facing claims. Custom browser-session baselines can be supplied with `baseline=custom`, `baseline_input_price_per_million_usd`, and `baseline_output_price_per_million_usd`.

Anomaly reports are deterministic operational triage views rather than machine-learning anomaly detection. The built-in rules group errors, fallbacks, multi-attempt requests, slow requests, expensive requests, quota warning/reject states, and abnormal key states such as disabled, revoked, expired, or suspended; normal active key state is not anomalous.

The browser shell adds shared usability controls across tabs: URL-backed selected tab, global filters, server sort/direction, limit, and cursor state; global filters for caller/project/model/provider context; per-tab panels for tab-local controls such as baseline/status/cache/sort/traffic-shaping scope; a table-toolbar `Rows` server limit; clearly labeled quick filtering of the returned page or top-N rows; sortable headers; refresh; request-ID drilldown; and CSV export of current-page, top-N, or visible safe scalar columns. Server endpoints remain authenticated, bounded, and domain-scoped to the admin's policy domain unless an explicit `*` policy domain grants deployment-wide report access. The browser controls do not expose or persist bearer tokens.

CSV export scope is explicit in the button label: current cursor page for request/security detail, returned top-N rows for aggregate tabs, or visible rows for unpaged responses. Markdown detail export is a bounded current-filter report that includes the most recent matching request rows up to the requested `limit`, adds an export-scope note when more rows matched, and escapes raw HTML plus active Markdown table-cell syntax. Markdown summary export is available with `mode=summary`; it computes full-window totals and top-N aggregate sections in SQL so large windows do not require loading every matching raw request row. Both Markdown modes use safe scalar report fields only and must not expose prompts, raw images, tool payloads, bearer tokens, token hashes, provider keys, full config, or unsanitized upstream bodies. Cross-domain request IDs return `404` for domain-scoped admins.

Browser investigation examples:

- Find all recent errors for a user: open `/admin/reports/?tab=requests&since=8h&caller_user=<user>&status=500&limit=50&sort=timeUtc&direction=desc`, then use Next to page through the cursor-backed results. Changing `caller_user`, `status`, or `since` resets the cursor to the first page.
- Sort expensive requests by stored cost: open `/admin/reports/?tab=expensive-requests&since=24h&limit=50&sort=costUsd&direction=desc`, review the current page, and use `CSV current page` when the incident ticket needs those rows.
- Interpret Provider/model reports as top-N: `/admin/reports/?tab=provider-model-mix&since=24h&limit=50` shows the top 50 ranked provider/model rows for the selected filters. It is not page 1 of all providers; drill into the Requests tab with matching provider/model filters for row-by-row review.

## Report Dimensions

Reports include:

- Calls, errors, status codes, latency, and upstream attempts.
- Input tokens, output tokens, total tokens, and throughput.
- Presence-aware reasoning-token coverage when an upstream reports it. The count is a subset of output tokens, never added again to totals, costs, or throughput; a reported zero is distinct from unavailable telemetry. Request and attempt diagnostics expose reported/attempt/successful-attempt coverage so routed evaluations can identify partial provider/model/dialect reporting.
- Downstream user performance grouped by user, project, environment, and client, including average/max latency, TTFB, downstream duration, and downstream token throughput.
- Upstream endpoint performance grouped by provider, model, and API dialect, including average/max upstream duration, latency, TTFB, attempts, fallbacks, cost, and upstream token throughput.
- Request-time input/output token prices and calculated input/output/total USD cost.
- Image/VLM fields including image presence, image count, upstream image-token counts when reported, calculated image input cost, and upstream-reported billed cost when available.
- Usage by public router token ID, user, project, and environment.
- Usage by public token ID across multiple caller tokens for one user or project, including rotation and disabled-token review.
- Usage by caller ID, requested model, target provider/model/dialect, status, cache state, and stored caller IP when enabled.
- Usage by caller IP and hour.
- Usage by router model group.
- Usage by external provider and model.
- Contract pass/fail buckets, optional contract workload labels, and target validation buckets when model-group contracts are configured.
- Cache hits, misses, bypasses, occupancy, and hit rate.
- Browser troubleshooting buckets for quota, TPM/RPM or rate-limit, concurrency, max-token/context, upstream quota/billing, key-state, cache, fallback, multi-attempt, and HTTP error classes inferred from safe stored request fields.
- Traffic-shaping fields: applied flag, decision, scope, limiting bucket, retry-after milliseconds, queue wait milliseconds, queued/rejected counts, average/p50/p95/max queue wait, estimated input tokens, reserved output tokens, total reserved tokens, and per-bucket rows in `request_traffic_shape_events`.
- Optional decision telemetry summary when `server.decision_telemetry.enabled: true`: request-shape feature row counts, target candidate row counts, target filter reason buckets, routing-decision strategy buckets, routing signal rows, score/ranking term rows, policy execution rows, fallback transition rows, cache decision reason buckets, enabled dynamic-score signal names, score buckets, threshold buckets, max-token buckets, input-token buckets, admission reason buckets, policy outcome/error-class buckets, and fallback-reason buckets.
- Streaming and non-streaming request counts.
- Request IDs that can be joined to diagnostic attempt, trace-event, and terminal-error rows by administrators.

## Troubleshooting By Request ID

Every response includes `X-Request-Id`. Structured error responses also include `request_id` in the error details. The [Diagnostics Schema](../reference/diagnostics-schema) documents the columns, retention classes, indexes, foreign keys, population timing, and safety status for these tables. Administrators can use the request ID to inspect:

- `request_usage` for the terminal request status, selected target, token counts, cost fields, and non-secret routing/model-group/policy/pricing fingerprints.
- `request_attempts` for each upstream provider/model attempt, status code, duration, timeout/cancel flags, retryability, and sanitized error class/message.
- `request_trace_events` for ordered router decisions such as cache handling, upstream attempts, fallback, timeout, or terminal failure.
- `request_traffic_shape_events` for per-bucket caller/server traffic-shaping decisions, costs, retry-after, and queue wait.
- `request_upstream_shape_events` for provider/model/target admission, skip, rejection, and adaptive-backoff cooldown decisions.
- `request_shapes`, `request_translation_shapes`, and `request_translation_field_events` for safe request-shape and provider-translation triage. Use these to compare successful and failed requests for the same provider/model/dialect by stream flag, tool count, tool-choice mode, request bytes bucket, input-token bucket, output-cap bucket, reasoning controls, multimodal presence, request-shape fingerprint, and tool-schema fingerprint.
- `request_upstream_error_details` for bounded allowlisted provider 4xx/5xx fields such as code, type, param, request ID, and categorized provider message. These rows are enabled by default with diagnostics; set `store_sanitized_upstream_errors: false` only to suppress provider detail rows.
- `request_decision_shape_features`, `request_target_candidates`, `request_target_filter_reasons`, `request_routing_decisions`, `request_routing_signals`, `request_dynamic_score_terms`, `request_policy_executions`, `request_fallback_transitions`, and `request_cache_reasons` for normalized decision explainability when decision telemetry is enabled. Shape features include safe max-token and input-token buckets, score/ranking term rows include scalar score buckets, policy execution rows cover fail-closed errors before selection, and fallback transition rows link failed attempts to fallback targets.
- `request_errors` for the terminal sanitized error summary.

Diagnostic and decision telemetry rows do not store raw prompts, image payloads, image URLs, tool schemas, tool outputs, bearer tokens, provider keys, token hashes, full upstream headers, full config, or unsanitized upstream response bodies. See [Fields Intentionally Not Persisted](../reference/diagnostics-schema#fields-intentionally-not-persisted) for the canonical list.

For “small prompts work but real coding-agent requests fail,” filter to the same provider/model/dialect and compare successful versus failed attempts by request shape:

For encoding-related upstream 400/403 triage, filter Upstream failures or raw Requests by `provider`, `target_model`, `dialect`, `status`, and large-payload buckets such as `request_bytes_bucket`. Compare the failed active target with `/admin/reports/api/provider-catalog-status`: `forceStoreFalse=true` explains intentional `store:false` injection, while `outputTokenField=max_completion_tokens` explains Chat Completions cap translation. A 403 on `store` or a 400 on `max_tokens` usually points to catalog metadata drift rather than a provider outage.

For large Chat payload investigations, use only safe scalar shape fields. A useful first report is provider/model/dialect plus byte bucket, tool-count bucket, request-shape fingerprint, tool-schema fingerprint, upstream status, terminal status, request count, and error rate. Reproduce the dominant failed shape with a sanitized fixture, first against the direct upstream endpoint and then through a router smoke group pinned to the same target. Deployment validation should use a deployment-owned smoke group and an existing safe caller token; if those prerequisites are unavailable, record the prerequisite gap and keep token files or captured payloads out of notes. If direct and router both pass, treat older failures as stale evidence and update the investigation record with the passed request bytes and token scale. If the direct upstream passes but router fails, inspect translation rows and config metadata. If the direct upstream fails at the same shape, configure `request_shape_support` limits or keep the target out of broad coding-agent groups.

```sql
SELECT
  u.status,
  a.status_code AS upstream_status,
  rs.request_shape_fingerprint,
  rs.tool_schema_fingerprint,
  rs.total_request_bytes_bucket,
  rs.tool_schema_bytes_bucket,
  rs.estimated_input_tokens_bucket,
  rs.tool_count,
  rs.tool_choice_mode,
  rs.structured_output_present,
  rs.reasoning_present,
  rs.image_count,
  ts.translated_output_cap_field,
  ts.translated_output_cap_bucket,
  ts.translated_reasoning_control,
  COUNT(*) AS requests
FROM request_usage u
JOIN request_attempts a ON a.request_id = u.request_id
LEFT JOIN request_shapes rs ON rs.request_id = u.request_id
LEFT JOIN request_translation_shapes ts
  ON ts.request_id = a.request_id AND ts.attempt_index = a.attempt_index
WHERE u.ts >= :from
  AND u.ts < :to
  AND a.provider = :provider
  AND a.model = :model
  AND a.dialect = :dialect
GROUP BY 1,2,3,4,5,6,7,8,9,10,11,12,13,14,15
ORDER BY requests DESC;
```

When operators generate rollups, `usage_rollup_decision_buckets` preserves report-critical bucket counts after raw request or decision detail retention. Browser admin scalar APIs include `/admin/reports/api/dynamic-signals`, `/admin/reports/api/dynamic-score-buckets`, `/admin/reports/api/dynamic-thresholds`, `/admin/reports/api/max-token-buckets`, `/admin/reports/api/input-token-buckets`, and `/admin/reports/api/admission-reasons`.

Governed content capture is separate from diagnostics. It is disabled by default and, when enabled by the deployment operator, writes redacted request/response/upstream-error content to dedicated relational tables keyed by `request_id`. Maintenance operations require policy-based authorization for `content:capture`: `DELETE /v1/content-captures/<request_id>` uses action `delete` in the captured row's caller project/environment domain, and `POST /v1/content-captures/purge-expired` uses action `purge`. Existing `content_admin: true` caller entries remain compatible for their own domain. Both operations write audit rows. Usage reports remain metadata-oriented and do not print captured content.

## Filtering

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --caller-user <owner-user> \
  --caller-project <project> \
  --resolved-group <model-group> \
  --client <client> \
  --out usage-filtered.md
```

Reports use public token IDs and aggregated usage fields. They do not expose raw router tokens or raw provider API keys.

## Decision Telemetry Smoke

Decision telemetry is disabled unless the deployment sets `server.decision_telemetry.enabled: true`. After enabling it, administrators should run a text request, a negative no-eligible-target request such as a tool request against a target without tool support, a `dynamic_score` request, a script or external-policy success request if those strategies are enabled, a fail-closed policy request, an upstream failure followed by fallback success, and a `Cache-Control: no-cache` request. Then generate a Markdown report and confirm it includes a Decision Telemetry Summary with safe buckets such as `static`, `tool-support`, `external-policy-invalid-target`, `upstream_rate_limited`, or `cache-request-no-cache`, and open an admin request drilldown to confirm the `decisionTelemetry` child rows are present.

## Performance Triage

Use the downstream user performance section to identify which users, projects, or clients are seeing slow responses. Use the upstream endpoint performance section to identify provider/model/dialect combinations with high upstream duration, low token throughput, elevated errors, or fallback pressure. The per-request throughput table remains available for request-level drilldown when a grouped row needs investigation.

For Cursor, opencode, and other large-context developer tools, start with the troubleshooting buckets, max-token buckets, input-token buckets, and usage by client/project/public token ID. Several 150K-token requests can exhaust TPM inside a rolling window even when daily or monthly budgets remain healthy. Distinguish router-side `429 tpm-exceeded`, `rpm-exceeded`, `concurrency-exceeded`, or `quota-exhausted` responses from upstream provider `429` attempts and from client cancellations by checking the terminal request status, attempt rows, and request trace events.

Cost fields are captured when each request finishes. Reports do not look up current provider pricing, which means a June report keeps the June price even if an upstream vendor changes rates in July. Operators should update provider catalog metadata whenever prices, modality support, or tool-capability validation changes.

For image requests, `input_price_per_million_usd` remains the fallback input-token rate. If a VLM has separate image pricing, configure `image_input_price_per_million_tokens_usd` for upstream-reported image tokens or `image_input_price_per_image_usd` for fixed per-image cost allocation. When an upstream returns billed cost, the router stores those values as upstream-reported cost fields in addition to router-calculated cost fields.

For prompt-cache accounting, configure optional `cached_input_price_per_million_usd` on the catalog or target. The router persists upstream `cached_input_tokens` when reported and applies the cached price only when both the count and price are known for that request. Missing evidence keeps ordinary input pricing.

For a buyer-facing explanation of cost policy and cost allocation, see [Cost Governance](/docs/evaluation/cost-governance).

For anonymized graphical examples generated from production-style data, see [Report Examples](/docs/operations/report-examples).
