---
title: Dynamic Score Routing
doc_type: howto
---

# Dynamic Score Routing

`strategy: dynamic_score` is the built-in configurable routing strategy for model groups that should adapt to request shape, cost, observed performance, reliability, workload complexity, and evaluation metadata without adding custom router code.

The caller-visible model group does not change. A client requests one deployment-defined model group from `/v1/models`, and the caller token must be allowed to use that group. The router then scores only that requested group's eligible `targets[]`. A cheaper or faster target in another group is never considered. If the group has an optional [model-group contract](./model-group-contracts), its hard requirements and quality floors run before dynamic scoring.

For the canonical strategy comparison, start with [Routing Strategy Decision Tree](../routing/strategy-decision-tree). For the broader routing-policy ownership model, see [Customer-Controlled Routing](../routing/customer-controlled-routing).

## Configuration

This is a focused adaptation of the `dynamic_score` group in
`config.example.yaml`; provider names remain illustrative, while policy keys
match the shipped sample.

```yaml
models:
  adaptive-agent:
    strategy: dynamic_score
    targets:
      - provider: baseten
        model_ref: gpt-oss-120b
        weight: 60
        tags: [validated, coding, tool_capable]
      - provider: minimax
        model_ref: m3
        weight: 25
        tags: [validated, low_cost, tool_capable]
      - provider: openai
        model_ref: gpt-5.4-nano
        weight: 5
        tags: [fallback]
    routing_policy:
      dynamic_score:
        cold_start_policy: configured_weight
        min_observations: 20
        observation_window_seconds: 600
        max_score_adjustment_percent: 70
        affinity:
          # enabled defaults to true
          ttl_seconds: 600
          max_entries: 10000
        hard_filters:
          require_requested_api_skin: true
          require_input_modalities: true
          require_tool_support_when_tools_present: true
          require_honors_max_tokens_when_caller_capped: true
        signals:
          request_shape: { enabled: true }
          prompt_features:
            enabled: true
            max_scan_bytes: 16384
            features: [code, diff, stack_trace, summarize, extract, security_review, tool_agent]
          complexity: { enabled: true }
          observed_performance: { enabled: true }
          cost: { enabled: true }
          evaluation_metadata: { enabled: true }
        score_terms:
          - name: cheapest_fast_enough
            when: { complexity_lte: standard }
            expression: "0.45 * cost_score + 0.25 * latency_score + 0.20 * throughput_score + 0.10 * reliability_score"
          - name: complex_quality_floor
            when: { complexity_gte: complex }
            require_tags: [validated]
            expression: "0.45 * eval_quality_score + 0.25 * reliability_score + 0.20 * latency_score + 0.10 * cost_score"
        thresholds:
          max_error_rate: 0.03
          max_timeout_rate: 0.02
          max_p95_latency_ms: 10000
```

Group names and targets are examples. Use names, upstreams, and quality contracts that match the deployment.

## How Selection Works

The router first applies the same request eligibility rules used by other strategies:

- API skin and upstream dialect must preserve the caller request.
- Tool-bearing requests require compatible tool passthrough.
- Image-bearing requests require matching input modality metadata.
- Reasoning or thinking requests require compatible `reasoning` target metadata.
- Explicit positive caller token caps, including OpenAI Chat `max_completion_tokens: 1`, skip targets marked `honors_max_tokens: false`.

`hard_filters.require_reasoning_support_when_requested` is an additional dynamic-score hard filter for deployments that want that rule declared in policy. It is not the only reasoning-routing mechanism: compatible target `reasoning` metadata is still the baseline eligibility source for ordinary weighted, failover, script, external, and dynamic-score groups. See [Reasoning Routing](./reasoning-routing) for the weighted-group pattern.

After eligibility, `dynamic_score` applies configured thresholds and score terms. Supported score names include:

| Score | Meaning |
| --- | --- |
| `cost_score` | Lower configured input/output price ranks higher. |
| `latency_score` | Lower observed p95 latency ranks higher. |
| `throughput_score` | Higher observed output tokens per second ranks higher. |
| `reliability_score` | Lower error, timeout, and fallback rates rank higher. |
| `complexity_score` | Bounded request-complexity bucket from prompt size, tools, images, output budget, and enabled prompt features. |
| `eval_quality_score` | Optional configured evaluation quality or pass-rate metadata for the target, including target `validation` metadata when present. |

Cold start is deterministic. Until `min_observations` is reached, targets are ordered by configured group-local weight. After that, score terms are blended with configured weights according to `max_score_adjustment_percent`, so operators can cap how far live signals move traffic away from the declared mix.

## Conversation Affinity

Dynamic-score routing pins the first selected target for requests that share a conversation prefix. Affinity is enabled by default, isolated by caller, expires after 600 seconds by default, and is bounded to 10,000 process-local entries by default. Set `affinity.enabled: false` to disable it, or configure `ttl_seconds` and `max_entries`.

The router retains only a SHA-256 key derived from caller identity, model
group, normalized inbound API dialect, system context, and the complete first
normalized message, including its role and normalized content parts. Requests
without messages use normalized input text/parts. It does not retain the raw
prefix. A client must resend the same initial prefix on later turns; sending
only each new incremental turn produces a different key.

TTL starts when the pin is created or replaced and is not extended by hits.
Capacity eviction removes the oldest created/replaced entry rather than
maintaining LRU recency. The pin records the initially selected primary before
the upstream result is known, so a successful fallback does not automatically
repin the conversation to the fallback. Current eligibility always wins: if
the pinned target no longer supports the request's tools, modality, API shape,
spend ceiling, or health threshold, the router reselects and replaces the pin.

Chat-to-Responses stateful sessions are separate and unchanged. Their explicit session header and `previous_response_id` behavior do not share dynamic-score affinity state.

## Streaming

Same-dialect OpenAI Chat and Anthropic Messages requests proxy native upstream SSE and flush complete events incrementally, including tool-call/tool-use and usage events. Canceling the client request cancels the upstream request. After the first event is written, the router does not fallback or replay output from another target.

Cross-dialect bridges and OpenAI Responses retain unary upstream behavior and translated/synthesized downstream responses.

## Operations

The strategy uses in-memory rolling observations for latency, upstream duration, TTFB, output throughput, status, timeout class, error class, and fallback use. It does not read the usage database while routing. Historical usage tables and decision-telemetry reports remain useful for offline validation; they do not select the next target on the hot path.

Router response-cache hits are excluded from adaptive observations so local cache latency cannot make a target look artificially fast. After a successful fallback, terminal usage/cost fields and later observations attribute the serving target; failed primary attempts still update reliability signals through attempt detail.

Decision traces and telemetry rows are safe scalar diagnostics. They include fields such as strategy, cold-start mode, enabled signal names, request-shape buckets, selected provider/model, score bucket, observation count, candidate count, normalized reasoning fields when a caller explicitly requested reasoning or thinking, affinity outcome, and fallback-transition rows after pre-commit upstream failures. Failed attempts update the same in-memory observation store used by later dynamic-score decisions, so provider 429s, 5xxs, timeouts, decode errors, and client cancellations affect future reliability/timeout/fallback signals according to the configured scoring policy. These rows do not include raw prompts, raw images, raw tool outputs, router tokens, token hashes, provider keys, full upstream headers, or full config contents.

When decision telemetry is enabled, usage and admin reports expose safe dynamic-score buckets for operations: enabled signal names, score/value/final-score buckets, threshold/filter buckets, max-token cap filtering, max-token buckets, large input-token buckets, and quota/admission reason buckets. Daily rollups preserve those buckets in normalized rows so operators can keep commercial reporting after raw request-level detail expires.

Roll out on a deployment-defined test group with interchangeable validated targets before enabling broad production traffic. Test simple text, code/debug prompts, multi-turn affinity, TTL expiry, caller isolation, tool requests, forced tool requests, image requests where supported, structured-output requests where supported, reasoning or thinking requests where supported, streaming cancellation, and low explicit max-token caps. Roll back affinity with `affinity.enabled: false`; restarting clears process-local pins. The full strategy rollback is to change the group to `weighted`, remove unsafe reasoning metadata, or disable strict thresholds and score terms.
