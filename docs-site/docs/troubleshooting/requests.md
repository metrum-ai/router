---
title: Request Troubleshooting
doc_type: howto
---

# Request Troubleshooting

Every request returns an `X-Request-Id` header. Use that ID to join caller symptoms to logs, usage rows, upstream attempts, trace events, terminal errors, and browser report drilldown. The [Diagnostics Schema](../reference/diagnostics-schema) documents the safe columns available for request-level triage.

The workflow is symptom -> evidence -> fix:

| Caller symptom | Evidence to open | Common fix path |
|---|---|---|
| `401` or `403` | `/v1/models`, caller token state, admin/report/metrics policy. | Issue or rotate the token, grant the model group, or use the correct admin identity. |
| `429` before any upstream attempt | Quota, traffic-shaping, TPM/RPM, concurrency, and license-volume rows. | Adjust caller policy, queueing, or workload shape. |
| Upstream `429`, quota, or billing errors | Attempt rows, provider capacity shaping, adaptive backoff, fallback rows. | Tune provider shaping, route weights, fallback mix, or upstream account capacity. |
| `502 no-eligible-target` | Candidate and filter rows, request-shape diagnostics, provider catalog status. | Add compatible targets or correct modality/tool/context metadata. |
| `502 routing-policy-error` | Policy execution outcome, script/runtime class, external-policy duration and response class. | Restore the reviewed policy, reduce script concurrency, or use external-policy baseline rollback. |
| Slow or timed-out request | Attempt duration, TTFB, fallback, client cancellation, downstream throughput. | Isolate the provider/model/client path before changing the whole group. |
| Stream ends without a terminal event | Response `X-Request-Id`, committed attempt, cancellation/timeout state, proxy buffering. | Treat output as incomplete; fix upstream/intermediary streaming before retrying or changing weights. |
| Agent or large-context failure | Request bytes, tool-schema bytes, estimated-token buckets, output-cap buckets, dialect and bridge rows. | Route around incompatible targets or add validated `request_shape_support`. |

## 1. Capture The Caller View

Record these safe fields:

- UTC timestamp;
- request ID;
- HTTP status;
- router error code from `error.type`, if present;
- `error.details.error_class`, `error.details.upstream_status`, `X-Router-Error-Class`, and `X-Upstream-Status` when present;
- retry hints such as `Retry-After`, `retry_after_seconds`, or `error.details.retryable`;
- requested model group;
- API shape, such as Chat Completions, Responses, or Anthropic Messages;
- client base URL shape, such as `/v1` for OpenAI-compatible clients or `/anthropic` for Claude Code and Anthropic-compatible clients;
- client name, such as Codex CLI, Claude Code, Cursor, or an internal service;
- public token ID when available from reports;
- whether the request used streaming, tools, images, large input context, or a large output cap.

Do not record raw prompts, image payloads, bearer tokens, provider keys, tool outputs, or full request bodies unless a governed content-capture process is explicitly enabled for the deployment. Use the public token ID from diagnostics instead of raw router tokens or token hashes.

For the canonical downstream error fields and examples, see the [Error Reference](../reference/errors). Treat `request_id` as the handoff key: callers can share it safely, and administrators use it to open request evidence without asking for secrets or full payloads.

## 2. Check Caller Access

```bash
curl -i -H "Authorization: Bearer $ROUTER_TOKEN" \
  "$ROUTER_BASE_URL/v1/models"
```

Expected: the requested model group appears in the response. If it is absent, the caller token is not allowed to use that group or the group is not configured.

Common access outcomes:

| Status | Meaning | Operator action |
|---|---|---|
| `401` | Missing, malformed, expired, or invalid caller token. | Issue or rotate the caller token. |
| `403` | Caller is authenticated but not authorized for the surface or model group. | Review caller access, admin policy, or metrics/report role. |
| `403 metrics-forbidden` | Ordinary caller attempted `/metrics`. | Use a metrics-admin token only for metrics scraping. |
| `403 reports-forbidden` | Ordinary caller attempted admin reports. | Use an authorized admin report identity. |

## 3. Open Request Evidence

With an authorized admin report identity, open the safe evidence bundle:

```bash
curl -u admin:<password> \
  "$ROUTER_BASE_URL/admin/reports/api/request-evidence?request_id=<request_id>"
```

The request evidence bundle shows what the router safely knew and recorded: caller/project/client labels, public token ID when configured, requested model group, resolved group, selected upstream/provider model target, stored request-time token and cost fields, latency/throughput, quota/caller-token/cache state, traffic-shaping state, target candidate/filter summaries, attempts, sanitized upstream errors, and trace rows when those sections exist.

Use `diagnosticCompleteness` and `evidenceSections` to interpret gaps. `missing` on a failed request means a diagnostic section expected for that phase was not recorded; `not_applicable` means the request path did not reach that phase or the feature was disabled.

Request evidence bundles do not expose raw prompts, image URLs or payloads, tool schemas, tool outputs, provider API keys, router bearer tokens, token hashes, full upstream headers, unsanitized upstream bodies, cookies, OIDC tokens, or full config.

## 4. Check Quota And Token Admission

Router-side quota, traffic-shaping, and admission failures usually return `429`. Distinguish them from upstream provider `429` attempts:

- Router hard-limit failures such as `rpm-exceeded`, `tpm-exceeded`, `concurrency-exceeded`, and `quota-exhausted` appear as the terminal caller response before any upstream attempt.
- `traffic-shaped` appears as a terminal caller response with a safe bucket and `Retry-After` when a configured caller/server shaping bucket limits the burst.
- Upstream `429` attempts may be followed by fallback to another target.
- Terminal upstream failures include safe `X-Router-Error-Class`, `X-Upstream-Status`, `error.details.error_class`, `error.details.upstream_status`, `error.details.reason_code`, and `error.details.reason` fields. When available, the error body can also include safe request-shape hints such as dialect, tool count, tool-choice mode, request-size bucket, tool-schema bucket, estimated-input bucket, and output-cap bucket. Use these fields to separate upstream `400`/`413`/`429` provider responses from router-side `429` policy responses. `upstream_bad_request` usually means the selected upstream rejected the request shape or parameters; `upstream_request_too_large` means the request should be reduced before retry.
- `upstream_bad_request` usually points to provider/request-shape incompatibility, not caller quota. Do not blindly retry the same large payload; compare request-shape and translation-shape buckets first.
- Large-context developer tools can exhaust TPM through in-flight reservations even when daily or monthly budget remains available.

Use usage reports or admin browser troubleshooting buckets for quota, TPM/RPM, concurrency, traffic-shaping bucket, input-token, and max-token signals. For exact field names and retention classes, see the [Diagnostics Schema](../reference/diagnostics-schema).

Use Traffic tuning advisor when the question is whether to increase burst, change queueing, slow a caller, tune provider capacity, or route around an incompatible target:

```bash
router-usage-report \
  --driver sqlite \
  --db /app/state/usage.sqlite \
  --since 24h \
  --traffic-tuning-advisor \
  --caller-user <owner-user>
```

Interpretation examples:

- User sees errors but all shaping buckets were admitted or absent: treat `route_around_incompatible_target` as a request-shape/provider compatibility issue. Inspect upstream failures and request-shape failures instead of increasing burst or queue depth.
- User is being queued and cancellations increased: treat `disable_queue_for_latency_sensitive_client` as a signal to lower queue wait or fail fast for that client.
- Provider 429s affect multiple users: treat `investigate_provider_429_capacity` as shared capacity or entitlement work. Tune provider/model shaping, adaptive backoff, route weights, or upstream account limits before increasing one caller's burst.
- Provider 401/403/404 access failures are different from caller-token errors. Invalid provider credentials are terminal for the request; target-specific entitlement, region/project, policy, or model-access failures may route around to another eligible target. If all attempts fail this way, callers receive `503 upstream-access-denied` with a request ID.
- Large Cursor, Codex, Claude Code, or opencode payloads fail on selected upstreams: compare request-shape failure buckets, output-cap buckets, tool/modality metadata, and provider/model/dialect rows, then route around targets that cannot handle that shape.

## 5. Check Upstream Attempts

For a slow or failed request, inspect:

- selected provider/model/dialect;
- upstream status code;
- upstream duration and TTFB;
- timeout or cancellation flags;
- retryability;
- fallback transitions;
- sanitized terminal error class.

If only one provider/model/dialect is failing, isolate that upstream before changing the broader model group. If all targets are failing, inspect shared config, network, license, database, or caller request shape.

For `upstream-failed` with `error_class = upstream_bad_request`, start with shape and translation evidence before retry policy. Compare the failed provider/model/dialect with successful rows by upstream status, sanitized upstream code/type/param, request bytes bucket, tool-schema bytes bucket, tool count, tool-choice mode, structured-output flag, reasoning flag, image count, requested and translated output cap, request-shape fingerprint, and tool-schema fingerprint. Common fixes are to remove or lower an incompatible target, add `request_shape_support` limits, correct target encoding metadata, validate a bridge flag, or keep that provider in a staging group until a sanitized fixture passes.

## 6. Check Request Shape

Common request-shape causes:

- requested model group does not support the API skin used by the client;
- tool calls are sent to a target without validated tool support;
- image input is sent to a text-only target;
- estimated input plus output cap exceeds target context limits;
- request bytes or tool schema bytes exceed configured target request-shape limits;
- a forced tool-choice shape is not validated for the selected upstream;
- streaming behavior differs from the caller expectation.

Model-group contracts and provider catalog metadata should describe validated modalities, tools, dialects, pricing, and max-token behavior.

### Native Streaming

Same-dialect OpenAI Chat and Anthropic Messages requests use native upstream
SSE. Confirm that the selected target dialect matches the inbound dialect,
multiple deltas arrive before total completion, and the reverse proxy does not
buffer event streams. OpenAI Responses and cross-dialect bridges use unary
upstream calls with router-encoded caller streaming, so they have different
TTFB characteristics.

After the first native event, the response is committed: a later upstream error
cannot become a JSON error response and the router does not replay the request
to another target. The caller still sees HTTP `200`, with no appended synthetic
error event, while diagnostics record `499` for cancellation or `502` for
another committed interruption. A stream without its normal terminal event is
incomplete. Keep `X-Request-Id`, then inspect the selected attempt for timeout,
client cancellation, status, and response bytes. Failed committed streams
release quota/license reservations instead of reconciling partial streamed
tokens. For Chat callers that request `stream_options.include_usage`, verify a
compatible target received the option and emitted the final usage event; the
router does not synthesize one when the upstream omits it.

Native streams for groups using `pii_filter` preserve redaction placeholders.
Buffered responses can restore them, but arbitrary SSE boundaries do not allow
safe per-chunk restoration.

### Routing Policy And Dynamic Affinity

For `routing-policy-error`, identify whether the group uses `script` or
`external`. Script decisions run in fresh isolated VMs with a per-group
`script_max_concurrent` cap; queued decisions honor cancellation. External
policy calls use a bounded per-group client and propagate caller cancellation.
Use `external_policy.mode: baseline` to stop policy calls, `shadow` to record
recommendations without changing the target, and `enforce` only after shadow
evidence passes. Policy output is always checked against the already eligible
target list and cannot widen caller access.

For unexpected `dynamic_score` target changes, inspect the safe affinity
signal. Expired pins are reselected after the configured TTL; ineligible pins
are replaced when current tools, modality, API shape, contract, spend, or
health gates reject the old target. Pins are caller-isolated and process-local,
so they do not survive restart and are not shared automatically between
replicas.

### Reasoning Controls

For missing reasoning controls, first call `/v1/models` with the same caller token. A reasoning-enabled group advertises `supported_reasoning_levels` and `default_reasoning_level`. If those fields are absent, check whether the caller is allowed to the group, whether the reasoning target is active under `models.<group>.targets[]`, and whether the active target skin matches the client surface: OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, or Anthropic Messages `thinking`. Catalog-only metadata does not make a group reasoning-capable.

### Large Agent Payloads

For “small requests work but large Cursor/Codex/Claude Code requests fail,” inspect the request drilldown or usage DB rows for `request_token_estimates`, `request_target_candidates`, and `request_target_filter_reasons`. Safe fields to compare are estimated total input tokens, requested output cap, total reserved tokens, request bytes, target `context_tokens`, context headroom, `request_bytes_fit`, `tool_schema_fit`, and bounded reasons such as `request-shape-context-exceeded`, `request-shape-max-request-bytes`, or `request-shape-tool-schema-bytes`. Operators can replay a sanitized synthetic shape when the deployment has a safe smoke caller, a dedicated validation group, and report access. Use that validation group instead of changing a broad production coding group just to reproduce the shape. These diagnostics intentionally do not contain raw prompts, raw tool schemas, images, bearer tokens, token hashes, provider keys, or full config.

If the large request reached upstream and returned `upstream_bad_request`, add `request_attempts`, `request_translation_shapes`, and sanitized upstream error details to the comparison. The likely issue is no longer just router-side eligibility; it may be a provider payload limit, unsupported field combination, wrong output-token cap name, unsupported forced tool choice, unsupported structured-output or reasoning control, or an active target that passed ordinary text but not representative coding-agent payloads.

### Claude Code Model Selection

For Claude Code sessions that appear to stop abruptly even when terminal request rows show HTTP 200, first prove model resolution with the same caller token. `/v1/models` should return the intended group, and Claude Code should set both its main model and subagent model to that group. In request evidence, blank `requested_model` or `resolved_group` values with `403 model-not-allowed` point to model selection rather than provider failure. If model resolution is correct, compare the selected Anthropic Messages target or explicitly validated bridge target, fallback flag, timeout/client-canceled markers, visible output size where available, finish/stop reason, and token totals. Very short visible output with large token totals can indicate a target-quality or reasoning-budget problem even when transport succeeded.

For Claude Code setup, prefer `ANTHROPIC_BASE_URL=https://<router-host>/anthropic` with `ANTHROPIC_AUTH_TOKEN` and unset `ANTHROPIC_API_KEY`. If an older setup still uses the router origin and `/v1/messages`, it should remain compatible, but troubleshoot it as legacy configuration and migrate the base URL before comparing new behavior. Subagents can send a different model value than the main session; compare `ANTHROPIC_MODEL`, `CLAUDE_CODE_SUBAGENT_MODEL`, and any subagent frontmatter `model` against `/v1/models` with the same token.

For agent compatibility regressions, run a sanitized fixture matrix against a dedicated smoke group that the smoke caller is allowed to use. The fixture matrix should cover Codex Responses reasoning/tools, Cursor Chat tools and bridge shapes, Claude Code Messages thinking/tools, opencode/aider Chat flows, large tool schemas, provider-skin mismatch, no-eligible diagnostics, upstream entitlement/fallback, and upstream error classification. Use the emitted request IDs to compare selected target, bridge direction, translated reasoning control, attempts, fallback, and sanitized error class in reports. If the deployment lacks a smoke group, caller access, or report DB access, record the prerequisite gap rather than changing an active production group solely for the test.

### Images, Tools, And Bridges

For Cursor-style OpenAI Chat requests with tools and an image, check whether any target in the requested group supports both OpenAI Chat tools and image input. If not, the router should return `502 no-eligible-target` with zero upstream attempts. Candidate/filter rows should show safe reasons such as `input-modality-image` or `dialect-tool-passthrough`.

For OpenAI Chat requests that are intended to use a Responses-only target, confirm the selected group has a target with `dialect: openai-responses` and `bridges.chat_to_responses.enabled: true`. Text requests need only the bridge opt-in. Tool requests also need bridge `tools: true` plus target `tool_support.openai_responses`; `tool_choice`, parallel tool calls, structured output, images, and reasoning each need matching bridge metadata. Chat→Responses streaming is incremental with `server.streaming.translator: incremental` (the default); `synthesized` uses a unary upstream call. Reasoning requests also need compatible target `reasoning` metadata. If stateful sessions are enabled, the caller must send the configured session header. `backend: memory` is for local, single-process, or sticky-routed deployments; multi-replica deployments should use explicitly configured `backend: redis` with env credential refs, TLS where appropriate, and bounded timeouts. Common filter reasons include `chat-to-responses-bridge-disabled`, `chat-to-responses-tools-unsupported`, `chat-to-responses-tool-choice-unsupported`, `chat-to-responses-reasoning-unsupported`, and `chat-to-responses-image-unsupported`.

When the bridge is selected, request evidence should show `inbound_dialect` as `openai-chat`, target/attempt dialect as `openai-responses`, `bridge_direction = chat_to_responses`, and a translation-shape row for `/v1/responses`. Stateful session validation should show a first upstream request without `previous_response_id` and a second same-session upstream request with the prior Responses `id`; operational traces use safe event names such as `bridge_session_requested`, `bridge_session_lookup_hit`, and `bridge_session_previous_response_applied`. Redis backend misses or errors should continue stateless and emit `bridge_session_lookup_miss` or `bridge_session_backend_error` without raw Redis credentials or session headers. If an injected continuation is stale, expected trace events are `bridge_session_previous_response_stale_purged` and `bridge_session_stateless_retry`. Use translation field events for safe field names and actions only; they intentionally do not store prompts, tool schemas, tool outputs, images, session header values, bearer tokens, token hashes, provider keys, or full config.

If every target is skipped, callers receive `502 no-eligible-target` before upstream with a request ID. Recovery is usually a config change: add accurate `context_tokens` or `request_shape_support`, remove a too-small target from the affected group, or keep the target in a smoke group until a large-payload validation passes.

For `/v1/responses` requests that should be able to use Chat-only upstreams, check whether the target has explicit `responses_to_chat` metadata for the requested shape. Chat-bridged targets skip unsupported fields before upstream. Common safe filter reasons include `responses-to-chat-bridge-disabled`, `responses-to-chat-previous-response-id`, `responses-to-chat-hosted-tools`, `responses-to-chat-tool-choice`, `responses-to-chat-image`, `responses-to-chat-reasoning`, `responses-to-chat-structured-output`, and `responses-to-chat-streaming`. A successful bridge keeps the caller-facing response in Responses format while usage shows inbound `openai-responses`, target `openai-chat`, and bridge direction `responses_to_chat`; reasoning-preserving bridge attempts also show `translated_reasoning_control = reasoning_effort`.

For reasoning bridge failures, use the request ID to compare four safe fields: inbound dialect, selected or skipped target dialect, `bridge_direction`, and `translated_reasoning_control`. A native Chat reasoning pass should show `reasoning_effort`; native Responses should show `reasoning`; native Messages should show `thinking`. Chat-to-Responses reasoning should additionally show `bridge_direction = chat_to_responses` and requires `bridges.chat_to_responses.reasoning: true`. Responses-to-Chat reasoning should fail with `responses-to-chat-reasoning` unless the target explicitly validates `responses_to_chat.reasoning`.

Examples of request-shape confusion:

- Codex usually sends `/v1/responses`; a group with only Chat targets needs a validated Responses-to-Chat bridge, and `previous_response_id` is not supported by the stateless bridge.
- Claude Code sends Anthropic Messages requests, preferably through `/anthropic/v1/messages`; a Chat or Responses reasoning target does not satisfy Anthropic `thinking` unless an Anthropic-compatible target or explicitly validated Messages bridge is active.
- Cursor, opencode, and aider may use OpenAI Chat or Anthropic-compatible shapes depending on client configuration. If a client sends a Responses-style `reasoning` object to `/v1/chat/completions`, troubleshoot the stored Chat shape and bridge metadata rather than assuming a Responses target was used.

## 7. Verify Recovery

After a config, credential, quota, or upstream fix:

```bash
curl -fsS "$ROUTER_BASE_URL/readyz"

curl -fsS "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "replace-with-allowed-model-group",
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "max_tokens": 16
  }'
```

Then confirm the request appears in usage reports with the expected provider/model, status, latency, cost, and fallback state.
