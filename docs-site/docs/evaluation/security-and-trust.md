---
title: Security And Trust
doc_type: explanation
---

# Security And Trust

Metrum AI Router is designed to keep provider credentials, private upstream endpoints, routing policy, and operational telemetry under deployment control while giving applications a stable API.

## What Stays Server-Side

- Provider API keys.
- Private vLLM, SGLang, Baseten-style, or other upstream endpoint credentials.
- Full routing config and target weights.
- Caller token hashes.
- Metrics-admin credentials.
- Browser-admin password hashes and identity policy.
- TypeScript routing script files and external policy service authentication.

Callers receive a router endpoint, a router-issued caller token, and the model groups that token may request.

## Caller Access Controls

Router-issued caller tokens can encode:

- allowed model groups;
- user, project, environment, and client metadata for reporting;
- RPM and TPM limits;
- concurrency limits;
- daily, monthly, and lifetime request or token budgets;
- metrics-admin privilege when an operator token is intentionally created.

The `/v1/models` response is filtered by the caller token. Requests for unlisted groups fail before an upstream provider key is used.

## Browser Admin Identity

Deployments can enable HTTP Basic authentication for `/admin/*` routes. It is disabled by default, uses bcrypt password hashes from deployment secrets or environment variables, and requires HTTPS unless explicitly allowed for local development. When TLS terminates at a reverse proxy, configure `trusted_proxy_cidrs` so forwarded HTTPS state is accepted only from that proxy path.

Basic Auth establishes a subject such as `basic:admin`; it does not grant access by username alone. Metrics and admin-report permissions are handled by the deployment authorization policy.

## Diagnostics And Data Handling

Usage and diagnostics are designed for operational triage without storing sensitive request content by default.

Expected diagnostic fields include request IDs, selected upstream/provider model, model group, status, attempt summaries, latency, sanitized errors, token counts, image counters, cost fields, cache behavior, and fallback events.

Diagnostic rows exclude raw prompts, raw image payloads, raw router tokens, token hashes, provider API keys, raw tool outputs, full upstream headers, and unsanitized upstream response bodies.

Governed content capture is a separate opt-in deployment mode. When enabled, captured content is redacted and encrypted with AES-256-GCM before storage, stored in dedicated relational tables keyed by `request_id`, and maintained through authorized `content:capture` delete/purge operations with audit rows. Enablement requires `encryption.enabled: true`, a `local_key_id`, and local AES-256 key material supplied out of band through `CONTENT_CAPTURE_LOCAL_KEY` in a deployment secret. Delete-by-request is scoped to the captured row's caller project/environment domain. It is disabled by default and is not part of ordinary diagnostics or usage reports. Usage-database expiry is configured under `server.retention`.

## PII Filtering

Model groups can redact configured text patterns before target selection, cache-key generation, routing-policy inputs, and upstream calls. Placeholder mappings are kept in memory for the request lifecycle unless a separate governed content-capture feature is explicitly enabled.

Regex filtering is a practical gateway control, not a full legal or compliance-grade detector. Deployments that need stronger detection should integrate a governed DLP or privacy service and validate the exact data flow.

See [PII Filtering](../configuration/pii-filtering).

## Metrics Isolation

`/metrics` exposes global operational telemetry and must be restricted to caller subjects authorized for `metrics` `read`. Existing `metrics_admin: true` caller config remains compatible through generated authorization grants. Normal application caller tokens receive `403 metrics-forbidden` and should use `/v1/usage` or generated reports for their own usage visibility.

Content-capture maintenance uses separate `content:capture` `delete`/`purge` authorization. Delete-by-request is scoped to the captured row's caller project/environment domain. Existing `content_admin: true` caller config remains compatible through generated grants for its own domain. Do not grant it to application caller tokens or assume metrics-admin access includes content access.

## Security Access Reporting

When enabled, security access reports persist safe scalar events for authorized API calls, unauthorized or invalid caller-token attempts, model access denials, forbidden metrics/report/content operations, Basic admin authentication checks, and admin report reads or exports. Events can include caller identity, owner user, project, endpoint, method, status, timestamp, source IP or trusted-proxy-derived IP metadata when configured, user agent or client, public token ID, and coarse location enrichment when the deployment adds it.

Security reports do not store raw prompts, raw images, raw bearer tokens, token hashes, provider keys, raw tool outputs, unsanitized upstream response bodies, raw cookies, OIDC tokens, or full config. Security report APIs require `admin:security_reports` authorization separately from ordinary usage report authorization.

Security access events remain inside authenticated admin reports; public
examples are anonymized and publish no employee identifiers. See the
[Privacy Notice](../privacy) for hosted-docs processing and the linked
managed-instance contracting materials.

## Private Upstreams

Enterprise-hosted inference services can remain on private network names while applications call the router. The router can mix those internal services with external providers in one model group for migration, overflow, or fallback.

For VLM services that fetch image URLs, configure upstream media-domain controls so the model server cannot fetch arbitrary internal URLs.

## Deployment Evidence To Review

Before production rollout, ask for:

- dependency and container scan summaries;
- secret-scan results for release artifacts;
- provider-key storage method;
- caller-token and public token ID policy summary;
- metrics-admin token owner;
- private-upstream network policy;
- diagnostics redaction verification;
- rollback criteria and owner.

See [Deployment Security Assessment](./deployment-security-assessment) for the detailed checklist.
