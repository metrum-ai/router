# Dynamic Score Routing Runbook

`strategy: dynamic_score` is a model-group-local routing strategy. It lets operators combine reusable scalar signals without adding one Go strategy per policy idea.

Public customer-facing strategy ownership guidance lives in `docs-site/docs/routing/customer-controlled-routing.md`. Keep this runbook aligned with that page when changing dynamic-score behavior, proof requirements, or examples.

## Policy Design Checklist

- Define the workload and owner.
- Choose whether this belongs in one model group, multiple groups, or a separate router instance.
- Choose the strategy: `static`, `failover`, `weighted`, `dynamic_score`, `script`, `external`, or a contract-backed combination.
- Define eligible providers and models under `models.<group>.targets[]`; do not treat provider catalog entries as active routes.
- Document required API shapes, tool modes, modalities, reasoning controls, structured-output support, and max-token cap behavior.
- Define quality, cost, latency, throughput, error-rate, timeout, and fallback targets.
- Run direct upstream smokes for every provider/model/dialect/skin being claimed.
- Run router-level smokes through each caller API shape and negative no-eligible-target path.
- Run representative evaluation or proof for the workload.
- Define rollback: remove the target from affected groups, remove or tighten the capability metadata that made it eligible, isolate it behind a restricted smoke group, relax a contract only when the contract is too strict, switch strategy, or restore the previous config.

## Configuration Contract

Callers still request one deployment-defined model group. The router authenticates the caller, checks the caller token allow list, loads only that requested group's `targets[]`, applies request eligibility filters, and then scores only those group-local targets.

Dynamic scoring must not be used to cross from one model group contract into another. If a cheaper or faster target belongs in the policy, add and validate that target in the requested group.

The following is a focused adaptation of the shipped
`config.example.yaml` policy. Provider names are illustrative; policy keys
match the reference group.

```yaml
models:
  adaptive-agent:
    strategy: dynamic_score
    targets:
      - { provider: baseten, model_ref: gpt-oss-120b, weight: 60, tags: [validated, coding, tool_capable] }
      - { provider: minimax, model_ref: m3, weight: 25, tags: [validated, low_cost, tool_capable] }
      - { provider: openai, model_ref: gpt-5.4-nano, weight: 5, tags: [fallback] }
    routing_policy:
      dynamic_score:
        cold_start_policy: configured_weight
        min_observations: 20
        observation_window_seconds: 600
        max_score_adjustment_percent: 70
        affinity:
          # enabled defaults to true; set enabled: false for independent picks.
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
        thresholds:
          max_error_rate: 0.03
          max_timeout_rate: 0.02
          max_p95_latency_ms: 10000
```

## Validation

Before rollout, validate the group targets the same way as weighted routing:

- Direct upstream text smoke for each provider/model/dialect.
- Direct tool smoke before claiming tool support.
- Direct image smoke plus router-level image smoke before marking `input_modalities: [text, image]`.
- Router-level smoke for each API skin that will be used by clients.
- Explicit low-budget cap smoke, including OpenAI Chat `max_completion_tokens: 1`, to confirm unsafe targets are skipped when `honors_max_tokens: false`.

If `hard_filters.require_reasoning_support_when_requested` is enabled, treat it as an extra dynamic-score hard filter layered on top of normal request eligibility. It is not the only reasoning-routing mechanism. Weighted, failover, script, external, and dynamic-score groups all rely on validated target `reasoning` metadata to preserve explicit OpenAI Chat `reasoning_effort`, OpenAI Responses `reasoning`, or Anthropic Messages `thinking` controls.

Then run a fixed request matrix against the dynamic group:

- simple text;
- code/debug prompt;
- tool request;
- forced tool request where supported;
- image request where supported;
- structured-output request where supported;
- low explicit max-token cap.

Drive enough traffic to pass `min_observations` and verify selection changes from deterministic configured-weight cold start to score-based selection.

## Conversation Affinity

Dynamic-score groups pin a conversation prefix to the first selected target by
default. The key is a SHA-256 digest of the caller ID, model group, normalized
inbound dialect, system context, and complete first normalized message,
including its role and normalized content parts. Requests without messages use
their normalized input text/parts. Raw prompt content is not retained. Clients
must resend the same initial normalized prefix on later turns; clients that
send only a new incremental turn produce a different key.

Pins are process-local, expire 600 seconds after creation/replacement by
default, and are bounded to 10,000 entries by default. Hits do not refresh the
expiry or make an entry more recent for eviction. At capacity, the oldest
created/replaced entry is removed; this is bounded creation-time eviction, not
an LRU cache.

Affinity runs after request eligibility, spend ceilings, hard filters, and
health thresholds. A pin only reorders that already-eligible list. It is
created for the initially selected primary before the upstream result is known,
so a successful fallback does not repin the conversation to the fallback.
A pin is ignored and replaced if its provider/model/dialect/index no longer
matches an eligible target for the current request shape. Different callers
never share pins. Disable the behavior for a group with
`affinity.enabled: false`.

Chat-to-Responses is stateless: callers replay full history. Bridged requests always set `store:false` and never send `previous_response_id`, including when legacy session configuration is enabled. Responses requests with `previous_response_id` exclude `force_store_false` targets; zero eligible targets returns `400 continuation_unsupported`.

## Streaming Behavior

Streaming OpenAI Chat requests routed to OpenAI Chat targets and streaming Anthropic Messages requests routed to Anthropic targets use native upstream SSE. The router flushes complete events incrementally and preserves native text, tool-call/tool-use, and usage events. Client cancellation closes the upstream request. Once any event is sent downstream, the router never starts a fallback or replays the stream.

Cross-dialect bridges remain unary upstream calls with translated downstream responses. OpenAI Responses streaming also retains its existing unary/synthesized behavior.

## Diagnostics

Use request logs, usage DB, and trace events. The `routing_decision` trace event contains safe scalar metadata only:

- strategy and mode;
- enabled signal names;
- request-shape buckets;
- candidate count;
- selected provider/model;
- score bucket and observation count;
- cold-start flag.

Diagnostics must not include raw prompts, images, tool outputs, router tokens, token hashes, provider keys, full upstream headers, or full config contents.

When a selected target fails before a stream is committed and the router tries a fallback, failed attempts update the in-memory observation store used by later dynamic-score requests. Router response-cache hits are excluded from that store. After a successful fallback, terminal usage and cost fields attribute the serving target while attempt detail preserves the failed primary. With decision telemetry enabled, `request_fallback_transitions` also records the failed candidate, fallback candidate, safe error class, retryable flag, and whether the fallback attempt succeeded. Affinity emits safe scalar `affinity_hit`, `affinity_miss`, `affinity_expired`, `affinity_ineligible`, or `affinity_disabled` routing signals.

Dynamic score does not read the usage database or decision-telemetry reports while selecting a target. Those surfaces explain past decisions; the hot path uses process-local observation windows only.

## Rollout And Rollback

Roll out first on a dedicated test group with interchangeable validated targets and a non-sensitive caller token allowed only to that group. Compare p95 latency, error rate, fallbacks, cost, and selected target mix against the weighted baseline.

Rollback is config-only: set `affinity.enabled: false`, switch the model group `strategy` to `weighted`, lower strict thresholds, or remove score terms. Restarting also clears process-local pins. After production config changes, follow the normal timestamped-backup, compose validation, restart, health check, authenticated smoke, and stale-doc search process.
