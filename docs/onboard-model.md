# Model And Endpoint Onboarding Guide

Source-only internal runbook. Do not add this file to `scripts/package_docs_allowlist.txt` or the public Docusaurus sidebar.

Use this procedure before adding or promoting any upstream model, provider endpoint, self-hosted service, or alternate API skin. It is agent-agnostic: the same evidence standard applies whether the caller is an application, Codex, Claude Code, Cursor, Warp, opencode, aider, Harbor, or another client.

Related references:

- Capability smoke curl templates: [smoke-commands-reference.md](smoke-commands-reference.md)
- Smoke matrix and deterministic gates: [SMOKE_TEST_MATRIX.md](SMOKE_TEST_MATRIX.md)
- Public-safe provider/model guide: `docs-site/docs/reference/add-provider-model.md`
- Self-hosted vLLM/SGLang notes: [SELF_HOSTED_UPSTREAMS.md](SELF_HOSTED_UPSTREAMS.md)

## 1. Define The Candidate And Intended Surface

Record the provider, endpoint, served model ID, router provider name, model reference name, intended model groups, caller API shapes, expected clients, and whether the candidate is catalog-only, smoke-only, or intended for active routing.

Treat model group names as deployment-defined strings. Do not hardcode reference names such as `default`, `fast`, `small`, `medium`, `high`, `big-coder`, or `vision` as product constants.

## 2. Verify Current External Facts

Before editing config, verify current primary/provider documentation or provider metadata for:

- endpoint base URL and API path;
- authentication scheme and required headers;
- exact served model ID or suffix;
- context limits and max output behavior;
- text, image, audio, video, and tool support;
- streaming, structured-output, reasoning, and hosted-tool behavior;
- current input, output, cache, image, or enterprise chargeback pricing;
- account, region, billing, and entitlement availability.

Use source-dated notes in internal docs or config comments when the fact may change. For OpenRouter-hosted models, prefer OpenRouter model metadata and validate Nitro suffixes with a real completion call.

## 3. Direct-Smoke The Exact Upstream

Run direct provider smokes before involving the router. Test every API skin and request shape that will be claimed:

- OpenAI Chat text, streaming, max-token cap, tools, forced `tool_choice`, structured output, reasoning, and image inputs as applicable;
- OpenAI Responses text, function tools, tool-result continuation, streaming, structured output, reasoning, and image inputs as applicable;
- Anthropic Messages text, client tools, thinking, max-token cap, streaming, and image inputs as applicable.

Use the placeholder curl templates in [smoke-commands-reference.md](smoke-commands-reference.md). A pass on one skin does not imply a pass on another skin. If the upstream returns `401`, `403`, model-not-found, empty content, malformed tools, or non-grounded image answers, keep the candidate out of active routing until the exact failure is understood. For `403` or `404`, distinguish invalid provider credentials from account entitlement, region/project restriction, policy/privacy block, and model-access denial; only the exact provider key, model ID, dialect, and request shape that passed the smoke should be promoted.

When one upstream model exposes multiple API skins, create separate provider entries for each validated skin instead of overriding target dialects on an unrelated provider. For example, MiniMax `MiniMax-M3` uses separate reference providers for OpenAI Chat (`minimax`), OpenAI Responses (`minimax_responses`), and Anthropic Messages (`minimax_anthropic`) so Codex/Responses, Cursor/OpenAI Chat, and Claude Code/Messages eligibility can be validated and rolled back independently.

## 4. Capture Capability Probe Results

The repository now has an offline, synthetic contract check:

```bash
make capability-smoke-unit
```

It validates versioned scalar-only fixtures, fake-adapter classification, and
configuration/evidence assertions without credentials or network access. Its
results are explicitly **non-promotable**: they prove the contract and never
certify a provider, account, route, target, or weight. `SKIP_TESTS=true` is the
only bypass for this deterministic check and emits a warning. The reserved
`make capability-smoke-live` target currently fails closed; it does not probe
providers or write evidence until a protected-live follow-up is approved.

Every future live result must bind the exact provider, account identity class,
endpoint fingerprint and path, API skin, model plus suffix, inbound dialect,
bridge direction, request shape, capability case, and profile version.

The reusable offline capability-contract verifier derives the evidence identity
from the resolved target, not from submitted evidence. Claim verification first
limits derivation to the matching resolved target's provider, non-secret account
identity class, endpoint fingerprint and path, API skin, model, and suffix.
Inbound dialect, bridge direction, and request shape remain surface checks
because one target can expose more than one callable surface. This prevents an
unrelated same-model target's unsupported composite shape from rejecting a valid
claim. Whole-group advertised-capability verification still derives every
resolved target and fails closed on any unsupported composite shape. A future protected
promotion workflow must use the same binding. `account_identity_class` must equal
the provider's non-secret `key_id`; `endpoint_fingerprint` is the SHA-256 of
the full resolved upstream URL after lowercasing its scheme and host, including
the actual joined path and any configured query. The query is never stored in
evidence; it is only hash-bound. `endpoint_path` separately records the path
the router actually calls, and OpenRouter-style model suffixes such as `:nitro`
remain separate from the base model ID. A valid but different account,
endpoint, skin, model suffix, inbound dialect, bridge direction, request
shape, or unapproved profile cannot satisfy the active target.

Use one request-shape identity per capability case. `text`, `tools-omitted`,
`tools-auto`, `tools-forced`, `image`, and `structured-outputs` are distinct shapes. Native
and translated calls are also distinct: bridge evidence must name the caller's
inbound dialect and `chat_to_responses`, `responses_to_chat`, or the exact
`<inbound-skin>_to_<upstream-skin>` direction for another allowlisted runtime
translation; direct evidence uses the target dialect and `none`. Tool evidence
is accepted only for the target's exact API-skin vocabulary (`tools` for Chat,
`function` for Responses, and `client_tools` for Anthropic), and explicit
unsupported-feature metadata overrides broad capability metadata. In
particular, `tool_choice` suppresses both explicit `tools-auto` and
`tools-forced` evidence because the automatic probe sends
`tool_choice: "auto"`; `forced_tool_choice` suppresses only `tools-forced`.
Tool requests that omit `tool_choice` require separately passing `tools-omitted`
evidence; they are never satisfied by an automatic or forced probe. Targets that
need composite image-plus-tool or image-plus-structured evidence fail closed
until the evidence schema defines that composite request shape.
An explicit JSON `tool_choice: null` is not omitted: it must pass explicit
tool-choice eligibility on a same-skin request, and it is rejected for the
Chat-to-Responses and Responses-to-Chat bridges because those translations do
not have a lossless null representation.
An empty `supported_inbound_dialects` allowlist follows the router's generic
text/image translation behavior. The verifier therefore requires separate
Chat-to-Anthropic, Responses-to-Anthropic, or OpenAI-to-Replicate evidence when
those implicit directions are callable. Set an explicit allowlist to narrow
the accepted inbound surfaces.


Record public-safe evidence for each probe:

- date, provider, endpoint family, model ID, dialect, skin, and account/region class;
- request shape, streaming mode, max-token cap, tool-choice mode, structured-output mode, modality, and reasoning settings;
- status code, selected upstream model, finish reason, token usage, image-token usage when reported, latency, and fallback/no-fallback status;
- observed response summary, not raw prompts, raw images, raw tool schemas, raw tool outputs, bearer tokens, provider keys, router tokens, token hashes, or full config;
- pass/fail decision and the exact metadata field or route eligibility the evidence supports.

Separate transport success from task quality. A model that accepts an image is not necessarily good enough for OCR, browser control, or agent workflows.

## 5. Add Catalog Metadata Only

Add or update `providers.<provider>.models.<model_ref>` with structured metadata:

- exact upstream model ID;
- input and output modalities;
- per-million-token pricing and pricing source/update evidence when known;
- image or per-image pricing only when the upstream or chargeback model uses separate image pricing;
- `tool_support` by skin only after real smokes pass;
- structured-output, reasoning, hosted-tool, max-token, and provider-specific notes only after exact validation.

Keep routing weights only under `models.<group>.targets[]`. Keep unavailable or unentitled models catalog-only.

The `gemini-generate-content` dialect is limited to unary text generation. A
Gemini catalog entry may omit `activation_evidence`, but an active model-group
target using that dialect must bind evidence to the exact model and dialect:

```yaml
activation_evidence:
  exact_model: <exact-provider-model-id>
  dialect: gemini-generate-content
  direct_text_passed: true
  router_text_passed: true
  validated_at: YYYY-MM-DD
```

These fields are operator-attested configuration assertions. The router checks
their presence, date, exact model, and dialect consistency; it does not verify
an external evidence artifact.

Collect `direct_text_passed` first with the exact provider/model/account. The
first live router smoke requires a controlled bootstrap because an active
Gemini target will not load without `router_text_passed: true`: use an isolated
restricted staging group and caller, treat the flag as a provisional
change-controlled assertion, start the candidate config, run the exact router
text smoke immediately, and remove/stop the target if it fails. Retain the flag
and consider broader promotion only after that smoke passes and its safe scalar
evidence is recorded outside this repository. If that isolated bootstrap is
not available, keep the model catalog-only.

The codec rejects tools, images, structured output, reasoning controls, and
streaming; do not advertise or activate those shapes without a separately
implemented codec and exact direct plus router evidence.

Catalog metadata is not active routing eligibility by itself. If one upstream model is exposed through multiple provider skins, add and validate each skin as a separate provider or target dialect before expecting callers to use it. For example, `tool_support.openai_responses` on a catalog entry inherited by an `openai-chat` target documents metadata for that model, but Responses clients will not select that target unless an `openai-responses` skin or an explicitly validated bridge target is active in the requested group. Before promotion, check `/admin/reports/api/provider-catalog-status` and confirm each intended group shows the right `activeEligibilitySkin`, nonempty `effectiveToolSupport`, and no surprising `inactiveToolSupport` for the caller surface being validated.

## 6. Add A Restricted Smoke Group

Create a deployment-defined smoke or staging group with the candidate as the only target, or as the only target for the specific request shape being validated. Restrict caller access to test tokens, internal operators, or explicitly opted-in users and agents. Validate sample and local production snapshots with structured YAML parsing before starting the router.

Do not promote directly from catalog to broad active groups. The smoke or staging group proves router encoding, target eligibility, diagnostics, cost calculation, and error behavior without exposing ordinary callers.

## 7. Router-Smoke The Same Shapes

Run router-level smokes through the same API shapes that passed directly upstream:

- `/v1/chat/completions` for OpenAI Chat clients;
- `/v1/responses` for Responses-compatible clients and Codex-style tool flows;
- `/anthropic/v1/messages` for Anthropic-compatible clients and Claude Code-style tool flows; legacy `/v1/messages` remains a compatibility alias, not the preferred validation path.

For Chat-only upstreams exposed to Responses callers through the stateless bridge, keep the target dialect `openai-chat` and add explicit `responses_to_chat` metadata only after validation. The minimum bridge evidence is direct OpenAI Chat text, cap, and function-tool smoke plus router-level `/v1/responses` text and function-tool smokes through a restricted model group. Verify the upstream attempt uses `/chat/completions`, the caller receives Responses output, usage shows inbound `openai-responses` and target `openai-chat`, and translation diagnostics record `bridge_direction = responses_to_chat`. Enable `responses_to_chat.reasoning` only after a separate bridge smoke proves Responses `reasoning.effort` is preserved as Chat `reasoning_effort`. Stateful `previous_response_id`, provider-hosted tools, file/code/computer tools, images, structured output, and streaming remain unsupported until separate bridge flags and smokes pass.

For each route, verify selected provider/model, usage, request-time cost, latency, attempts, fallback status, and safe diagnostics. Include max-token cap checks, no-eligible-target checks for unsupported shapes, and negative media URL safety checks for image-capable targets. For tool or agent routes, run the actual client smoke in a disposable sandbox when client compatibility is part of the claim.

When a group is intended to serve several client surfaces, run the same smoke matrix against the same model group for each surface and compare selected provider/model/dialect distribution. If all traffic for one surface unexpectedly goes to a single fallback, inspect provider catalog status first: the group may have multiple active targets overall but only one effective target for that surface's native skin.

For OpenAI Chat coding-agent routes that claim both tools and image input, include a combined request with multiple messages, representative function-tool schemas, one image part, `stream:true`, and no caller output cap. The same configured target must satisfy the OpenAI Chat dialect, tool support, and image modality together. Record a negative no-eligible smoke for a group that lacks such a combined target and verify zero upstream attempts plus safe candidate/filter diagnostics.

For Chat-to-Responses bridge routes, first validate the exact upstream through direct OpenAI Responses text and function-tool smokes. Then create a restricted router smoke group whose target has `dialect: openai-responses` and explicit `bridges.chat_to_responses` metadata. Send Chat Completions unary and streaming text and function-tool requests through `/v1/chat/completions`; verify the upstream path is `/v1/responses`, the target dialect in usage is `openai-responses`, the inbound dialect remains `openai-chat`, and `request_translation_shapes` plus field events contain only safe scalar buckets and field names. Enable `bridges.chat_to_responses.reasoning` only after a bridge smoke proves Chat `reasoning_effort` is preserved as Responses `reasoning.effort`. If `bridges.chat_to_responses.stateful_sessions.enabled` will be enabled, send two same-session requests with the configured header and verify the second upstream Responses request includes the first upstream response `id` as `previous_response_id`, while a different caller or session header value does not reuse it. Use `backend: memory` only for local, single-process, or sticky-routed deployments. For `backend: redis`, validate startup with Redis address, namespace, env credential refs, TLS, and timeout config; then prove the second request can be served by a different router process sharing the Redis namespace. Also run a stale-session mock that rejects an injected ID and verify safe trace events for purge plus stateless retry, plus a Redis outage/timeout test that continues stateless with `bridge_session_backend_error`. With `server.streaming.translator: incremental` (the default), verify Responses SSE reaches the Chat caller incrementally; `synthesized` uses a unary upstream call. Run negative smokes for image input, structured output, or reasoning when the bridge metadata does not enable them.

For OpenAI Chat coding-agent routes that will receive large repository or IDE sessions, also run a sanitized large-payload fixture with representative message count and tool schemas. Use `scripts/large_payload_chat_smoke.py`, `scripts/prod_smoke_regressions.py` with a fixture from `testdata/smokes/production-derived/`, or an equivalent synthetic helper, not captured customer content. Run it direct to the upstream first, then through a restricted local router smoke group pinned to the same target, and finally through production only when a deployment-owned safe caller token and smoke group are available. Record request bytes, tool count, serialized tool-schema bytes, output cap, prompt-token scale, status, finish reason, latency, token usage, attempts, fallback, and safe request-shape buckets. If production prerequisites are missing, record the missing base URL, caller access, or report access as a blocker without printing raw tokens, token hashes, prompts, tool schemas, provider keys, or full config.

## 8. Promote Conservatively With Rollback

Move the candidate into stable active routing only after direct and router smokes, workload acceptance gates, and docs/config review pass for the exact request shapes the stable group will receive. Start with low weight or a narrow group, then monitor status, latency, usage, costs, fallback, provider errors, and caller complaints.

Use this promotion checklist for broad or coding-agent groups:

- direct upstream smokes passed for each exact provider/model/account/API skin;
- router-level smokes passed through the staging group for OpenAI Chat, OpenAI Responses, Anthropic Messages, or bridge directions that callers will use;
- large tool-bearing payloads passed when the target will serve Cursor, opencode, aider, Codex, Claude Code, or other repository/IDE agent traffic;
- smokes covered tool count, serialized tool-schema bytes, request byte bucket, output-cap field, forced or object `tool_choice`, streaming mode, reasoning or thinking controls, structured output, and modalities that the group advertises;
- provider catalog status shows the expected `activeEligibilitySkin`, effective tool support, modalities, and no misleading inactive capability as active support;
- usage/report evidence contains safe request IDs, selected provider/model/dialect, attempts, fallback state, request-shape buckets, latency, token usage, and sanitized error classes;
- workload acceptance, Harbor, unit-test, OCR, browser-control, extraction, or other objective verifier passed at the group quality target;
- rollback is written down before the weight or caller allow-list change.

Partial compatibility should become metadata, not a blanket removal. If a target handles ordinary text and small tools but fails large coding-agent payloads, keep or promote it only with accurate `request_shape_support` limits such as `max_request_bytes`, tool-schema byte limits, supported inbound dialects, bridge flags, and capability metadata. The router should skip that target for unsupported shapes before upstream instead of discovering the incompatibility as repeated provider HTTP 400s.

Do not blanket-switch a stable group from Chat to Responses, from Responses to Chat, or to an Anthropic-compatible skin to mitigate an incident. First identify the incident surface and bridge direction from request evidence, then validate and promote only the separate skin or bridge target that passed that surface.

Define rollback before promotion:

1. remove or lower the active target weight;
2. remove capability metadata that made unsafe requests eligible;
3. add or tighten `request_shape_support` so known-bad shapes skip before upstream;
4. isolate the target back into a smoke or staging group;
5. restore the previous config snapshot and restart;
6. rerun `/readyz`, `/v1/models`, affected API smokes, and the representative workload fixture that triggered rollback.

## 9. Update Docs, Tests, And Evidence

Keep behavior, config, docs, and tests aligned in the same change:

- update `config.example.yaml`, local production snapshots, and production config only when requested and validated;
- update internal runbooks and public Docusaurus docs when routing, auth, models, CLI/API behavior, telemetry, deployment, or production behavior changes;
- update diagnostics schema docs when GORM diagnostic models change;
- run stale-doc searches for old provider/model names, capability claims, route status, and private markers;
- run relevant validation such as `go test ./...`, targeted router tests, `make docs-qa`, and live smokes when provider/model activation is in scope.

Before final handoff, include changed files, validation commands/results, safe onboarding evidence, and any intentionally deferred live-provider or production steps.
