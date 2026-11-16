---
title: Routing
doc_type: explanation
---

# Routing

Metrum AI Router lets callers request a stable, deployment-defined model group while operators change the provider and model mix behind that group. The caller sends `model: "<group-name>"`; the router checks caller access, filters the group's targets for the request shape, applies the configured routing strategy, and forwards the request to one eligible upstream target.

Group names are deployment-defined. Names such as `fast`, `high`, `big-coder`, or `vision` may appear in examples from a reference or hosted deployment, but they are not product-required names. Callers should discover allowed groups from [`/v1/models`](../getting-started/available-models).

For strategy selection, start with [Routing Strategy Decision Tree](./strategy-decision-tree). For the full policy ownership model, examples, proof workflow, and warnings, see [Customer-Controlled Routing](./customer-controlled-routing).

## Routing Pipeline

Every model request follows the same routing pipeline:

1. Authenticate the caller token and verify that the caller is allowed to request the model group.
2. Load only the targets configured under that requested group.
3. Apply model-group contracts and hard eligibility filters.
4. Filter targets for API skin, tools, structured outputs, input modalities, reasoning controls, max-token cap behavior, and known request-size/context fit.
5. Exclude targets that are temporarily unavailable because of provider/model/target shared traffic shaping or adaptive upstream backoff.
6. Run the group's routing strategy over the remaining eligible targets.
7. Retry through configured fallback targets when the selected upstream fails in a retryable way.
8. Record safe request, usage, cost, latency, routing-decision, attempt, fallback, and upstream-shape telemetry.

The router does not select targets from another group just because they are cheaper, lower latency, or more capable. The model group is the caller-facing contract.

For `dynamic_score`, conversation affinity may reorder the already-eligible
targets toward a caller/prefix pin before the upstream attempt. It never
restores a filtered target or widens model-group access. See [Dynamic Score
Routing](../configuration/dynamic-score-routing).

## Choose A Strategy

| Strategy | Use When | Notes |
| --- | --- | --- |
| Learned policy | The router should pick the cheapest eligible target that clears a measured quality floor, using per-target models calibrated on your own outcome data. | See [Learned Routing Policy](./learned-routing-policy). |
| Dynamic score | The router should adapt within the group using cost, latency, throughput, reliability, request shape, and validation signals. | See [Dynamic Score Routing](../configuration/dynamic-score-routing). |
| External policy | Routing policy should live in a trusted standalone service with its own deployment and observability. | See [External Routing Policy Service](../configuration/external-routing-policy). |
| TypeScript policy | Routing policy should be deployment-local and programmable inside the router process. | See [TypeScript Routing Policy](../configuration/routing-typescript). |
| Model-group contract | The group needs explicit workload requirements, quality floors, and validation gates before selection. | A contract is a pre-filter used with another strategy, not a standalone selector. See [Model Group Contracts](../configuration/model-group-contracts). |
| Failover | Targets should be tried in a deterministic priority order. | Useful when one target is primary and others are fallback only. |
| Weighted | Validated targets should share traffic by configured percentages. | Common choice for conservative production mixes and gradual promotion. |
| Static | One exact upstream target should serve the group. | Best for smoke groups, canaries, and tightly controlled workloads. |

Use an optional model-group `contract` together with one of the strategies above when the group needs explicit workload requirements, quality floors, and validation gates before selection. A contract is a pre-filter, not a seventh selector. See [Model Group Contracts](../configuration/model-group-contracts).

Legacy compatibility selectors are documented in [Deprecated Selectors](../reference/deprecated-selectors). Prefer `dynamic_score` for config-only observed-signal scoring, or `script`/`external` for programmable policy.

## Capability Filtering

Request-shape filtering happens before the routing strategy runs. A weighted or dynamic group can contain targets with different capabilities, but each request only sees targets that satisfy its requirements.

| Request Shape | Target Requirement |
| --- | --- |
| OpenAI Chat tools | `tool_support.openai_chat` includes the required tool mode. |
| OpenAI Responses function tools | `tool_support.openai_responses` includes `function`. |
| Anthropic Messages client tools | `tool_support.anthropic_messages` includes `client_tools`. Empty tool metadata is not treated as tool-capable. |
| OpenAI Chat structured outputs | `tool_support.openai_chat` includes `structured_outputs`. |
| OpenAI Responses structured outputs | `tool_support.openai_responses` includes `structured_outputs`. |
| Image input | `input_modalities` includes `image`. |
| Image-only fallback | `input_modalities` includes `image` and optional `request_shape_support.required_input_modalities: [image]` is satisfied. |
| Explicit reasoning or thinking | Target `reasoning` metadata is compatible with the caller field. |
| Positive max-token cap | Target is not marked as unsafe for caller caps. |
| Large input or output reserve | Estimated input plus requested output cap fits the target context window and configured request-shape limits. |

If no configured target in the requested group satisfies the full Chat, Responses, or Messages request shape, the router returns a 400 compatibility or modality error before sending an upstream request; see [Error Reference](../reference/errors) for codes and exceptions. OpenAI-shaped error details can include the request ID and bounded requirement/reason labels, not prompts, tool schemas, images, tokens, provider keys, or deployment config.

## Context And Large-Agent Payloads

For large coding-agent clients, target eligibility also compares safe router-side estimates with configured target metadata. The router considers request bytes, estimated input tokens, tool-schema size, caller output cap or router default reserve, and `context_tokens`. Known limits are enforced before the routing strategy runs, so weighted routing recalculates over targets that can fit the request.

Unknown limits are allowed by default for compatibility with existing deployments, but decision telemetry records `limit_unknown` so operators can inventory gaps. To keep a target out of large coding-agent traffic until validation passes, configure `request_shape_support.supports_large_coding_agent_payloads: false` or explicit limits such as `max_request_bytes`, `max_estimated_input_tokens`, and `max_tool_schema_bytes`.

## Shared Upstream Capacity

Operators can configure provider, provider-model, and target `traffic_shape` buckets to protect shared upstream account capacity across all caller tokens. This is different from caller RPM/TPM/concurrency policy: a caller can be within its own quota while a provider account or one upstream model is temporarily at capacity.

Shared shaping is enforced at upstream admission time. Cache hits are served before provider capacity is consumed, and a cacheable repeat request does not get rerouted or rejected only because the upstream bucket is currently empty. If the selected target is throttled before an upstream attempt starts, the router skips it and tries the next fallback target. When every otherwise eligible upstream attempt is throttled by provider-side shared capacity, callers receive `503 upstream-capacity-throttled` with a request ID and `Retry-After` when calculable.

Adaptive backoff uses safe upstream classifications. A provider `429` starts rate-limit backoff when configured; provider quota, billing, credit, or balance exhaustion starts a separate quota backoff. The router does not expose raw upstream response bodies in caller errors or telemetry.

## Stable Group, Changing Upstreams

This example keeps the caller-visible group stable while the operator changes active upstreams and weights:

```yaml
models:
  production-general:
    strategy: weighted
    targets:
      - provider: hosted_openai_compatible
        model_ref: balanced-text
        weight: 70
      - provider: private_vllm
        model_ref: internal-coding
        weight: 20
      - provider: hosted_openai_compatible
        model_ref: low-cost-fallback
        weight: 10
```

Callers continue to request `production-general`:

```bash
curl "$ROUTER_BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "production-general",
    "messages": [{"role": "user", "content": "Summarize this incident note."}],
    "max_tokens": 300
  }'
```

The operator can later add, remove, or reweight targets without changing the client request, as long as the group continues to satisfy its workload contract.

## Fallback Behavior

Fallback stays inside the requested group. The router can retry after retryable upstream failures such as transient network errors, selected upstream timeouts, provider overload responses, provider rate limits, provider quota or billing exhaustion, and 5xx responses. Ordinary non-retryable upstream 4xx responses, including malformed-request, policy, and authorization errors, stop fallback so the same caller payload is not replayed to another provider. Non-retryable caller errors, authentication errors, forbidden model groups, caller quota failures, and license failures stop before upstream routing.

For deterministic fallback, configure a failover-style group or a strategy-specific fallback order. For weighted or dynamic groups, keep every fallback target validated for the same API skins and workload requirements that callers depend on.

When a fallback target answers successfully, terminal usage and request-time cost fields record that serving target and its prices. Per-attempt rows still preserve the failed primary. Router response-cache entries for the successful response use the serving target's cache key.

Same-dialect OpenAI Chat and Anthropic Messages streaming requests proxy native upstream SSE incrementally. OpenAI Responses and cross-dialect bridges remain unary upstream calls with synthesized dialect-correct caller SSE. Fallback retries apply only before the first native event is committed; the router never reroutes or replays a request after streaming output begins.

## Validation Checklist

Before exposing a group broadly:

- Call `/v1/models` with the intended caller token and confirm the group is visible only to the right callers.
- Run text requests through each supported API skin: Chat Completions, Responses, or Messages.
- Run tool, forced-tool, structured-output, image, reasoning, streaming, and low max-token cap smokes when those request shapes are in scope.
- Run large-context and explicit-output-cap smokes when coding-agent or retrieval-heavy payloads are in scope.
- Confirm selected targets stay inside the requested group and record usage, cost, latency, attempts, and fallback telemetry.
- Validate quality with a workload-appropriate harness such as unit tests, extraction accuracy checks, OCR targets, browser-control tasks, tool-call correctness checks, golden datasets, product acceptance tests, or an agent benchmark.
- Roll back by removing the target from `models.<group>.targets[]`, removing the capability metadata that made it eligible for the failing request shape, isolating it in a restricted smoke group, or changing the group to a simpler known-good strategy.

For target metadata and onboarding requirements, see [Providers And Models](../providers-models/overview) and [Model Metadata](../reference/model-metadata).
