# Usage DB And Reporting Design

This document is for maintainers. The external solution brief should describe capabilities without exposing implementation details.

## Storage

- Usage persistence is implemented with GORM.
- Supported drivers are `sqlite` and `postgres`.
- New generic local, Docker Compose, and Kubernetes config uses SQLite at `server.usage_db.path`; container installs use `/app/state/usage.sqlite`.
- SQLite is one active router writer: generic Kubernetes stays one replica/Recreate/ReadWriteOnce.
- SQLite usage database files are created/chmodded `0600`; keep the containing state directory private and do not loosen permissions on WAL or SHM sidecars.
- PostgreSQL is an explicit multi-replica or externally managed deployment choice via `server.usage_db.driver: postgres` and deployment-owned DSN. Compose provides it only through the localhost-only override.

## Relational Schema Rule

The entire usage DB schema must remain relational-only:

- Do not use JSON, JSONB, array, or packed multi-value text columns.
- Store request usage as scalar columns on `request_usage`.
- If a future feature needs one-to-many data, add a child table with scalar columns and a foreign key to `request_usage`.
- Keep schema tests that inspect the actual DB column types.

`request_usage.target_region` stores the optional deployment-declared region
of the target that actually completed the request, including a successful
fallback. It is empty for unlabelled targets and historical rows. It is safe
diagnostics metadata, not evidence that location policy was enforced. Schema
migration `2026090901` owns this additive column; the immutable version-1
baseline must not create it.

Diagnostic child tables are part of the usage DB and follow the same rule:

- `request_attempts`: one scalar row per upstream attempt, including provider/model, status, timing, timeout/cancel flags, retryability, and sanitized error class/message.
- `request_trace_events`: ordered scalar router events such as request accepted, cache decision, upstream attempt, fallback, timeout, and terminal failure.
- `request_traffic_shape_events`: one scalar row per evaluated caller/server traffic-shaping bucket, with scope, bucket, decision, cost, retry-after, queue wait, estimated input tokens, reserved output tokens, and total reserved tokens.
- `request_upstream_shape_events`: one scalar row per provider/model/target shaping decision, including scope, provider, model reference or label, dialect, bucket, decision, retry-after milliseconds, estimated input tokens, reserved output tokens, total reserved tokens, and safe backoff reason.
- `request_shapes`: one scalar row per routed request, including inbound dialect, requested/resolved group, client, stream flag, input item/message/role counts, tool result/function output counts, tool count, tool-choice mode, safe field-presence booleans, multimodal counts/flags, input-text/tool-schema/request-size buckets, estimated-input-token bucket, requested output-cap field/bucket, and HMAC request/tool-schema fingerprints.
- `request_translation_shapes`: one scalar row per upstream attempt, including provider/model/dialect/path, bridge direction, translated stream/tool/tool-choice/output-cap/reasoning controls, translated request-size bucket, strip/rewrite/unsupported/warning counts, and request/tool-schema fingerprints.
- `request_translation_field_events`: bounded scalar child rows keyed by request ID, attempt index, and sequence for allowlisted field names or `other`, with action and reason buckets.

Bridge traffic uses the existing dialect and translation-shape columns rather than a separate bridge table. For Chat-to-Responses, the parent usage row records inbound `openai-chat`; attempts and translation-shape rows record target `openai-responses`, `bridge_direction = chat_to_responses`, the translated `/v1/responses` path, and safe scalar shape buckets. For Responses-to-Chat, the parent usage row records inbound `openai-responses`; attempts and translation-shape rows record target `openai-chat`, `bridge_direction = responses_to_chat`, the translated `/chat/completions` path, and safe scalar shape buckets. Unsupported bridge features should appear as bounded filter reasons or translation field events, not as raw payload captures.
- `request_upstream_error_details`: scalar allowlisted provider 4xx/5xx error fields keyed by request and attempt, including status, class, field name, sanitized field value, source path, and truncation flag.
- `request_errors`: one scalar terminal error row per failed request for fast incident queries.

These tables are keyed by `request_id`. They must not store raw prompts, image payloads, image URLs, raw tool schemas, raw tool outputs, bearer tokens, provider keys, token hashes, full upstream headers, raw Retry-After headers, or unsanitized provider response bodies.

Reasoning production proof uses the same relational rows. Join `request_usage`, selected `request_attempts`, `request_shapes`, `request_translation_shapes`, and optional `request_target_candidates` / `request_target_filter_reasons` by `request_id` and `attempt_index`; assert terminal 2xx status, `fallback_used = false` unless fallback is deliberately under test, the expected provider/model/dialect, the expected bridge direction when a bridge is under test, and the expected `translated_reasoning_control` value. OpenAI Chat `reasoning_effort` records `reasoning_effort`, OpenAI Responses `reasoning.effort` records `reasoning`, and Anthropic Messages `thinking` records `thinking`. For rejected explicit reasoning requests, candidate rows should show whether the target lacked reasoning support, lacked the required bridge reasoning flag, or was filtered for another request-shape reason, and there should be no selected incompatible upstream attempt. Stateful Chat-to-Responses backend activity and stale recovery use existing `request_trace_events` rows with bounded names such as `bridge_session_lookup_hit`, `bridge_session_lookup_miss`, `bridge_session_set`, `bridge_session_delete`, `bridge_session_backend_error`, `bridge_session_previous_response_stale_purged`, and `bridge_session_stateless_retry`; the rows do not store raw session headers, Redis credentials, prompts, or response bodies.

When an OpenAI-compatible upstream reports `completion_tokens_details.reasoning_tokens` (or Responses `output_tokens_details.reasoning_tokens`), the nullable count is stored on that `request_attempts` row. `request_usage.reasoning_tokens` sums only reported attempt values; its attempt, successful-attempt, and reported-attempt scalar counters make complete, partial, and unavailable coverage queryable. A reported `0` remains distinct from an omitted value. Reasoning tokens are already included in output tokens and are never added again to total-token, cost, or throughput calculations.

When an upstream reports cached-input evidence, `request_usage.cached_input_tokens` stores the nullable count. OpenAI Chat uses `prompt_tokens_details.cached_tokens`, OpenAI Responses uses `input_tokens_details.cached_tokens`, Anthropic uses `cache_read_input_tokens` (with Anthropic input totals normalized to include cache read/write fields), and Gemini uses `cachedContentTokenCount` when it is a valid subset of prompt tokens. A reported `0` remains distinct from an omitted value. Invalid counts (negative or greater than applicable input) are rejected and stored as null. Router response-cache hits clear inherited upstream cache-read evidence so a local cache hit is not treated as prompt-cache savings for the current request. Request rows also snapshot nullable `cached_input_price_per_million_usd` from the served target. Cost accounting uses that price only when both the cached count and cached price are non-null and `0 <= cached <= applicable input`: `(input - cached) * input_price / 1e6 + cached * cached_price / 1e6`. Otherwise ordinary input pricing is retained.

Usage rollups are generated from stored `request_usage` rows and also follow the scalar relational rule:

- `usage_rollup_runs`: one row per generated hourly, daily, or monthly rollup window, with draft/finalized status, UTC source window, source table name, source request count, source min/max request timestamp, deterministic source checksum, aggregate row counts, decision-bucket row count, router version/commit, safe error text, and generation/finalization timestamps.
- `usage_rollup_hourly`: scalar aggregate rows per UTC hour and reporting dimension for recent operational trend reporting.
- `usage_rollup_daily`: scalar aggregate rows per UTC day and reporting dimension in the rollup window, keyed to `usage_rollup_runs`.
- `usage_rollup_monthly_billing`: scalar aggregate rows per UTC calendar month and reporting dimension for **customer internal chargeback and optional enterprise contract true-up summaries**. This is **not** a Metrum product billing ledger; Metrum commercial billing is handled outside the router by Metrum finance.
- `usage_rollup_decision_buckets`: scalar aggregate rows per rollup bucket, model group, strategy, bucket kind, bucket name, and optional secondary bucket. It preserves report-critical decision buckets after raw request/decision detail retention, including max-token buckets, input-token buckets, quota/admission reason buckets, enabled dynamic-score signal names, dynamic score buckets, and threshold/filter buckets.
- `usage_rollup_audit_events`: scalar audit rows for rollup create, draft regeneration, and finalization events with a safe summary.

Rollup dimensions include caller ID/user/project/environment, token ID, client, inbound dialect, requested model, resolved group, routing strategy, upstream provider/model/dialect, status class, stream flag, cache outcome, image-input flag, PII-filter flag, contract bucket, target-validation status, and optional savings baseline ID. Measures include request/success/error/cache/fallback/attempt counts, input/output/total tokens, input image count, input image tokens, request-time calculated and upstream-reported cost sums, optional baseline input/output/total costs and savings, latency/duration sums/counts/maxima, throughput sums/counts/minima/maxima, and cache snapshot sums/maxima.

Draft rollups for the same exact type/window may be regenerated idempotently. Finalized rollup windows are immutable through the rollup generator, and new rollup runs are rejected if their type/window overlaps an existing finalized window. Retention uses finalized daily rollup metadata as a prerequisite before `usage_detail` delete batches can run and does not mutate finalized rollup rows.

Commercial retention tables also follow the scalar relational rule:

- `retention_policy_versions`: active config-derived retention policy versions with a scalar policy hash, dry-run flag, default batch size, and activation timestamps.
- `retention_policy_rules`: one row per data class rule, with data class, enabled flag, retention days, batch size, and the `usage_detail` finalized-rollup prerequisite.
- `retention_jobs`: one row per status or run job with policy version, mode, status, requested-by, dry-run flag, and timestamps.
- `retention_job_table_results`: per-job/per-table counts for candidate rows, legal-hold skipped rows, eligible rows, blocked rows, deleted rows, cutoff timestamp, and status.
- `legal_holds`: active or released holds keyed by hold ID, data class, optional request ID, timestamp range, reason, subject, creator/releaser, and timestamps.
- `legal_hold_audit_events`: scalar audit rows for hold create/release/update workflows.

The current retention foundation initializes policy rows from `server.retention`, records dry-run counts for all known classes, and can delete one configured batch for `usage_diagnostics`, governed `content_capture` (including selected header children), and rollup-gated `usage_detail` when `dry_run: false`. Legal holds are checked by `data_class`, optional `request_id`, and timestamp range when counting skipped rows and selecting delete batches; request-scoped legal holds on child data classes also block `usage_detail` parent deletion so cascade rules cannot remove held child telemetry. Decision telemetry child rows are counted through their parent `request_usage.ts`. It does not archive content, schedule jobs, delete unsupported classes through the generic runner, or provide a full admin UI/API for hold lifecycle.

Normalized decision telemetry is an optional first-slice diagnostic feature under `server.decision_telemetry`. It is disabled by default and writes only safe scalar child rows:

- `request_decision_shape_features`: one row per safe request-shape feature such as caller dialect, stream flag, tool count, image count, structured-output flag, max-token flag, max-token bucket, input-token bucket, and cacheability.
- `request_target_candidates`: one row per bounded group target candidate with provider, model, dialect, configured weight, tool-only flag, scalar capability flags, validation status/age bucket, eligibility flag, and selected flag.
- `request_target_filter_reasons`: one row per bounded candidate filter bucket, such as `tool-only-target`, `tool-support`, `dialect-tool-passthrough`, `structured-output-support`, `max-tokens-honored`, or `contract-*`.
- `request_routing_decisions`: one row per selected routing decision with strategy, selected candidate index, provider, model, dialect, fallback count, and optional safe class label.
- `request_routing_signals`: one row per safe routing signal used by a strategy, such as enabled `dynamic_score` signals and scalar policy knobs.
- `request_dynamic_score_terms`: one row per bounded candidate/rank/term/score contribution or simple-strategy ranking explanation. `dynamic_score` rows include scalar value/contribution/final-score buckets; simple `static`, `failover`, `weighted`, `latency`, `cost`, `script`, and `external` rows use stable configured-order, configured-weight, configured-rank, or policy-output terms without random seeds or raw policy payloads.
- `request_policy_executions`: one row per script/external policy execution, including fail-closed errors before target selection and configured external fallback executions. Rows store strategy, policy kind, outcome, safe error class/message, duration, eligible/all target counts, selected candidate index when any, fallback count, and terminal error type.
- `request_fallback_transitions`: one row per upstream failure that moves to a fallback target, with failed/fallback candidate indexes, provider/model/dialect labels, fallback reason/error class, retryable flag, and whether that fallback attempt succeeded.
- `request_cache_reasons`: one row per cache decision bucket, such as `cache-hit`, `cache-miss`, `cache-request-no-cache`, `cache-tool-request`, `cache-image-request`, `cache-structured-output`, `cache-streaming`, or `cache-temperature`.

Request usage rows also store non-secret reproducibility fields: router version/build date, routing config fingerprint, model-group config fingerprint, routing-policy fingerprint, and pricing-catalog fingerprint. Fingerprints are SHA-256 hashes of redacted canonical routing metadata. They exclude provider API keys, router token hashes, raw prompts, raw policy request/response bodies, policy headers, raw URLs, script paths, and full config contents; script policy changes are represented by a script-content hash only when the file is available.

Decision telemetry must not store prompt text, image URLs or bytes, tool schemas, tool outputs, bearer tokens, provider keys, token hashes, full config, policy request/response JSON, or routing script raw request mirrors. Keep new reason names stable, lowercase, and safe for reports.

Request evidence bundles are assembled at query time from these normalized tables. They are returned as JSON by admin report APIs, but no evidence bundle is persisted as a JSON/JSONB/array/blob column. The assembler joins `request_usage` to diagnostic child tables by `request_id` and returns safe sections for request summary, admission/shaping, request shape, target eligibility, attempts, sanitized upstream errors, trace timeline, and cost accounting. It also returns section-level completeness states so operators can distinguish present evidence, disabled or not-applicable evidence, and unexpectedly missing rows.

Evidence completeness is a reporting assertion, not a routing decision. For non-2xx requests, a missing expected section such as attempts, terminal error, request shape, target eligibility, sanitized upstream error detail, or trace timeline should be treated as a telemetry regression unless the request was rejected before that phase. The bundle may summarize row counts, selected target, candidate/filter counts, stored request-time prices/costs, upstream-reported billed costs, and latency/throughput fields, but it must not include raw prompts, raw responses, raw image URLs or payloads, raw tool schemas, raw tool outputs, provider keys, router tokens, token hashes, full headers, unsanitized upstream bodies, or full config.

Governed content-capture tables are separate from diagnostics and also follow the relational-only rule:

- `request_content_captures`: redacted and AES-256-GCM-encrypted request, response, and upstream-error content rows with scalar request/route metadata, retention timestamp, redaction counts, truncation flags, nonce, key identifier, and encrypted status.
- `request_content_headers`: allowlisted encrypted header values keyed to a capture row, with separate nonce, key identifier, and encrypted-status columns; authorization, API-key, token, secret, cookie, and key-like headers must be rejected before storage.
- `request_content_audit_events`: read/delete/purge audit events with actor caller metadata, action, request ID, affected row count, and reason.

Content-capture rows are keyed by `request_id` so administrators can join them to `request_usage`. This is an explicit opt-in enterprise feature; default usage and diagnostics behavior remains metadata-only.

Casbin policy lifecycle tables also live in the usage DB and follow the same relational-only rule:

- `authz_policy_sets`: one row per versioned policy set with status, creator, and created/activated/retired timestamps.
- `authz_policy_rules`: one scalar row per Casbin `p` rule with sequence, subject or role, domain, object, action, and effect.
- `authz_role_links`: one scalar row per Casbin `g` role link with sequence, subject, role, and domain.
- `authz_policy_audit_events`: create, activation, rollback, and validation-failure audit events with safe actor, request, action, and summary fields.

These tables must not store raw router tokens, token hashes, provider keys, password material, prompts, images, tool outputs, or full config. Policy activation validates rows before status changes, and startup in DB policy mode fails closed when no single valid active policy set exists.

## Request Metrics

Each request row stores:

- caller, token, client, requested model group, resolved target provider/model, status, cache state, attempts, fallback state, and token counts.
- `caller_ip`: source IP derived from `X-Forwarded-For`, `X-Real-IP`, or the direct remote address.
- `upstream_duration_ms`: router-observed upstream provider/fallback call duration.
- `downstream_duration_ms`: router-to-caller response write duration.
- upstream/downstream output-token/sec and total-token/sec.
- cache snapshot: enabled state, item count, occupied bytes, max bytes, and occupancy percentage.
- request-time pricing: input/output dollars per million tokens, optional cached-input dollars per million tokens, pricing source/update date, and calculated input/output/total USD cost.
- optional cached-input evidence: nullable `cached_input_tokens` from upstream usage when reported.
- PII-filter metadata: `pii_filter_applied`, `pii_filter_mode`, `pii_filter_replacements`, and `pii_filter_rule_count`; never raw matched values or placeholder mappings.
- diagnostic traceability: child rows keyed by request ID for upstream attempts, trace events, and terminal errors.
- traffic-shaping metadata: `traffic_shape_applied`, `traffic_shape_decision`, `traffic_shape_scope`, `traffic_shape_bucket`, retry-after, queue-wait, estimated input tokens, reserved output tokens, total reserved tokens, and child rows in `request_traffic_shape_events` for each evaluated bucket.
- request-shape and provider-translation traceability: child rows keyed by request ID and attempt index for safe request structure, translated upstream payload structure, field strips/rewrites, size/token/output-cap buckets, reasoning/multimodal/tool-shape buckets, and HMAC fingerprints.
- optional decision telemetry traceability: child rows keyed by request ID for request-shape features, candidate eligibility/capability metadata, filter buckets, routing decisions, routing signals, score/ranking term rows, policy execution rows, fallback transition rows, and cache reason buckets when `server.decision_telemetry.enabled: true`.
- derived report buckets: max-token bucket, input-token bucket, admission reason, enabled dynamic-score signal names, score buckets, and threshold buckets. Multi-value decision buckets are kept as child/rollup rows, never arrays or packed JSON.
- optional governed content-capture traceability: separate content rows keyed by request ID only when `server.content_capture.enabled` and a capture scope are configured.

For cache hits, upstream duration and upstream TPS are absent because no provider call occurs. Downstream duration and downstream TPS are still measured.

Cost columns are stored as scalar values on each row. Reports must sum stored cost values; they must not recalculate historical cost from current provider catalog pricing.

## Report Pagination Queries

Admin request and security-event APIs page directly from relational tables rather than loading every matching row into memory. Request pages apply the same safe filters and Casbin domain scope as aggregate reports, count the filtered rows, then order by a deterministic key. The default request order is `request_usage.ts DESC, request_usage.request_id DESC`; supported request sorts add a stable tie-breaker, for example `total_cost_usd DESC, ts DESC, request_id DESC` for expensive requests. Security-event pages use `security_access_events.ts DESC, security_access_events.id DESC` by default and can sort by status, outcome, surface, or reason with the same timestamp/id tie-breaker.

Cursor values are signed by the router process and contain only the endpoint, sort, direction, and last-row scalar sort values. They do not contain SQL, raw filters, prompts, tokens, token hashes, provider keys, or full config. A malformed or mismatched cursor fails with `400 invalid-report-filter`. Because the signing key is in memory, cursors are short-lived page positions and may become invalid after a router restart.

Keep indexes aligned with the common filters used before pagination: timestamp, caller user/project/environment, token ID, client, requested model, resolved group, target provider/model, status, and traffic-shaping fields. When adding a new raw/event-like report API, prefer DB-backed cursor pagination with an indexed natural timestamp plus a deterministic unique tie-breaker. Direct scalar aggregate APIs, including savings by user/key/group/project/provider-model, usage by key/caller, requested-model, provider/model mix, latency/throughput, errors/fallbacks, cache, project chargeback, and other single-row-per-dimension reports, should group, sort, and limit in SQL and run a separate SQL summary query when the summary must represent the full filtered window. Aggregate APIs can stay top-N when a full paginated aggregate would require expensive grouping, but their responses must return `pagination.mode: "top_n"` and a clear note.

## Durability

Durable across container restarts when volumes are preserved:

- usage DB rows.
- JSONL request logs.
- per-request timing, TPS, and cache snapshot fields.
- diagnostic attempt, trace, and terminal error rows when diagnostics are enabled.
- traffic-shaping request rows and per-bucket event rows for admitted, queued, and rejected shaped requests.
- request-shape, translation-shape, and translation field-event rows when usage DB persistence is enabled.
- decision telemetry rows when `server.decision_telemetry.enabled: true`.
- content-capture rows and content-capture audit rows when governed content capture is enabled.
- authz policy sets, policy rows, role links, and policy audit rows when DB-backed authorization is enabled.
- usage rollup run, daily aggregate rows, and daily decision-bucket rollup rows after an operator generates them.
- retention policy, job result, legal hold, and legal hold audit rows after an operator runs dry-run retention status.

Not durable across router restarts:

- in-memory response cache contents.
- in-memory traffic-shaping token buckets and queue depths.
- in-process Prometheus counters and gauges.

## Destructive reset boundary

Never use a destructive database reset for production migration, upgrade, rollback, repair, or schema change. Fresh and existing schemas are owned by the explicit migration manifest, not serving-startup schema creation. A destructive reset is permitted only for an empty, confirmed non-production environment after independent verification of the environment, database identity, and absence of retained data. This design document intentionally provides no removal command. Follow [Data migration framework](DATA_MIGRATIONS.md) for backup, restore, and the non-serving deployment-job gate.

Do not delete JSONL request logs unless explicitly requested.
