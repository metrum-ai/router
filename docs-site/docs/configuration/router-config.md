---
title: Router Configuration
doc_type: reference
---

# Router Configuration

The router is configured with YAML plus environment-loaded secrets. Keep provider credentials in the deployment environment, a secret manager, or a protected runtime `env.json`; the shipped `env.example.json` is only a shape template.

`config.example.yaml` is the shipped reference config and the source of truth for example field values. The examples in this section are partial subsets unless a code block is explicitly titled `config.example.yaml`.

Model group names are deployment-defined. Names such as `default`, `fast`, `small`, `medium`, `high`, `big-coder`, or `vision` may appear in reference examples because a sample or hosted deployment uses them; the product does not require those names.

For admin authentication, admin authorization, and PII filtering, see the canonical Security And Governance pages: [Admin Authentication](./admin-authentication), [Admin Authorization](./admin-authorization), and [PII Filtering](./pii-filtering).

## File Layout

The top-level config shape is:

- `server`: listener, default model group, cache, logging, usage persistence, license enforcement, decision telemetry, upstream timeouts, diagnostics, traffic shaping, admin auth, content capture, retention, and admin reports.
- `state_path`: local router state path.
- `providers`: upstream provider skins, credentials, model catalog metadata, capability metadata, pricing metadata, and shared provider traffic shaping.
- `models`: deployment-defined caller-facing model groups and target selection strategy.
- `users`: explicit user or service-account records.
- `projects`: explicit project records.
- `project_memberships`: user-to-project role/status bindings.
- `callers`: bearer-token subjects, owner/project binding, allowed model groups, metrics/content roles, rate limits, quotas, and caller traffic shaping.

## Module References

Use these pages as the canonical homes for each configuration area:

| Topic | Canonical page |
| --- | --- |
| Runtime licensing | Removed in 3.0.0. See [Software Licenses](../legal/software-licenses) |
| Provider skins, model catalog metadata, modalities, tools, pricing fields | [Provider Catalog](./provider-catalog) |
| Shared provider/model/target capacity controls | [Provider Traffic Shaping](./provider-traffic-shaping) |
| Model groups and weighted routing | [Model Groups](./model-groups) |
| Caller tokens, users, projects, memberships, allow lists, `/v1/models` visibility | [Caller Tokens](./caller-tokens) |
| Caller burst smoothing and bounded queueing | [Caller Traffic Shaping](./caller-traffic-shaping) |
| Cache, usage DB, decision telemetry, diagnostics, content capture, retention | [Cache And Usage Store](./cache-usage-store) |
| Model-group quality contracts | [Model Group Contracts](./model-group-contracts) |
| Reasoning eligibility | [Reasoning Routing](./reasoning-routing) |
| Dynamic-score routing | [Dynamic Score Routing](./dynamic-score-routing) |
| TypeScript routing | [TypeScript Routing](./routing-typescript) |
| External policy services | [External Routing Policy](./external-routing-policy) |
| Self-hosted OpenAI-compatible upstreams | [Self-Hosted Upstreams](./self-hosted-upstreams) |
| Image/VLM routing | [Image Analysis And VLM Routing](./image-analysis-vlm) |

## Operational Notes

Change config with structured YAML tooling, validate the result, run the relevant smoke tests, and keep deployment-facing guidance current when behavior changes. For a strategy-by-strategy ownership guide that ties caller access, group-local routing, validation, policy services, and rollback evidence together, see [Customer-Controlled Routing](../routing/customer-controlled-routing).

An active model-group target may set an optional bounded `region` scalar, for
example `region: deployment-region-a`. The router records the region of the
target that actually served the request after fallback. The value is
operator-declared metadata, not a legal or provider-behavior guarantee; validate
it through the deployment's provider and infrastructure controls. Adding this
field requires the current usage-schema migration before serving. Roll back the
config by removing the field; package rollback across the additive diagnostics
migration follows the restore-required migration contract.

For managed configuration sources, keep exactly one validated active revision
per runtime scope. Do not let a deployment select arbitrarily between competing
active revisions: the loader samples at most two matching records and refuses
to serve when more than one is present. Correct the configuration state or roll
back to the reviewed revision before serving traffic.

## Managed Catalog Capability Foundation

The current managed-configuration foundation is an operator-installed,
read-only relational projection; it is not enabled by normal router startup,
and YAML remains the runtime configuration source today. When a deployment
uses that projection for a provider catalog, each catalog model has one
capability record plus normalized rows for validated tools, modalities,
request-shape limits, inbound dialects, unsupported features, and explicit
bridges. The loader rejects a model with no capability record rather than
silently dropping its tool, image, bridge, output-cap, or output-token
semantics.

Catalog capability metadata is inherited by targets that use `model_ref`.
Target-specific capability overrides and traffic-shaping overrides are not yet
part of the managed projection; keep them in the reviewed YAML bootstrap
configuration until the managed activation workflow supports typed overrides.
This boundary keeps client-facing model eligibility deterministic while the
control plane evolves.

## OpenAI Endpoint Compatibility

`server.openai_compatibility.tolerate_responses_body_on_chat_endpoint` is disabled by default. Leave it disabled for deployments where clients use the normal API paths: Chat Completions bodies on `/v1/chat/completions` and Responses bodies on `/v1/responses`.

Enable it only after validating a client adapter that posts a Responses-shaped body to `/v1/chat/completions`. The router detects the request shape from JSON fields, not from client names, and accepts only the documented subset in [API Compatibility](../reference/api-compatibility#mixed-openai-endpoint-compatibility). Roll back by setting the flag back to `false` and restarting or redeploying the router; ordinary Chat Completions requests are unaffected by the disabled mode.

## Streaming translator

`server.streaming.translator` accepts `incremental` (default) or `synthesized`.
Incremental mode uses native same-dialect Chat, Responses, and Anthropic streams.
Responses emits each recognized event immediately and transforms protocol IDs
using `server.identifiers`; it retains terminal usage but no response text.
Unknown Responses events are dropped and counted in `stream_unknown_events`.
Cross-dialect streams continue to use unary upstream calls and `writeIRStream`.
Synthesized mode forces this unary/encoded behavior for all streaming requests.

Request logs expose scalar `stream_mode`: `native_same_dialect` or `synthesized`.
`native_bridge` is reserved for future incremental bridges; none are added here.
The first flushed caller frame commits the response. After a partial write or
flush failure, fallback is also forbidden because bytes may have reached the
caller. Cancellation closes the attempt's upstream request. Truncation never
fabricates a successful `response.completed` event.
