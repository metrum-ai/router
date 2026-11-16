---
title: Solution Brief
doc_type: explanation
---

# Metrum AI Router Solution Brief

Metrum AI Router is a self-managed, provider-neutral LLM smart router for
teams that need stable client APIs with deployment-owned routing, access
control, budgets, and operational evidence. Gateway functions underneath
routing keep provider keys, quotas, and usage evidence enforceable.

Clients use OpenAI-compatible or Anthropic-compatible APIs and request a model
group rather than a raw upstream model. The router authenticates the caller,
checks group access and request shape, selects an eligible configured target,
injects the upstream credential, translates supported dialects, and records
sanitized usage and performance fields.

## Operational Value

- provider optionality behind stable application configuration;
- server-side key custody and scoped caller access;
- weighted, failover, dynamic-score, TypeScript, and external-service policy;
- caller-isolated dynamic-score affinity and reversible external-policy
  baseline/shadow/enforce promotion;
- native incremental OpenAI Chat and Anthropic Messages streaming, with
  translated caller streaming for Responses and cross-dialect bridges;
- text, tool, image, and agent request-shape eligibility;
- caller budgets and upstream traffic shaping;
- request-time cost, latency, throughput, attempt, fallback, and cache reports;
- private vLLM/SGLang-style OpenAI-compatible upstream support;
- objective workload validation before lower-cost targets are promoted.

## Learned Routing Policy

Learned Routing Policy (LRP) recommends the cheapest eligible target predicted
to meet an operator quality floor. It trains offline (LightGBM, isotonic
calibration) and serves through `strategy: external` with `baseline`,
`shadow`, and `enforce` modes. Feedback carries status, usage, cost, and
latency. Quality labels arrive offline.

See [Learned routing policy](/docs/routing/learned-routing-policy),
[Train and evaluate](/docs/routing/lrp-train-and-evaluate), and
[Serve and promote](/docs/routing/lrp-serve-and-promote).
Synthetic results do not authorize live promotion.

Different models are good at different jobs and to different degrees. The goal
is the least expensive validated model or mix that still completes each
workload. Teams can prove that with unit tests, extraction accuracy, OCR
targets, tool-call assertions, browser tasks, golden datasets, product
acceptance tests, or agent harnesses such as Harbor.

## Deployment Boundary

Operators run the router as a Linux binary, Docker Compose service, or
Kubernetes workload. They own TLS, network controls, secrets, provider
accounts, state storage, backups, upgrades, incident response, and the exact
model-group quality contracts. See [Installation](/docs/installation/),
[Architecture And Limitations](/docs/reference/architecture-limitations), and
[Deployment Readiness](/docs/evaluation/deployment-readiness).

