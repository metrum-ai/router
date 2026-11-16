---
title: Concepts And Glossary
doc_type: explanation
---

# Concepts And Glossary

Metrum AI Router keeps client integrations stable by separating caller-facing names from upstream provider/model details.

## Request Flow

1. A caller sends an OpenAI-compatible or Anthropic-compatible request to the router.
2. The caller token, also called a router token in setup guides, authenticates the user, service, project, environment, and allowed model groups.
3. The `model` value is interpreted as a deployment-defined model group.
4. The router filters that group's targets by API dialect, tool support, modality, max-token cap behavior, cache eligibility, and target state.
5. The configured policy selects one eligible target.
6. The router injects the upstream provider credential server-side and forwards the request.
7. The response is returned in the caller's API shape, and usage, cost, latency, fallback, and diagnostic metadata are recorded.

## Core Terms

| Term | Meaning |
|---|---|
| Router endpoint | The deployment URL callers use instead of direct provider endpoints. |
| Caller token | A router-issued bearer token with allow lists, caller metadata, limits, and a public token ID for reporting. |
| Model group | A caller-facing policy name such as an organization-defined general, coding, low-cost, VLM, or private-upstream group. |
| Target | One configured upstream/provider model entry inside a model group. |
| Provider catalog | Metadata about upstream providers and models, including model IDs, pricing, modalities, tool support, and validation notes. |
| Routing policy | The strategy that selects an eligible target: weighted, failover, dynamic score, TypeScript script, or external policy service. |
| API dialect | The request/response surface: OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages. |
| Tool dialect | The tool-call protocol a target has been validated to support, such as OpenAI Chat tools, Responses function tools, or Anthropic client tools. |
| VLM | Vision-language model behavior for image-bearing prompts, OCR, screenshots, diagrams, or browser-control context. |
| Metrics-admin token | A separate operator router token allowed to read global Prometheus telemetry from `/metrics`. Ordinary caller tokens receive `403 metrics-forbidden`. |

## Model Groups As Contracts

A model group should define what work it is intended to handle, who may call it, which API shapes it supports, what modalities and tools are allowed, how success is measured, and which cost/latency/reliability targets matter.

This lets a platform team change upstream providers, weights, fallback order, or policy logic without asking every client to change raw provider model IDs. It also lets the team prove that a cheaper or faster mix still completes the job before promotion.

See [Model Group Quality Criteria](./evaluation/model-group-quality) for a complete contract template.

## Public Versus Upstream Model Names

The model IDs returned by `/v1/models` are router model groups filtered by the caller token's allow list. They are not a complete inventory of upstream models or provider models.

Names used in examples are illustrative. Your deployment may expose different group names and different upstream providers.
