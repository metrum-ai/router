# Tool-aware routing runbook

`strategy: tool_aware` is an opt-in model-group strategy for tool-heavy agent groups. It validates the
tool calls an upstream returns, records their reliability per target, orders targets by that evidence,
and can retry or escalate a bad call once, within hard bounds. Every other strategy is unchanged and
never runs this code: a `tool_routing` block on any other strategy is a configuration error.

Issue: [#436](https://github.com/metrum-ai/router/issues/436). Measured result:
[tool-aware routing case study](../docs-site/docs/evaluation/tool-routing-case-study.md).

## Configuration contract

```yaml
models:
  agent-tools:
    strategy: tool_aware
    tool_routing:
      mode: observe              # observe | enforce
      cold_start: configured_weight
      min_samples: 20            # outcomes before a target's score may reorder it
      window_seconds: 900        # reliability window
      validate:
        arguments_json: true     # arguments must parse as a JSON object
        schema: basic            # off | basic (documented subset, see below)
        name_must_be_offered: true
      recovery:                  # enforce mode only
        max_retries_same_target: 1   # hard cap 2
        max_escalations: 1           # hard cap: eligible targets - 1
        on_exhausted: deliver_last   # the only implemented value
      streaming:
        hold_until_validated: false  # must stay false: the hold buffer is not implemented
    targets:
      - {provider: openai, model_ref: gpt-5.4-nano, weight: 100}
      - {provider: openai, model_ref: gpt-5.4-mini, weight: 1}
```

Rules the router enforces at startup:

- `tool_aware` requires the block; the block is rejected on every other strategy.
- `mode: enforce` requires a non-zero recovery budget; recovery fields in `observe` mode are rejected.
- `max_retries_same_target` is 0-2 and `max_escalations` is at most the eligible target count minus one.
- `on_exhausted` accepts `deliver_last` only. The router never fabricates a corrected tool call.
- `streaming.hold_until_validated: true` is refused while streaming recovery is unimplemented.

## Rollout order

1. **Observe.** Add the group with `mode: observe`. Requests are answered exactly as the group would
   answer without the block, and each attempt records `tool_validation` (calls, valid, invalid, first
   failure class and tool, class counts, `action: observed`). The reliability counters fill in.
2. **Read the evidence.** `request_usage` rows are unchanged in this release, but the request log
   carries the per-attempt annotation, and the summary metrics
   `metrum_ai_router_tool_call_valid_total` / `_invalid_total` expose the same counts. If the group
   never records an invalid call, enforcement will never act: fix the group's prompt or target choice
   instead of switching to enforce and hoping.
3. **Enforce.** Set `mode: enforce` with a recovery budget. Watch `_retries_total`,
   `_escalations_total` and the `tool_recovery_*` trace events. Recovery stops at the request
   deadline, at the effective spend ceiling, when the budget is spent, or when a retry hits an
   upstream error; the last received response is delivered in every one of those cases.
4. **Re-measure.** Compare against the same group with `strategy: static` on the validation set you
   care about. Report the numbers you got, including the ones that did not move.

## What is validated

Failure classes, first one wins per call:

| Class | Meaning |
| --- | --- |
| `invalid_json` | the argument string is not a JSON object (empty, partial, wrong type) |
| `unknown_tool` | the function name is not one the caller offered |
| `missing_required` | a property in the tool's `required` list is absent |
| `schema_violation` | type, `enum`, `const`, `anyOf`/`oneOf`/`allOf`, `items`, `minItems`/`maxItems` or `additionalProperties: false` mismatch |
| `forced_tool_mismatch` | a forced/required `tool_choice` produced no call, or a different tool |
| `truncated_arguments` | the response hit its output cap with incomplete arguments |

The `basic` schema subset is `type`, `properties`, `required`, `items`, `enum`, `const`,
`additionalProperties` (boolean or schema), `minItems`, `maxItems`, `anyOf`, `oneOf`, `allOf`; a few
documentation-only keywords (`description`, `title`, `$schema`, `default`) are ignored. Any other
keyword (`$ref`, `patternProperties`, numeric ranges, `format`, ...) makes that sub-schema
**unknown**: the call passes and the attempt records `unsupported_schema_keyword`. The validator never
invents a failure from a schema it cannot read, because a false escalation is worse than no
escalation.

Validation is bounded: at most 64 tool calls per response, 256 KiB of argument text per call, 2048
schema nodes and depth 16.

## Recovery semantics and cost

- Retry the target that actually served the response, then escalate through the remaining eligible
  targets in delivery order. Streamed responses are never retried (the bytes are already on the
  wire); they are recorded as `observed_streaming`.
- Discarded attempts are billed: their token usage is added to the request's usage before quota
  settlement, cost calculation and usage-row write. A retry cannot hide its spend or escape a token
  quota, and the attempt log shows the discarded tokens.
- The delivered attempt is the only one marked `selected`. When the budget is spent, the last
  response is delivered unchanged.

## Current limits

- **Streaming recovery is not implemented.** Streamed responses cannot be retried; the flag that
  would buffer tool-call deltas is refused by config validation.
- **No seed ingestion yet.** Reliability comes only from live observations in this process; the
  compatibility-matrix seed block from #572's results is not wired.
- **No usage-row columns yet.** Per-request tool counts live in the request log and metrics; the
  `request_usage` columns (with its schema migration) are the next slice.
- **Ordering needs two sampled targets.** With one target accumulating outcomes and none other ready,
  the configured order stands; only recovery can change an outcome in that state.
- **Observed-only for streamed PII paths and committed streams**, as above.
- Reliability counters are process-local and rebuilt from the window after a restart, like
  dynamic-score observations.

## Rollback

Set `mode: observe` (keeps the telemetry, stops the retries), or remove the `tool_routing` block and
switch the group back to `static`/`weighted` in one configuration change. The block is rejected on
every other strategy, so a partial rollback cannot leave a stale policy behind.

Copyright 2026 Metrum AI. SPDX-License-Identifier: Apache-2.0
