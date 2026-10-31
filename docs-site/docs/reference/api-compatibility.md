---
title: API Compatibility
doc_type: reference
---

# API Compatibility

Metrum AI Router exposes OpenAI-compatible and Anthropic-compatible HTTP surfaces so clients can keep familiar SDKs while routing, provider credentials, policy, quotas, and accounting stay server-side.

The router endpoint is deployment-specific. Use the base URL and model groups issued by your administrator or Metrum-managed instance.

## Supported Surfaces

OpenAI-compatible clients use the `/v1` base URL:

| Endpoint | Compatibility target | Typical clients |
|---|---|---|
| `/v1/chat/completions` | OpenAI Chat Completions-style requests | OpenAI SDK chat clients, Warp-style OpenAI-compatible agents |
| `/v1/responses` | OpenAI Responses-style requests | Codex CLI, Responses-compatible agent frameworks |
| `/v1/responses/input_tokens` | Unbilled input counting through a native Responses target | SDK token budgeting |
| `/v1/responses/compact` | Metered stateless compaction through a native Responses target | Conversation compaction |
| `/v1/models` | OpenAI-style model discovery | Client setup and allow-list discovery |
| `/v1/codex/models.json` | Codex-native local model catalog | Codex CLI fetch-then-run setup |
| `/v1/usage` | Router usage lookup | Authenticated caller quota and usage |
| `/admin/reports/api/quota-status` | Admin live quota remaining | Reports-admin remaining/limits for one or many callers |

### Unsupported OpenAI endpoints

After caller authentication, legacy text completions and moderations return
501 `unsupported_endpoint`; neither is planned for v4.5. Embeddings and stored
Responses retrieval, deletion, input-item listing, and cancellation also return
501 in v4.5; their implementation is deferred to v4.6. Files, uploads, batches,
images, audio, vector stores, assistants, threads, fine-tuning, realtime, videos,
containers, conversations, agents, ChatKit threads, and skills are outside the
v4.5 scope and return the same error. These errors have `message`, `type`,
`param`, and `code` fields. See the [error reference](errors).

Responses input counting requires a native Responses target in the requested
model group. It observes caller request rate and concurrency limits, but does not
bill tokens; the audit record has `endpoint=responses.input_tokens`. A request
with `previous_response_id` returns 501 until stored-response affinity is available.
Responses compaction likewise requires a native Responses target and applies the
same token admission and metering as Responses. It bypasses the response cache;
`previous_response_id` is rejected with 501 until affinity is available.

Anthropic legacy `/v1/complete`, message batches, beta files, and skills are
not planned for v4.5 and return authenticated 501 errors in Anthropic format.
`/v1/messages/count_tokens` remains supported.

Anthropic-compatible clients use the `/anthropic` base URL:

| Endpoint | Compatibility target | Typical clients |
|---|---|---|
| `/anthropic/v1/messages` | Anthropic Messages-style requests | Claude Code CLI, Anthropic-compatible clients |
| `/anthropic/v1/messages/count_tokens` | Native Anthropic count proxy; estimated fallback for other transports | Unbilled Claude Code compaction and compatibility checks |

Legacy Anthropic aliases remain available for existing clients: `/v1/messages` and `/v1/messages/count_tokens`. New setup should prefer `/anthropic` so OpenAI-compatible and Anthropic-compatible client configuration stays visibly separate.

Model discovery stays on `/v1/models` for both setup flows. An Anthropic-compatible client should call `/v1/models` with the same router token, choose one returned deployment-defined model group, then send Messages traffic to `/anthropic/v1/messages`.

`GET /v1/codex/models.json` uses the same caller-token authentication and allow-list boundary as `/v1/models`, then limits the result to groups eligible for Codex's OpenAI Responses requests. It returns `{ "models": [...] }` containing safe group metadata: group slug/display name/summary, context limits, reasoning levels, tool and image capability flags, truncation policy, and Codex-safe defaults. Those fields come only from native Responses targets or exact validated Responses-to-Chat bridge capabilities; Chat-only and Anthropic-only targets are not advertised. Reasoning-default fields are present only when an eligible non-tool-only target advertises active reasoning. It never returns upstream provider or model identifiers, target weights, URLs, pricing, credentials, caller identity, tokens, token hashes, or routing internals. Clients should accept additive fields safely. See [Codex CLI](../getting-started/codex-cli) for the required local-file workflow.

Operational and administrative endpoints remain on the router origin:

| Endpoint | Purpose | Typical clients |
|---|---|---|
| `/readyz`, `/healthz`, `/version` | Router operational endpoints | Load balancers and operators |
| `/admin/auth/check` | Browser-admin Basic Auth validation stub | Operators enabling browser-admin surfaces |
| `/admin/auth/login`, `/admin/auth/callback`, `/admin/auth/me`, `/admin/auth/logout` | Browser-admin OIDC session routes | Operators enabling OIDC browser admin surfaces |
| `/admin/reports/*` | Router-specific admin reports | Authorized administrators |

`/metrics` is an operator telemetry API. It requires a caller token whose subject is authorized for `metrics` `read`; existing `metrics_admin: true` caller entries receive compatible authorization grants at startup.

Caller tokens are checked by SHA-256 hash. Unknown or missing tokens return `401 unauthorized`. Configured inactive keys return safe status-specific `403` errors after token match, including `key-disabled`, `key-suspended`, `key-expired`, and `key-rotated`. Config validation requires every enabled key to reference an active `owner_user`, active project, and active project membership, so inactive users/projects/memberships are caught before startup.

Content-capture maintenance endpoints are administrative APIs, not model APIs. `DELETE /v1/content-captures/<request_id>` requires `content:capture` `delete` authorization in the captured row's caller project/environment domain; `POST /v1/content-captures/purge-expired` requires `content:capture` `purge`. Existing `content_admin: true` caller entries receive compatible authorization grants for their own domain. These endpoints never return captured content.

`/admin/auth/check` is not a model API. It is available only when `server.admin_auth.basic.enabled: true`; missing or invalid HTTP Basic credentials return `401`, valid credentials without the route permission return `403 admin-forbidden`, and valid credentials with `admin:auth:read` return safe subject metadata.

OIDC admin auth routes are not model APIs. They are available only when `server.admin_auth.oidc.enabled: true`. Login redirects to the IdP, callback creates a server-side session after OIDC verification, `/admin/auth/me` returns safe subject metadata, and logout invalidates the session. Excess pending login starts from one client return `429 oidc-login-rate-limited` responses.

`/admin/reports/*` is not a model API. It is disabled unless `server.admin_reports.enabled: true`, uses Basic Auth or OIDC sessions for browser-admin identity, and uses authorization policy decisions for read/export access. Report data is scoped to the admin's policy domain unless an explicit `*` policy domain grants deployment-wide access. Ordinary caller tokens receive `403 reports-forbidden`.


## Conformance Test Matrix

Router API compatibility is protected by deterministic release validation in addition to live provider smokes. Before changing request parsing, upstream encoding, tool routing, structured outputs, reasoning controls, streaming behavior, max-token handling, or provider-hosted tool policy, require release evidence for the affected API surfaces.

| Surface | What the conformance suite proves |
|---|---|
| OpenAI Chat Completions | Plain text, native same-dialect SSE timing/chunks/cancellation, optional final usage, `max_tokens`, `max_completion_tokens`, same-dialect tool passthrough, `tool_choice`, JSON-schema `response_format`, and `reasoning_effort` forwarding when the selected target supports reasoning. |
| OpenAI Responses | `max_output_tokens`, same-dialect function/namespace tool passthrough, JSON-schema `text.format`, hosted search/image and remote provider-hosted tool rejection before upstream, and same-dialect native SSE lifecycle (created/in-progress/output deltas/completed) with gated first-event proof. |
| Anthropic Messages | Message payload encoding, native same-dialect SSE text/tool-use/usage events and cancellation, caller `max_tokens`, and `thinking` forwarding when the selected target supports Anthropic token-budget reasoning. |
| Gemini `generateContent` target | Unary text encode/decode, exact model-specific endpoint construction, fail-closed unsupported shapes, and direct-plus-router activation evidence. This is an outbound adapter, not a caller endpoint. |

This suite uses mock upstreams and does not prove a real provider/model is entitled, fast, accurate, or compatible with every workload. Activating an upstream still requires direct provider smokes and router-level smokes for the exact provider, model, dialect, tools, images, structured-output, reasoning, and max-token behavior being advertised.

The outbound `gemini-generate-content` adapter is intentionally narrower than
the caller-facing APIs: it supports non-streaming text requests only and has no
caller-facing Gemini endpoint. Unknown dialect dispatch and unsupported Gemini
tools, images, structured output, reasoning, or streaming fail closed. Gemini
models may remain catalog-only; an active target additionally requires
configuration evidence for exact-model direct and router text passes.

Agent compatibility should also be validated with realistic synthetic request shapes. A complete smoke matrix should exercise Codex Responses reasoning/tools, Cursor Chat tools and bridge shapes, Claude Code Messages thinking/tools, opencode/aider Chat flows, large tool schemas, provider-skin mismatch, no-eligible diagnostics, and upstream error classification. Run it against a dedicated smoke group, for example `reasoning-bridge-smoke`, with a caller token that is explicitly allowed to that group.

The smoke emits safe scalar proof only: request IDs, API surface, status, selected provider/model/dialect, bridge direction when recorded, translated reasoning control when recorded, and request-shape buckets.

## Deterministic Compatibility Regression Suite

`make api-compat-mock` is the deterministic pytest entrypoint for
`tests/api_compat`. It uses the exact dependency versions and hashes in
`uv.lock`, provisions the locked Python and Go dependencies, then runs through
`uv --locked --offline` with Go module resolution disabled. It is included in
`make test`, so a clean supported runner does not need a separate manual
dependency step. Bootstrap and normal orchestration stop immediately if either
provisioning command fails. Only `make api-compat-bootstrap` may resolve dependencies;
`make api-compat-mock-offline` is separately invokable and fails closed when
its prerequisites are absent. The regression uses disposable Python and Go
cache locations, and the offline pytest invocation disables source-tree pytest
and bytecode caches so it leaves no repository-local virtual environment or
dependency cache residue. The mock suite itself makes no dependency or provider
network requests. It builds the router locally and uses a synthetic fake
upstream, with both listeners bound exclusively to loopback. It does not read
production configuration or retain authorization values or request bodies in
artifacts.

Operators who use an approved internal Go proxy or checksum database can set
`API_COMPAT_BOOTSTRAP_GO_PROXY` and `API_COMPAT_BOOTSTRAP_GO_SUMDB` in the
environment or as explicit `make` command-line overrides. Environment values
survive normal recursive `make api-compat-mock` and `make test` orchestration.
Both forms reach only `go mod download` as configuration data, never expanded
as Make or shell commands; a malformed mirror value makes the Go provisioning
step fail without running embedded syntax. Python provisioning does not receive
these settings. Before the offline recursive Make target starts, the runner
also removes the mirror variables and Make's recursive-override transport, so
command-line mirrors cannot reappear in the offline phase. These settings never
relax the offline mock phase.

The current caller-contract matrix covers `/v1/models`, OpenAI Chat
Completions, OpenAI Responses, Anthropic Messages, function tools and tool
choice, terminal SSE usage (including Chat empty-choices usage-only chunks),
authorization and model access, caller-visible errors, native same-dialect
streaming for Chat/Responses/Messages, and the non-streaming text/function-tool
paths of both explicit stateless bridges. Each represented pre-upstream
rejection asserts that the fake upstream received zero requests; generated
artifacts are scanned for the synthetic redaction canaries.

To add a case, extend the focused modules under `tests/api_compat/tests/` with
a synthetic request and explicit caller-visible response/upstream-shape
assertions, and add a matching row under `tests/api_compat/manifest/`. Add both
a positive case and the relevant pre-upstream negative case when a request
shape can be rejected. This suite is router compatibility evidence, not
provider capability certification; live providers require `make api-compat-live`
with an approved non-production matrix.

### Real-agent tool-loop smoke (initial coverage)

`make agent-tool-mock` exercises pinned real agents through a local router and
a scripted upstream. It currently covers pi, LangGraph and OpenAI Agents SDK
on Chat, and Claude Code on Messages: one streamed tool use followed by a
client-executed tool result at one same-dialect router hop. Each of these four
local cases passed once (1/1); this is a deterministic smoke, not a statistical
pass rate or a claim about live providers. The Claude case allows the
context-management beta on its test target. A Codex Responses case passed
once on a sandbox-capable CI VM, but was blocked locally by denied unprivileged
user namespaces; see [the suite README](https://github.com/metrum-ai/router/blob/main/tests/agent_compat/README.md).

Pi, LangGraph, and OpenAI Agents SDK also each passed one streamed tool loop
with a Chat caller through a Chat-to-Responses bridge to a scripted Responses
upstream. Each also passed one same-dialect Chat scenario with two streamed
tool calls in one assistant turn and two results in the continuation; this
proves neither simultaneous execution nor all parallel-tool shapes. The OpenAI
Agents SDK strict JSON response-format case additionally checks the schema's
strict flag, fields and required list at depths 0, 1 and 3, while the SDK
validates the returned typed output. These are mock cases, not arbitrary-schema
or live-provider certification.

An initial chain sweep runs each of the four native-dialect clients directly and
through one, two, three and five same-dialect hops. All 20 local deterministic
cells passed once; `make agent-chain-mock` produces a per-cell JSON report.
The same report includes four additional Claude Code Messages → OpenAI Chat
cases at depths 1, 2, 3 and 5. These use a scripted upstream with thinking
disabled and verify split streamed tool arguments and actual tool-result
continuation; they do not establish thinking/signature or cache-control
preservation across dialects. This checks successful streamed tool execution
and continuation, not complete stream equivalence or per-hop accounting.
Codex is a separate sandboxed Responses chain matrix, not part of that sweep.
Other clients/surfaces, other cross-dialect paths, broader parallel-tool
coverage, loop-protection validation across intermediaries and live nightly evidence remain open in #429 and #430.

The scheduled mock workflow produced these bounded per-cell results. Each run
used a local scripted SSE upstream and reports zero hosted-model calls:

| Scheduled run (UTC) | Agent chain cells | Sandboxed Codex Responses cells |
|---|---:|---:|
| [2026-10-28](https://github.com/metrum-ai/router/actions/runs/40673999064) | 20/20 | 5/5 |
| [2026-10-29](https://github.com/metrum-ai/router/actions/runs/40861651773) | 20/20 | 5/5 |
| [2026-10-30](https://github.com/metrum-ai/router/actions/runs/41044327781) | 24/24 | 5/5 |
| [2026-10-31](https://github.com/metrum-ai/router/actions/runs/41192854439) | 24/24 | 5/5 |

The four added cells from October 30 onward are the Claude Messages → Chat
probes described above. These are four scheduled observations, not a seven-day
acceptance streak or live-provider certification. Artifacts cover only the
specified cells; token and cost reconciliation and the other #430 acceptance
criteria remain open.

### Validation evidence (offline)

| Item | Value |
|---|---|
| Catalog tracker | [Issue #94](https://github.com/metrum-ai/router/issues/94) |
| Native Responses streaming | [PR #103](https://github.com/metrum-ai/router/pull/103) (`feat(router): native OpenAI Responses streaming + RESP P0 contracts`) |
| Evidence date | 2026-09-11 |
| Evidence tip SHA | `d9a05eda611689808d7c470537ac8b0050945367` (main at Wave 3 docs refresh; re-record after merges) |
| What is validated offline | Manifest-backed HTTP/SDK, Chat/Responses/Messages P0 shapes, MEDIA/STREAM/ROUTE/OPS/Harbor adapter contracts against loopback fake upstreams |
| Live evidence (local non-production) | 2026-10-10, repository `docs/evidence/api-compat/2026-10-10/`: native Chat/Responses/Messages `smoke-nonprod` matrix, Responses `previous_response_id` continuation, Anthropic stream and tool turn two, and real pi, Claude Code and Codex agents passing Harbor task HARBOR-01 (3 trials each) through the router |
| What is not claimed | Real provider entitlement, Harbor statistical certification, or production spend without operator-gated live evidence |

**Limitation (historical):** Older offline Responses smoke paths synthesized
caller SSE after a unary upstream call. That synthetic path was **not** native
streaming proof. Same-dialect native Responses streaming is now implemented and
covered by the RESP-02..06 contracts landed with #103. The `responses_to_chat` bridge (Responses caller, Chat upstream) translates Chat
SSE incrementally when `responses_to_chat.streaming: true` is enabled.
`server.streaming.translator: synthesized` retains the unary upstream fallback.

`make api-compat-live` is deliberately fail-closed and is not part of test,
build, package, or release targets. It keeps the operator gates for a
human-named approved matrix (`tests/api_compat/testdata/live/matrices/<id>.json`),
non-production environment, least-privilege caller identity, explicit base URL,
a protected mode-0600 credential file, and a confirmation bound to
`matrix:environment`. The runner then enforces hard request/token/time/spend
caps from the matrix (clamped by script ceilings). Missing or empty credentials
report disposition `blocked` with a nonzero exit and **never** count as
certification. Optional CI job `.github/workflows/api-compat-live.yml` reports
the same blocked disposition when live secrets are absent and does not fail the
default PR `make test` gate.

## Compatibility Matrix

| Capability | Chat Completions | Responses | Messages |
|---|---|---|---|
| Text input/output | Supported | Supported | Supported |
| Streaming | Same-dialect native upstream SSE is proxied incrementally | Same-dialect native upstream SSE is proxied incrementally | Same-dialect native upstream SSE is proxied incrementally |
| Tool calls | Requires `tool_support.openai_chat` | Requires `tool_support.openai_responses` | Requires `tool_support.anthropic_messages` |
| Structured outputs | `response_format` requires `tool_support.openai_chat: [structured_outputs]` | `text.format` requires `tool_support.openai_responses: [structured_outputs]` | No OpenAI structured-output equivalent |
| Reasoning/thinking | `reasoning_effort` requires target `reasoning` metadata | `reasoning` requires target `reasoning` metadata | `thinking` requires target `reasoning` metadata or validated target default thinking |
| Image input | Requires `image` in target `input_modalities` | Requires `image` in target `input_modalities` | Requires `image` in target `input_modalities` |
| Caller max-token caps | `max_tokens` and `max_completion_tokens` are enforced against configured target metadata | `max_output_tokens` is enforced against configured target metadata | `max_tokens` is enforced against configured target metadata |
| Cache eligibility | Eligible only for deterministic non-tool, non-image requests | Eligible only for deterministic non-tool, non-image requests | Eligible only for deterministic non-tool, non-image requests |
| Usage and cost rows | Recorded | Recorded | Recorded |

For same-dialect OpenAI Chat, OpenAI Responses, and Anthropic Messages targets, caller `stream: true` requests set upstream streaming and proxy native SSE events incrementally, including compatible tool and usage events. Once any event is committed downstream, the router does not replay the request to a fallback target; caller cancellation cancels the upstream request. For a Responses caller using a Chat upstream, `responses_to_chat.streaming: true` enables incremental Chat SSE → Responses SSE (`chat_upstream_to_responses_caller`). The router forces upstream `stream_options.include_usage: true`, emits ordered Responses lifecycle and tool-argument events, and includes usage in the terminal response. `server.streaming.translator: synthesized` retains synthesized SSE from a unary upstream call. This bridge does not map Chat completion IDs into `previous_response_id` sessions. For Chat callers using Responses upstreams, `bridges.chat_to_responses.enabled: true` enables incremental Responses SSE → Chat SSE (`responses_upstream_to_chat_caller`) when `server.streaming.translator` is `incremental` (the default). Tool IDs appear on the first fragment, argument strings remain incremental, and terminal chunks carry finish reason and available usage. The `synthesized` setting uses a unary upstream call.

If a request includes tools, structured-output fields, images, or an explicit max-token cap, the router filters the model group's target list before policy selection. Targets that do not satisfy the request shape are skipped. If no compatible target remains, the router returns `502 no-eligible-target` before sending an upstream request.

If a request reaches an upstream provider and that provider rejects the translated shape, the terminal response is usually `502 upstream-failed` with safe details such as `error.details.error_class = upstream_bad_request`, `error.details.upstream_status`, `X-Router-Error-Class`, `X-Upstream-Status`, and a request ID. Treat that as provider/model/dialect/request-shape evidence, not a generic retry signal. See [Error Reference](./errors) and [Request Troubleshooting](../troubleshooting/requests) for the downstream contract and request-ID workflow.

## Mixed OpenAI Endpoint Compatibility

Clients should normally send Chat Completions bodies to `/v1/chat/completions` and Responses bodies to `/v1/responses`. Some OpenAI-compatible client adapters can be configured incorrectly and post a Responses-shaped JSON body to the Chat Completions path. By default the router rejects that mismatch with `400 responses-body-on-chat-endpoint-disabled` while ordinary Chat Completions requests continue to work.

An operator can opt in to the compatibility mode:

```yaml
server:
  openai_compatibility:
    tolerate_responses_body_on_chat_endpoint: true
```

When enabled, detection is based on request fields such as `input`, `instructions`, `reasoning`, `text`, `previous_response_id`, or `max_output_tokens`; it is not based on user agent, client name, or SDK name. The accepted subset on the Chat path is Responses-style `input`, `instructions`, `reasoning`, function `tools`, `tool_choice`, `text.format`, Chat-style `response_format` converted to `text.format`, `previous_response_id` only for groups with a native Responses target, one positive output cap field normalized to `max_output_tokens`, and `stream`. Sampling fields such as `temperature`, `top_p`, and `parallel_tool_calls` are preserved when the selected target path supports them.

Unsupported fields such as `include`, `truncation`, `metadata`, `store`, mixed `messages` plus `input`, multiple output cap fields, provider-hosted tool descriptors, or conflicting `text.format` plus `response_format` return a clear 400-level compatibility error before upstream. Target-specific gaps such as an unvalidated Responses-to-Chat tool choice or reasoning bridge can still return `502 no-eligible-target` with safe filter diagnostics.

Example request:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-model-group",
    "input": "Reply with one short sentence.",
    "reasoning": {"effort": "low"},
    "max_output_tokens": 64
  }'
```

Successful compatibility requests are handled as OpenAI Responses ingress for routing and response shaping. Diagnostics record safe scalar evidence such as the Chat endpoint path, detected Responses body shape, compatibility mode, `inbound_dialect = openai-responses`, selected target dialect, bridge direction when a Responses-to-Chat bridge is used, translated output-cap field, and bounded filter or rejection reason. Diagnostics do not store raw prompts, images, tool schemas, bearer tokens, token hashes, provider keys, or full config.

## API Bridges

Deployments can expose a validated Chat Completions upstream to `/v1/responses` callers through an explicit stateless Responses-to-Chat bridge. This is useful for Codex or Responses-compatible clients when a model is only validated through OpenAI Chat Completions.

The bridge is never automatic. A target must keep dialect `openai-chat` and opt in with `responses_to_chat` metadata. The router maps Responses `input` and `instructions` to Chat messages, function tools and their `function_call` / `function_call_output` history to Chat tools and messages, `tool_choice` only when validated, and `max_output_tokens` to the Chat output cap field configured for the target. Function tools and history require `responses_to_chat.function_tools: true`; streaming additionally requires `responses_to_chat.streaming: true`. Function-call arguments must be valid JSON and function-call outputs must be strings; unsupported history shapes are rejected rather than silently flattened. If `responses_to_chat.reasoning: true` and the target's `reasoning` metadata is compatible, Responses `reasoning.effort` maps to Chat `reasoning_effort`; otherwise the target is skipped before upstream. The Chat response is returned to the caller as a Responses-shaped object.

Unsupported Responses features are rejected or skipped before upstream for Chat-bridged targets. Stateless bridge targets do not support `previous_response_id`; provider-hosted tools such as file search, code interpreter, computer use, MCP/SSE, hosted search, and image generation are not sent through the bridge. Images, reasoning, structured output, and streaming require separate bridge flags and validation before use.

Usage and diagnostics show both sides: `inbound_dialect = openai-responses`, `target_dialect = openai-chat`, and `request_translation_shapes.bridge_direction = responses_to_chat`. Reasoning-preserving attempts also record a safe `translated_reasoning_control` value such as `reasoning_effort`.

Same-dialect OpenAI Chat uses native passthrough, including tool-less text and streaming requests. Sampling controls (`seed`, `n`, `top_p`, penalties, `logprobs`, `top_logprobs`, `logit_bias`, and `user`) survive forwarding; every response choice, refusal, and finish reason is preserved. Routing model identity, persistence policy, and configured target encoding still apply. The IR is used for translation, not native upstream encoding. The optional deterministic text cache retains its normalized replay behavior; native multiple-choice, refusal, tool-call and logprob responses bypass that IR-only cache so replay cannot erase them.

For OpenAI Chat Completions, a present `max_tokens` must be an integer >= 1; otherwise the router returns HTTP 400 `invalid-request` before upstream, on both text and tools paths. There are no model-specific validation caps or silent truncation.

For OpenAI Chat Completions requests, both `max_tokens` and `max_completion_tokens` are treated as explicit output caps. If a Chat request sends both fields, `max_tokens` takes precedence for router eligibility and normalized upstream forwarding.

The same model group can therefore expose different effective upstream pools to different API surfaces. A Chat client can use only active Chat-compatible targets, a Responses client can use only active Responses-compatible targets, and a Messages client can use only active Anthropic-compatible targets unless the deployment has configured and documented an explicit bridge. Provider catalog metadata for another skin is not enough by itself; the active target's resolved skin controls eligibility.

## Anthropic Messages Eligibility

Anthropic-compatible inbound requests are filtered independently from OpenAI Chat and Responses requests. Native Anthropic Messages targets are eligible for `/anthropic/v1/messages` when their provider skin is configured as Anthropic-compatible and any requested tools, images, reasoning, and output-cap behavior have matching metadata.

### Translation eligibility migration and beta headers

Both Anthropic/OpenAI translation directions require explicit
`request_shape_support.supported_inbound_dialects` opt-in and
`validation_status: passed`. For this release only, the top-level
`eligibility.implicit_openai_to_anthropic: true` default preserves implicit
OpenAI-to-Anthropic routing. Each target relying on that exception emits a
startup warning. **The default flips to false in the next minor release.** Set
it to `false` now to enforce symmetric eligibility. Same-dialect routing does
not require translation validation.

Migrate existing file-owned configs with:

```bash
metrum-ai-routerctl config patch-eligibility --config config.yaml --dry-run --diff
metrum-ai-routerctl config patch-eligibility --config config.yaml --output migrated.yaml
metrum-ai-routerctl config validate --config migrated.yaml
```

The patcher retains unrelated YAML, emits explicit inbound dialects and `passed`
for Anthropic targets, and disables the legacy exception. This is a migration of
existing behavior, **not new validation evidence**: review and smoke-test each
translated provider/model/skin before applying. Existing nonempty inbound lists
remain restrictive. Dry-run never writes; `--diff` prints before/after lines.
Keep output secure if the original config embeds credentials.

Targets may declare `allowed_betas: [context-management-2025-06-27]` (also
inheritable from provider model metadata); omission or `[]` allows none. The
router forwards only the intersection of caller `anthropic-beta` values with
that list, including comma-separated and repeated headers. Provider headers
cannot bypass it. Known beta-gated body fields are not silently stripped:
`context_management` requires `context-management-2025-06-27`, `container`
requires `code-execution-2025-08-25`, and `mcp_servers` requires
`mcp-client-2025-04-04`. Presence without the matching permitted caller beta
returns HTTP 400 naming the field before upstream. Unknown provider fields
retain native passthrough semantics. Anthropic `thinking.type: adaptive` is a
reasoning request, just like `enabled`, and excludes non-reasoning targets.

When a deployment intentionally lets plain Anthropic Messages text use a non-native OpenAI Chat or Responses target, that active target must explicitly opt in with `request_shape_support.supported_inbound_dialects` including `anthropic`. This metadata is a validation claim for that provider/model/skin and model group; it should be based on direct upstream and router-level smoke evidence for the translated text path.

Tool-bearing Claude Code traffic has a stricter requirement. Use Anthropic Messages-compatible targets with `tool_support.anthropic_messages`, or a separately documented bridge that has passed the exact client-tool workflow. OpenAI Chat or Responses tool metadata alone does not make a target eligible for Anthropic client tools.

### OpenAI requests to an Anthropic target

An OpenAI Chat or Responses client can use an Anthropic target only if the request passes the target's dialect, modality, output-cap, reasoning and request-shape filters. For Responses tool requests, leading text-only system messages are mapped to the Anthropic system prompt; system messages after conversation items and non-text system content are rejected rather than reordered or dropped. For translated function tools, the target must explicitly list the caller dialect in `request_shape_support.supported_inbound_dialects`, set `request_shape_support.validation_status: passed`, and declare both `tool_support.anthropic_messages: [client_tools]` and the matching caller capability (`tool_support.openai_chat: [tools]` or `tool_support.openai_responses: [function]`). An explicit tool choice additionally requires `tool_choice` support on both sides. These flags are claims about the exact provider, model and request shape; enable them only after direct and routed tests. Strict function schemas, provider-hosted tools and unsupported content/history shapes are rejected rather than silently weakened.

For structured output, the target must declare `structured_output: true`. OpenAI `json_schema` is sent as Anthropic `output_config.format`; JSON syntax is checked before delivery, but schema conformance relies on the upstream. JSON-object mode adds a JSON-only instruction and validates the returned object. Both modes buffer translated streams before emitting output; combining them with tool requests or tool history is not supported. OpenAI Chat `logprobs` cannot be translated to Anthropic and such a target is ineligible.

For every route, also check the caller's group allow-list, the target's `input_modalities`, `honors_max_tokens`, `context_tokens`, `reasoning` and `request_shape_support` (including unsupported features and size limits). Targets with `force_store_false` cannot satisfy a `previous_response_id` continuation. See [model metadata](./model-metadata) and [routing](../routing/overview) for the complete configuration and eligibility controls. A passing mock is not proof that a real target supports these combinations.

If a caller can see a group in `/v1/models` but `/anthropic/v1/messages` returns `502 no-eligible-target`, the token is authorized but the requested group has no target that satisfies the Messages request shape. For plain text, administrators should check whether non-native translated targets are missing `supported_inbound_dialects: [anthropic]` or whether all eligible Messages targets are `tool_only`. For tools, images, or thinking, the group needs target metadata for those exact Anthropic Messages capabilities.

## Chat To Responses Bridge

Deployments can opt a target into an OpenAI Chat Completions to OpenAI Responses bridge. This lets a caller keep using `POST /v1/chat/completions` while the router calls a selected `openai-responses` upstream target. The bridge is config-driven and target-specific; Chat requests never route to Responses targets unless `bridges.chat_to_responses.enabled: true` is present on the resolved target metadata.

The bridge supports text and basic function-tool requests, including incremental streaming when `server.streaming.translator: incremental`. The router maps Chat messages into Responses `input`, system/developer messages into `instructions`, Chat function tools into Responses function tools, `max_tokens` or `max_completion_tokens` into `max_output_tokens`, and Responses text/function-call output back into Chat completion shape. If `bridges.chat_to_responses.reasoning: true` and the target's `reasoning` metadata is compatible, Chat `reasoning_effort` maps to Responses `reasoning.effort`; otherwise the target is skipped before upstream with a bounded filter reason. Usage rows keep `inbound_dialect = openai-chat` and `target_dialect = openai-responses`, and translation diagnostics record `bridge_direction = chat_to_responses`.

The Chat-to-Responses bridge is always stateless: callers supply full history, bridged requests set `store:false`, and the router never sends `previous_response_id`, even with legacy `stateful_sessions` configuration. Native Responses requests containing `previous_response_id` exclude targets with `force_store_false`; if no eligible target remains, the router returns `400 continuation_unsupported`.

Every stream response sends `X-Accel-Buffering: no` and `Cache-Control: no-cache`. Native stream requests require an upstream SSE content type; a JSON response returns `502 upstream_protocol_error`, rather than synthesized success. SSE lines are bounded to 64 KiB before allocation; an oversized line returns `502 upstream_frame_too_large` before commitment. Errors after streaming starts terminate the stream and cannot replace its HTTP status. `make lrp-e2e` verifies incremental delivery through a Docker nginx TLS HTTP/2 ingress; this optional case skips when Docker is absent.

Streaming Chat→Responses requests use incremental translation by default when the bridge is enabled and all other eligibility checks pass. Images, structured outputs, reasoning controls, forced or parallel tool modes, and other advanced fields require matching bridge metadata and target capability metadata. Stateful-session backend misses or transient backend errors do not reuse another session's continuation; the request continues stateless and emits safe trace events such as `bridge_session_lookup_miss` or `bridge_session_backend_error`. The inverse Responses-to-Chat bridge has separate metadata and behavior.

Common safe filter reasons include `chat-to-responses-bridge-disabled`, `chat-to-responses-tools-unsupported`, `chat-to-responses-tool-choice-unsupported`, `chat-to-responses-structured-output-unsupported`, and `chat-to-responses-image-unsupported`.

## Reasoning And Bridge Compatibility

| Caller endpoint | Native reasoning field | Same-dialect target | Bridge target |
|---|---|---|---|
| `/v1/chat/completions` | `reasoning_effort` | Requires active OpenAI Chat target reasoning metadata. | Chat-to-Responses reasoning requires `bridges.chat_to_responses.reasoning: true` plus Responses target reasoning metadata. |
| `/v1/responses` | `reasoning` | Requires active OpenAI Responses target reasoning metadata. | Responses-to-Chat reasoning is unsupported unless `responses_to_chat.reasoning` is explicitly validated for the target. |
| `/anthropic/v1/messages` | `thinking` | Requires active Anthropic Messages target reasoning/default-thinking metadata. | No general Messages bridge is implied by Chat or Responses bridge metadata. |

Tools and reasoning are filtered together. A request with tools and reasoning needs a target that supports both the caller's tool dialect and the requested reasoning control, or an explicitly validated bridge for both features. If no candidate supports the requested capabilities, the router returns `400 invalid_request_error` with an `unsupported_capability` code; candidate/filter diagnostics should show bounded reasons such as `reasoning`, `chat-to-responses-reasoning-unsupported`, or `responses-to-chat-reasoning` rather than an upstream attempt with the reasoning field stripped.

Bridge requests remain stateless unless a Chat-to-Responses target enables stateful sessions. A stateless bridge does not synthesize `previous_response_id` continuity for reasoning workflows. OpenAI Responses callers that send `previous_response_id` to a Chat-bridged target should expect a bridge filter reason unless that exact stateful behavior is documented for the target.

## Quotas And Output Caps

Before an upstream call, the router reserves the estimated input tokens plus the requested output budget for token-based admission. Chat Completions requests use `max_tokens` or `max_completion_tokens`, Responses requests use `max_output_tokens`, and Messages requests use `max_tokens`. Messages requests without a caller cap reserve the router default output cap when the router injects one.

TPM, daily token, monthly token, and lifetime key budgets include in-flight reservations. This prevents several concurrent large-cap requests from collectively exceeding a caller's budget. When a request completes, the reservation is reconciled to reported usage, or to an explicitly marked estimate when usage is absent. A failed committed stream can settle partial reported usage; requests without settled usage release their reservation. Router response-cache hits and count-token calls do not consume inference token quota.

Optional caller traffic shaping can also smooth short bursts before upstream calls. It is separate from hard TPM/RPM/quota admission and can return `429 traffic-shaped` with `Retry-After` and a safe bucket label when request-start or token-reservation throughput is exceeded.

Use realistic output caps in examples and clients. A small prompt with a very large output cap can be rejected near a token budget because the caller asked the router to reserve that much possible output.

## Model Names

The `model` field is a router model group, not necessarily a provider model ID. Model group names are deployment-defined. Names shown in examples are examples only.

If a compatible API request omits `model`, the router uses `server.default_model_group` when configured. If no default is configured, the router returns `400 missing-model`.

### Response model identity

The `model` field in a completion response depends on `server.responses.model_identity`:

- `upstream` (default): the response reports the upstream model that served the request. In a weighted or fallback group, the value can change from one request to the next.
- `requested_group`: the response reports the group the caller requested, which is one of the IDs returned by `/v1/models`. The value is the same on unary bodies, on every streaming chunk or event, after fallback, on response-cache hits and on every hop of a tool-call continuation. Upstream failure errors stop naming providers and upstream models (see [Errors](./errors#requested-group-error-redaction)).

Callers should treat the response `model` as an echo of the router group, not as a provider model ID to parse or branch on. Operators still see the serving upstream provider and model in `request_usage`, `request_attempts`, request logs and admin reports in both modes.

## Discover Allowed Model Groups

Call `/v1/models` with the same router token that the client will use for completions. The response is filtered to that token's allow list, so it shows the deployment-defined model groups the caller can request.

```bash
curl "$ROUTER_BASE_URL/v1/models" \
  -H "Authorization: Bearer $ROUTER_TOKEN"
```

Example response:

```json
{
  "object": "list",
  "data": [
    {
      "id": "default",
      "object": "model",
      "owned_by": "smart-llmrouter"
    },
    {
      "id": "vision",
      "object": "model",
      "owned_by": "smart-llmrouter"
    }
  ]
}
```

Use one of the returned `id` values as the `model` field in `/v1/chat/completions`, `/v1/responses`, or `/anthropic/v1/messages`. If a group is not listed, that token is not allowed to use it. Unknown or disallowed groups fail with `404 model_not_found` before any upstream provider is called; the response does not disclose whether a group exists.

The returned IDs are router model groups, not a full inventory of every upstream provider model. Platform teams can change the upstream provider/model mix behind a group without changing the caller-facing group name. With `server.responses.model_identity: requested_group`, these IDs are also exactly what completion responses report in their `model` field.

For caller-facing troubleshooting and administrator handoff guidance, see [Available Models And Access](../getting-started/available-models).

## Tool Calls

Tool requests only route to upstream targets that explicitly advertise support for the caller's API dialect and tool mode.

Omitted-choice tools, explicit automatic selection (`tool_choice: "auto"`), and forced selection are three distinct request shapes. Each requires separate passing capability evidence for the exact provider skin and any enabled bridge. A JSON `tool_choice` key with `null` is also explicit, never omitted: it is allowed only by same-skin explicit tool-choice eligibility and is rejected on Chat-to-Responses and Responses-to-Chat bridges because those bridges cannot preserve its meaning. A target marked with `request_shape_support.unsupported_request_features: [tool_choice]` remains eligible only for a key that is actually absent; it is skipped for every explicit selection, including null. Use `forced_tool_choice` when only forced selection is unsupported.

| Caller shape | Required target metadata |
|---|---|
| OpenAI Chat tools | `tool_support.openai_chat` |
| OpenAI Responses function tools | `tool_support.openai_responses` |
| Anthropic Messages client tools | `tool_support.anthropic_messages` |

Provider skins are part of this contract. A target configured through an OpenAI Chat provider is not a Responses target unless the resolved provider or target dialect is `openai-responses`. For deployments that validate the same upstream model through multiple APIs, use separate provider skins such as `minimax`, `minimax_responses`, and `minimax_anthropic` rather than relying on the upstream model name alone.

Tool-bearing requests bypass response caching because tool results depend on external shell, filesystem, browser, or client tool state.

OpenAI Responses provider-hosted tools such as Fireworks-documented `mcp` and `sse` tools are not the same as client-executed function or namespace tools. By default, the router rejects caller-supplied remote provider-hosted entries such as `mcp`, `sse`, file-search, code-interpreter, and computer-use tools with `400 provider-hosted-tools-forbidden` before any upstream call. Generic hosted search or image-generation descriptors also return `400 provider-hosted-tools-forbidden`; they are never silently stripped. Anthropic versioned `web_search_*` server tools use this same policy. Deployments should expose provider-hosted tools only after a separate security design covers allowlisted hosts, timeouts, network egress, and data-retention expectations.

The router controls upstream persistence policy for OpenAI-compatible requests. Same-dialect Chat Completions and Responses passthrough strip caller-supplied provider `metadata`; they send `store:false` upstream only when the resolved target sets `force_store_false: true`. Translated Responses calls use the same flag. Chat passthrough also honors target encoding metadata such as `output_token_field: max_completion_tokens` for upstreams that require `max_completion_tokens` instead of `max_tokens`.

Eligible Anthropic ↔ OpenAI Chat and Anthropic ↔ OpenAI Responses paths also translate SSE incrementally with the default `server.streaming.translator: incremental`. Text and tool deltas use the caller dialect’s lifecycle; tool IDs are encoded on their first fragment, and tool arguments remain raw string deltas without intermediate JSON parsing. Anthropic `message_start` uses the reservation input estimate, reconciled with upstream usage at completion. Existing inbound-dialect validation and tool admission gates are unchanged: translator tool support does not admit cross-dialect Anthropic tool requests. Reasoning requests stay on native Anthropic routes; these bridges do not synthesize thinking blocks. Cancellation and the first-frame commit boundary match native streaming. The `synthesized` setting retains unary upstream calls for these four directions. Other upstream dialects without an incremental translator still synthesize caller SSE where eligible; Gemini generateContent remains non-streaming only.

For same-dialect OpenAI Chat tool passthrough, the router forwards caller streaming upstream and proxies native SSE tool-call deltas. Caller `stream_options`, including `include_usage`, are forwarded for compatible targets. If a provider/model rejects that caller shape, configure `request_shape_support.unsupported_request_features: [stream_options]` for that target until the exact client, provider, model, dialect, and router path pass direct and router-level smokes.

Large OpenAI Chat coding-agent payload compatibility is a separate claim from ordinary text or tool support. Before a provider/model/dialect joins broad IDE or agent routes, validate representative request bytes, message count, serialized tool-schema size, explicit output cap, token scale, and router translation path with a synthetic fixture. If the target is not validated for that shape, keep it in a smoke group or configure `request_shape_support` limits so incompatible large requests skip it before upstream.

## Reasoning And Thinking

The router detects explicit reasoning requests in all supported caller dialects:

- OpenAI Chat Completions: `reasoning_effort`.
- OpenAI Responses: `reasoning`.
- Anthropic Messages: `thinking`.

Reasoning is handled inside the requested model group. The router does not switch callers to another group and does not silently drop explicit reasoning controls. If no configured target in that group can satisfy the requested reasoning shape together with tools, images, structured outputs, and max-token cap behavior, the response is `502 no-eligible-target`.

Reasoning controls are dialect-specific. Anthropic reasoning uses native Anthropic routes; the text/tool bridges do not translate thinking output. Validated Chat ↔ Responses bridges can map OpenAI reasoning effort where configured. Targets that reject `max_tokens` for reasoning traffic can be configured so the router sends `max_completion_tokens` instead.

For `/v1/models`, reasoning metadata is effective group metadata, not a catalog dump. The router should expose `supported_reasoning_levels` only when at least one active target in the requested group can actually serve that reasoning shape for the caller's API surface. Some upstreams also enforce minimum output budgets for reasoning requests; a tiny cap failure does not invalidate the reasoning capability, but it must be documented and tested with realistic budgets.

For OpenAI Chat, OpenAI Responses, and Anthropic Messages reasoning examples, see [Reasoning Routing](../configuration/reasoning-routing).

## Structured Outputs

Structured-output requests are routing contracts, not router-side schema execution. The router detects OpenAI Chat `response_format` and OpenAI Responses `text.format`, selects only targets with explicit dialect-matching `structured_outputs` metadata, and forwards the schema payload to the selected upstream. It does not validate arbitrary JSON Schema subsets or repair provider output unless a separate implementation adds that behavior. Unsupported schemas, strictness settings, or provider-specific JSON Schema subsets may produce upstream/provider errors.

Structured-output support is dialect-specific. Passing Chat Completions `response_format` does not prove Responses `text.format`, and Anthropic Messages has no OpenAI structured-output equivalent unless a deployment adds and documents an explicit compatible behavior.

Chat Completions JSON Schema example:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "example-structured",
    "messages": [{"role": "user", "content": "Extract the ticket id and priority from: INC-1234 high"}],
    "response_format": {
      "type": "json_schema",
      "json_schema": {
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "medium", "high"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    }
  }'
```

Responses JSON Schema example:

```bash
curl "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "example-structured",
    "input": "Extract the ticket id and priority from: INC-1234 high",
    "text": {
      "format": {
        "type": "json_schema",
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "medium", "high"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    }
  }'
```

Use a deployment-defined model group returned by `/v1/models`; `example-structured` is only a placeholder group name.

## Image Inputs

Image-bearing requests are accepted through Chat Completions, Responses, and Messages shapes. The router selects only targets with `image` in `input_modalities`.

Text-only and image-capable work do not need separate user workflows. A deployment can put text-capable and vision-capable upstreams behind the same model group, as long as each request is routed only to targets that satisfy its actual requirements.

## Router-Only Endpoints

Router-only endpoints are not part of OpenAI or Anthropic compatibility:

- `/healthz` always returns `200` with runtime build metadata when the process is serving HTTP. `/readyz` returns `200` when the in-memory config still validates; a failed readiness check returns `503` with a fixed `error: "not-ready"` and never echoes `Validate()` text, caller IDs, paths, or other config detail.
- `/version` returns the running router version, build timestamp, Go runtime version, OS, and architecture for administrators.
- `/v1/usage` returns usage/quota information for the authenticated caller.
- `/admin/reports/api/quota-status` returns live remaining and configured limits for authorized reports administrators; it never accepts ordinary caller tokens.
- `/metrics` returns Prometheus telemetry only for metrics-admin tokens.

Use SDKs for the compatible provider-style APIs they support. Router-only endpoints such as `/readyz`, `/version`, and `/v1/usage` are best called with ordinary HTTP clients.
