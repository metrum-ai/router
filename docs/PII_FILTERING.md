# PII Filtering Design Notes

Metrum AI Router supports model-group-level PII filtering through `models.<group>.pii_filter`.

## Design

- Filtering is configured per model group.
- Rules are deployment-owned regular expressions with a typed `placeholder_prefix`.
- The router applies filtering after caller auth, model access, and admission checks, and before target selection, cache-key generation, upstream encoding, routing-policy inputs, and provider calls.
- Requests evaluated against PII-enabled groups bypass response-cache reads and writes, including when no rule matches. Existing entries are not purged; if filtering is later disabled, those entries can be used until expiry or cache reset. For a strict cutover, pause and drain in-flight requests and confirm every replica has loaded the enabled policy before resuming. In-flight requests evaluated before activation may still use or populate the cache.
- Placeholder mappings are kept in memory for the request lifecycle only.
- The request object passed to TypeScript scripts is the redacted request, so `ctx.request.raw` does not carry configured PII matches after filtering.
- External routing policy services receive safe derived request context by default. If `external_policy.include_request: true` is explicitly enabled for a trusted service, the external policy `request` and `text` fields are built from the redacted request and do not carry configured PII matches after filtering.
- Raw request bodies are redacted for both ordinary and tool passthrough requests while preserving tool-call IDs, tool schemas, model names, roles, image URLs by default, and provider metadata.
- Scope ownership follows what a value is, not only which key holds it: `apply_to.system` covers instruction text wherever it sits (a `system`/`developer` message or a `content` field on such an item), and `apply_to.responses_input` covers the caller's Responses `input` array, including its items.
- Translated targets assemble a system prompt from several caller messages or text parts. `assembleTargetInstruction` is the single canonical representation for that text: the filter redacts each surface the caller can transmit, the bridge transmits the stored redacted text, and the response-cache key includes it. A match that exists only after the join is therefore replaced before dispatch, and two different transmitted instructions cannot share a cache entry. The instruction is filtered when `apply_to.system` or `apply_to.messages` is enabled; disabling both keeps the documented passthrough.
- Remaining limit: Responses system-message histories are still excluded from Anthropic and Bedrock Messages targets on PII-enabled groups (`system_pii_unsupported`), and developer-role histories stay excluded. Lifting that fail-closed guard is tracked in the same issue and requires an independent security review.

## Modes

- `redact_only`: replace configured matches with placeholders before upstream calls. This is the only redacting mode that passes validation.
- `fail_on_match`: reject matching requests before target selection and upstream calls.

`fail_on_match: true` forces blocking behavior regardless of mode.

Restoration is not configurable. Startup validation rejects `mode: redact_and_restore` and any explicit `restore_response` value with `F-011: pii-filter-redact-and-restore-unsupported` (`validatePIIFilter` in `internal/router/config.go`), so placeholders are caller-visible on every path, buffered and streaming. The restore code path (`piiRestoreEnabled`, `restorePIIPlaceholders`) and the `pii-response-restoration-skipped-native-stream` warning remain in the tree but cannot be reached from a validated configuration; the router never buffers native streams to restore placeholders that may span upstream chunks. Lifting F-011 is tracked separately and requires the canonical instruction and cache-identity work plus an independent security review.

When a request would exceed `max_replacements_per_request`, the router fails closed with `pii-filter-blocked` before selecting or calling an upstream target. The limit is not a fail-open truncation control; raise the configured cap only after testing that logs, diagnostics, policy payloads, and upstream requests still contain placeholders rather than raw matched values.

## Persistence

Usage JSONL and DB rows store only safe scalar metadata:

- `pii_filter_applied`
- `pii_filter_mode`
- `pii_filter_replacements`
- `pii_filter_rule_count`

Do not store raw matched values, regex capture groups, placeholder-to-original mappings, raw prompts, raw images, raw tool outputs, bearer tokens, provider keys, or token hashes in usage rows, diagnostics rows, metrics, or ordinary logs.

If an enterprise needs durable request/response content capture, use the governed content-capture design separately. Do not turn diagnostics or PII filtering into accidental content archives.

## Validation

Before rollout:

1. Validate config startup rejects invalid regexes, missing placeholder prefixes, duplicate rule names, and negative replacement limits.
2. Smoke OpenAI Chat, OpenAI Responses, and Anthropic Messages shapes.
3. Smoke tool-result text when the group supports tools.
4. Confirm upstream receives placeholders, not raw matched values.
5. Confirm usage DB, JSONL logs, diagnostics, and metrics contain only safe metadata.
6. Confirm TypeScript payload captures contain placeholders in `ctx.request.raw`, not raw matched values. For external policies, confirm the default payload omits request mirrors; if `external_policy.include_request: true` is approved, confirm external policy `request` and `text` contain placeholders.
7. Confirm PII-enabled requests bypass response-cache reads and writes, including when a matching entry already exists, and that two requests whose transmitted instructions differ in any part get different cache keys (`targetCacheKey`).
8. Confirm same-dialect native Chat and Messages streams return placeholders to the caller and do not expose raw matched values. Restoration is unreachable while F-011 rejects `redact_and_restore` and `restore_response`, so a configuration that sets either must fail startup.
9. Confirm requests over `max_replacements_per_request` return `pii-filter-blocked` and make no upstream attempt.

## Limitations

Regex filtering is not complete PII detection. Customer deployments should review each expression, set `max_replacements_per_request`, and use external DLP/privacy services for high-assurance detection. Keep external services trusted. Router-managed TypeScript contexts receive the redacted request after model-group PII filtering; external routing policy services receive raw/redacted request mirrors only with `external_policy.include_request: true`. Placeholder mappings remain request-local and are not sent to policy code or persisted. Native stream placeholder preservation is a transport limitation, not a guarantee about provider privacy, residency, retention, or training.
