# Documentation Maintenance Runbook

Source-only internal runbook. Do not add this file to `scripts/package_docs_allowlist.txt` or the public Docusaurus sidebar.

This runbook is the internal source of truth for keeping operator docs, packaged docs, and hosted Docusaurus product docs aligned with shipped behavior. It is not part of the public Docusaurus site and should not be added to `docs-site/sidebars.js`.

## Documentation Surfaces

| Tier | Surface | Location | Audience | Private details allowed? | Release/package status | Owner | Review checklist |
|---|---|---|---|---|---|---|---|
| Tier 1 | Router-served external docs | `docs-site/docs/**` | Customers, evaluators, application developers, platform admins | No private hostnames, SSH usernames, key paths, raw secrets, token hashes, full config, internal source-control workflow, or private deployment procedures | Built into the router binary and served under `/docs/` | Product/docs owner with feature owner | Complete external admin flow, links in `docs-site/sidebars.js`, `make docs-qa`, `make docs-build` when feasible |
| Tier 2 | Package-safe bootstrap Markdown | Files named in `scripts/package_docs_allowlist.txt`, currently `docs/PACKAGE_README.md`, `docs/BINARY_INSTALL.md`, `docs/DOCKER_COMPOSE_INSTALL.md`, `docs/KUBERNETES_INSTALL.md`, `docs/PACKAGE_VALIDATION.md`, and `docs/solution-brief.md` | External administrators before the router is running | Placeholders only; no private operational details or source-maintenance process | Copied into binary and Docker packages under `docs/` | Release/package owner with docs owner | Verify allowlist, generic install path, `/docs/` pointer, package validator, no secrets/private markers |
| Tier 3 | Operator source-checkout runbooks | `docs/*.md`, scripts, source comments not in Tier 2 | Operators, implementation reviewers, deployment owners | This repository is public open-source; do not check in private hostnames, SSH details, AWS account IDs, backup hosts, real caller IDs, or operator evidence. Placeholders only. Never include raw provider keys, raw router tokens, token hashes, real private keys, real customer payloads, or full production config | Not packaged unless explicitly reviewed and listed in the allowlist | Owning engineering area | Source-only header where appropriate, stale-doc search, matching Tier 1 public docs when behavior is external |
| Config examples | Packaged config templates | `config.example.yaml`, `config.minimal.example.yaml`, `env.example.json`, `env.minimal.example.json`, docs snippets | Operators and evaluators | Placeholders only | `config.example.yaml` and `env.example.json` are packaged as config templates, not docs. `config.minimal.example.yaml` is the local/dev starter. | Runtime config owner | Placeholder-only secret review and config validation |

Tier rule: Tier 1 is the primary external reference, Tier 2 is only the offline bootstrap needed to start Tier 1, and Tier 3 remains source-only unless a file is deliberately rewritten for package-safe bootstrap use.

## Source-Of-Truth Map

Use this map when public Docusaurus content changes. Public pages should explain caller-visible product behavior and link to safe customer actions. Internal docs and runbooks should hold rollout, validation, troubleshooting, rollback, and security-review detail.

| Public Docusaurus section | Public files | Internal/operator source of truth | Keep aligned when changing |
|---|---|---|---|
| Product overview and solution positioning | `docs-site/docs/overview.mdx`, `docs-site/docs/solution-brief.md` | `README.md`, `docs/solution-brief.md`, `docs/PRODUCT_CAPABILITY_MATRIX.md` | Feature set, deployment models, commercial evaluation language, hosted-doc privacy boundaries |
| Getting started and model access | `docs-site/docs/getting-started/*.md`, `docs-site/docs/getting-started/*.mdx` | `README.md`, `docs/SMOKE_TEST_MATRIX.md`, `config.minimal.example.yaml`, `config.example.yaml` | `/v1/models`, caller token access, CLI base URLs, token placeholders, local bootstrap, tested client smoke paths |
| Installation | `docs-site/docs/installation/*.md`, `docs-site/docs/operations/deployment-patterns.md` | `README.md`, `docs/DEPLOYMENT.md`, `docs/DOCKER_DEPLOYMENT.md`, `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md`, package scripts, release validation | Package contents, Docker Compose and binary install paths, Fleet/customer EKS ops, runtime paths, health/readiness checks, generic upgrade and rollback guidance |
| Router configuration | `docs-site/docs/configuration/router-config.md` | `config.example.yaml`, `README.md`, feature-specific runbooks in `docs/` | YAML fields, defaults, provider catalog vs model groups vs caller keys, validation behavior, rollout and rollback notes |
| Routing | `docs-site/docs/routing/strategy-decision-tree.md`, `docs-site/docs/routing/overview.md`, `docs-site/docs/routing/customer-controlled-routing.md`, `docs-site/docs/routing/learned-routing-policy.md`, `docs-site/docs/routing/lrp-train-and-evaluate.md`, `docs-site/docs/routing/lrp-serve-and-promote.md`, `docs-site/docs/routing/lrp-train-and-serve.md`, `docs-site/docs/configuration/model-group-contracts.md`, `docs-site/docs/configuration/reasoning-routing.md`, `docs-site/docs/configuration/dynamic-score-routing.md`, `docs-site/docs/configuration/routing-typescript.md`, `docs-site/docs/configuration/external-routing-policy.md`, `docs-site/docs/reference/deprecated-selectors.md` | `docs/LEARNED_ROUTING_POLICY.md`, `docs/MODEL_GROUP_CONTRACTS.md`, `docs/DYNAMIC_SCORE_ROUTING.md`, `docs/EXTERNAL_ROUTING_POLICY.md`, `README.md`, `config.example.yaml`, routing tests, `examples/typescript-request-shape/`, `examples/external-routing-policy/` | Model-group contracts, target eligibility, LRP train/serve, request-shape filtering, fallback behavior, dynamic score, TypeScript policy, external policy, reasoning controls, smoke and rollback criteria |
| TypeScript routing | `docs-site/docs/configuration/routing-typescript.md` | `README.md`, `config.example.yaml`, script examples and tests | Script context shape, safe egress, caller-visible errors, tested examples |
| External routing policy | `docs-site/docs/configuration/external-routing-policy.md` | `docs/EXTERNAL_ROUTING_POLICY.md`, `config.example.yaml`, policy examples and tests | Request/response schema, trusted-infrastructure boundary, failure behavior, safe metadata |
| Providers and models | `docs-site/docs/providers-models/overview.md`, `docs-site/docs/reference/model-metadata.md`, `docs-site/docs/reference/add-provider-model.md`, `docs-site/docs/configuration/self-hosted-upstreams.md` | `config.example.yaml`, `env.example.json`, `docs/SMOKE_TEST_MATRIX.md`, `docs/SELF_HOSTED_UPSTREAMS.md`, provider smoke notes | Catalog-only vs smoke group vs active target, pricing source/date, modalities, tool and structured-output metadata, direct/router smokes, rollout and rollback |
| Agents, tools, and vision | `docs-site/docs/agents-tools-vision/*.md`, `docs-site/docs/getting-started/codex-cli.mdx`, `docs-site/docs/getting-started/claude-code-cli.mdx`, `docs-site/docs/configuration/image-analysis-vlm.md` | `docs/SMOKE_TEST_MATRIX.md`, `docs/harbor-case-study.md`, `config.example.yaml`, client smoke scripts | OpenAI Chat tools, Responses function tools, Anthropic Messages tools, VLM/image input, combined capability validation, coding-agent workflow examples |
| Structured outputs | `docs-site/docs/agents-tools-vision/structured-outputs.md`, `docs-site/docs/reference/api-compatibility.md`, `docs-site/docs/reference/model-metadata.md` | `docs/SMOKE_TEST_MATRIX.md`, provider smoke notes, routing tests | Chat `response_format`, Responses `text.format`, eligibility metadata, combined tool plus structured-output smokes, limitations around validation and repair |
| PII filtering | `docs-site/docs/configuration/pii-filtering.md` | `docs/PII_FILTERING.md`, `config.example.yaml`, redaction tests | Model-group `pii_filter`, redaction timing, safe telemetry, non-persistence of placeholder maps |
| Self-hosted upstreams | `docs-site/docs/configuration/self-hosted-upstreams.md`, `docs-site/docs/providers-models/overview.md` | `docs/SELF_HOSTED_UPSTREAMS.md`, `config.example.yaml`, smoke scripts | vLLM/SGLang-style setup, served model IDs, tool parser validation, private URL handling |
| API compatibility and errors | `docs-site/docs/reference/api-compatibility.md`, `docs-site/docs/reference/errors.md` | `README.md`, router handlers, tests, `docs/SMOKE_TEST_MATRIX.md` | OpenAI/Anthropic compatibility, structured error types, auth, quota, license, no-eligible-target, and upstream error semantics |
| Native streaming and cancellation | `docs-site/docs/reference/api-compatibility.md`, `docs-site/docs/reference/errors.md`, `docs-site/docs/troubleshooting/requests.md` | `docs/SMOKE_TEST_MATRIX.md`, `docs/SELF_HOSTED_UPSTREAMS.md`, `docs/TROUBLESHOOTING_RUNBOOK.md`, native-stream tests | Same-dialect native SSE, cross-dialect unary behavior, tool/usage events, response commitment, cancellation, fallback prohibition, PII placeholders |
| Request-path bounds | `docs-site/docs/configuration/routing-typescript.md`, `docs-site/docs/configuration/external-routing-policy.md`, `docs-site/docs/configuration/image-analysis-vlm.md`, `docs-site/docs/troubleshooting/requests.md` | `docs/EXTERNAL_ROUTING_POLICY.md`, `docs/TROUBLESHOOTING_RUNBOOK.md`, config comments and request-path hardening tests | Script VM concurrency, policy client timeout/cancellation, image DNS budget, safe errors, rollout and rollback |
| Runtime and Fleet package boundaries | `docs-site/docs/reference/architecture-limitations.md`, installation artifact pages | `docs/ADR_FLEET_AND_CUSTOMER_CLI_BOUNDARIES.md`, package validation | Serving/request-path isolation from Fleet, neutral shared contracts, customer-image binary exclusions |
| Fixed-model outcome gates | `docs-site/docs/evaluation/prove-router-quality.md` | `docs/EVALUATION_EVIDENCE_PLAYBOOK.md`, `examples/fixed-model-ocr-baseline/`, gate self-test | Preregistration, fixed-model control, scalar-only artifacts, nonzero regression exit, protected live evidence |
| Target region metadata | `docs-site/docs/reference/model-metadata.md`, `docs-site/docs/configuration/router-config.md`, `docs-site/docs/privacy.md`, diagnostics schema | `config.example.yaml`, `docs/MODEL_GROUP_CONTRACTS.md`, `docs/DATA_MIGRATIONS.md` | Bounded operator-declared value, served-fallback attribution, non-enforcement boundary, migration and rollback |
| Usage reporting and admin reports | `docs-site/docs/usage-cost-reports/overview.md`, `docs-site/docs/operations/usage-reporting.md`, `docs-site/docs/operations/admin-browser-reports.md`, `docs-site/docs/operations/report-examples.mdx` | `docs/USAGE_REPORTING_PLAYBOOK.md`, `docs/USAGE_DB_DESIGN.md`, `docs/SECURITY_REVIEW_NOTES.md` | Report fields, scalar usage schema, request-time cost storage, latency/throughput views, authorization, redaction, retention, customer-safe examples |
| Security and governance | `docs-site/docs/security-governance/overview.md`, `docs-site/docs/evaluation/security-and-trust.md`, `docs-site/docs/evaluation/deployment-security-assessment.md` | `docs/SECURITY_REVIEW_NOTES.md`, `docs/AUTHORIZATION.md`, `docs/ADMIN_AUTH.md`, `docs/ADMIN_OIDC.md`, `docs/PII_FILTERING.md` | Caller auth, admin auth, Casbin authorization, metrics-admin isolation, report security, PII boundaries, private upstream controls |
| Observability and operations | `docs-site/docs/operations/observability.md`, `docs-site/docs/operations/key-generation.md`, `docs-site/docs/evaluation/deployment-readiness.md`, `docs-site/docs/evaluation/operational-acceptance.md` | `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md`, `docs/DEPLOYMENT.md`, `docs/DOCKER_DEPLOYMENT.md`, `docs/USAGE_REPORTING_PLAYBOOK.md`, `docs/TROUBLESHOOTING_RUNBOOK.md` | Readiness/health/version, metrics, logs, request IDs, deployment validation, backup/rollback, public-safe operational acceptance |
| Troubleshooting | `docs-site/docs/troubleshooting/*.md`, `docs-site/docs/reference/errors.md`, usage/report pages | `docs/TROUBLESHOOTING_RUNBOOK.md`, `docs/USAGE_REPORTING_PLAYBOOK.md`, `docs/SECURITY_REVIEW_NOTES.md` | Customer-safe error triage, request IDs, quota/upstream distinctions, slow-request analysis, internal-only escalation details |
| Evaluation overview and proof points | `docs-site/docs/evaluation/evaluate-smart-router.md`, `docs-site/docs/evaluation/commercial-evaluation.md`, `docs-site/docs/evaluation/product-capabilities.md` | `docs/PRODUCT_CAPABILITY_MATRIX.md`, `docs/SMOKE_TEST_MATRIX.md`, `docs/USAGE_REPORTING_PLAYBOOK.md` | Proof-package contents, evaluation access language, tested capabilities |
| Model-group quality and benchmarks | `docs-site/docs/evaluation/model-group-quality.md`, `docs-site/docs/evaluation/harbor-case-study.mdx` | `docs/SMOKE_TEST_MATRIX.md`, `docs/harbor-case-study.md`, usage reports, evaluation scripts | Success criteria, workload verifier, model-group contracts, source-dated benchmark data |
| Deployment readiness and operational acceptance | `docs-site/docs/evaluation/deployment-readiness.md`, `docs-site/docs/evaluation/operational-acceptance.md` | `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md`, `docs/DEPLOYMENT.md`, `docs/DOCKER_DEPLOYMENT.md`, `docs/SMOKE_TEST_MATRIX.md` | Rollout, rollback, smoke tests, package validation, operational cleanup |
| Security and trust | `docs-site/docs/evaluation/security-and-trust.md`, `docs-site/docs/evaluation/deployment-security-assessment.md` | `docs/SECURITY_REVIEW_NOTES.md`, `docs/AUTHORIZATION.md`, `docs/USAGE_REPORTING_PLAYBOOK.md` | Metrics isolation, admin/report/content authorization, diagnostics redaction, dependency/container scan notes |
| Competitive landscape | `docs-site/docs/evaluation/competitive-landscape.md` | Source-dated market notes and current product capability docs | Primary-source citations, dated pricing, customer-value framing, no private deployment facts |
| Release notes and upgrades | `docs-site/docs/release-notes/*.md` | Package/release scripts, `README.md`, `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md`, `docs/DEPLOYMENT.md`, `docs/DOCKER_DEPLOYMENT.md`, `docs/SECURITY_REVIEW_NOTES.md`, release validation notes | Customer-safe shipped behavior, config/database/license changes, validation checklist, rollback notes, no source-control or private deployment details |

## Release Notes Workflow

Before packaging a release, run `make release-notes-from-git` to draft release-note entries from available router release tags. Review and hand-edit the draft before publishing so each entry is customer-safe and covers highlights, operator impact, caller impact, validation, and rollback.

Keep public release notes focused on shipped release information. Do not leave release-note templates, authoring instructions, placeholder bullets, or maintainer checklists in `docs-site/docs/release-notes/**`.

Each public release-note entry should include:

- release version, release date, and build timestamp when available;
- package architecture or artifact notes when operators must choose a package;
- config, database, license, metrics, reporting, or deployment changes;
- client-visible API, error, model metadata, routing, tool, VLM, or CLI compatibility changes;
- validation evidence such as `/readyz`, `/version`, `/v1/models`, completion smokes, metrics, reports, license checks, package validation, and changed model-group/API-skin smokes;
- rollback guidance naming the previous package/config/license/database action when relevant.

Never include private hostnames, SSH details, raw router tokens, token hashes, provider API keys, full production config, private signing details, customer-specific license payloads, internal source-control workflow, or placeholder release-note template text in public release notes.

Run `make docs-qa` before `make docs-build`. The docs QA checks that the hosted docs include a version banner component, per-page docs metadata, a releases index, at least one release-note entry, and no forbidden public release-note patterns. Public release notes should name shipped router versions and build timestamps, not internal source-control workflow details.

## Behavior-Change Documentation Requirements

Every behavior, config, deployment, model, auth, CLI/API, telemetry, evaluation, or security change needs both sides of the documentation set unless the change is purely internal and not caller/operator visible.

For operator/deployment docs, update the relevant files in `README.md`, `docs/`, scripts, and config comments. Cover configuration shape, validation, rollout, smoke tests, rollback, operational impact, and security impact.

For public Docusaurus docs, update `docs-site/docs/**` so customers know what to request, what the proxy does, what errors to expect, and what evidence to ask operators for. Keep examples generic with placeholder endpoints and placeholder tokens. Do not add public pages to `docs-site/sidebars.js` unless the task explicitly includes navigation work.

Specific high-risk changes require extra coverage:

- Model/provider routing changes: document model-group intent, API shapes, modalities, tool dialects, validation evidence, price metadata, promotion criteria, and rollback criteria.
- Evaluation or benchmark changes: explain the product decision context, success criteria, verifier used, workload limits, cost/latency evidence, and date of data collection.
- Harbor case-study changes: keep `docs-site/docs/evaluation/harbor-case-study.mdx` and `docs/harbor-case-study.md` aligned; source-date Harbor, Terminal-Bench, and supported-agent claims; use primary Harbor/Terminal-Bench links first; describe Harbor as one optional verifier; include router-versus-fixed-model controls and claim boundaries; avoid private hostnames, raw tokens, token-file paths, or universal model-ranking language.
- Security or auth changes: document caller-visible errors, admin/operator controls, least-privilege policy, redaction boundaries, smoke tests, and audit/report fields.
- Usage/telemetry changes: document database/report fields as scalar queryable data, saved request-time cost inputs, latency/performance dimensions, retention impact, and customer-safe examples.
- TypeScript or external policy routing changes: document trusted input shape, safe context fields, egress boundaries, fail-closed behavior, caller-visible errors, and at least one tested runnable example.
- PII/content-capture changes: distinguish routing demos from outbound redaction, keep raw matched values and placeholder maps out of persisted telemetry, and document governed capture separately.

## Public Terminology And Tone Checklist

Use this checklist for Tier 1 public docs and customer-facing package-safe Tier 2 docs. Keep exact config fields, API fields, error codes, report tab names, and diagnostic bucket values unchanged, then make the surrounding prose use customer-facing terms.

| Concept | Preferred public wording | Avoid in prose unless quoting exact fields or errors |
|---|---|---|
| Client credential | caller token or router token | internal key, API key, bearer key |
| Reportable token identifier | public token ID | token hash, raw token, token suffix |
| Caller-facing route name | model group | model alias, route, deployment constant |
| Selected backend | upstream model or provider model; provider/model when referring to report dimensions | raw model when the caller sees only a model group |
| Troubleshooting artifacts | sanitized evidence, request evidence, diagnostics | raw prompts, raw traces, full request body |
| Deployment paths | private managed, enterprise self-hosted, hosted evaluation | shared public multitenant inference service unless explicitly contrasting what the product is not |
| Access or capability boundary | unavailable, not entitled, not validated, not configured, or not enabled for this deployment | unsupported as a broad product judgment |
| Spend attribution | cost allocation, cost attribution, or project chargeback when customer-facing | internal chargeback, internal billing process |

Style rules:

- Lead with the customer action or capability boundary before explaining the implementation detail.
- Prefer "the caller token is not allowed to use the model group" over "the key cannot access the model."
- Prefer "the upstream/provider model is not entitled or not validated for this request shape" over broad "not supported" language.
- Refer to `/v1/models` as the caller-facing source of truth for allowed model groups.
- Refer to request drilldown output as request evidence or diagnostics, and explicitly say it is sanitized when discussing support packets.
- Keep field names such as `token_id`, `token_sha256`, `model`, `provider`, `model-not-allowed`, `no-eligible-target`, and report column labels exact when documenting configuration, JSON, SQL, errors, or UI labels.

## Evaluation And Security Grouping

Keep public evaluation pages grouped around buyer and operator proof points, not private operational history:

- Evaluation proof path: `commercial-evaluation`, `evaluate-smart-router`, `product-capabilities`, `model-group-quality`, and `harbor-case-study`.
- Operational rollout path: `deployment-readiness` and `operational-acceptance`.
- Security proof path: `security-and-trust` and `deployment-security-assessment`.
- Cost and market path: `cost-governance`, `report-examples`, and `competitive-landscape`.

Internal runbooks should mirror those groups:

- Evaluation evidence lives in `docs/SMOKE_TEST_MATRIX.md`, `docs/PRODUCT_CAPABILITY_MATRIX.md`, evaluation scripts, and source-dated case-study artifacts.
- Security evidence lives in `docs/SECURITY_REVIEW_NOTES.md`, `docs/AUTHORIZATION.md`, admin/report runbooks, scan output summaries, and deployment sign-off records.
- Operational evidence lives in `docs/DEPLOYMENT.md`, `docs/DOCKER_DEPLOYMENT.md`, `docs/CUSTOMER_INSTANCE_OPERATIONS_RUNBOOK.md`, package validation, smoke-test logs, and rollback notes.

When one slice affects more than one group, update all affected public pages and internal sources in the same change. For example, a new admin security report changes security/trust, usage reporting, error reference, router config, authorization docs, and deployment readiness.

## Stale-Doc Search Checklist

Before finishing a docs-affecting change, run targeted searches for old names, outdated model status, removed fields, stale errors, and private markers. Start with the changed concept and then search adjacent surfaces from the source-of-truth map.

Recommended commands:

```bash
rtk rg -n "<changed-field>|<old-field>|<new-field>" README.md docs docs-site config.example.yaml internal scripts
rtk rg -n "<old-model>|<new-model>|<provider>|<group-name>" README.md docs docs-site config.example.yaml internal scripts
rtk rg -n "current|active|reference|production|hosted|catalog-only|fallback|failover" README.md docs docs-site
rtk rg -n "metrics-forbidden|reports-forbidden|content-forbidden|model-not-allowed|no-eligible-target" README.md docs docs-site internal
rtk rg -n "raw prompt|raw image|token hash|provider key|private key|full config|SSH|hostname" README.md docs docs-site scripts
```

For provider/model/pricing/capability work, also search source-dated model names and old route policy labels. Mark historical sections with a date and reason when old benchmark output must remain for comparison.

## Secret-Safety Checks

Docs and examples must never expose:

- provider API keys;
- raw router tokens or token suffixes;
- token hashes;
- real `license.json` payloads, signing keys, signing-service credentials, or customer-specific license data;
- full production config contents;
- raw prompts, raw images, raw tool outputs, raw policy request/response JSON, cookies, OIDC tokens, or unsanitized provider responses;
- private hostnames, IP addresses, SSH usernames, key paths, live compose paths, backup paths, or production token-file paths in public Docusaurus docs;
- private upstream URLs in public examples unless written as generic placeholders.

Use placeholders such as `https://router.example.com`, `ROUTER_TOKEN`, `rtr_metrum_<user>_<project>_<env>_<key>_<secret>`, `PROVIDER_API_KEY`, and `config/config.yaml`.

Run:

```bash
make docs-diag-schema
make docs-qa
make secret-check
```

Run `docs-diag-schema` whenever diagnostics, usage, reporting, retention, security-access, or governed content-capture schema structs change. `docs-qa` checks public-facing docs for known private deployment markers and stale current-route claims. `secret-check` verifies environment examples, package-content validation tests, and license SKU checks. For public docs changes, run `make docs-build` when feasible.

## Packaging Boundaries

Do not add this runbook to `scripts/package_docs_allowlist.txt`. Packaged docs should remain customer/operator safe and must pass package validation.

When packaging changes, review the allowlist explicitly:

- keep Tier 2 limited to offline bootstrap files and package-safe solution material;
- verify every allowlisted file explains how to reach `/docs/` after startup;
- remove source-checkout runbooks such as `docs/DEPLOYMENT.md`, `docs/DOCKER_DEPLOYMENT.md`, `docs/SECURITY_REVIEW_NOTES.md`, `docs/SMOKE_TEST_MATRIX.md`, and `README.md` unless they have been rewritten and reviewed as package-safe bootstrap docs;
- run the package validator self-test after allowlist changes;
- when building packages, confirm packaged docs exactly match `scripts/package_docs_allowlist.txt`.

If a new internal runbook contains production-specific operations, keep it out of `docs-site/`, out of the package allowlist, and out of public examples. If customer-facing behavior depends on that runbook, write a separate sanitized Docusaurus explanation.

## Final Review Checklist

- The changed public docs have matching internal/operator source-of-truth updates.
- The changed internal docs point to any public caller-facing behavior that must be kept aligned.
- Public docs contain no private hostnames, SSH details, raw secrets, token hashes, full configs, or internal-only deployment procedures.
- Model groups are described as deployment-defined contracts, not hardcoded product constants.
- Pricing, competitive, provider, and serving-framework claims are source-dated or revalidated during the task.
- Evaluation docs state the verifier, workload, success criteria, date, and limits of the evidence.
- Security docs distinguish ordinary caller access, metrics-admin access, browser-admin identity, admin-report authorization, and content-capture maintenance authorization.
- Stale-doc searches and relevant docs checks have been run or explicitly reported as not feasible.
