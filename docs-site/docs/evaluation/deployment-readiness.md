---
title: Deployment Readiness
doc_type: explanation
---

# Deployment Readiness

Plan and validate Metrum AI Router as a deployment-owned GenAI control plane. A production-ready deployment proves more than API reachability: it defines the [deployment pattern](../operations/deployment-patterns), model-group quality targets, security controls, client compatibility, cost policy, operational readiness, and rollout criteria for every model group exposed to callers.

Model groups are deployment contracts. Each group should state who it is for, which API shapes it supports, which modalities and tools it can serve, what task outcomes it is expected to complete, and what cost/latency envelope it should stay inside. The goal is not to route every request to the most expensive model. The goal is to maintain the required outcome for each workload while using the lowest-cost reliable provider/model mix that passes the group criteria.

For the strategy ownership model behind those deployment contracts, see [Customer-Controlled Routing](../routing/customer-controlled-routing).

## Rollout Validation Flow

Use this sequence for a new deployment or major routing update:

1. Define the deployment shape: on-prem, customer cloud, or Metrum-managed.
2. Define model groups and their success criteria.
3. Validate provider and private-upstream metadata.
4. Run security assessment checks.
5. Run client acceptance checks.
6. Run model-group quality validation with Harbor or a similar workload harness.
7. Confirm usage, cost, quota, telemetry, and diagnostics.
8. Record rollout, rollback, and promotion criteria.

## Platform Readiness

Confirm:

- TLS and ingress ownership;
- deployment hostname and version visibility;
- provider key storage and rotation process;
- private upstream access model for vLLM, SGLang, Baseten-style, or other OpenAI-compatible services;
- model groups visible to each caller class;
- caller token policy, quotas, budgets, and rate limits;
- clients requiring OpenAI Chat, OpenAI Responses, Anthropic Messages, tool calls, or image inputs;
- usage database, request logs, metrics-admin access, and backup policy.

Acceptance checks:

- `/readyz` and `/version` return build metadata.
- Browser docs show the running binary version and build timestamp.
- `/v1/models` is filtered by the presented caller token.
- Disallowed model groups return `403 model-not-allowed` before any provider call.
- `/metrics` is available only to caller subjects authorized for `metrics` `read`.
- `/v1/usage` or generated reports provide caller-specific usage visibility.
- If browser admin reports are enabled, `/admin/reports/api/summary?since=24h` succeeds only for a Basic Auth or OIDC session subject allowed by authorization policy, and an ordinary router caller token receives `403 reports-forbidden`.
- If `server.admin_auth.authorization.source: db` is used, a single active validated policy set exists in the usage DB before rollout, malformed policy activation preserves the last known valid set, and rollback to the previous retired set is tested.

## Model-Group Quality Contracts

Every model group should have a quality contract before broad access:

| Contract Field | Example |
|---|---|
| Intended users | product app, coding agent, evaluation key, restricted project |
| Intended tasks | short chat, extraction, summarization, agentic coding, OCR, browser-control context |
| API shapes | OpenAI Chat, OpenAI Responses, Anthropic Messages |
| Required tools | OpenAI Chat tools, Responses function tools, Anthropic client tools |
| Required modalities | text, image/VLM, audio/video when configured |
| Success criteria | Harbor reward score, unit-test pass, extraction accuracy, OCR target, tool-call correctness |
| Cost target | maximum average cost/request or target savings against a baseline |
| Latency target | p50/p95 latency or throughput floor |
| Reliability target | fallback rate, timeout rate, provider error ceiling |
| Promotion rule | when weight or caller access can increase |
| Rollback rule | when to reduce weight, disable a target, or isolate a group |

Use [Model Group Quality Criteria](./model-group-quality) for the full template.

## Security Assessment

Security validation should cover secrets, telemetry, diagnostics, network exposure, and dependency/container scans. Use [Deployment Security Assessment](./deployment-security-assessment) for the checklist.

Minimum acceptance:

- provider keys stay server-side;
- caller tokens are stored only as hashes;
- raw prompts, raw images, provider keys, router tokens, and token hashes are excluded from diagnostics as described in the [Diagnostics Schema](../reference/diagnostics-schema#fields-intentionally-not-persisted);
- `/metrics` requires a caller subject authorized for `metrics` `read`;
- release deployments have `server.license.enabled: true`, a mounted current license file, `/readyz` success, and a documented renewal/rollback procedure;
- release handoff includes the deployment's release-validation matrix and artifact inspection evidence;
- browser admin reports, when enabled, require browser-admin identity plus authorization policy `admin:reports` policy and remain separate from public `/docs/`;
- governed content capture is disabled unless required by policy, and any enabled deployment has `content:capture` delete/purge authorization, redaction rules, retention, purge, and backup handling reviewed;
- private upstreams are reachable only through approved network paths;
- image-fetching VLM services have media-domain restrictions where applicable;
- TypeScript and external policy egress use exact hostname allowlists, HTTPS by default, approved `allow_http` exceptions only for trusted infrastructure, and redirect revalidation;
- container/package and dependency scan results are reviewed under the deployment's security process.

## Client Acceptance

Validate real clients, not only curl:

- OpenAI Chat through `/v1/chat/completions`;
- Codex CLI through `/v1/responses`;
- Claude Code CLI through `/v1/messages`;
- OpenAI Chat tool clients such as Warp-style agents;
- image-bearing requests through the same developer-accessible model groups used for text and tools.

For CLI tool tests, require the agent to create or edit a file and assert the file contents. Text-only responses are useful smoke checks, but they are not sufficient evidence for agent tool compatibility.

## Outcome And Cost Control

Validation should measure outcome first, then optimize the provider/model mix. A lower-cost target is valuable only when the model group still completes the objective it is responsible for.

For agentic workloads, use Harbor or a similar harness to compare:

- task success or reward score;
- generated artifact correctness;
- tool-call behavior;
- input/output/image token volume;
- latency and throughput;
- fallback count and provider error rate;
- selected provider/model mix;
- actual router-calculated and upstream-reported cost.

This enables substantial cost benefits without degrading the result the group is meant to deliver. Simple tasks can use lower-cost routes; complex coding, long-context, tool-heavy, or VLM tasks can route to stronger targets only when the request actually needs them.

## Operational Readiness

Before production rollout, validate:

- timestamped deployment/config backups;
- rollback command path;
- request ID based troubleshooting;
- diagnostic tables and sanitized error rows;
- quota, budget, RPM, TPM, concurrency, and traffic-shaping behavior;
- usage reports grouped by caller, project, environment, model group, provider, model, status, cache, latency, tokens, and cost;
- optional browser admin reports checked for summary, request evidence/drilldown, Markdown export, no-store headers, local static assets, diagnostic completeness states, and `403 reports-forbidden` for ordinary caller tokens;
- cleanup of uploaded packages, replaced deployment trees, stale `/tmp` files, and accumulated Docker artifacts.

Use [Operational Readiness](./operational-acceptance) for a release checklist.
