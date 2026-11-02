---
title: Model Group Contracts
doc_type: explanation
---

# Model Group Contracts

Model group contracts let an operator publish stable, deployment-defined model groups while changing the provider/model mix behind them. A contract describes the workloads, API shapes, minimum capabilities, validation quality floor, and operational targets that a group is intended to satisfy.

Callers still request a model group they are allowed to use. `/v1/models` remains the caller-facing source of truth for allowed groups. Contract metadata does not let a caller route into another group.

## Example

```yaml
models:
  support-chat:
    strategy: weighted
    contract:
      display_name: Support chat
      caller_visible_notes: Low-latency support-answering group.
      intended_workloads: [support_chat]
      supported_api_shapes: [openai_chat]
      required_capabilities:
        input_modalities: [text]
        output_modalities: [text]
        honors_max_tokens_when_caller_capped: true
      quality_floor:
        require_tags: [validated]
        min_eval_quality_score: 0.90
        min_eval_pass_rate: 0.95
        max_eval_age_days: 30
        allowed_validation_status: [passed]
      operational_targets:
        max_p95_latency_ms: 10000
        max_error_rate: 0.03
        max_timeout_rate: 0.02
      reporting:
        expose_workload_labels: true
        expose_quality_floor_bucket: true
    targets:
      - provider: private-gpu
        model_ref: support-balanced
        weight: 70
        tags: [validated, low_latency]
        validation:
          status: passed
          workload: support_chat
          validated_at: "2026-06-25"
          quality_score: 0.94
          pass_rate: 0.98
          harness: golden-support-set
      - provider: hosted
        model_ref: support-fallback
        weight: 30
        tags: [validated, fallback]
        validation:
          status: passed
          workload: support_chat
          validated_at: "2026-06-25"
          quality_score: 0.92
          pass_rate: 0.96
          harness: golden-support-set
```

Names such as `support-chat`, `private-gpu`, and `hosted` are examples. Use names that match your deployment policy.

## Enforcement Order

The router enforces contracts after caller authorization and ordinary request eligibility:

1. Authenticate the caller.
2. Confirm the requested model group is allowed for that caller.
3. Filter only that group’s targets for API dialect, tools, structured outputs, input modalities, and explicit output-cap safety.
4. Apply the optional group contract and target validation quality floor.
5. Run `static`, `weighted`, `failover`, `dynamic_score`, `script`, or `external` on the remaining targets.

Contract enforcement is group-local. A cheaper, faster, or more capable target in another group is never selected unless the caller requested that other allowed group.

## Contract Fields

`supported_api_shapes` accepts `openai_chat`, `openai_responses`, and `anthropic_messages`.

`required_capabilities` can require tools, forced tool choice, structured outputs, input/output modalities, minimum context tokens, and safe handling of explicit caller output caps.

`quality_floor` can require target tags, target validation status, minimum quality score, minimum pass rate, and a maximum validation age.

`operational_targets` can enforce observed error rate, timeout rate, p95 latency, and output-token throughput when the router has observations for a target.

Target `validation` metadata is safe scalar metadata about how a target was tested. Keep validation notes free of prompts, images, tool outputs, bearer tokens, provider keys, token hashes, private headers, and full config snippets.

## Promotion Checklist

Use a staging group before changing a stable group that callers depend on. A provider/model/API-skin target is ready for stable promotion when:

- direct upstream smokes passed for the exact provider account, model ID, endpoint, and API skin;
- router-level smokes passed through the staging group for each caller surface that the stable group exposes;
- Chat, Responses, Anthropic Messages, and bridge routes were validated separately;
- tool-bearing requests covered the expected tool dialect, tool count, tool-choice mode, output-cap field, streaming mode, and structured-output shape;
- coding-agent groups covered representative large request bytes, serialized tool-schema bytes, message count, prompt-token scale, and any image or reasoning controls the client can send;
- usage or report evidence shows selected provider/model/dialect, attempts, fallback state, request-shape buckets, latency, token usage, and sanitized error classes;
- a workload verifier such as unit tests, extraction accuracy, OCR goldens, browser-control tasks, Harbor, or product acceptance tests passed at the group quality target;
- rollback can lower weight, remove the target, tighten request-shape metadata, move the target back to staging, or restore the previous config.

If only some request shapes pass, keep the target eligible only for those shapes. For example, a target that works for small OpenAI Chat tool requests but fails large IDE payloads should have request-shape limits instead of being treated as compatible with every coding-agent request.

## Strategy Interaction

Contracts are not a separate strategy. They filter the target list before the configured strategy runs.

`dynamic_score` can use target tags and target validation quality/pass-rate metadata as scoring hints after hard contract floors have passed.

TypeScript and external policy strategies receive safe contract and validation metadata plus the contract-filtered eligible target list. Returned decisions are validated against that list and fail closed if they choose an ineligible target.

## Errors And Reports

If ordinary request eligibility leaves at least one target but none satisfies the contract, callers receive `502 no-eligible-target`. If ordinary eligibility rejects all configured targets first, Chat, Responses, and Messages instead return a 400 compatibility error. See [Error Reference](../reference/errors). Contract error details include safe requirement buckets such as:

- `contract-required-api-shape`
- `contract-required-modality`
- `contract-required-tools`
- `contract-quality-floor`
- `contract-validation-expired`
- `contract-no-validated-target`

Usage and admin reports expose safe scalar buckets: contract present, pass/fail bucket, failure reason, optional workload label, validation status, validation workload, and validation age bucket. They do not include raw prompts, raw images, tool outputs, bearer tokens, provider keys, token hashes, private upstream headers, or full config values.

## Rollout

Start with a deployment-defined test group. Add validation metadata to each intended target, run representative text, tool-agent, VLM/OCR, structured-output, and low-token-cap requests that match the declared contract, then confirm selected targets stay inside the requested group.

Rollback is a config change: remove or relax the `contract` block, loosen a specific quality floor or operational threshold, refresh stale validation metadata after retesting, or restore the previous strategy and weights.
