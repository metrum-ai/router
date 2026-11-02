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

## Modes

- `redact_only`: replace configured matches with placeholders before upstream calls.
- `redact_and_restore`: redact before upstream and restore placeholders in buffered downstream text responses. Same-dialect native OpenAI Chat and Anthropic Messages streams preserve placeholders in caller-visible SSE; the router does not buffer native streams or attempt unsafe per-chunk restoration when a placeholder may span upstream chunks.
- `fail_on_match`: reject matching requests before target selection and upstream calls.

`fail_on_match: true` forces blocking behavior regardless of mode.

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
7. Confirm PII-enabled requests bypass response-cache reads and writes, including when a matching entry already exists.
8. Confirm same-dialect native Chat and Messages streams preserve placeholders, record `pii-response-restoration-skipped-native-stream`, and do not expose raw matched values.
9. Confirm requests over `max_replacements_per_request` return `pii-filter-blocked` and make no upstream attempt.

## Limitations

Regex filtering is not complete PII detection. Customer deployments should review each expression, set `max_replacements_per_request`, and use external DLP/privacy services for high-assurance detection. Keep external services trusted. Router-managed TypeScript contexts receive the redacted request after model-group PII filtering; external routing policy services receive raw/redacted request mirrors only with `external_policy.include_request: true`. Placeholder mappings remain request-local and are not sent to policy code or persisted. Native stream placeholder preservation is a transport limitation, not a guarantee about provider privacy, residency, retention, or training.
