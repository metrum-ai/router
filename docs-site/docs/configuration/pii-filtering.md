---
title: PII Filtering
doc_type: howto
---

# PII Filtering

Model groups can redact configured text patterns before the router sends a request to an upstream model. This is useful when a deployment needs central privacy controls for LLM and agent traffic without changing every client.

PII filtering is deployment-defined. Group names, rules, and placeholder names below are examples. Set `secrets_enabled: true` to add the bounded built-in detector pack for private-key blocks, common AWS/GitHub/Slack/Stripe, OpenAI, Anthropic and Google provider credentials, router tokens, JWTs, bearer authorization values, and credential assignments. Built-in matches use the same `mode`, `apply_to`, and replacement limit as configured rules. This pack is heuristic and does not replace a full DLP product.

The added DLP processing cost was measured with deterministic Go microbenchmarks
(`-benchtime=200ms -count=3`) on Go 1.27.4 and an AMD EPYC 9R14 CPU. Values are
the median of three runs:

| Path | Input | Measured CPU time |
| --- | --- | ---: |
| Request matcher (`BenchmarkSecretMatcher`) | 2.75 KiB benign request | 0.741 ms/request |
| Stream window (`BenchmarkSecretStreamWindow`) | 33.3 KiB response in 128-byte deltas | 54.2 ms/stream |

These are local microbenchmarks, not end-to-end latency guarantees. The stream
measurement includes scanning a complete 33.3 KiB response. The current
implementation rescans retained text on each chunk, so worst-case work is
quadratic in the held length and bounded by the 64 KiB candidate cap; incremental
scanning may reduce this cost later. First-token delay
also includes the ordinary 512-byte rolling tail, a plausible JWT held from its
start up to 12,290 bytes, or a PEM block held through its end marker up to 64 KiB.

With `secrets_enabled`, text-only streams that use only the built-in rules use
a rolling inspection tail so credentials split across adjacent SSE chunks are
redacted before release. PEM blocks are held through their matching end marker,
up to 64 KiB. A plausible JWT is held from its `eyJ` marker while its bounded
segments are inspected; segments over 4,096 bytes fail closed. A single
provider-token or credential-assignment candidate longer than the 256-byte
token window fails the stream closed rather than releasing a partial match.
For streamed responses, `redact_only` redaction is best-effort when a secret is
split across stream events (for example an added snapshot and the following
delta). `fail_on_match` blocks the response in those cases and is the mode to
use when a guarantee is required. A JSON-encoded tool-call argument is decoded
before inspection; string values
are checked with their property names as `key=value`, while header-shaped
values are also checked as `key: value`, including JSON-escaped characters.
Object-valued Chat arguments and Anthropic `tool_use.input` receive the same key/value
inspection. If an argument string is malformed or truncated, the filter scans a
best-effort normalized copy in both forms so partial JSON cannot hide a credential. Responses
function-call arguments and custom tool input are also redacted in output snapshots.
For streamed tool calls, enable
`tool_routing.streaming.hold_until_validated` so complete bounded arguments are
held, scanned and redacted before release; `fail_on_match` blocks a call with a
secret. Without this hold, a tool-bearing stream on a DLP-enabled group fails
closed with HTTP 400 `secret-dlp-stream-inspection-required`. Enable
`secrets_enabled` only on groups that need this protection. Streams with custom
rules also fail closed because those expressions
do not declare a maximum match length. The 512-byte ordinary rolling tail is
extended from the start of any unresolved JWT or PEM candidate. Response
redaction runs only when
`secrets_enabled` is active and `apply_to.messages` is enabled; custom-only PII
configurations keep their existing response behavior.

## Model Group Configuration

```yaml
models:
  sensitive-workloads:
    strategy: weighted
    pii_filter:
      enabled: true
      secrets_enabled: true
      mode: redact_only
      max_replacements_per_request: 200
      apply_to:
        system: true
        messages: true
        responses_input: true
        tool_results: true
        image_urls: false
      rules:
        - name: email
          expression: '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
          placeholder_prefix: EMAIL
        - name: us_phone
          expression: '\b(?:\+1[-. ]?)?\(?[2-9]\d{2}\)?[-. ]?[2-9]\d{2}[-. ]?\d{4}\b'
          placeholder_prefix: PHONE
        - name: us_ssn
          expression: '\b\d{3}-\d{2}-\d{4}\b'
          placeholder_prefix: US_SSN
    targets:
      - { provider: private-vllm, model_ref: internal-sensitive-model, weight: 70 }
      - { provider: baseten, model_ref: gpt-oss-120b, weight: 30 }
```

With this configuration, text such as:

```text
Email jane.doe@example.com or call 415-555-0199.
```

is sent upstream as:

```text
Email [EMAIL_1] or call [PHONE_1].
```

Placeholder mappings are kept in memory for the request lifecycle and are not
persisted by default.

When a request exceeds `max_replacements_per_request`, the router returns
`pii-filter-blocked` before target selection or any upstream call. Treat the cap
as a fail-closed safety limit, not as a partial-redaction mode.

Placeholders are caller-visible on every path, including buffered responses and
router-generated SSE. Response restoration is **not configurable**: startup
validation rejects `mode: redact_and_restore` and any explicit
`restore_response` value with
`F-011: pii-filter-redact-and-restore-unsupported`. The router does not buffer
streams to restore placeholders and does not attempt unsafe per-chunk
restoration when a placeholder could span upstream chunks. See
[Architecture Limitations](../reference/architecture-limitations).

## Modes

- `redact_only`: send placeholders upstream and return placeholders downstream.
- `fail_on_match`: reject the request before upstream target selection when any
  rule matches.

`fail_on_match: true` can also be used with `redact_only` to force blocking
behavior. An omitted `mode` defaults to `redact_only`, and an omitted `apply_to`
scope defaults to `true` for `system`, `messages`, `responses_input` and
`tool_results`, while `image_urls` defaults to `false`.

`redact_and_restore` and `restore_response` are rejected by
configuration validation (F-011), so a group that needs placeholders removed
from caller-visible text must restore them in the caller.

## Request Surfaces

PII filtering applies to normalized text across supported caller APIs:

- OpenAI Chat `messages[].content`
- OpenAI Responses `input`
- Anthropic Messages `messages[].content`
- text parts in multimodal messages
- tool-result text when `apply_to.tool_results: true`

The router preserves image parts, tool-call IDs, tool schemas, usage fields, request IDs, provider metadata, and model metadata. Image URLs are not filtered unless `apply_to.image_urls: true`. When secret filtering is enabled for streaming, Chat refusal deltas use the same per-choice rolling inspection as content and reasoning; `redact_only` replaces matching credentials, and `fail_on_match` blocks them before forwarding.

Scope ownership follows what a value is, not only which field holds it: `apply_to.system` covers instruction text in `system`/`developer` messages and in `content` fields on those items, and `apply_to.responses_input` covers the caller's Responses `input` array including its items.

Translated targets build a system prompt from several caller messages or text parts. The router filters that assembled instruction at the boundary where it is transmitted, so a pattern that only matches after the join (for example a value split across two leading system messages) is replaced with a placeholder before dispatch. The instruction is filtered when `apply_to.system` or `apply_to.messages` is enabled; disabling both leaves it unfiltered. The same text is part of response-cache identity, so two requests whose transmitted instructions differ cannot share a cache entry.

Responses system-message histories are served by Anthropic and Bedrock Messages targets because the assembled instruction is filtered at its transmitted boundary; `fail_on_match` blocks before dispatch. If a PII-enabled group disables both `apply_to.system` and `apply_to.messages`, the router cannot redact that instruction and Messages targets stay excluded (`system_pii_unsupported`). Responses developer-role histories stay excluded from them (`developer_pii_unsupported`); an eligible target in another dialect can still be selected.

User code never runs inside the router process. External routing policy services receive only safe derived context by default. If `external_policy.include_request: true` is explicitly enabled for a trusted policy service, the external payload includes redacted `request` and `text` fields. Placeholder mappings remain request-local and are not sent to policy code.

## Governed content capture

PII filtering does not enable durable capture. Governed content capture is a
separate disabled-by-default feature. If explicitly enabled, it requires
redaction plus AES-256-GCM application encryption, a configured `local_key_id`,
local key material supplied through `CONTENT_CAPTURE_LOCAL_KEY` in the deployment secret boundary, and
authorized retention/deletion operations. Its usage-database expiry class is
configured under `server.retention`.

## API Examples

OpenAI Chat:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "sensitive-workloads",
    "messages": [
      { "role": "user", "content": "Email jane.doe@example.com or call 415-555-0199." }
    ],
    "max_tokens": 64,
    "stream": false
  }'
```

OpenAI Responses:

```bash
curl "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "sensitive-workloads",
    "input": "Email jane.doe@example.com before drafting the reply.",
    "max_output_tokens": 64,
    "stream": false
  }'
```

Anthropic Messages:

```bash
curl "$ROUTER_BASE_URL/anthropic/v1/messages" \
  -H "x-api-key: $ROUTER_TOKEN" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "sensitive-workloads",
    "max_tokens": 64,
    "messages": [
      { "role": "user", "content": "Summarize account 123-45-6789 for jane.doe@example.com." }
    ]
  }'
```

For all three APIs, the upstream target receives placeholders such as
`[EMAIL_1]` and `[US_SSN_1]`, and the caller receives the same placeholders back:
restoration is rejected by configuration validation (F-011) on unary and
streaming paths alike.

## Logging And Usage

Usage logs and the usage database record safe scalar metadata:

- `pii_filter_applied`
- `pii_filter_mode`
- `pii_filter_replacements`
- `pii_filter_rule_count`

They do not store raw matched values or placeholder mappings by default. Diagnostics and metrics must also avoid raw matched values.

## Operational Guidance

Regex filters are a practical gateway control, not a complete legal or compliance-grade PII detector. Use narrowly reviewed expressions, set `max_replacements_per_request`, and test each model group with representative prompts before rollout.

For production-grade detection, integrate an external DLP/privacy service through a deployment-owned policy service or a future managed detector. External routing policy services do not receive request text by default; keep any service with `external_policy.include_request: true` in trusted infrastructure because that opt-in can share request content after configured router redaction.

## Smoke Test

Use a test upstream or a private validation group and confirm the upstream receives placeholders, not raw values. This OpenAI Chat example should be repeated for any Responses, Anthropic Messages, tool-result, and image-bearing traffic the model group will accept:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "sensitive-workloads",
    "messages": [
      { "role": "user", "content": "Email jane.doe@example.com or call 415-555-0199." }
    ],
    "max_tokens": 64,
    "stream": false
  }'
```

Check the upstream capture, router logs, and usage rows for the same `X-Request-Id`. Raw matched values should not appear upstream or in diagnostics.
Also test a request with more matches than `max_replacements_per_request`; it should fail with `pii-filter-blocked` and produce no upstream request.

Repeat the Chat request with `"stream": true` against a same-dialect test
target. Confirm the upstream receives placeholders and that caller-visible SSE
keeps the same placeholders, so no raw matched value crosses either boundary.
A `pii_filter` with `mode: redact_and_restore` or `restore_response` must fail
startup with `F-011: pii-filter-redact-and-restore-unsupported` rather than
silently serving placeholders.
