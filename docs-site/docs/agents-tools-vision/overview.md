---
title: Agents, Tools, And Vision
doc_type: explanation
---

# Agents, Tools, And Vision

Metrum AI Router keeps agent clients on stable caller-facing model groups while filtering each request to upstream targets that have passed the exact API-skin and capability checks needed for that turn.

This matters because ordinary text, function tools, client tools, image input, structured outputs, and reasoning controls are separate compatibility surfaces. A model that works for one surface is not automatically safe for another.

## Workload Surfaces

| Workload | Caller surface | Required target validation |
|---|---|---|
| OpenAI-compatible chat tools | `/v1/chat/completions` with `tools` and `tool_choice` | `tool_support.openai_chat` for the exact provider/model/dialect. |
| Codex and Responses tools | `/v1/responses` with function tools | `tool_support.openai_responses` for the exact Responses skin. |
| Claude Code client tools | `/anthropic/v1/messages` with Anthropic-style tools | `tool_support.anthropic_messages` for the exact Messages skin. |
| Image and VLM requests | OpenAI Chat, Responses, or Messages image payloads | Validated `image` input modality on the exact active target and API shape. |
| Structured outputs | Chat `response_format` or Responses `text.format` | `structured_outputs` metadata for the same dialect and target. |

## Routing Behavior

Caller-visible model groups can contain multiple target types. The router filters targets before selection so a text-only target can serve ordinary text while tool-bearing or image-bearing requests use targets validated for those capabilities.

That lets a coding-agent user keep one allowed model group for a mixed task instead of manually switching between a language route and a vision route. If no eligible target remains after filtering, the caller receives a router error before an unsafe upstream call is attempted.

## What To Validate

Before adding or increasing an agent-capable target:

- Run a direct upstream smoke for the exact provider model, API path, tool schema, tool-choice mode, image payload, or structured-output schema.
- Run the same request through the router model group.
- Confirm the selected upstream target, finish reason, usage fields, fallback behavior, and caller-visible response shape.
- Use realistic output budgets for reasoning or VLM models; tiny budgets are useful only for cap-enforcement checks.
- Record any limitation in provider metadata instead of broadening active routing.

For coding-agent clients, validate the client workflow as well as the raw API shape. A passing client smoke should prove text, workspace edits, tools, model-group selection, and image attachments where the client supports them. See [Coding-Agent Client Matrix](../getting-started/coding-agent-clients).

OpenAI Chat clients that send tools and images in the same request need one eligible target that supports both capabilities on the OpenAI Chat skin. Do not treat a Responses image target or an Anthropic tool target as a substitute for that OpenAI Chat request. A safe failure returns a 400 compatibility or modality error before upstream and records only scalar request-shape and target-filter diagnostics. See [Error Reference](../reference/errors).

Claude Code compatibility is validated on the Anthropic Messages skin, even when the same upstream model also has OpenAI Chat or Responses metadata. New Claude Code setup should use `https://<router-host>/anthropic`; legacy `/v1/messages` aliases remain for existing clients. A Chat or Responses tool smoke does not prove client-tool, subagent, thinking, max-token, or large-context behavior for Claude Code.

## Related Pages

- [Codex CLI](../getting-started/codex-cli)
- [Claude Code CLI](../getting-started/claude-code-cli)
- [Coding-Agent Client Matrix](../getting-started/coding-agent-clients)
- [Image Analysis And VLM Routing](../configuration/image-analysis-vlm)
- [Structured Outputs](./structured-outputs)
- [API Compatibility](../reference/api-compatibility)
- [Harbor Agentic Coding Case Study](../evaluation/harbor-case-study)
