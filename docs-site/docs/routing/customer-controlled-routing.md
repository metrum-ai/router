---
title: Customer-Controlled Routing
doc_type: explanation
---

# Customer-Controlled Routing

Metrum AI Router is not a black-box global model chooser. A caller requests one allowed, deployment-defined model group, and the deployment owner controls which providers, private upstreams, capabilities, policies, validation gates, and fallback paths exist inside that group.

Use this page as the routing-policy ownership map. Start with the [Routing Strategy Decision Tree](./strategy-decision-tree) when choosing between static, failover, weighted, dynamic score, TypeScript, external, or contract-backed routing. The detailed reference pages remain the source of truth for [router config](../configuration/router-config), [model metadata](../reference/model-metadata), [dynamic score routing](../configuration/dynamic-score-routing), [TypeScript routing](../configuration/routing-typescript), [external policy services](../configuration/external-routing-policy), [model-group contracts](../configuration/model-group-contracts), [PII filtering](../configuration/pii-filtering), [usage reporting](../operations/usage-reporting), and [deployment readiness](../evaluation/deployment-readiness).

Model group names are deployment-defined. Examples on this page use names such as `regulated-review`, `agent-coding`, `adaptive-support`, and `team-local-coding` only as examples. Callers should discover their allowed groups from [`/v1/models`](../getting-started/available-models).

## Routing Contract

Every request follows the same ownership boundary:

1. The caller authenticates with a router token.
2. Token, user, project, and environment policy determine which model groups the caller may request.
3. The caller requests one model group.
4. The router loads only that group's configured targets.
5. Request-shape filters remove ineligible targets for API shape, tools, modalities, structured outputs, reasoning or thinking controls, explicit output-cap safety, and any model-group contract.
6. The group's configured strategy chooses from the remaining targets.
7. Usage and reporting record request-time provider, model, cost, latency, throughput, attempt, fallback, and policy evidence without exposing raw provider keys or router tokens.

The router does not route across unauthorized groups and does not activate provider catalog entries unless they appear under `models.<group>.targets[]`.

## Strategy Selection

| Need | Strategy / mechanism | Good fit | Caution |
| --- | --- | --- | --- |
| Always use one target | `static` | Regulated workload, fixed-model evaluation, smoke group | No provider diversity |
| Ordered fallback | `failover` | Reliability with a preferred provider first | Watch total timeout and retry budget |
| Weighted mix | `weighted` | Gradual rollout, cost mix, provider diversity | Needs monitoring and rollback criteria |
| Fast configurable scoring | `dynamic_score` | Cost, latency, reliability, request-shape, and evaluation scoring with scalar terms and observed signals | Keep terms lightweight and group-local; this is the mature built-in adaptive scorer |
| Legacy stubs only | `latency`, `cost`, `semantic` | Older configs that still parse these names | Stub maturity only: configured RPM/cost ranks or keyword classification, not live observations or embeddings; prefer `dynamic_score` |
| Custom logic in config | TypeScript `script` | Deployment-specific routing using safe context | Scripts are trusted deployment code |
| Separate policy service | `external` | Enterprise policy engine, sidecar, or ML scorer | Secure network, auth, timeout, and fail-closed behavior are required |
| Reserved decision-model contract | `intelligent` | Validate licensed bounded configuration while serving the deterministic baseline | Baseline-only in the current release; it does not call the decision model or alter selection |
| Hard capability promise | Model-group `contract` | Quality and capability floor per group | Stale validation or strict floors can remove all targets |

These mechanisms can be combined carefully. For example, a group can use `dynamic_score` after a model-group contract filters stale validation, or an external policy service can receive only contract-filtered eligible targets.

## Customer-Owned Controls

| Control | Where it lives | Ownership decision |
| --- | --- | --- |
| Group membership and weights | `models.<group>.targets[]` | Which validated provider/model targets can serve the group and at what relative weight |
| Provider keys and private upstreams | `providers`, deployment secrets, private base URLs | Which external or enterprise-hosted services are reachable |
| Caller/user/project access | `callers[].allow`, users, projects, memberships, admin policy | Which teams can see and request each group through `/v1/models` |
| Quotas and rate limits | Caller and token budget config | How much traffic a key, owner, project, or environment may send |
| Capabilities and validation metadata | Provider catalog, target overrides, `validation`, `contract` | Which request shapes and quality gates a target may satisfy |
| Processing-location label | `models.<group>.targets[].region` | Optional deployment-declared metadata for selected-target diagnostics; enforcement requires reviewed eligibility or policy controls |
| PII filtering | `models.<group>.pii_filter` | Whether text is redacted, restored for buffered responses, or blocked before routing policy and upstream calls; native streams preserve placeholders |
| Cache behavior | Server cache config and request cache controls | Which responses may be reused and which requests bypass cache |
| Retention policy | Usage DB, logs, rollups, retention settings | How long safe scalar telemetry and reports are kept |
| Reports and evaluation labels | Usage reporting, decision telemetry, validation harness labels | Which evidence supports promotion, rollback, and cost allocation |
| License and feature boundaries | Signed license plus runtime config | Which product capabilities the deployment may enable |

## Examples

The snippets below use real config field names but are intentionally abbreviated. Add the corresponding `providers`, caller access, pricing, metadata, validation details, and secrets in the full deployment config.

### Fixed-Control Group

Use `static` when a team wants one upstream target for evaluation, regulated review, or a known-good rollback path.

```yaml
models:
  regulated-review:
    strategy: static
    targets:
      - provider: enterprise_openai_compatible
        model_ref: reviewed-model
```

### Failover Group

Use `failover` when target order is the policy. The first eligible target is tried first; later targets are retry fallbacks for retryable upstream failures.

```yaml
models:
  support-primary:
    strategy: failover
    targets:
      - provider: private_vllm
        model_ref: support-model
      - provider: hosted_openai_compatible
        model_ref: support-fallback
      - provider: backup_openai_compatible
        model_ref: support-backup
```

### Weighted Coding-Agent Group

Use `weighted` when every active target has passed the workload gate and the deployment wants a controlled provider mix. Tool-only target overrides let ordinary text and agent tool traffic use different eligible targets inside the same group.

```yaml
models:
  agent-coding:
    strategy: weighted
    targets:
      - provider: hosted_openai_compatible
        model_ref: coding-balanced
        weight: 65
      - provider: private_gpu
        model_ref: coding-private
        weight: 25
      - provider: hosted_responses_skin
        model_ref: tool-capable-model
        weight: 10
        dialect: openai-responses
        tool_only: true
      - provider: hosted_anthropic_skin
        model_ref: tool-capable-model
        weight: 10
        tool_only: true
```

### Dynamic-Score Group

Use `dynamic_score` when a group should adapt within its own eligible target list using configured scalar signals. Keep scoring terms understandable and tied to the group contract.

```yaml
models:
  adaptive-support:
    strategy: dynamic_score
    targets:
      - provider: hosted_openai_compatible
        model_ref: support-balanced
        weight: 60
        tags: [validated, support]
        validation:
          status: passed
          workload: support_chat
          validated_at: "2026-06-25"
          quality_score: 0.94
          pass_rate: 0.98
          harness: golden-support-set
      - provider: private_vllm
        model_ref: support-low-latency
        weight: 40
        tags: [validated, low_latency]
        validation:
          status: passed
          workload: support_chat
          validated_at: "2026-06-25"
          quality_score: 0.92
          pass_rate: 0.97
          harness: golden-support-set
    routing_policy:
      dynamic_score:
        cold_start_policy: configured_weight
        min_observations: 20
        observation_window_seconds: 600
        max_score_adjustment_percent: 50
        signals:
          request_shape: { enabled: true }
          observed_performance: { enabled: true }
          cost: { enabled: true }
          evaluation_metadata: { enabled: true }
        score_terms:
          - name: cheapest_fast_enough
            expression: "0.40 * cost_score + 0.30 * latency_score + 0.20 * reliability_score + 0.10 * eval_quality_score"
```

### TypeScript Script Policy

Use `strategy: script` when policy should run inside the router process and can be packaged with deployment config. The checked-in request-shape example under `examples/typescript-request-shape/router.ts` routes by prompt size, tools, images, structured outputs, and explicit reasoning signals:

```yaml
models:
  script-request-shape:
    strategy: script
    script: examples/typescript-request-shape/router.ts
    targets:
      - { provider: hosted_openai_compatible, model_ref: compact, tier: cheap, weight: 70 }
      - { provider: hosted_openai_compatible, model_ref: long-context, tier: heavy, weight: 20 }
      - { provider: hosted_openai_compatible, model_ref: tool-model, tier: tool, weight: 10 }
```

```typescript
export function route(ctx) {
  const preferredTiers = ctx.text.length > 8000 ? ["heavy"] : ["cheap"];
  // The checked-in example also inspects ctx.context.toolCount, imageCount,
  // hasStructuredOutput, and reasoning.requested before picking a tier.
  const entries = ctx.targets.map((target, index) => ({ target, index }));
  const preferred =
    entries.find((entry) => preferredTiers.includes(entry.target.tier)) || entries[0];
  return {
    targetIndex: preferred.index,
    fallbackIndexes: entries.filter((entry) => entry.index !== preferred.index).map((entry) => entry.index),
    classLabel: `request-shape:${preferred.target.tier}`,
  };
}
```

Scripts receive safe caller and target metadata, not raw provider keys, raw router tokens, or token hashes. If a group enables `pii_filter`, the script context is built from the redacted request. The packaged `scripts/router.ts` sample is a caller-regex weighted picker; use `examples/typescript-request-shape/` for request-shape routing.

### External Policy Service

[Learned Routing Policy](learned-routing-policy.md) provides an outcome-trained
external service and offline training CLI, with per-group quality floors,
cost-aware selection and explicit evaluation before promotion.

Use `strategy: external` when policy belongs in a trusted standalone service. The service receives already eligible targets and returns a target decision; the router validates that decision before any upstream call.

```yaml
models:
  policy-service-group:
    strategy: external
    external_policy:
      url: https://routing-policy.internal.example/route
      allow_hosts: [routing-policy.internal.example]
      timeout_ms: 500
      max_response_bytes: 65536
      headers:
        Authorization: ${ROUTING_POLICY_AUTH_HEADER}
      on_error: fail_closed
      include_request: false
    targets:
      - { provider: hosted_openai_compatible, model_ref: compact, tier: compact, weight: 70 }
      - { provider: private_vllm, model_ref: long-context, tier: long_context, weight: 30 }
```

Abbreviated policy request shape:

```json
{
  "group": "policy-service-group",
  "context": {
    "estimatedTokens": 2200,
    "textChars": 9200,
    "imageCount": 0,
    "toolCount": 1,
    "maxTokens": 512,
    "stream": false
  },
  "caller": {
    "id": "team-prod",
    "project": "product",
    "environment": "prod",
    "allow": ["policy-service-group"]
  },
  "targets": [
    {"provider": "hosted_openai_compatible", "modelRef": "compact", "tier": "compact", "weight": 70},
    {"provider": "private_vllm", "modelRef": "long-context", "tier": "long_context", "weight": 30}
  ]
}
```

Response shape:

```json
{
  "targetIndex": 1,
  "fallbackIndexes": [0],
  "classLabel": "prompt-size:long-context"
}
```

By default, the policy request does not include prompt text, messages, image URLs/data, tool schemas, tool outputs, or `request.raw`. Enable `external_policy.include_request: true` only for a trusted service that is allowed to receive request content.

### Hierarchical Topology

Some enterprises separate central governance from team-local strategy by running more than one router instance. This is topology guidance, not a one-command feature.

```text
application or agent
  -> team router group: team-local-coding
      - local private GPU target
      - central enterprise router as an OpenAI-compatible upstream
  -> central router group: enterprise-approved-coding
      - centrally approved hosted and private targets
```

The team router still exposes deployment-defined local groups to its callers. The central router still enforces its own caller tokens, allowed groups, provider keys, routing strategy, telemetry, and model-group contracts.

## Proof Workflow

Policy changes should be evidence-driven:

| Step | Evidence |
| --- | --- |
| Pre-change baseline | Current selected providers/models, cost/request, p95 latency, fallback rate, error rate, and outcome score |
| Safe config summary | Group-local diff showing changed targets, weights, strategies, contracts, policy URL, or script path without secrets |
| Representative evaluation | Workload-specific tests such as unit tests, extraction accuracy, OCR targets, tool-call correctness, browser-control tasks, golden datasets, product acceptance tests, or agent benchmarks |
| Direct upstream smokes | Text, tool, image, reasoning, streaming, and output-cap checks for the exact provider/model/dialect/skin being claimed |
| Router smokes | Authenticated requests through each caller API shape plus negative compatibility-error and forbidden-group checks |
| Report window | Usage report or admin report showing selected target mix, latency, throughput, cost, attempts, fallbacks, and safe policy labels |
| Rollback trigger | Concrete threshold for disabling a target, reducing weight, relaxing a contract, or reverting to a simpler strategy |

See [Model Group Quality Criteria](../evaluation/model-group-quality), [Evaluate Metrum AI Router](../evaluation/evaluate-smart-router), [Deployment Readiness](../evaluation/deployment-readiness), [Operational Acceptance](../evaluation/operational-acceptance), and [Cost Governance](../evaluation/cost-governance) for rollout and acceptance planning.

## What Not To Do

- Do not treat provider catalog entries as active routing weights. Catalog metadata becomes traffic only when a target is listed under `models.<group>.targets[]`.
- Do not route across unauthorized groups. Caller allow lists and `/v1/models` are the caller-facing boundary.
- Do not claim tool, image, reasoning, structured-output, or max-token behavior from provider marketing copy alone. Validate the exact upstream and router skin before adding active traffic.
- Do not hardcode reference names such as `default`, `fast`, `small`, `medium`, `high`, `big-coder`, or `vision` as product-required group names in public docs or client code.
- Do not store raw prompts, responses, tool outputs, images, bearer tokens, provider keys, token hashes, full policy payloads, or full deployment config as diagnostics.
- Do not optimize only for cost. Promote cheaper routes only when the group still meets the workload outcome, latency, reliability, and security criteria.
