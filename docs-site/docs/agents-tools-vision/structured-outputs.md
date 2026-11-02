---
title: Structured Outputs
doc_type: explanation
---

# Structured Outputs

Structured-output requests ask an upstream model to return JSON matching a caller-provided schema. Metrum AI Router treats these requests as routing requirements: it selects only targets that have explicit structured-output metadata for the caller API skin, then forwards the schema payload to the selected upstream.

The router does not currently perform application-level JSON Schema validation, provider-specific schema-subset enforcement, or output repair. A target can be eligible and still return an upstream error if the provider rejects a schema feature, strictness setting, or combination of fields.

## Supported Request Shapes

| Caller API | Structured Field | Required Metadata |
| --- | --- | --- |
| OpenAI Chat Completions | `response_format` with JSON Schema | `tool_support.openai_chat: [structured_outputs]` |
| OpenAI Responses | `text.format` with JSON Schema | `tool_support.openai_responses: [structured_outputs]` |
| Anthropic Messages | No OpenAI structured-output equivalent | Use tools or another deployment-documented pattern instead. |

Structured-output support is dialect-specific. A target that passes Chat Completions `response_format` is not automatically valid for Responses `text.format`, and a target that supports tools is not automatically structured-output capable.

## Eligibility And Errors

When a request includes structured-output fields, the router filters the requested model group's targets before strategy selection. The remaining target must also satisfy any other request requirements, such as tools, images, reasoning, streaming, and explicit max-token cap behavior.

If no configured target in the requested group satisfies all requirements for Chat, Responses, or Messages, the router returns a 400 compatibility error and does not call an upstream provider. See [Error Reference](../reference/errors). If an eligible upstream rejects the schema or returns malformed output, the caller sees the provider or router error for that upstream attempt according to normal retry and fallback behavior.

## Chat Completions Example

Use a model group returned by `/v1/models`; `structured-json` is only a placeholder.

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "structured-json",
    "messages": [
      {
        "role": "user",
        "content": "Extract the ticket id and priority from: INC-1234 urgent"
      }
    ],
    "response_format": {
      "type": "json_schema",
      "json_schema": {
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "normal", "urgent"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    },
    "max_tokens": 200
  }'
```

The target must advertise:

```yaml
tool_support:
  openai_chat: [structured_outputs]
```

If the same request also includes tools or forced tool choice, the selected target must advertise those capabilities for `openai_chat` too:

```yaml
tool_support:
  openai_chat: [tools, tool_choice, structured_outputs]
```

## Responses Example

```bash
curl "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "structured-json",
    "input": "Extract the ticket id and priority from: INC-1234 urgent",
    "text": {
      "format": {
        "type": "json_schema",
        "name": "ticket_extract",
        "strict": true,
        "schema": {
          "type": "object",
          "properties": {
            "ticket_id": {"type": "string"},
            "priority": {"type": "string", "enum": ["low", "normal", "urgent"]}
          },
          "required": ["ticket_id", "priority"],
          "additionalProperties": false
        }
      }
    },
    "max_output_tokens": 200
  }'
```

The target must advertise:

```yaml
tool_support:
  openai_responses: [structured_outputs]
```

For Responses function tools plus structured outputs, validate the combined request and declare both capabilities:

```yaml
tool_support:
  openai_responses: [function, structured_outputs]
```

## Smoke-Test Pattern

For every target that claims structured outputs:

1. Run a direct upstream request for the exact provider, model ID, account, dialect, and schema shape.
2. Run the same request through a router smoke group with one target.
3. Repeat with streaming if clients will stream structured-output requests.
4. Repeat with tools when the target claims both tool and structured-output support for the same dialect.
5. Repeat with a tiny explicit output cap if callers rely on cap forwarding.
6. Inspect usage and routing telemetry for the selected provider/model, request status, token counts, calculated cost, latency, attempts, and fallback status.

Keep the target catalog-only or smoke-only until the schema subset used by the deployment's clients passes both direct and router-level smokes.

## Operational Guidance

- Keep schemas simple and aligned with the provider's documented JSON Schema subset.
- Use realistic output budgets for acceptance tests so reasoning-heavy targets have enough room to produce final JSON.
- Treat provider-specific schema errors as validation findings; remove `structured_outputs` metadata or remove the target from active groups until fixed.
- Record safe validation notes in catalog metadata without storing prompts, raw tool outputs, provider keys, router tokens, token hashes, or full config.
- Link client teams to [`/v1/models`](../getting-started/available-models) so they use a deployment-defined group that is allowed for their token.

For broader request-surface details, see [API Compatibility](../reference/api-compatibility). For provider promotion and rollback, see [Providers And Models](../providers-models/overview).
