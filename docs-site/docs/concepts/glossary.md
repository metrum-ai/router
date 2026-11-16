---
title: Glossary
doc_type: reference
---

# Glossary

Use this glossary for short canonical definitions. For the request flow and model-group ownership model, see [Concepts](../concepts).

| Term | Definition | Canonical page |
|---|---|---|
| allow list | The model groups a caller token may request. Requests outside the list fail before upstream credentials are used. | [Available Models And Access](../getting-started/available-models) |
| API dialect | The caller-facing API shape, such as OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages. | [API Compatibility](../reference/api-compatibility) |
| attempt | One upstream provider/model try for a request. A request can have multiple attempts when fallback is configured. | [Usage Reporting](../operations/usage-reporting) |
| attempt timeout | The time budget for one upstream attempt before the router can fail or move to fallback. | [Router Configuration](../configuration/router-config) |
| cache eligibility | The request and target conditions that decide whether a response may be served from or written to cache. | [Router Configuration](../configuration/router-config) |
| cache key | A non-secret fingerprint used to identify reusable cached responses after configured redaction and eligibility checks. | [Router Configuration](../configuration/router-config) |
| caller | The authenticated application, service, or user represented by a router token and metadata. | [Available Models And Access](../getting-started/available-models) |
| caller token | A router-issued bearer token with allow lists, caller metadata, limits, a public token ID for reporting, and optional admin privileges. | [Available Models And Access](../getting-started/available-models) |
| content-admin | An operator subject authorized for governed content-capture maintenance actions such as delete or purge. | [Admin Authorization](../configuration/admin-authorization) |
| contract | A model-group capability and quality promise enforced before target selection. | [Model Group Contracts](../configuration/model-group-contracts) |
| conversation affinity | Optional `dynamic_score` behavior that hashes a caller and normalized conversation prefix to reorder the currently eligible targets toward the original primary for a fixed process-local TTL. | [Dynamic Score Routing](../configuration/dynamic-score-routing) |
| decision telemetry | Safe scalar routing evidence recorded for policy, eligibility, fallback, score, and filter decisions. Reports explain decisions; they do not drive the hot-path selector. | [Usage Reporting](../operations/usage-reporting) |
| dialect | A provider or caller API shape used to translate requests and responses without changing the caller contract. | [API Compatibility](../reference/api-compatibility) |
| dynamic_score | Built-in strategy that blends configured weights with in-process rolling observations for latency, throughput, reliability, catalog cost, and evaluation metadata. | [Dynamic Score Routing](../configuration/dynamic-score-routing) |
| effort enum | A reasoning-control value such as low, medium, or high when a provider supports effort-style reasoning selection. | [Reasoning Routing](../configuration/reasoning-routing) |
| eligibility | The request-shape and policy checks a target must pass before it can be selected. | [Routing Strategy Decision Tree](../routing/strategy-decision-tree) |
| fallback | A retry path to another eligible target after a retryable upstream failure or configured failover order. | [Customer-Controlled Routing](../routing/customer-controlled-routing) |
| fingerprint | A safe identifier or hash used for diagnostics, cache decisions, or routing evidence without exposing raw content or tokens. | [Usage Reporting](../operations/usage-reporting) |
| image input | Image-bearing request content sent through a supported VLM-capable API shape. | [Image Analysis And VLM Routing](../configuration/image-analysis-vlm) |
| input modality | A target-supported input type such as text or image. | [Model Metadata](../reference/model-metadata) |
| metrics-admin | An operator subject authorized to read global `/metrics`; ordinary caller tokens receive `403 metrics-forbidden`. | [Observability](../operations/observability) |
| model group | A caller-facing, deployment-defined policy name that owns a target list and routing strategy. | [Concepts](../concepts) |
| native streaming | Incremental proxying of same-dialect OpenAI Chat, OpenAI Responses, or Anthropic Messages upstream SSE; after the first event the response is committed and cannot fall back. | [API Compatibility](../reference/api-compatibility) |
| output modality | A target-supported output type, normally text unless validated otherwise. | [Model Metadata](../reference/model-metadata) |
| owner user | The configured owner identity for caller tokens, reporting, access, and usage grouping. | [Router Configuration](../configuration/router-config) |
| policies | Authentication, authorization, routing, limits, contracts, traffic shaping, retention, and deployment rules that govern requests. | [Customer-Controlled Routing](../routing/customer-controlled-routing) |
| policy service | A trusted deployment service called by `strategy: external` to choose from eligible targets. | [External Routing Policy Service](../configuration/external-routing-policy) |
| external policy mode | Promotion state for `strategy: external`: `baseline` skips the call, `shadow` records a recommendation without changing service, and `enforce` applies a valid eligible-target decision. | [External Routing Policy Service](../configuration/external-routing-policy) |
| project | A deployment-owned grouping for caller access, usage reports, and operational ownership. | [Router Configuration](../configuration/router-config) |
| project_membership | The configured relationship that authorizes a user or service identity inside a project/environment domain. | [Router Configuration](../configuration/router-config) |
| provider catalog | Metadata about upstream providers and models, including model IDs, pricing, modalities, tool support, and validation notes. | [Model Metadata](../reference/model-metadata) |
| provider-route | A configured provider/model route exposed only through model-group targets, not directly to callers. | [Add A Provider Or Model](../reference/add-provider-model) |
| public token ID | A non-secret token identifier stored in config as `token_id` and shown in reports so operators can correlate usage without exposing raw router tokens or token hashes. | [Caller Tokens](../configuration/caller-tokens) |
| reasoning control | Request fields that ask for provider reasoning or thinking behavior and become target eligibility requirements. | [Reasoning Routing](../configuration/reasoning-routing) |
| reasoning mode | A target's validated reasoning behavior, such as supported effort levels or thinking controls. | [Reasoning Routing](../configuration/reasoning-routing) |
| request shape feature | A safe request property such as API skin, tools, image input, reasoning, output cap, prompt size, or structured-output request. | [Dynamic Score Routing](../configuration/dynamic-score-routing) |
| reservation | A quota or traffic-shaping hold based on estimated input and requested output budget before upstream completion. | [Error Reference](../reference/errors) |
| route strategy | The configured mechanism that selects a target: static, failover, weighted, dynamic_score, script, or external. An optional contract is a pre-filter, not a selector. | [Routing Strategy Decision Tree](../routing/strategy-decision-tree) |
| router endpoint | The deployment URL callers use instead of direct provider endpoints. | [API Quickstart](../getting-started/hosted-quickstart) |
| routing policy | The deployment-owned rules and strategy that choose among eligible targets inside one requested model group. | [Customer-Controlled Routing](../routing/customer-controlled-routing) |
| savings | Hypothetical spend difference versus an explicit baseline price; calculated from stored request-time costs, not from re-pricing history with current catalog prices. | [Cost Governance](../evaluation/cost-governance) |
| skin | A client compatibility surface, such as OpenAI Chat, OpenAI Responses, or Anthropic Messages. | [API Compatibility](../reference/api-compatibility) |
| stateful session | Optional Chat-to-Responses bridge continuation that stores upstream `previous_response_id` under a caller session header. This is separate from `dynamic_score` conversation affinity. | [API Compatibility](../reference/api-compatibility) |
| target | One configured upstream/provider model entry inside a model group. | [Concepts](../concepts) |
| target region | Optional operator-declared selected-target diagnostic label; it does not enforce processing location and is not exposed to callers or routing policies. | [Model Metadata](../reference/model-metadata) |
| target selection | The process of filtering eligible targets and choosing one according to the model group's strategy. | [Routing Strategy Decision Tree](../routing/strategy-decision-tree) |
| target-level context eligibility | A target-specific check that skips targets unable to satisfy the request context, such as max-token or modality requirements. | [Router Configuration](../configuration/router-config) |
| tier | Deployment-defined target metadata used by policy, reports, or scripts to group targets by role or cost class. | [TypeScript Routing Policy](../configuration/routing-typescript) |
| token budget | The configured or requested token allowance used for quota admission, traffic shaping, and upstream output caps. | [Error Reference](../reference/errors) |
| tool dialect | The tool-call protocol a target has been validated to support, such as OpenAI Chat tools or Anthropic client tools. | [Agents, Tools, And Vision](../agents-tools-vision/overview) |
| usage rows | Relational usage database records for request, attempt, cost, latency, routing, quota, and diagnostic facts. | [Usage Reporting](../operations/usage-reporting) |
| usage_db | The configured relational database used for usage reports, cost accounting, and operational triage. | [Usage Reporting](../operations/usage-reporting) |
| VLM | Vision-language model behavior for image-bearing prompts, OCR, screenshots, diagrams, or browser-control context. | [Image Analysis And VLM Routing](../configuration/image-analysis-vlm) |
