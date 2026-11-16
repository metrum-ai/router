---
title: Product Capabilities
doc_type: explanation
---

# Product Capabilities

This page summarizes Metrum AI Router capabilities for enterprise deployments.

## Capabilities

| Area | Capability |
|---|---|
| API compatibility | OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, filtered `/v1/models`, caller `/v1/usage`, health/version endpoints |
| Routing | Static, weighted, failover, dynamic-score, TypeScript-scripted, and external-policy model group routing |
| Capacity pooling | Compatible requests can use several separately rate-limited upstream providers, accounts, models, or private endpoints behind one model group |
| Caller governance | Router-issued caller tokens, per-key allow lists, rate limits, traffic shaping, token budgets, concurrency limits |
| Provider control | Server-side provider keys, deployment-defined provider catalogs, active targets separate from catalog metadata |
| Tools | Dialect-specific tool metadata and request filtering for OpenAI Chat, OpenAI Responses, and Anthropic Messages |
| Images/VLM | Image input detection across supported request shapes and modality-aware target filtering |
| Cost accounting | Request-time input/output/image prices, calculated cost fields, upstream-reported billed cost fields |
| Usage reporting | CLI Markdown reports and optional authenticated browser reports by caller, project, environment, token ID, provider, model, model group, client, status, cache, latency, and token counts |
| Observability | JSONL logs, relational usage DB, diagnostics child tables, optional governed content-capture tables, metrics-admin Prometheus telemetry |
| Caching | In-process LRU/TTL cache for eligible non-tool responses with cache snapshots in usage rows |
| Safe troubleshooting | Request IDs, sanitized upstream error details, request-shape buckets, attempts, fallbacks, and evidence bundles without raw prompts, tool outputs, router tokens, token hashes, or provider keys by default |
| Noisy-neighbor control | Caller limits, traffic shaping, provider shaping, adaptive backoff, and report views for user/client/provider impact |
| Provider outage handling | Retryable upstream timeouts, 429s, quota/billing failures, network errors, and 5xx responses can fall back to other eligible targets |
| Deployment | Linux binary and Docker Compose packages with embedded product documentation and optional embedded admin report assets |
| Agent clients | Codex CLI and Claude Code CLI workflows validated through router-compatible API shapes |
| Private upstreams | OpenAI-compatible vLLM, SGLang, Baseten-style, and other internal services can be configured as providers |

## Best Fit

Metrum AI Router is designed as the governed smart-routing layer for enterprise GenAI traffic. Gateway functions underneath routing keep credentials, quotas, and evidence enforceable. It works well when a platform team wants to:

- expose stable model-group names to developers and agents;
- keep provider keys and private inference endpoints server-side;
- route by API dialect, tool support, image modality, latency, cost, quota, and custom policy;
- combine usable upstream capacity without giving callers direct provider credentials;
- reduce provider-specific outage and rate-limit blast radius;
- protect shared provider accounts from noisy-neighbor workloads;
- explain cost spikes and fallback behavior with request-level evidence;
- record request-time token, image, cost, cache, and provider selection details;
- validate and roll out new upstream models without rewriting every client.

Broader enterprise controls such as identity integration, procurement process, managed hosting terms, and compliance workflows are handled as part of the selected deployment and operating model.

## Activation Standard

Provider catalogs are metadata, not proof. A model should only become active after the exact deployment validates:

- provider key entitlement;
- direct upstream text behavior;
- realistic token budget behavior;
- small max-token cap behavior when caps matter;
- tool behavior for the intended API shape;
- image behavior when image modality is advertised;
- router-level behavior through the exposed model group.

Models can remain catalog-only until validation passes.

## Deployment Options

Metrum AI Router can be deployed:

- inside an enterprise network;
- in a customer cloud account;
- as a Metrum-managed evaluation or private-managed deployment (one customer per instance).

Deployment-specific hostnames, model group names, caller policies, provider keys, and upstream mixes are configuration choices, not product constants.
