---
title: Model Groups
doc_type: reference
---

# Model Groups

Model groups are caller-facing routing contracts. A caller sends one deployment-defined model value, and the router chooses from that group's eligible targets according to the configured strategy and request shape.

This is the `default` group from `config.example.yaml`; the shipped sample config is the source of truth.

```yaml title="config.example.yaml"
models:
  default:
    strategy: weighted
    targets:
      - provider: fireworks
        model_ref: deepseek-v4p1-flash-1017
        weight: 70
      - provider: openai
        model_ref: gpt-6-luna
        weight: 30
        bridges:
          chat_to_responses:
            enabled: true
            text: true
            tools: true
            tool_choice: true
```

This 70/30 mix applies when both targets are eligible for the request. The
Fireworks target serves OpenAI Chat, including tools. Responses requests use
Luna through its native Responses API. Both targets are configured for text
input. Anthropic Messages text can use Luna; Anthropic tool calls need a
separately validated Anthropic-capable route (see
[issue #295](https://github.com/metrum-ai/router/issues/295)).

## Schema

Weights are local to each model group. A target with weight `60` in one group has no relationship to a target with weight `60` in another group. Targets reference `providers.<name>.models.<ref>` and can override safe metadata such as `dialect`, `tool_only`, `input_modalities`, per-attempt timeout, traffic shaping, and validation fields.

`tool_only: true` keeps a target eligible only for requests that include tools. Broad developer-facing groups that accept Anthropic Messages should also keep at least one validated native Anthropic target without `tool_only`, so a plain-text Messages turn is not rejected with a 400 compatibility error; see [Error Reference](../reference/errors).

Groups can use `static`, `failover`, `weighted`, `dynamic_score`, `script`, or
`external` strategies. Contracts, request-shape filters, modalities, tools,
structured outputs, reasoning metadata, and max-token safety all filter the
target list before strategy selection. The licensed `intelligent` configuration
contract is accepted as a baseline-only foundation in the current release: it
does not call its decision model or change the selected serving target. Use
external-policy `shadow` and `enforce` modes for a shipped adaptive promotion
path.

## Stable And Staging Groups

Use stable groups for day-to-day application and agent traffic. Use staging or smoke groups for new provider/model/API-skin combinations, bridge experiments, large-payload validation, and opt-in trials. Grant staging access deliberately through caller tokens so teams can test without changing the default group that production users depend on.

Promote a target from staging to a stable group only after the exact caller request shapes pass. For coding-agent and IDE traffic, that means more than a simple text response: validate the API surface, tool dialect, tool-choice mode, streaming behavior, output-cap field, request byte scale, serialized tool-schema size, reasoning or thinking controls, bridge direction, and modalities that callers will send.

If a target supports ordinary text or small tool requests but not large repository or agent payloads, keep that distinction in metadata. Request-shape gates such as `request_shape_support.max_request_bytes`, tool-schema byte limits, supported inbound dialects, and explicit bridge flags let the router skip the target for unsupported shapes while still using it where it is proven.

Avoid treating a provider model as universally compatible across API skins. OpenAI Chat, OpenAI Responses, Anthropic Messages, Chat-to-Responses bridges, and Responses-to-Chat bridges are separate compatibility surfaces. A target should enter a stable group for a surface only after direct upstream and router-level smokes pass for that surface.

## Capacity Pooling

A model group can pool usable capacity across multiple upstream providers or private endpoints. Each provider account or model may have a different RPM, TPM, concurrency, quota, or billing envelope. When several targets are validated for the same request shape, the router can distribute traffic across those separately limited upstreams instead of forcing all callers through one provider bottleneck.

Capacity pooling is bounded by the group contract:

- caller-side RPM, TPM, concurrency, daily/monthly quota, and lifetime budgets still run before upstream selection;
- only eligible targets count for a given request shape, including API dialect, tools, images, reasoning, context window, structured output, and output-cap behavior;
- provider, provider-model, or target traffic shaping can intentionally slow or skip an upstream to protect shared capacity;
- failover applies only to retryable upstream failures such as timeout, network error, rate limit, provider quota/billing exhaustion, or 5xx;
- non-retryable malformed-request or policy 4xx responses stop fallback so the same bad payload is not replayed to another provider.

Use usage and admin reports to verify the actual provider/model mix, upstream 429s, fallback success, latency, and throughput before increasing weights or promoting a larger traffic window.

## Rollback

Roll back by restoring the prior target list, reducing or removing a bad target's weight, removing a failed capability override, or switching the caller back to a previous model group allow-list. Verify with `/v1/models`, a positive smoke for expected traffic, and a negative capability-error smoke when capability filtering changed.

## Related

See [Routing Overview](../routing/overview), [Customer-Controlled Routing](../routing/customer-controlled-routing), [Model Group Contracts](./model-group-contracts), [Dynamic Score Routing](./dynamic-score-routing), [TypeScript Routing](./routing-typescript), [External Routing Policy](./external-routing-policy), and [Router Configuration](./router-config).
