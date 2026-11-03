# Self-Hosted vLLM And SGLang Upstreams

Metrum AI Router can route to enterprise-owned inference services that expose OpenAI-compatible APIs, including vLLM and SGLang. Keep these services on private network names, expose the router as the governed ingress point, and use router model groups as the caller-facing contract.

Hosted OpenAI-compatible providers such as Crusoe Managed Inference, Baseten Model APIs, or OpenRouter use the same provider catalog shape but are not self-hosted upstreams. Configure their public `/v1` endpoints with `dialect: openai-chat` and provider API keys, then run the same direct and router-level validation gates before activation. Keep hosted provider examples in public docs free of private hostnames and deployment-specific secrets.

Upstream references checked on 2026-06-17:

- vLLM online serving: https://docs.vllm.ai/en/latest/serving/online_serving/
- vLLM multimodal inputs: https://docs.vllm.ai/en/stable/features/multimodal_inputs/
- vLLM tool calling: https://docs.vllm.ai/en/latest/features/tool_calling/
- SGLang multimodal language models: https://docs.sglang.io/docs/supported-models/multimodal_language_models
- SGLang tool parser: https://docs.sglang.io/docs/advanced_features/tool_parser

## Reference deployments

`deploy/reference/<vendor>-<tier>/` holds deployable reference configurations for
one mid-range NVIDIA datacenter GPU, an NVIDIA GB10 edge system, an AMD Instinct
MI350P and an AMD Strix Halo edge system. Each tier has the same file set
(`config.yaml` model groups, `compose.yaml`, `images.lock`, `SIZING.md`, README,
`results/`) and uses only official upstream images pinned by digest, so moving
between vendors is a configuration change. Start from the tier README; the
model-group shape and the escalation group are identical across tiers.

## Configuration Pattern

Use `dialect: openai-chat` for vLLM or SGLang `/v1/chat/completions` services. Use `dialect: openai-responses` only after validating the upstream's `/v1/responses` behavior with the exact client and tool path.

If the internal service requires a bearer token, set `auth_scheme: bearer` and load `api_key` from the deployment environment. If access is enforced by network policy, mTLS, or a service mesh, omit `api_key`; the router only sends upstream authorization when a provider API key is configured.

```yaml
providers:
  vllm_qwen_tools:
    base_url: http://vllm-qwen-tools.inference.svc.cluster.local:8000/v1
    dialect: openai-chat
    auth_scheme: bearer
    api_key: ${VLLM_QWEN_API_KEY}
    api_key_env: VLLM_QWEN_API_KEY
    key_id: vllm-qwen-tools-prod
    models:
      qwen3-coder-tools:
        model: qwen3-coder-tools
        tier: coding
        input_price_per_million_usd: 0.00
        output_price_per_million_usd: 0.00
        input_modalities: [text]
        output_modalities: [text]
        pricing_notes: internal GPU allocation; set chargeback values if reports need allocated cost
        tool_support:
          openai_chat: [tools, tool_choice]

models:
  internal-coder:
    strategy: weighted
    targets:
      - { provider: vllm_qwen_tools, model_ref: qwen3-coder-tools, weight: 100 }
      - { provider: vllm_qwen_tools, model_ref: qwen3-coder-tools, weight: 100, tool_only: true }
```

The `model` value must match the upstream server's served model ID. For vLLM this is commonly controlled with `--served-model-name`; otherwise it defaults to the served model argument. Validate with:

```bash
curl -fsS "$UPSTREAM_BASE_URL/models" \
  -H "Authorization: Bearer $UPSTREAM_API_KEY"
```

For self-hosted models, set `input_price_per_million_usd` and `output_price_per_million_usd` to the enterprise chargeback rate if one exists. Use `0.00` only when reports should show token volume without allocated GPU cost. The router stores those prices and calculated costs on each usage row at request time.

For VLMs, set `input_modalities` and `output_modalities` after direct image/video smokes pass. Use `image_input_price_per_million_tokens_usd` when the upstream reports image tokens with a separate rate, or `image_input_price_per_image_usd` for fixed per-image chargeback. The router logs image count, upstream image-token counts when reported, calculated image cost, and upstream-reported billed cost when available. vLLM accepts OpenAI-style image content for supported multimodal models; use media access controls such as `--allowed-media-domains` in production so the upstream cannot fetch arbitrary internal URLs.

VLM catalog example:

```yaml
providers:
  vllm_qwen_vl:
    base_url: http://vllm-qwen-vl.inference.svc.cluster.local:8000/v1
    dialect: openai-chat
    auth_scheme: bearer
    api_key: ${VLLM_QWEN_VL_API_KEY}
    api_key_env: VLLM_QWEN_VL_API_KEY
    key_id: vllm-qwen-vl-prod
    models:
      qwen-vl:
        model: qwen-vl
        tier: vision
        input_price_per_million_usd: 0.00
        output_price_per_million_usd: 0.00
        image_input_price_per_image_usd: 0.0005
        input_modalities: [text, image]
        output_modalities: [text]
        pricing_notes: internal GPU allocation plus per-image chargeback

models:
  vision:
    strategy: weighted
    targets:
      - { provider: vllm_qwen_vl, model_ref: qwen-vl, weight: 100 }
```

## Tool-Calling Notes

The router forwards OpenAI-compatible chat `tools`, `tool_choice`, `parallel_tool_calls`, tool-result messages, and related Chat Completions fields to `openai-chat` targets. This is the compatibility path used by OpenAI-compatible agents such as Warp Agent. Tool-bearing OpenAI Chat requests only route to targets with explicit `tool_support.openai_chat`. The model emits `tool_calls`; the client or agent runtime executes those tools and sends tool-result messages back. The router does not execute application tools.

vLLM tool calling depends on the model, chat template, and parser. For example:

```bash
vllm serve Qwen/Qwen3-Coder-30B-A3B-Instruct \
  --host 0.0.0.0 \
  --port 8000 \
  --served-model-name qwen3-coder-tools \
  --api-key "$VLLM_QWEN_API_KEY" \
  --enable-auto-tool-choice \
  --tool-call-parser qwen3_xml
```

SGLang tool calling similarly depends on the model parser. For example:

```bash
python3 -m sglang.launch_server \
  --model-path Qwen/Qwen2.5-7B-Instruct \
  --host 0.0.0.0 \
  --port 30000 \
  --tool-call-parser qwen25
```

Do not assume a model is tool-capable because the server accepts `tools`. Validate that the response contains correctly shaped tool calls in both non-streaming and streaming modes if clients use both. Add `tool_support.openai_chat: [tools, tool_choice]` only after that validation passes for the exact served model, chat template, parser, and client protocol.

For a same-dialect `openai-chat` streaming smoke, verify that the router forwards
`stream: true`, preserves compatible `stream_options`, and proxies native SSE
containing `delta.tool_calls`, terminal `finish_reason: "tool_calls"`, and a
usage event when the caller requested `stream_options.include_usage`. Canceling
the downstream request must cancel the upstream call. Once the router writes
the first native event, it cannot safely retry, replay, or fall back to another
target. Same-dialect OpenAI Responses streaming proxies native upstream SSE.
Cross-dialect bridge paths remain unary upstream calls with router-encoded
caller streaming.

## Structured-Output Notes

Structured-output support is a separate request-shape capability. For OpenAI Chat, validate `response_format` with `type: json_schema`; for Responses, validate `text.format` with `type: json_schema`. Passing one dialect does not prove the other. Anthropic Messages has no OpenAI structured-output equivalent unless a deployment adds and documents an explicit compatibility layer.

Example metadata after validation:

```yaml
tool_support:
  openai_chat: [tools, tool_choice, structured_outputs]
  openai_responses: [function, structured_outputs]
```

The router forwards schema payloads to the upstream and uses metadata only for eligibility. It does not validate arbitrary JSON Schema subsets, enforce provider-specific schema restrictions, or repair model output. Unsupported schemas can still produce upstream/provider errors. If structured-output smokes fail, remove `structured_outputs` from the provider model or target override; remove the target from active groups if the failure makes the upstream unsafe for the deployed group contract.

## Acceptance Smokes

Direct upstream text smoke:

```bash
curl -fsS "$UPSTREAM_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $UPSTREAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen3-coder-tools",
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "max_tokens": 16,
    "stream": false
  }'
```

Direct upstream tool smoke:

```bash
curl -fsS "$UPSTREAM_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $UPSTREAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen3-coder-tools",
    "messages": [{"role": "user", "content": "Call get_weather for San Francisco in fahrenheit."}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get weather for a city.",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string"},
            "unit": {"type": "string", "enum": ["celsius", "fahrenheit"]}
          },
          "required": ["location", "unit"]
        }
      }
    }],
    "tool_choice": "auto",
    "stream": false
  }'
```

Direct upstream VLM smoke:

```bash
curl -fsS "$UPSTREAM_BASE_URL/chat/completions" \
  -H "Authorization: Bearer $UPSTREAM_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen-vl",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "Read the receipt. Reply with only the merchant name."},
        {"type": "image_url", "image_url": {"url": "https://cdn.learnopencv.com/wp-content/uploads/2018/06/04100007/receipt.png"}}
      ]
    }],
    "max_tokens": 64,
    "stream": false
  }'
```

Router text smoke:

```bash
curl -fsS "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "internal-coder",
    "messages": [{"role": "user", "content": "Reply OK only."}],
    "max_tokens": 16,
    "stream": false
  }'
```

Before adding a self-hosted tool target to production:

- Directly validate `/v1/models`, text completions, and tool calls against the upstream.
- Validate the same text and tool requests through the router model group.
- Directly validate structured-output requests for each claimed dialect, then repeat through the router group.
- For targets that claim both tools and structured outputs, validate a combined request for the same dialect.
- Run a negative structured-output router smoke against a group with no compatible target and expect `502 no-eligible-target` before any upstream request.
- Keep `tool_only: true` targets for upstreams that are validated only for tool-bearing traffic.
- Keep caller allow lists scoped to the enterprise model groups the caller actually needs.
- Record the upstream model ID, parser flags, chat template, server version, smoke results, and rollback path in deployment notes.
