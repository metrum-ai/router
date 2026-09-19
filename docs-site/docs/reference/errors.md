---
title: Error Reference
doc_type: reference
---

# Error Reference

Metrum AI Router returns structured errors intended to be useful to both callers and administrators. Every response includes `X-Request-Id`; include that ID when asking an administrator to inspect router traces.

## Standard Error Shape

Caller-facing errors use a sanitized JSON envelope. Clients should parse `error.type` first, then use `X-Request-Id` when escalating to an operator. Some route-specific errors also include `error.details.request_id`.

```json
{
  "error": {
    "type": "upstream-failed",
    "message": "all eligible upstream targets failed for model \"example-coder\" after 1 attempt(s); contact the router operator with the request_id",
    "details": {
      "request_id": "req_0123456789abcdef0123456789abcdef",
      "model": "example-coder",
      "dialect": "openai-chat",
      "attempts": 1,
      "error_class": "upstream_bad_request",
      "upstream_status": 400,
      "retryable": false,
      "fallbackUsed": false
    }
  }
}
```

Fields are intentionally bounded:

- `error.type`: stable router error category for client handling.
- `error.message`: safe human-readable summary. Do not parse it for control flow.
- `X-Request-Id`: request identifier for operator drilldown on every response.
- `error.details.request_id`: request identifier included by upstream-failure and other route-specific errors when a details object is present.
- `error.details.retryable`, `Retry-After`, or `retry_after_seconds`: retry guidance when present.
- `error.details.error_class` and `X-Router-Error-Class`: sanitized upstream or router failure class.
- `error.details.upstream_status` and `X-Upstream-Status`: upstream HTTP status when a provider returned one.
- `requirements`, `bucket`, `model`, `dialect`, `attempts`, and `fallbackUsed`: safe triage context for administrators.

The error body, headers, diagnostics, and reports must not expose raw prompts, raw images or image URLs, raw tool schemas, raw tool outputs, bearer tokens, provider API keys, router tokens, token hashes, full upstream headers, unsanitized upstream bodies, cookies, OIDC tokens, or full config. If governed content capture is enabled, captured content is stored separately from usage diagnostics and is controlled by its own authorization and retention workflow.

## Common Errors

| Type | HTTP status | Meaning | Caller action | Admin action |
|---|---:|---|---|---|
| `missing-model` | 400 | The request omitted `model` and no `server.default_model_group` is configured. | Set a model group allowed for your token. | Configure `server.default_model_group` if omitted model should be accepted. |
| `model-not-allowed` | 403 | The caller token is not allowed to use the requested model group. | Use `/v1/models` to see allowed groups; see [Available Models And Access](../getting-started/available-models). | Update the caller `allow` list if access is intended. |
| `key-disabled` | 403 | The matched caller token is configured but disabled. | Use an active caller token or ask for the token to be re-enabled. | Re-enable only if the caller token should still be trusted and assigned to active ownership records. |
| `key-suspended` | 403 | The matched caller token is temporarily suspended. | Use another active caller token or wait for an administrator to restore access. | Review the suspension reason and reactivate only after the hold is cleared. |
| `key-expired` | 403 | The matched caller token is past its configured expiration time. | Rotate to a current caller token. | Issue a replacement caller token and retire the expired one according to rotation policy. |
| `key-rotated` | 403 | The matched caller token has been replaced by a newer token. | Switch the client to the replacement caller token issued by the administrator. | Confirm clients have migrated, then retire or delete the rotated token when appropriate. |
| `rpm-exceeded`, `tpm-exceeded`, `concurrency-exceeded`, or `quota-exhausted` | 429 | Request, token, daily/monthly, or concurrency policy blocked the request before an upstream call. | Honor `Retry-After` when present, reduce traffic, lower an unrealistic output cap, or ask for a quota change. | Inspect caller limits, recent usage, in-flight traffic, and `request_errors` retryability. |
| `traffic-shaped` | 429 | Caller traffic-shaping buckets rejected a burst immediately or timed out a bounded queue wait before upstream calls. | Retry after `Retry-After`, reduce burst size, reduce context, or lower output caps. | Inspect `traffic_shape_*` fields and `request_traffic_shape_events` for the limiting bucket and queue wait. |
| `key-exhausted` | 403 | The caller token's lifetime token budget is exhausted or the request's reserved token budget would exceed the remaining lifetime budget. | Use a caller token with remaining budget, lower an unrealistic output cap, or ask for a new budget. | Inspect the caller token lifetime budget and issue or re-enable tokens according to policy. |
| `pii-filter-blocked` | 400 | The requested model group is configured to reject requests that match PII filter rules, or the request exceeded the configured PII replacement cap. | Remove the sensitive value, reduce matched values, or use an approved workflow. | Review the model group's `pii_filter` rules, mode, and `max_replacements_per_request`. |
| `pii-filter-failed` | 502 | The router could not apply the configured PII filter. | Retry after the administrator resolves configuration. | Check regex validation, filter limits, and request shape. |
| `no-eligible-target` | 502 | No configured upstream target satisfies the request requirements. | Try a different allowed group only if instructed. | Add or enable a target that supports the requested dialect, tools, modalities, and cap behavior. |
| `routing-policy-error` | 502 | A TypeScript or enforced external routing policy failed, timed out, returned an invalid decision, or selected a target outside the eligible list. | Retry only if the operator says the policy failure is transient; otherwise provide the request ID. | Inspect safe policy execution/error telemetry, script concurrency, external-policy reachability and timeout, then restore the reviewed policy or use its documented baseline rollback. |
| `upstream-rate-limited` | 503 | All eligible upstream attempts were rejected by provider-side rate limits. | Retry later with backoff, or contact the administrator with the request ID if it persists. | Inspect `request_attempts`, provider status, and upstream rate-limit policy. |
| `upstream-capacity-throttled` | 503 | Every otherwise eligible target is temporarily unavailable because provider/model/target shared shaping or adaptive backoff is protecting upstream capacity. | Retry after the `Retry-After` window when present, or contact the administrator with the request ID. | Inspect `request_upstream_shape_events`, `request_trace_events`, current traffic-shape config, and recent upstream `429` or quota events. |
| `upstream-quota-exhausted` | 503 | All eligible upstream attempts failed because a provider reported exhausted balance, credits, quota, billing, or payment state. | Retry later only after the provider account is funded or quota is restored; include the request ID when escalating. | Inspect `request_attempts` for `upstream_quota_exhausted`, then verify provider account balance, billing, quota, and entitlement state. |
| `upstream-access-denied` | 503 | All eligible upstream attempts failed because provider credentials, entitlement, region, policy, or model access prevented serving the request. | Do not rotate the router caller token for this symptom. Contact the operator with the request ID. | Inspect `request_attempts` for provider access classes, check provider credentials and entitlement, and confirm whether fallback recovered any target. |
| `upstream-failed` | 502 | All eligible upstream attempts failed for another upstream error class, or a request was blocked by upstream payload controls such as private image URL egress policy. `error.details.reason_code` gives the sanitized cause when it is known, for example `upstream_bad_request` or `upstream_request_too_large`. | Retry only after checking whether the request shape is allowed; do not retry blocked private image URLs unchanged. If `reason_code` is `upstream_bad_request` or `upstream_request_too_large`, reduce request size/tool payload or contact the operator with the request ID. | Inspect request attempts, fallback behavior, provider status, sanitized upstream error details, request/translation shape rows, request-shape buckets, redirect responses, response-size limits, and image URL egress policy. |
| `upstream-timeout` | 504 | The upstream did not complete within configured timeout. | Retry with a smaller task or larger timeout if available. | Tune timeout, fallback, provider mix, or client token budget. |
| `metrics-forbidden` | 403 | `/metrics` was requested with a caller token that is not authorized for metrics. | Use `/v1/usage` for caller usage. | Grant metrics access through authorization policy or an existing `metrics_admin: true` operator caller. |
| `reports-forbidden` | 403 | `/admin/reports/*` was requested without an authorized admin subject or without `admin:security_reports` for security access reports. | Do not call admin report endpoints from application clients. | Grant authorization policy `admin:reports` and, when needed, `admin:security_reports` read/export policy only to approved admin subjects. |
| `security-reports-disabled` | 503 | A security access report API was requested while `server.admin_reports.security.enabled` is false. | Do not call security report APIs on deployments where security reports are disabled. | Enable security reports only after configuring report authorization and trusted proxy IP handling. |
| `reports-disabled` | 503 | Admin reports are unavailable because reporting is disabled or the usage DB is unavailable. | Retry only after an administrator enables reports. | Check `server.admin_reports` and `server.usage_db` configuration. |
| `invalid-report-filter` | 400 | An admin report filter, time range, row limit, sort key, direction, offset, or pagination cursor is invalid. | Use a bounded time range, a positive `limit`, a supported `sort`, `direction=asc` or `direction=desc`, and the unmodified `next_cursor` returned by the same endpoint/sort/direction. | Check `default_since`, `max_range`, `max_rows`, endpoint-specific sort keys, and whether the cursor is malformed, stale, tampered, or mismatched to the requested report. |
| `report-query-failed` | 500 | The report query failed. | Retry later or ask an administrator to inspect the request ID. | Inspect usage DB health and `admin_report_query_failed` router logs; PostgreSQL failures include safe `pg_code`, `pg_severity`, and bounded `pg_message` fields when available. |
| `content-forbidden` | 403 | A content-capture maintenance endpoint was requested without `content:capture` authorization. | Do not call content-capture admin endpoints from application clients. | Grant authorization policy `content:capture` `delete`/`purge` policy or use a compatible `content_admin: true` operator caller. |
| `admin-forbidden` | 403 | A browser-admin route was requested by an authenticated Basic subject without the required route permission. | Ask the administrator to grant the appropriate admin policy or route permission. | Verify the subject and authorization policy before enabling broader admin surfaces. |
| `license-missing` | 503 | License enforcement is enabled but no readable license file is available. | Contact the router operator with the request ID. | Mount the issued license file at `server.license.path` and verify permissions. |
| `license-invalid` | 503 | The license file is malformed, unverifiable, uses an unknown key, or otherwise cannot be trusted. | Contact the router operator with the request ID. | Replace the license with a valid operator-issued file; do not expose payloads or signatures in tickets. |
| `license-expired` | 403 | The license signature is valid but the license is expired. | Contact the router operator. | Renew or restore a valid license file, then restart or wait for recheck. |
| `license-not-yet-valid` | 503 | The license `not_before` time is in the future. | Contact the router operator. | Check the issued license dates and system clock. |
| `license-product-mismatch` | 503 | The license is not issued for Metrum AI Router. | Contact the router operator. | Install the correct product license. |
| `license-feature-forbidden` | 403 | The request uses a feature not enabled by the current license. | Use an enabled feature or ask the operator for access. | Review licensed feature gates for routing, reporting, dynamic score, TypeScript, external policy, contracts, rollups, or content capture. |
| `license-limit-exceeded` | 403 | The deployment exceeds a licensed limit such as model groups or callers. | Contact the router operator. | Reduce configured usage or update the license. |
| `license-volume-exceeded` | 429 | The license-wide lifetime request or token budget is exhausted. | Retry only after the operator installs a replacement or expanded license. | Review the license usage counters and install the contracted replacement or top-up license. |
| `license-window-exceeded` | 429 | The license-wide rolling request or token window is at its ceiling. | Retry after the licensed window clears. | Inspect current traffic and the license window limits. |
| `license-concurrency-exceeded` | 429 | The router-wide licensed in-flight request limit is reached. | Retry with backoff. | Inspect current in-flight traffic or update the license limit. |
| `license-skin-forbidden` | 403 | The license allows the requested model group but not the requested API skin. | Use an API shape allowed for the deployment. | Review `allowed_skins` in the active license. |
| `license-admin-limit-exceeded` | 403 | The configured admin subject count exceeds the licensed limit. | Contact the router operator. | Reduce configured admins or install a license with a larger admin limit. |
| `license-retention-limit-exceeded` | 403 | Configured retention exceeds the licensed maximum retention days. | Contact the router operator. | Lower retention settings or install a license with the contracted retention limit. |
| `license-instance-limit-exceeded` | 503 | The running instance is outside the licensed instance scope. | Contact the router operator. | Install the license issued for this deployment instance or correct instance binding. |
| `license-revoked` | 403 | The active signed revocation bundle revokes the current license. | Contact the router operator. | Install a replacement operator-issued license or update the revocation bundle. |
| `license-suspended` | 403 | The active signed revocation bundle suspends the current license. | Contact the router operator. | Resolve the deployment-policy hold or install an updated license and revocation bundle. |
| `license-superseded` | 403 | The active signed revocation bundle marks the current license as superseded. | Contact the router operator. | Install the replacement license identified through the approved support channel. |
| `license-revocation-required` | 503 | Revocation enforcement requires a current signed bundle, but no readable bundle is available. | Contact the router operator. | Mount the required signed revocation bundle at `server.license.revocation.path`. |
| `license-revocation-check-failed` | 503 | The configured revocation bundle is malformed, expired, untrusted, invalidly signed, or rolled back to an older epoch. | Contact the router operator. | Replace the revocation bundle with a current operator-issued signed bundle. |
| `license-clock-rollback` | 503 | The local wall clock moved backwards beyond tolerance. | Contact the router operator. | Correct system time and inspect the license state file. |

## Eligibility Requirements

The `no-eligible-target` response includes a `requirements` list. Examples:

- `text`: request needs text input support.
- `image`: request includes image input.
- `tools`: request includes tool definitions.
- `openai-chat_tool_passthrough`: OpenAI Chat tool payload must be preserved.
- `openai-responses_function`: Responses function tools are required.
- `anthropic-messages_client_tools`: Anthropic Messages client tools are required.

Resolution is usually a configuration update. The model group must contain at least one enabled target whose provider dialect and metadata satisfy those requirements.

When a model group has an optional contract, the same error can include safe `contract-*` requirement buckets. Examples include `contract-required-api-shape`, `contract-required-modality`, `contract-required-tools`, `contract-quality-floor`, `contract-validation-expired`, and `contract-no-validated-target`. These buckets mean the requested group was allowed for the caller, but no target inside that same group satisfied the configured contract after ordinary request eligibility.

## Max-Token Cap Errors

If a caller sets `max_tokens`, OpenAI Chat `max_completion_tokens`, or Responses `max_output_tokens`, the router skips targets known not to honor output caps when that metadata is configured. Keep a target cataloged but inactive for capped traffic by setting `honors_max_tokens: false` after a failed cap smoke. For OpenAI Chat requests that include both `max_tokens` and `max_completion_tokens`, `max_tokens` takes precedence.

Explicit output caps also affect quota admission. The router reserves estimated input tokens plus `max_tokens`, `max_completion_tokens`, or `max_output_tokens` before upstream calls, then reconciles the reservation to actual usage when the request finishes. Failed or canceled upstream calls release the reservation, and cache hits do not consume persisted token quota.

## Traffic-Shaped Responses

`traffic-shaped` is a router-side 429 used for configured caller burst smoothing. It is distinct from hard `rpm-exceeded`, `tpm-exceeded`, `concurrency-exceeded`, `quota-exhausted`, and lifetime-budget failures, and distinct from upstream provider 429s that can later become `503 upstream-rate-limited`.

Safe example:

```json
{
  "error": {
    "type": "traffic-shaped",
    "message": "caller traffic shaping limit exceeded for model group big-coder; retry later or reduce request burst",
    "request_id": "req_0123456789abcdef0123456789abcdef",
    "retry_after_seconds": 2,
    "bucket": "caller.input_tokens_per_sec"
  }
}
```

Clients should honor `Retry-After`, apply backoff, reduce concurrent bursts, reduce context size, or lower very large output caps. When queueing is enabled, a `traffic-shaped` response can mean the request waited up to the deployment's bounded `max_wait_ms` and still could not be admitted, or that the per-caller queue was already at `max_depth`. Administrators should inspect `request_usage.traffic_shape_applied`, `traffic_shape_decision`, `traffic_shape_scope`, `traffic_shape_bucket`, `traffic_shape_retry_after_ms`, `traffic_shape_queue_wait_ms`, `traffic_shape_estimated_input_tokens`, `traffic_shape_reserved_output_tokens`, `traffic_shape_total_reserved_tokens`, and child rows in `request_traffic_shape_events`.

## Upstream Failure Diagnostics

Terminal upstream failures include safe fields that help clients and operators distinguish provider responses from router-side admission errors:

- `X-Router-Error-Class`: sanitized upstream class such as `upstream_bad_request`, `upstream_rate_limited`, or `upstream_quota_exhausted`;
- `X-Upstream-Status`: upstream HTTP status when one was returned;
- `error.details.error_class` and `error.details.upstream_status`: JSON equivalents for clients that do not expose response headers;
- `error.details.reason_code` and `error.details.reason`: caller-actionable sanitized cause when the class is known, such as `upstream_bad_request` for rejected request shape/parameters or `upstream_request_too_large` for upstream payload-size rejection;
- `error.details.inbound_dialect`, `target_dialect`, `tool_count`, `tool_choice_mode`, `request_bytes_bucket`, `tool_schema_bytes_bucket`, `estimated_input_tokens_bucket`, `output_cap_field`, and `output_cap_bucket`: safe request-shape hints when available;
- `error.details.router_quota_state` and `error.details.router_key_state`: safe caller admission state at the time of the request.

An upstream `400` normally remains a `502 upstream-failed` terminal router response because the proxy could not satisfy the caller request, but its `error_class` and `reason_code` are `upstream_bad_request` and `retryable` is false in diagnostics. This usually means the selected upstream rejected the request shape or parameters; do not blindly retry the same payload. An upstream `413` returns `reason_code: upstream_request_too_large`, which means callers should reduce context, attachments, tool schemas, or output caps before retrying. A router-side `429` such as `traffic-shaped`, `tpm-exceeded`, or `rpm-exceeded` happens before upstream attempts and does not include `X-Upstream-Status`.

Image URL validation also uses the `upstream-failed` envelope with a safe
`reason_code`. `image_url_forbidden` means the URL scheme, literal address, or
initially resolved destination is not allowed. `image_url_dns_failure` means
the hostname could not be resolved, and `image_url_dns_timeout` means the
request-wide 500 ms DNS budget expired. The router does not include the URL in
the response or diagnostics. Use a reviewed public object-store URL or a data
URL; operators should keep private-address allowance disabled unless a private
VLM deployment has independent egress controls.

## Native Stream Failures

Same-dialect OpenAI Chat and Anthropic Messages streams commit the downstream
response when the first native SSE event is written. If the upstream fails or
the connection ends unexpectedly after that point, HTTP status and headers
cannot be replaced with a JSON error envelope, and the router does not replay
the request against a fallback target. The caller still observes the committed
HTTP `200`; no JSON or synthetic SSE error is appended. Diagnostics record
`499` for client cancellation or `502` for another committed interruption.
Clients should treat a stream that lacks its dialect's terminal event as
incomplete and may retry according to their idempotency policy. Preserve the
response's `X-Request-Id` for operator correlation.

Canceling the downstream request cancels the upstream request and is recorded
as a cancellation rather than a fallback opportunity. A normally completed
Chat stream may end with `[DONE]` or a terminal `finish_reason`; a requested
`stream_options.include_usage` usage event is forwarded when the upstream
provides it; the router does not synthesize a missing caller-visible usage
event. A committed stream that later fails releases its quota/license
reservation rather than charging partially observed streamed tokens. Native
PII-filtered streams preserve placeholders because safe
restoration cannot be performed independently across arbitrary SSE chunk
boundaries.

Safe upstream bad-request example:

```json
{
  "error": {
    "type": "upstream-failed",
    "message": "selected upstream rejected the request shape or parameters for model \"big-coder\" after 1 attempt(s); contact the router operator with the request_id",
    "details": {
      "model": "big-coder",
      "dialect": "openai-chat",
      "target_dialect": "openai-chat",
      "attempts": 1,
      "retryable": false,
      "request_id": "req_0123456789abcdef0123456789abcdef",
      "error_class": "upstream_bad_request",
      "upstream_status": 400,
      "reason_code": "upstream_bad_request",
      "reason": "selected upstream rejected the request shape or parameters",
      "tool_count": 11,
      "tool_choice_mode": "auto",
      "request_bytes_bucket": "gt-1mb",
      "output_cap_bucket": "omitted",
      "fallbackUsed": false
    }
  }
}
```

The response excludes raw prompts, image payloads, image URLs, tool schemas, tool outputs, bearer tokens, provider keys, router tokens, token hashes, raw upstream bodies, and raw provider headers.

`upstream_bad_request` usually means provider/request-shape incompatibility rather than a transient outage. Common causes are a provider rejecting a translated field, a target missing the right API skin, an unsupported forced tool choice, structured-output or reasoning controls sent to a target that has not validated them, image input sent to a text-only target, the wrong output-token cap field, request bytes or tool schema bytes beyond a target limit, or stale target metadata such as `force_store_false` on an upstream that rejects `store:false`. Callers should provide the request ID and avoid repeatedly sending the same large payload unchanged. Operators should group by provider, model, dialect, upstream status, sanitized upstream code/type/param, request-size bucket, tool-schema bucket, tool count, tool-choice mode, output-cap bucket, reasoning/image/structured-output flags, and request-shape fingerprint.

Safe large coding-agent example:

```json
{
  "error": {
    "type": "upstream-failed",
    "message": "all eligible upstream targets failed for model \"agent-validation\" after 1 attempt(s); contact the router operator with the request_id",
    "details": {
      "request_id": "req_0123456789abcdef0123456789abcdef",
      "model": "agent-validation",
      "dialect": "openai-chat",
      "attempts": 1,
      "error_class": "upstream_bad_request",
      "upstream_status": 400,
      "retryable": false,
      "fallbackUsed": false
    }
  }
}
```

The operator should inspect safe shape diagnostics for the same request ID, such as request bytes bucket, tool-schema bytes bucket, tool count, tool-choice mode, translated output cap, and sanitized provider code/param. They should reproduce with a sanitized synthetic fixture against a staging or smoke model group before changing broad production routes.

## Cursor And Large-Context TPM Troubleshooting

Router caller limits can include:

- `rpm`: requests per rolling minute;
- `tpm`: estimated input plus reserved output tokens per rolling minute;
- `concurrent`: in-flight request count.
- `traffic_shape`: optional short-burst smoothing for request starts and token-reservation throughput.

Large-context clients such as Cursor, opencode, coding agents, and repository-wide tools can hit `429 tpm-exceeded` even when daily or monthly budgets are healthy. A few 150K-token requests inside the same rolling minute can exceed TPM, especially when each request also reserves the requested output cap.

Caller guidance:

- reduce selected files, repository context, diff size, or prompt attachments;
- lower unrealistic output caps;
- retry after the rolling window clears;
- include `X-Request-Id` when escalating.

Administrator guidance:

- inspect usage by client, owner user, project, public token ID, requested model group, input-token bucket, max-token bucket, and quota bucket;
- raise TPM for trusted production keys when the workload is approved;
- route routine large-context work to cheaper or smaller groups only after those groups pass the workload verifier;
- distinguish router `429 rpm-exceeded`, `tpm-exceeded`, `concurrency-exceeded`, or `quota-exhausted` from upstream provider `429` attempts and client cancellations by reviewing `request_usage`, `request_attempts`, `request_trace_events`, and `request_errors`.
- distinguish `429 traffic-shaped` from hard `tpm-exceeded` by checking `traffic_shape_bucket`, queue wait, retry-after, and per-bucket rows in `request_traffic_shape_events`.

## Upstream Provider Quota And Billing Errors

Provider-side balance, credit, quota, billing, and payment failures are distinct from caller-token `quota-exhausted` responses. The router first tries eligible fallback targets. If a fallback succeeds, the caller receives the successful response and diagnostics record the failed attempt. If every eligible attempt fails with provider quota or billing signals, the caller receives `503 upstream-quota-exhausted`.

Provider/model shared traffic shaping is also distinct from caller `429` responses. It protects upstream account or model capacity shared by many callers, so the router returns `503 upstream-capacity-throttled` when all otherwise eligible targets are temporarily unavailable before an upstream call can start. The response includes a safe request ID, target count, and `Retry-After` when the current bucket or adaptive backoff window is calculable.

```json
{
  "error": {
    "type": "upstream-capacity-throttled",
    "message": "all currently eligible upstream targets for model \"default\" are temporarily capacity-throttled; retry later or contact the router operator with the request_id",
    "details": {
      "model": "default",
      "dialect": "openai-chat",
      "target_count": 2,
      "retry_after_ms": 1000,
      "retryable": true,
      "request_id": "req_0123456789abcdef0123456789abcdef",
      "fallbackUsed": false
    }
  }
}
```

Safe example:

```json
{
  "error": {
    "type": "upstream-quota-exhausted",
    "message": "upstream provider quota, credits, or billing limits were exhausted for model \"default\" after 2 attempt(s); retry later or contact the router operator with the request_id",
    "details": {
      "model": "default",
      "dialect": "openai-chat",
      "attempts": 2,
      "last_error": "upstream status 402 upstream provider quota, credits, or billing limit exhausted",
      "retryable": true,
      "request_id": "req_0123456789abcdef0123456789abcdef",
      "fallbackUsed": true
    }
  }
}
```

The error body is sanitized. It does not include provider account identifiers, raw upstream response bodies, upstream headers, provider API keys, router tokens, token hashes, prompts, images, or tool output.

## Troubleshooting With Request IDs

Administrators can use `X-Request-Id` with `/admin/reports/api/request-evidence?request_id=<request_id>` or the path-style drilldown `/admin/reports/api/request/<request_id>` to inspect:

- `request_usage` for terminal status, selected target, token counts, cost, and cache behavior.
- `request_attempts` for each provider/model attempt.
- `request_trace_events` for routing, fallback, timeout, and cache decisions.
- `request_upstream_shape_events` for provider/model/target admission, skip, rejection, and adaptive-backoff cooldown decisions.
- `request_upstream_error_details` for bounded allowlisted provider 4xx/5xx fields when diagnostics are enabled. Deployments can set `server.diagnostics.store_sanitized_upstream_errors: false` to suppress those rows.
- `request_errors` for sanitized terminal error summaries.

The evidence bundle also reports diagnostic completeness so operators can tell whether a section is present, not applicable, or unexpectedly missing. Evidence and diagnostic rows exclude prompt text, raw image payloads, raw image URLs, raw tool schemas, raw tool outputs, raw router tokens, token hashes, provider API keys, full upstream headers, cookies, OIDC tokens, full config, and unsanitized upstream bodies. The [Diagnostics Schema](./diagnostics-schema#fields-intentionally-not-persisted) is the canonical public reference for safe and forbidden persisted data.

For provider quota or billing incidents, look for `request_attempts.error_class = 'upstream_quota_exhausted'` and terminal `request_errors.error_type = 'upstream-quota-exhausted'`. A successful request can still have an `upstream_quota_exhausted` attempt row when fallback succeeded.

If governed content capture is enabled by an operator, captured content lives in separate content-capture tables and remains outside usage reports and diagnostics. Delete and retention-purge maintenance endpoints require `content:capture` `delete`/`purge` authorization; delete-by-request is scoped to the captured row's caller project/environment domain.

## Identifier transformation errors

| Error | Status | Meaning |
| --- | --- | --- |
| `identifier-transform-unavailable` | 503 | Rewrite mode has no initialized transform; routing fails closed. |
| `invalid-tool-call-id` | 400 | A prefixed ingress identifier is malformed, expired/unknown, or fails authentication. |
| `not-ready` | 503 on `/readyz` | Configuration or identifier transform is unavailable; validation details are never exposed. `/healthz` remains 200. |

Decode telemetry contains only `id-decode-unknown-epoch`,
`id-decode-auth-failed`, or `id-decode-malformed`, never the rejected identifier
or key material. Configuration rejection `F-011:
pii-filter-redact-and-restore-unsupported` disables unsupported PII restoration.
