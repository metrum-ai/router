---
title: Image Analysis And VLM Routing
doc_type: howto
---

# Image Analysis And VLM Routing

Metrum AI Router accepts image inputs through the OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages API shapes. Image-bearing requests can use the same deployment-defined router model-group names as text requests, but the router only selects upstream targets that advertise `image` in `input_modalities` and are validated for the inbound API skin and request shape.

Model-group modality metadata is aggregate metadata. For example, a group can advertise image input because it contains a validated Responses target while having no eligible Anthropic Messages image target. Validate Chat, Responses, and Messages independently. If an authorized group returns a 400 compatibility or modality error, inspect the requested dialect, image modality, tools, and output-cap requirements instead of treating another skin's image pass as sufficient. See [Error Reference](../reference/errors).

## Configure A Vision-Capable Target

Add modality and pricing metadata to each upstream model after a direct provider smoke and a router-level image smoke pass.

```yaml
providers:
  openai:
    base_url: https://api.openai.com/v1
    dialect: openai-responses
    api_key: ${OPENAI_API_KEY}
    api_key_env: OPENAI_API_KEY
    key_id: openai-primary
    models:
      gpt-5-4-nano:
        model: gpt-5.4-nano
        tier: vision
        input_price_per_million_usd: 0.20
        output_price_per_million_usd: 1.25
        input_modalities: [text, image]
        output_modalities: [text]
      gpt-5-4:
        model: gpt-5.4
        tier: vision
        input_price_per_million_usd: 2.50
        output_price_per_million_usd: 15.00
        input_modalities: [text, image]
        output_modalities: [text]
        pricing_notes: Keep pricing source and update-date evidence in config.example.yaml. Use as a direct-provider replacement for OpenRouter-hosted Anthropic vision weight only when validation passes and the deployment accepts the cost profile.

  xai:
    base_url: https://api.x.ai/v1
    dialect: openai-chat
    api_key: ${XAI_API_KEY}
    api_key_env: XAI_API_KEY
    key_id: xai-primary
    models:
      grok-4-3:
        model: grok-4.3
        tier: vision
        input_price_per_million_usd: 1.25
        output_price_per_million_usd: 2.50
        image_input_price_per_million_tokens_usd: 1.25
        input_modalities: [text, image]
        output_modalities: [text]
        tool_support:
          openai_chat: [tools, structured_outputs]

  openrouter:
    base_url: https://openrouter.ai/api/v1
    dialect: openai-chat
    api_key: ${OPENROUTER_API_KEY}
    api_key_env: OPENROUTER_API_KEY
    key_id: openrouter-primary
    models:
      openrouter-claude-sonnet-4-6:
        model: anthropic/claude-sonnet-4.6
        tier: vision
        input_price_per_million_usd: 3.00
        output_price_per_million_usd: 15.00
        input_modalities: [text, image]
        output_modalities: [text]
        pricing_notes: Keep pricing source and update-date evidence in config.example.yaml; activate only after the intended image workload passes.

models:
  # Example dedicated VLM route. Your deployment can use any group name.
  example-vlm:
    strategy: weighted
    targets:
      - provider: xai
        model_ref: grok-4-3
        weight: 65
      - provider: openai
        model_ref: gpt-5-4
        weight: 27
      - provider: openai
        model_ref: gpt-5-4-nano
        weight: 8
```

Use `image_input_price_per_million_tokens_usd` when the provider reports image tokens. Use `image_input_price_per_image_usd` for customer cost allocation or providers that bill per image. If neither image-specific field is set, image tokens use the normal input-token price.

Price alone is not sufficient for promotion. Validate the exact account, model ID, API dialect, image payload shape, and task quality before adding a target to broad `vision` traffic. A candidate that accepts images but misses the expected OCR answer, leaks reasoning into a strict extraction response, or supports only data-URL images while common clients send remote image URLs should remain catalog-only or in a dedicated smoke group.

For capped requests, keep model quality and cap enforcement separate. A model can pass a realistic image-analysis smoke and still be unsafe for requests where the caller explicitly sets a small output cap such as OpenAI Chat `max_tokens` or `max_completion_tokens`, Responses `max_output_tokens`, or Anthropic Messages `max_tokens`. If a target returns far more output than requested, keep it cataloged but set `honors_max_tokens: false` on the catalog entry or target override; the router will skip it for capped requests and continue using other eligible VLM targets.

## Image URL Egress Policy

The router validates dereferenceable `http` and `https` image URLs before selecting an upstream target. By default, URLs that point to or resolve to loopback, link-local, RFC1918/private, multicast, unspecified, or other reserved addresses are rejected before any provider call. All hostname lookups in one request share a bounded 500 ms DNS budget. The router does not dereference image URLs or probe redirects; the upstream VLM may follow a redirect after receiving the URL, so deployment egress controls must independently block private, metadata, and administrative destinations. Inline `data:` URLs and base64 image blocks remain supported because they do not ask the upstream VLM to fetch a network URL.

Keep `server.upstream.allow_private_image_urls: false` for hosted and ordinary
private-upstream deployments. Setting it to `true` bypasses the router's
entire image-URL admission step—including scheme, host/address, and DNS
checks—not only private-address rejection. Use it only when a reviewed private
VLM design intentionally permits that behavior and deployment egress controls
block metadata services, internal administration endpoints, redirects, DNS
rebinding, and any other prohibited destination.

Blocked image URLs fail before target selection and before provider authentication is used. A failure usually means the URL is malformed, uses a non-HTTP scheme, resolves to a private or reserved address, cannot be resolved, or exceeds the request-wide DNS budget. Safe diagnostics distinguish `image_url_forbidden`, `image_url_dns_failure`, and `image_url_dns_timeout` without storing the URL.

## OpenAI Chat Example

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<allowed-vlm-model-group>",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "Read the receipt. Reply with only the merchant name."},
        {"type": "image_url", "image_url": {"url": "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}}
      ]
    }],
    "max_tokens": 512,
    "stream": false
  }'
```

## OpenAI Responses Example

```bash
curl "$ROUTER_BASE_URL/v1/responses" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<allowed-vlm-model-group>",
    "input": [{
      "role": "user",
      "content": [
        {"type": "input_text", "text": "Read the receipt. Reply with only the merchant name."},
        {"type": "input_image", "image_url": "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}
      ]
    }],
    "max_output_tokens": 512,
    "stream": false
  }'
```

## Anthropic Messages Example

```bash
curl "$ROUTER_BASE_URL/anthropic/v1/messages" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<allowed-vlm-model-group>",
    "max_tokens": 512,
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "Read the receipt. Reply with only the merchant name."},
        {"type": "image", "source": {"type": "url", "url": "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}}
      ]
    }]
  }'
```

## Codex CLI Image Smoke

Codex CLI sends image attachments through the OpenAI Responses API shape. Configure an OpenAI-compatible provider that points at the router, then attach an image with `--image`.

```bash
export METRUM_ROUTER_KEY="$ROUTER_TOKEN"

mkdir -p tmp/router-vision-smoke
curl -fsSL "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png" \
  -o tmp/router-vision-smoke/receipt.png

codex exec --ignore-user-config --ephemeral --skip-git-repo-check \
  --image tmp/router-vision-smoke/receipt.png \
  -c 'model="<allowed-vlm-model-group>"' \
  -c 'model_provider="metrum-ai-router"' \
  -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
  -c 'model_providers.metrum-ai-router.base_url="'"$ROUTER_BASE_URL"'/v1"' \
  -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
  -c 'model_providers.metrum-ai-router.wire_api="responses"' \
  "Read the attached receipt image. Reply with only the merchant name." </dev/null
```

## Claude Code Image Validation

Claude Code uses the Anthropic Messages API shape. For non-interactive validation, send the same image content blocks to `/anthropic/v1/messages` with `ANTHROPIC_AUTH_TOKEN` set to the router token.

```bash
unset ANTHROPIC_API_KEY
export ANTHROPIC_BASE_URL="$ROUTER_BASE_URL/anthropic"
export ANTHROPIC_AUTH_TOKEN="$ROUTER_TOKEN"

curl "$ANTHROPIC_BASE_URL/v1/messages" \
  -H "Authorization: Bearer $ANTHROPIC_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "<allowed-vlm-model-group>",
    "max_tokens": 512,
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "Read the receipt. Reply with only the merchant name."},
        {"type": "image", "source": {"type": "url", "url": "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}}
      ]
    }]
  }'
```

## Routing And Logging Behavior

Image-bearing requests bypass response caching. The router logs `input_has_image`, `input_image_count`, `input_image_tokens` when the upstream reports them, calculated `image_cost_usd`, and upstream-reported billed cost when the provider includes it.

Keep catalog-only VLM candidates out of active traffic until the exact API shapes you plan to support pass. Some providers advertise image support in a model catalog before the current account, region, or endpoint can actually serve image requests.

Validate the exact provider, model ID, suffix, API dialect, account entitlement, and region you plan to route. Treat image processing and OCR accuracy as separate gates: a model may accept and analyze an image but still fail an exact-answer OCR test. That can be acceptable for a conservative general VLM group, but not for OCR-specific or browser-control routes unless the exact workload verifier passes consistently.

Record dated provider-specific investigation results in deployment-private notes or a clearly labeled historical case study. Keep the public product docs focused on the validation method and customer-facing behavior, because provider catalogs, entitlements, and model quality change quickly.
