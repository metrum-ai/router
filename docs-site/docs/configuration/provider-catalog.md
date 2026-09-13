---
title: Provider Catalog
doc_type: reference
---

# Provider Catalog

Provider catalog entries define upstream skins, credentials, model IDs, request dialects, tested capabilities, modalities, and request-time cost metadata. Callers request deployment-defined model groups; they do not need to know upstream provider model IDs.

This example is a partial subset of `config.example.yaml`; the shipped sample config is the source of truth for exact model IDs, pricing fields, source URLs, and update dates.

```yaml title="config.example.yaml"
providers:
  baseten:
    base_url: https://inference.baseten.co/v1
    dialect: openai-chat
    api_key: ${BASETEN_API_KEY}
    api_key_env: BASETEN_API_KEY
    key_id: baseten-default
    models:
      gpt-oss-120b:
        model: openai/gpt-oss-120b
        tier: coding
        input_price_per_million_usd: 0.1
        output_price_per_million_usd: 0.5
        input_modalities:
        - text
        output_modalities:
        - text
        tool_support:
          openai_chat:
          - tools
          - tool_choice
```

## Schema

Provider-level fields identify the upstream API skin:

- `base_url`, `dialect`, `auth_scheme`, `api_key_env`, and `headers` control how the router calls upstream.
- `models.<ref>.model` is the exact upstream model ID.
- `input_price_per_million_usd`, `output_price_per_million_usd`, optional `cached_input_price_per_million_usd`, optional image pricing fields, `pricing_source`, `pricing_updated_at`, and `pricing_notes` are stored in `config.example.yaml` and copied into request usage rows at request time.
- `cached_input_price_per_million_usd` is optional. Omit it when unknown. Set `0` when cache reads are known free. The DB-backed catalog projection stores the same nullable scalar so managed configs do not silently drop it.
- `input_modalities` and `output_modalities` describe validated I/O such as `text`, `image`, or `video`.
- `tool_support.openai_chat`, `tool_support.openai_responses`, and `tool_support.anthropic_messages` are per-skin validation evidence, not marketing claims.
- `reasoning` and `honors_max_tokens` further constrain eligible targets for reasoning requests or explicit caller output caps.

Internal vLLM, SGLang, Baseten, Crusoe, Fireworks, OpenRouter, Anthropic, MiniMax, Kimi, xAI, and OpenAI-compatible services all use this same catalog shape. Configure separate provider skins when the same upstream exposes multiple dialects, such as OpenAI Chat and OpenAI Responses.

`api_key_env` names the deployment environment variable for the upstream
credential. The router resolves that value only in runtime memory; do not put
the raw credential in a config database, catalog metadata, headers, logs, or
examples. A catalog-only provider can remain unconfigured when its deployment
does not have an entitled credential.

For the current managed catalog projection, capability metadata is stored as
one required catalog capability record with normalized child records rather
than JSON or packed lists. See [Managed Catalog Capability
Foundation](./router-config#managed-catalog-capability-foundation) for its
read-only scope and target-override boundary.

## Capability Declaration Rules

Capability fields are eligibility controls, not marketing descriptions. If a capability is omitted, the router treats it as unavailable and skips the target for requests that require it.

Use a repeatable provider probe as the first pass for direct upstream evidence. The probe should exercise the exact upstream base URL, model ID, API dialect, tool mode, image mode, structured-output mode, and output-cap behavior that will be declared in metadata.

Map the probe output mechanically:

| Probe | Passing metadata | Failure or not tested |
|---|---|---|
| `text` | catalog the exact `model` ID with `input_modalities: [text]` and `output_modalities: [text]` | do not activate the target |
| `max-tokens-cap` | omit `honors_max_tokens` because the default is true | set `honors_max_tokens: false` when the cap is not honored |
| `omitted-choice-tools` | add `tools`, `function`, or `client_tools` for the tested skin and retain the passing omitted-choice evidence | omit tool support for that skin |
| `auto-tools` | retain separate passing evidence for explicit `tool_choice: "auto"` | mark `tool_choice` unsupported when that explicit shape fails |
| `forced-tools` | add `tool_choice` where the skin uses OpenAI-style tool choice and retain separate forced-choice evidence | omit `tool_choice` or mark `forced_tool_choice` unsupported |
| `structured-outputs` | add `structured_outputs` for the tested skin | omit structured-output support |
| `image-input` | add `image` to `input_modalities` after router-level image smoke also passes | keep text-only modalities |
| `reasoning-effort` or Anthropic thinking | add `reasoning.supported`, `mode`, and the tested control type | omit reasoning metadata |

Date-stamp the evidence in `pricing_notes` or validation notes. Include what passed, what failed, and what was not tested. A model passing one skin does not imply any other skin passed; validate OpenAI Chat, OpenAI Responses, and Anthropic Messages independently.

## Rollback

If a cataloged model fails validation, remove it from every active `models.<group>.targets[]` entry first, keep the provider catalog entry only if it remains useful for smoke testing, and restart or reload through the normal deployment path. Remove capability fields such as `structured_outputs`, `image`, or `tool_choice` when only that request shape fails.

## Related

See [Providers And Models](../providers-models/overview), [Add A Provider Or Model](../reference/add-provider-model), [Model Metadata](../reference/model-metadata), [Self-Hosted Upstreams](./self-hosted-upstreams), and [Router Configuration](./router-config).
