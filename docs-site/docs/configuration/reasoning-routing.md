---
title: Reasoning Routing
doc_type: howto
---

# Reasoning Routing

Reasoning routing lets callers request explicit reasoning or thinking controls while the router keeps provider credentials, model selection, budgets, and usage accounting server-side. The caller still requests one deployment-defined model group. The router filters that group's targets to the ones that can safely preserve the requested reasoning shape, then runs the group's configured strategy on the remaining targets.

Use this when a group should handle both ordinary requests and explicit reasoning requests. Ordinary requests can continue using the full ordinary eligible target mix. Requests with OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, or Anthropic Messages `thinking` use only compatible reasoning targets inside the same requested group.

## Reasoning Across API Surfaces

Reasoning is normalized for routing, but support remains API-surface specific. The router preserves explicit reasoning controls only through validated same-dialect targets or explicitly enabled bridge paths.

| Caller request | Upstream target | Current support |
|---|---|---|
| OpenAI Chat `reasoning_effort` | OpenAI Chat target | Supported when the active target has compatible `reasoning` metadata. The translated upstream field is `reasoning_effort`, or the target's configured OpenAI Chat encoding variant. |
| OpenAI Chat `reasoning_effort` | OpenAI Responses target through `chat_to_responses` | Supported only when `bridges.chat_to_responses.enabled: true`, `bridges.chat_to_responses.reasoning: true`, and compatible target `reasoning` metadata are all present. The translated upstream field is Responses `reasoning.effort`; summaries are sent only when the target supports them. |
| OpenAI Responses `reasoning` | OpenAI Responses target | Supported when the active target has compatible `reasoning` metadata. `reasoning.summary` is preserved only for targets that advertise summary support. |
| OpenAI Responses `reasoning` | OpenAI Chat target through `responses_to_chat` | Unsupported by default. A target must explicitly opt into `responses_to_chat.reasoning` after exact validation; otherwise the target is skipped with a bounded bridge filter reason such as `responses-to-chat-reasoning`. |
| Anthropic Messages `thinking` | Anthropic Messages target | Supported when the active target has compatible token-budget or default-thinking metadata. Budget and `max_tokens` rules are provider-specific and must be documented on the target. |
| Cross-provider effort-to-budget translation | Different reasoning-control family | Conservative and target-specific. Do not assume that an effort level maps to a durable token budget, or that a token budget maps to a provider's quality tier, unless the exact target metadata and smokes prove it. |

Unsupported today unless a target explicitly documents otherwise: broad automatic Chat/Responses reasoning bridges, `previous_response_id` continuity on stateless bridges, provider-hosted tools through a bridge, and preserving reasoning controls through unvalidated streaming, image, structured-output, or forced-tool bridge shapes. OpenAI's [reasoning guide](https://developers.openai.com/api/docs/guides/reasoning) checked on 2026-06-30 recommends the Responses API and `previous_response_id` or replayed prior output items for preserving reasoning context across turns. Anthropic [extended-thinking docs](https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking) checked on 2026-06-30 document Messages-specific thinking constraints, and MiniMax M3 [tool-use docs](https://platform.minimax.io/docs/guides/text-m3-function-call) checked on 2026-06-30 require preserving full response objects, including thinking/reasoning fields, in tool loops. Treat those as upstream integration requirements, not as router support unless the deployment has validated the corresponding target path.

## Configure Metadata

Reasoning metadata is eligibility metadata. Add it only after direct upstream and router-level smokes pass for the exact provider, model ID, dialect, and API skin. A model name or provider marketing page is not enough.

Minimal provider catalog example:

```yaml
providers:
  baseten:
    dialect: openai-chat
    models:
      glm-5-2:
        model: zai-org/GLM-5.2
        reasoning:
          supported: true
          mode: opt_in
          control: effort_enum
```

Use `control: effort_enum` for upstreams that accept levels such as `low`, `medium`, and `high`. Use `control: token_budget` for Anthropic-style thinking budgets. Add compatibility fields only when the exact upstream behavior is known:

```yaml
reasoning:
  supported: true
  mode: opt_in
  control: token_budget
  min_budget_tokens: 2048
  max_budget_tokens: 24576
  budget_must_be_less_than_max_tokens: true
  rejects_max_tokens: true
  rejects_temperature: true
  rejects_top_p: true
  supports_summaries: true
```

## Bridge Reasoning

Reasoning can cross an explicit bridge only when both the bridge flag and target reasoning metadata say the exact shape was validated. For Chat-to-Responses targets, set `bridges.chat_to_responses.reasoning: true` only after a router smoke proves Chat `reasoning_effort` reaches the upstream as Responses `reasoning.effort`. For Responses-to-Chat targets, set `responses_to_chat.reasoning: true` only after a router smoke proves Responses `reasoning.effort` reaches the upstream as Chat `reasoning_effort`.

If a reasoning request reaches a bridge target without the matching flag, the target is skipped before upstream. Use a dedicated smoke group rather than a broad production group while validating this behavior, and grant only scoped validation callers access to that group for the test window.

## Weighted Group Pattern

A mixed weighted group can prioritize validated reasoning targets only when the caller asks for reasoning. This keeps ordinary traffic on the configured weighted mix while preventing explicit reasoning controls from being dropped or sent to incompatible targets.

```yaml
models:
  coding:
    strategy: weighted
    targets:
      - provider: baseten
        model_ref: gpt-oss-120b
        weight: 60
      - provider: baseten
        model_ref: glm-5-2
        weight: 20
        reasoning:
          supported: true
          mode: opt_in
          control: effort_enum
      - provider: provider_without_reasoning
        model_ref: low-cost-text
        weight: 20
```

In this example, `coding` is only a sample group name. Ordinary compatible requests can use all ordinary eligible targets. A request that includes `reasoning_effort`, Responses `reasoning`, or Messages `thinking` can use only the target with compatible `reasoning` metadata. If several targets remain, the weighted strategy still applies to those remaining targets.

If no configured target remains for Chat, Responses, or Messages, the router returns a 400 compatibility error before sending an upstream request. OpenAI-shaped routing error requirements include `reasoning`. See [Error Reference](../reference/errors).

Codex CLI sessions use OpenAI Responses and commonly send both function tools and a `reasoning` object. That request shape needs at least one **Responses** target in the requested group with both `tool_support.openai_responses` function tools and validated `reasoning` metadata (`supported: true` with a compatible control such as `effort_enum`). A Chat Completions target, or a Chat provider only labeled with `dialect: openai-responses` without Responses-catalog reasoning metadata, is not enough even when Responses+tools without reasoning still succeeds. Prefer a dedicated Responses provider skin for Codex rather than relying on dialect overrides. The reference config keeps MiniMax Responses and OpenAI `gpt-5.4-nano` as Responses reasoning-capable coding-group examples; deployments must keep equivalent metadata on the live coding groups they expose to Codex.

Reasoning changes for coding-agent groups should be validated with a sanitized agent smoke matrix as well as tiny direct provider probes. The matrix should include OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, Anthropic Messages `thinking`, bridge positive cases, and negative cases such as previous-response state on stateless bridges, unsupported streaming bridge requests, image+tools+reasoning without a compatible target, and thinking budget/output-cap conflicts. Run it against a dedicated smoke group with a caller token explicitly allowed to that group.

## Caller Examples

Use the base URL and model groups issued by the deployment administrator. Public examples use `https://<router-host>` and placeholder router tokens only.

OpenAI Chat Completions with `reasoning_effort`:

```bash
ROUTER_BASE_URL="https://<router-host>"

curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "coding",
    "messages": [{"role": "user", "content": "Reason briefly and answer OK."}],
    "reasoning_effort": "low",
    "max_tokens": 256,
    "stream": false
  }'
```

OpenAI Responses with `reasoning`:

```bash
ROUTER_BASE_URL="https://<router-host>"

curl "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "coding",
    "input": "Reason briefly and answer OK.",
    "reasoning": {"effort": "low", "summary": "auto"},
    "max_output_tokens": 256,
    "stream": false
  }'
```

Anthropic Messages with `thinking`:

```bash
ROUTER_BASE_URL="https://<router-host>"

curl "$ROUTER_BASE_URL/anthropic/v1/messages" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "coding",
    "max_tokens": 1024,
    "thinking": {"type": "enabled", "budget_tokens": 512},
    "messages": [{"role": "user", "content": "Reason briefly and answer OK."}]
  }'
```

Anthropic thinking budgets interact with the caller output cap. Some upstreams require `budget_tokens` to be less than `max_tokens`, while interleaved-thinking modes can define a larger total thinking budget across tool loops. Keep this as target metadata and validate the exact Messages skin before exposing it.

OpenAI Chat reasoning bridged to a Responses target uses the same caller shape as native Chat. The difference is target metadata, not a new caller field:

```yaml
models:
  bridge-smoke:
    strategy: static
    targets:
      - provider: responses_provider
        model_ref: responses-reasoning-model
        bridges:
          chat_to_responses:
            enabled: true
            reasoning: true
        reasoning:
          supported: true
          mode: opt_in
          control: effort_enum
```

Run the Chat request with `model: "bridge-smoke"` and verify telemetry shows inbound Chat, target Responses, `bridge_direction = chat_to_responses`, and `translated_reasoning_control = reasoning`.

Responses-to-Chat reasoning is rejected unless the target explicitly validates it. A shape example:

```bash
curl -i "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chat-bridge-smoke",
    "input": "Reason briefly and answer OK.",
    "reasoning": {"effort": "low"},
    "max_output_tokens": 256
  }'
```

For a Chat bridge target without `responses_to_chat.reasoning`, expect a 400 compatibility error and a bounded bridge filter reason such as `responses-to-chat-reasoning` before upstream; see [Error Reference](../reference/errors). A `previous_response_id` on the same stateless bridge should be filtered separately because stateless Chat targets cannot preserve Responses conversation state.

## Verify Model Metadata

Call `/v1/models` with the same router token the client will use. The response is filtered to that token's allow list.

```bash
curl "$ROUTER_BASE_URL/v1/models" \
  -H "Authorization: Bearer $ROUTER_TOKEN"
```

Reasoning-capable groups expose safe Codex metadata such as supported levels and summary support. Groups without validated reasoning targets omit these reasoning fields instead of returning empty or false placeholders.

```json
{
  "object": "list",
  "data": [
    {
      "id": "coding",
      "object": "model",
      "owned_by": "smart-llmrouter",
      "default_reasoning_summary": "none",
      "supported_reasoning_levels": [
        {"effort": "low", "description": "Fast responses with lighter reasoning"},
        {"effort": "medium", "description": "Balances speed and reasoning depth for everyday tasks"},
        {"effort": "high", "description": "Greater reasoning depth for complex problems"}
      ],
      "supports_reasoning_summaries": true
    }
  ]
}
```

The returned `id` values are deployment-defined router model groups, not provider model IDs and not a full upstream inventory.

For OpenAI Responses targets, publish these fields only after direct and router-level smokes pass for the exact model and endpoint. For example, a validated Responses target may advertise `low`, `medium`, and `high` after those `reasoning.effort` values return useful output and tool behavior is verified. Provider minimum output budgets still apply; if an upstream rejects tiny `max_output_tokens` values, keep that caveat in model metadata and use realistic acceptance budgets for coding-agent traffic.

## Prove The Running Deployment

Source config is not enough. After deployment, administrators should prove the running router with the same caller token and model group that clients use:

1. Call `/v1/models` and confirm the group advertises `supported_reasoning_levels` and `default_reasoning_level`.
2. Run one OpenAI Chat request with `reasoning_effort`.
3. Run one OpenAI Responses request with `reasoning.effort`.
4. Run one Anthropic Messages request with `thinking` when that surface is enabled.
5. Join usage telemetry by `X-Request-Id` and confirm the selected provider/model/dialect and translated reasoning control.

For bridge smokes, also confirm `request_translation_shapes.bridge_direction` is `chat_to_responses` or `responses_to_chat`. Chat-to-Responses reasoning smokes should show `translated_reasoning_control = reasoning`; Responses-to-Chat reasoning smokes should show `translated_reasoning_control = reasoning_effort`.

Use an operator-approved reasoning smoke workflow to automate this for staging and production deployments. The smoke output should contain only safe scalar evidence: request IDs, model group, selected provider/model/dialect, translated reasoning control, and fallback status. It must not print router tokens, provider keys, prompts, tool schemas, raw responses, or full config.

For smaller deployments, the same proof can be collected manually with the request examples above plus read-only usage-report or diagnostics access.

For manual SQL verification, join by request ID and attempt index. This shape shows only safe scalar fields:

```sql
SELECT
  ru.request_id,
  ru.inbound_dialect,
  ra.provider,
  ra.model,
  ra.dialect AS target_dialect,
  rts.bridge_direction,
  rts.translated_reasoning_control
FROM request_usage ru
JOIN request_attempts ra
  ON ra.request_id = ru.request_id
JOIN request_translation_shapes rts
  ON rts.request_id = ra.request_id
 AND rts.attempt_index = ra.attempt_index
WHERE ru.request_id = '<request-id>';
```

## Negative Eligibility Test

Run a reasoning request against a test group that has no reasoning-compatible target. The router should fail before upstream:

```bash
curl -i "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "text-only-test",
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "reasoning_effort": "low",
    "max_tokens": 256,
    "stream": false
  }'
```

Expected result:

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "unsupported_capability",
    "param": "reasoning",
    "message": "no eligible upstream target is configured for model \"text-only-test\" with openai-chat requests requiring text, reasoning, max_tokens; unsupported request capability: reasoning",
    "details": {
      "model": "text-only-test",
      "dialect": "openai-chat",
      "requirements": ["text", "reasoning", "max_tokens"],
      "hint": "ask the router administrator to add or enable an upstream target for this model group that supports the requested API dialect, tools, and input modalities"
    }
  }
}
```

## Validation Checklist

Before enabling reasoning metadata in an active group:

- run a direct upstream smoke for the exact provider, model ID, dialect, and reasoning control;
- run the same request through a router-level smoke group;
- test OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, and Anthropic Messages `thinking` separately when those API skins will be exposed;
- use realistic output budgets for acceptance because reasoning-heavy models can spend small caps on internal reasoning and return empty final content;
- run low-cap tests such as `max_tokens: 1`, `max_completion_tokens: 1`, or `max_output_tokens: 1` to prove cap forwarding, target skipping, or configured translation;
- verify `/v1/models` exposes the intended reasoning metadata to allowed callers;
- run a negative compatibility-error test and confirm no upstream attempt is recorded;
- query usage/reporting telemetry after success and failure cases to confirm selected provider/model, attempts, status, latency, throughput, token counts, cost fields, and safe reasoning metadata.

If validation fails, remove the `reasoning` metadata or keep the target catalog-only until the exact request shape passes. Roll back by restoring the previous target metadata, relaxing a reasoning-only contract or dynamic-score hard filter, or reverting the group config from the deployment backup.

## Dynamic Score And Contracts

The same request eligibility rules apply to `weighted`, `failover`, `dynamic_score`, `script`, and `external` strategies. Dynamic score can add `hard_filters.require_reasoning_support_when_requested: true`, but target metadata is still the baseline that tells the router which targets can preserve explicit reasoning controls.

Use a model-group contract `required_capabilities.reasoning` when the group itself is intended to always be reasoning-capable. Use per-target reasoning metadata in a mixed weighted group when ordinary requests should still use non-reasoning targets, but explicit reasoning requests must be restricted to validated reasoning targets.

## Public Documentation Safety

Public docs and examples must not include real router tokens, provider keys, token hashes, production hostnames, private deployment paths, private headers, or exact private production config. Use placeholder URLs, placeholder token variables, sample model group names, and sanitized response examples.
