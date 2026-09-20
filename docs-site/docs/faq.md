---
title: FAQ
doc_type: reference
---

# FAQ

These short answers route integrators to the canonical documentation page.

## First Setup

### How do I run the router locally from source?

Use [Local Quickstart](./getting-started/local-quickstart) and
`python3 scripts/local_dev_bootstrap.py --out-dir tmp/local-dev`.

### Which base URL should my client use?

Use the router deployment URL, not a direct provider URL. Start with [API Quickstart](./getting-started/hosted-quickstart) and [API Compatibility](./reference/api-compatibility).

### Which `model` value should I send?

Call `/v1/models` with your router token and use one returned model-group ID. See [Available Models And Access](./getting-started/available-models).

### Why does `/v1/models` not show every upstream provider model?

It returns only model groups allowed for the caller token. See [Available Models And Access](./getting-started/available-models) and [Concepts](./concepts).

### How do I choose Docker Compose, binary, or Kubernetes?

Use the matrix in [Installation](./installation/) and the topology guidance in [Enterprise Deployment Patterns](./operations/deployment-patterns).

### Where do I validate release packages before handoff?

Use [Package Validation And Security Checks](./installation/package-validation).

### How do I evaluate a deployment?

Follow [Evaluate Metrum AI Router](./evaluation/evaluate-smart-router) with
your own provider accounts and representative workloads, then use
[Installation](./installation/) for self-managed installation choices.

## Authentication And Access

### What does `403 model-not-allowed` mean?

The token is valid but cannot use the requested model group. See [Error Reference](./reference/errors) and [Available Models And Access](./getting-started/available-models).

### What does `403 metrics-forbidden` mean?

The caller token is not authorized for global Prometheus metrics. See [Error Reference](./reference/errors), [Admin Authorization](./configuration/admin-authorization), and [Observability](./operations/observability).

### What should I do when a key is disabled, suspended, expired, or rotated?

Switch to an active key or ask the operator to rotate access. See [Error Reference](./reference/errors) and [User Key Generation](./operations/key-generation).

### How are admin reports protected?

Admin report pages require browser-admin authentication and authorization. See [Admin Authentication](./configuration/admin-authentication), [Admin Authorization](./configuration/admin-authorization), and [Admin Browser Reports](./operations/admin-browser-reports).

## Routing And Capabilities

### What does `502 no-eligible-target` mean?

The allowed group exists, but no target in that group supports the request shape. See [Error Reference](./reference/errors), [Available Models And Access](./getting-started/available-models), and [Routing Strategy Decision Tree](./routing/strategy-decision-tree).

### Which routing strategy should I use?

Start with [Routing Strategy Decision Tree](./routing/strategy-decision-tree). It links to the detailed pages for static, failover, weighted, dynamic score, TypeScript, external policy, and contracts.

### How do I route by cost, latency, quality, or request shape?

Use [Dynamic Score Routing](./configuration/dynamic-score-routing) unless the policy needs local code or an external service; compare the choices in [Routing Strategy Decision Tree](./routing/strategy-decision-tree).

### When should I use TypeScript routing?

Use it for trusted deployment-local policy code. See [TypeScript Routing Policy](./configuration/routing-typescript).

### When should I use an external policy service?

Use it when routing policy should run as its own trusted service. See [External Routing Policy Service](./configuration/external-routing-policy).

### How do model-group contracts relate to routing?

Contracts enforce capability and quality floors before selection. See [Model Group Contracts](./configuration/model-group-contracts) and [Model Group Quality Criteria](./evaluation/model-group-quality).

### How do I add a provider or model?

Follow [Add A Provider Or Model](./reference/add-provider-model) and keep [Model Metadata](./reference/model-metadata) current.

## Client Setup

### How do I configure Codex CLI?

Use the OpenAI Responses-compatible setup in [Codex CLI](./getting-started/codex-cli).

### How do I configure Claude Code CLI?

Use the Anthropic Messages-compatible setup in [Claude Code CLI](./getting-started/claude-code-cli).

### Can Codex or Claude Code send images through the same coding group?

Yes, when the deployment has validated multimodal tool-capable targets for the **exact API skins** used by those clients. A group's aggregate `image` modality may be backed by a Responses target for Codex without an Anthropic Messages image target for Claude Code. Run each documented image smoke before rollout; `502 no-eligible-target` means the group is allowed but no target satisfies that client's image shape. See [Codex CLI](./getting-started/codex-cli), [Claude Code CLI](./getting-started/claude-code-cli), and [Image Analysis And VLM Routing](./configuration/image-analysis-vlm).

### What should I do for `429 traffic-shaped`, quota, or rate-limit errors?

Honor `Retry-After`, reduce burst size or output caps, and ask an operator to inspect usage. See [Error Reference](./reference/errors), [Request Troubleshooting](./troubleshooting/requests), and [Usage Reporting](./operations/usage-reporting).

### What should I send support when a request fails?

Send the request ID, status code, client type, model group, and sanitized timing details. Do not send raw tokens, provider keys, prompts, images, or full configs. See [Troubleshooting](./troubleshooting/) and [Security And Trust](./evaluation/security-and-trust).
