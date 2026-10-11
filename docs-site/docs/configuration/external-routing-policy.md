---
title: External Routing Policy Service
doc_type: howto
---

# External Routing Policy Service

Use `strategy: external` when a deployment wants routing decisions to come from a standalone policy service instead of from TypeScript running inside the router. Callers still request one deployment-defined model group. The router filters the group to eligible targets, applies any optional model-group contract, sends safe request context to the policy service, validates the returned target, and then calls the selected upstream.

This is useful when the routing policy should be developed, tested, deployed, and observed as its own service. For smaller local rules, see [TypeScript Routing Policy](./routing-typescript).

For the canonical strategy comparison, start with [Routing Strategy Decision Tree](../routing/strategy-decision-tree). For the broader routing-policy ownership model, see [Customer-Controlled Routing](../routing/customer-controlled-routing).

## Admin Setup

Configure a model group with `strategy: external`, a policy URL, an exact host allowlist, and the targets the policy may choose from:

```yaml
models:
  adaptive:
    strategy: external
    external_policy:
      url: https://routing-policy.internal.example/route
      mode: shadow
      allow_hosts: [routing-policy.internal.example]
      timeout_ms: 500
      max_response_bytes: 65536
      headers:
        Authorization: ${ROUTING_POLICY_AUTH_HEADER}
      on_error: fail_closed
      # Off by default. Set true only for a trusted policy service that is allowed
      # to receive raw/redacted request content.
      include_request: false
      # Optional deployment salt for pseudonymous context.conversationKey (#21).
      # conversation_key:
      #   salt: deployment-owned-salt
      # Opt-in post-completion callback with request ID, selected target, status,
      # stored usage/cost/latency only (#22). Default URL replaces /route with /feedback.
      # feedback:
      #   enabled: true
      #   timeout_ms: 500
      #   max_retries: 2
      #   retry_backoff_ms: 100
      #   on_delivery_failure: log
      # Opt-in caller verifier-hint metadata for offline evaluation (#23).
      # Never executable. Allowed kinds: none, exact, regex, json_schema.
      # verifier_hints:
      #   enabled: true
      #   allowed_kinds: [exact, regex, json_schema]
      #   max_spec_bytes: 4096
    targets:
      - { provider: baseten, model_ref: gpt-oss-120b, tier: cheap, weight: 70 }
      - { provider: minimax, model_ref: m3, tier: heavy, weight: 30 }
```

`on_error` defaults to `fail_closed`. Use `fallback` only when the group is allowed to use the normal configured target order if the policy service is unavailable or returns an invalid decision.

`mode` defaults to `enforce` for backward compatibility. New adaptive policies
should begin with `shadow`: the router validates and records the recommendation
but serves normal eligible target order. Promote by changing the reviewed
configuration to `enforce`. Roll back to `baseline` to serve normal eligible
order without calling the policy service. Shadow failures never affect serving.
Every mode still requires a valid `url` and exact matching `allow_hosts` when
configuration is loaded, so switching modes cannot silently introduce an
unreviewed destination.

Policy URLs use HTTPS by default. Plain HTTP is accepted only for trusted loopback hosts such as `localhost`, `127.0.0.1`, and `::1`, or when `external_policy.allow_http: true` is explicitly set for a trusted non-local endpoint. Redirects are revalidated before they are followed; every hop must keep an allowed `http`/`https` scheme and an exact hostname from `allow_hosts`.

The router also rejects non-loopback private/link-local destination IPs and
nondefault ports for nonlocal endpoints, even when a hostname is allowlisted.
Use shared-network-namespace loopback for a private policy sidecar. Deployment
firewall and service-mesh controls supplement those checks.

[Learned Routing Policy](../routing/learned-routing-policy.md) provides an
outcome-trained service and protected offline training CLI using this contract.
For conversation keys, completion feedback, and verifier-hint metadata see
[External Policy Context](./external-policy-context).

Callers continue to use the model group name:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "adaptive",
    "messages": [{"role": "user", "content": "Summarize this note in one sentence."}],
    "max_tokens": 128
  }'
```

## Policy Request

The router sends a JSON `POST` body to the policy service:

```json
{
  "group": "adaptive",
  "context": {
    "model": "adaptive",
    "dialect": "openai-chat",
    "estimatedTokens": 9,
    "textChars": 36,
    "messageCount": 1,
    "messageTextChars": 36,
    "imageCount": 0,
    "toolCount": 0,
    "hasTools": false,
    "hasStructuredOutput": false,
    "maxTokens": 128,
    "maxTokensField": "max_tokens",
    "temperatureSet": false,
    "stream": false,
    "reasoning": {
      "requested": true,
      "kind": "effort",
      "effort": "medium",
      "source": "openai_chat.reasoning_effort"
    }
  },
  "inputModalities": ["text"],
  "requirements": ["text", "max_tokens"],
  "caller": {
    "id": "example-caller",
    "user": "example-user",
    "project": "example-project",
    "environment": "staging",
    "tokenId": "rtr_metrum_example_caller_k1",
    "allow": ["adaptive"]
  },
  "targets": [
    {
      "provider": "baseten",
      "model": "openai/gpt-oss-120b",
      "modelRef": "gpt-oss-120b",
      "dialect": "openai-chat",
      "tier": "cheap",
      "weight": 70,
      "inputPricePerMillionUsd": 0.10,
      "outputPricePerMillionUsd": 0.50,
      "toolSupport": {"openaiChat": ["tools", "tool_choice"]},
      "inputModalities": ["text"],
      "outputModalities": ["text"],
      "keyId": "baseten-primary",
      "apiKeyEnv": "BASETEN_API_KEY",
      "keyConfigured": true
    }
  ],
  "now": "2026-06-21T12:00:00Z"
}
```

By default, the policy request does not include raw prompt text, normalized message bodies, image URLs or base64 data, tool result text, tool schemas, or `request.raw`. Use the derived `context` object for routing signals such as prompt size, estimated token count, message count, image count, tool count, structured-output presence, explicit output cap, streaming flag, safe metadata key names, and normalized reasoning or thinking fields.

The default body still discloses pseudonymous caller and deployment inventory:
caller ID, user, project, environment, public token ID, allowed groups, and
eligible targets' provider/model identifiers, dialect, tier, weight, pricing,
capability/validation metadata, configured key ID, API-key environment-variable
name, and key-configured boolean. Treat the service, transport, logs, and
backups as trusted routing infrastructure. `targets[].region` is not included
and cannot be used by this policy input as a residency gate.

If a deployment needs a trusted policy service to inspect request content, set `external_policy.include_request: true`. That opt-in adds `request` and `text` fields to the policy body. When the model group has `pii_filter` enabled, those fields are built from the redacted request object; placeholder mappings stay in router memory for the current request and are not sent to the policy service. Without `pii_filter`, `include_request: true` can send raw prompts/messages, image references or data, tool schemas, and tool outputs to the external service.

`targets` contains only targets already eligible for the request shape. Filtered
targets are not sent in another policy-request field. For example, image
requests only include image-capable targets, tool requests only include
compatible tool targets, reasoning or thinking requests only include compatible
reasoning targets, and capped requests skip targets marked as not honoring max
tokens. Each target includes safe capability metadata such as modalities, tool
support, structured-output support, reasoning support, validation status,
prices, and configured key identifiers.

When the group has a model-group contract, the policy body includes safe `contract` metadata and target `validation` metadata. `targets[]` is already filtered by the contract, and policy responses are validated against that eligible list. The policy service cannot select a contract-ineligible fallback.

The router does not send raw router tokens, token hashes, provider API key
values, or full deployment config.

## Policy Response

Return one target decision:

```json
{
  "targetIndex": 1,
  "fallbackIndexes": [0],
  "classLabel": "prompt-size:heavy",
  "metadata": {"reason": "large prompt"}
}
```

The router accepts:

- `targetIndex`: zero-based index into the request's `targets` array.
- `target`: selector such as `{ "provider": "baseten", "model": "openai/gpt-oss-120b" }`.
- `fallbackIndexes` or `fallbacks`: optional fallback order.
- `classLabel`: optional telemetry token stored in logs and usage records. Use at most 64 characters from letters, numbers, `_`, `-`, `.`, and `:`. Do not echo prompts, secrets, HTML, or user input; unsafe values are stored as `unsafe_class_label`.

Returned targets are validated against the eligible target list. A policy service cannot select an unconfigured provider/model, a target outside the requested group, or a target filtered out for tools, modalities, or token-cap behavior.

## Tested Demo Service

The deployment examples include a runnable prompt-size policy service:

```bash
python3 examples/external-routing-policy/prompt_size_policy.py
```

It listens on `http://127.0.0.1:18090/route`, sends requests with `context.textChars <= 8000` to a `cheap` target, sends larger requests to a `heavy` target, and returns the same response schema shown above.

The repository also includes an adaptive signal policy under
`examples/external-routing-policy/adaptive_signal_policy.py`. That reference
scores observed latency, throughput, reliability, and optional quality labels,
keeps short-lived conversation pins, ignores response-cache hits, and books
fallback outcomes to the serving target. Run the synthetic harness with:

```bash
python3 scripts/run_adaptive_signal_policy_demo.py
# or: make adaptive-signal-policy-demo
```

That harness is wiring evidence only. It is not provider-backed quality
evidence and is not a built-in online-learning control plane. Built-in
`dynamic_score` remains the in-router observed-performance strategy; external
policy is for deployment-owned logic that needs its own process, pins, or
feedback loop.

The reference config includes an example group named `external-policy-demo`:

```yaml
models:
  external-policy-demo:
    strategy: external
    external_policy:
      url: http://127.0.0.1:18090/route
      mode: shadow
      allow_hosts: [127.0.0.1]
      timeout_ms: 500
      max_response_bytes: 65536
      on_error: fail_closed
      include_request: false
    targets:
      - { provider: baseten, model_ref: gpt-oss-120b, tier: cheap, weight: 70 }
      - { provider: minimax, model_ref: m3, tier: heavy, weight: 30 }
```

Names such as `external-policy-demo`, `cheap`, and `heavy` are examples. Deployments can choose their own model group names and target metadata.

## Decision 3.0 (d3) Policy

`examples/external-routing-policy/d3_policy.py` asks a vLLM Semantic Router
Decision 3.0 (d3) model typed questions about the redacted request text
(difficulty, multi-step reasoning, task kind) and maps the answers to a `cheap`
or `strong` eligible target with declarative rules. It needs
`include_request: true`, so run it and the d3 server inside the router's trust
boundary. The reference config includes this group:

```yaml title="config.example.yaml"
models:
  d3-policy-demo:
    strategy: external
    external_policy:
      url: http://127.0.0.1:18094/route
      mode: shadow
      allow_hosts:
      - 127.0.0.1
      timeout_ms: 300
      max_response_bytes: 65536
      on_error: fail_closed
      include_request: true
    targets:
    - provider: baseten
      model_ref: gpt-oss-120b
      tier: cheap
      weight: 1
    - provider: openai
      model_ref: gpt-5.5
      tier: strong
      weight: 1
```

See [Decision 3.0 (d3) External Policy](../routing/d3-external-policy) for the
adapter config, serving d3, privacy guidance and tests.

## Security And Operations

- Keep policy services inside trusted infrastructure because they receive request-shape context, caller metadata, pricing metadata, and target capability metadata. They receive prompt text, message content, tool schemas, image references or image data, and tool outputs only when `external_policy.include_request: true` is explicitly configured.
- Leave `include_request` off unless the policy service has the same trust boundary as the router and the deployment has approved content sharing. Prefer derived fields such as `context.textChars`, `context.estimatedTokens`, `context.imageCount`, and `context.toolCount`.
- Use exact `allow_hosts`; wildcard host allowlists are not supported.
- Use HTTPS for non-local policy services. Use `external_policy.allow_http: true` only for an approved trusted internal endpoint; loopback HTTP is reserved for local demos and sidecars.
- Treat redirects as policy-service egress: a redirect to a non-allowlisted hostname fails before the redirected service is reached.
- Put policy-service authentication in `external_policy.headers`, not in application requests.
- Keep `timeout_ms` low because routing happens before any upstream model call. The router creates one concurrency-safe policy HTTP client per model group at startup, reuses its connection pool, propagates caller cancellation, and defaults the client timeout to 500 ms when the field is unset.
- Start new adaptive policies in `shadow`, compare recommendation and served-target outcomes, promote with `enforce`, and use `baseline` for immediate policy-call rollback.
- Use `fail_closed` for sensitive routing policy. Use `fallback` only when the configured target order is an acceptable default.
- Treat `routing-policy-error` as a deployment/configuration issue. The response means the policy service failed, timed out, returned non-JSON, returned non-2xx, or selected an invalid target.

When decision telemetry is enabled, external-policy executions write safe scalar policy rows. Successful enforced policy decisions record outcome, selected candidate index, fallback count, and safe class label. Shadow decisions record `shadow_recommended` plus a `shadow_recommended_candidate` routing signal while the routing-decision row continues to identify the baseline target actually served. Baseline mode records its bounded outcome without calling the policy. Fail-closed policy errors before enforced selection record an execution row even when no routing-decision row exists, with policy kind, outcome, duration, eligible/all target counts, safe error class, and terminal error type. `on_error: fallback` records the configured fallback outcome and then normal routing/fallback telemetry explains the selected target. These rows do not store the policy request body, policy response JSON, prompt text, tool schemas, bearer tokens, provider keys, token hashes, raw URLs, policy headers, or full config.

## Outcome-Calibrated Example

The repository includes an outcome-calibrated reference policy under
`examples/external-routing-policy/`. It demonstrates how a team can use a
deployment-owned dataset, human-reviewed outcomes, and an OpenAI-compatible
embeddings service to route simple and difficult workloads to different eligible
targets. The reference first rejects candidates that miss each class's required
pass rate, then weights the remaining candidates toward lower observed cost.

This workflow is operator-controlled: it emits a profile and a YAML patch for
review rather than changing an active group. The synthetic arithmetic,
benchmark-code, and folder-listing cases prove the mechanics only. Teams should
use a larger representative dataset and their own verifier or reviewer process
before promotion.

The reference policy requires `include_request: true` because it classifies the
trusted request content. Enable that only when the policy service is inside the
approved trust boundary. For PII-filtered groups, the router sends redacted
content. The service uses the existing eligible target, price, capability, and
safe caller context already present in the external-policy request; it never
needs router tokens, provider credentials, token hashes, or full configuration.
Keep response-bearing human-review artifacts in that same protected boundary;
the committed examples contain synthetic data only.

The repository's synthetic coding demonstration is a repeatable local wiring
test only; it uses fake embeddings and mock upstreams and is not a
production-quality claim. For a real evaluation, operators run the reference
policy with their secret-held OpenAI-compatible embedding credential (for
example `text-embedding-3-small`), collect real candidate responses through a
dedicated staging group, apply a workload-specific reviewer or verifier, and
calibrate the reviewable profile. A protected live runner then records the
router request ID, selected eligible target, class label, usage, response, and
verifier result for new requests. It adds a unique run marker so a response
cache cannot bypass policy evaluation. Keep this evidence within the trusted
deployment boundary and approve it before changing production weights.
