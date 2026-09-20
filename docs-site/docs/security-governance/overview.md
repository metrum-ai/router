---
title: Security And Governance
doc_type: explanation
---

# Security And Governance

Metrum AI Router centralizes caller access, provider credentials, admin access, telemetry boundaries, license enforcement, and optional content governance so applications and agent clients do not handle provider keys directly.

## Governance Layers

| Layer | Purpose |
|---|---|
| Caller tokens | Authenticate application and agent traffic, bind public caller metadata, and limit allowed model groups. |
| Model-group allow lists | Prevent callers from requesting groups outside their contract or project scope. |
| Quotas and budgets | Enforce RPM, TPM, concurrency, traffic shaping, daily/monthly token limits, and spend controls before upstream calls. |
| Admin authentication | Protect browser/admin endpoints with Basic Auth or OIDC when enabled by the deployment. |
| Policy-based authorization | Authorize admin/report/security/content actions by subject, object, and action. |
| Metrics isolation | Keep `/metrics` restricted to metrics-admin callers. |
| PII filtering | Redact configured text before target selection, cache-key generation, policy inputs, and upstream calls. |
| License enforcement | Gate licensed capabilities with safe status surfaces and caller-visible `license-*` errors. |

## Data Handling Boundaries

Examples use placeholder tokens, placeholder hosts, and sample model group names. Operational diagnostics and reports expose request IDs, caller labels, selected provider/model, status, timing, token, cost, and sanitized error fields so administrators can investigate without sharing credentials or customer content.

Usage-database expiry is configured under `server.retention`. Governed content
capture remains disabled unless explicitly enabled; enabled capture requires
AES-256-GCM application encryption, a configured `local_key_id`, and local
32-byte key material supplied through `CONTENT_CAPTURE_LOCAL_KEY` in the deployment secret boundary.

`security_access_events` remain available only through authenticated,
authorized admin reports. Public documentation and examples are anonymized.
The 2026-08-19 employment-privacy review of this public surface found no
employee identifiers published; access reporting stays inside authenticated
admin reports with retention class `security_access_events`.

Managed-instance privacy and contracting information has one canonical home in
the [Privacy Notice](../privacy) and linked DPA, subprocessor, and transfer
materials.

Hosted docs load external scripts only from the same origin and permit the
inline bootstrap required by the static documentation runtime. The self-hosted
support chat widget loads only after affirmative consent. The Permissions
Policy allows `microphone=(self)` solely so a consented voice chat can request
microphone access; camera, geolocation, payment, USB, and browsing topics are
disabled.

## Related Pages

- [Admin Authentication](../configuration/admin-authentication)
- [Admin Authorization](../configuration/admin-authorization)
- [PII Filtering](../configuration/pii-filtering)
- [Software License And Third-Party Notices](../legal/software-licenses)
- [Security And Trust](../evaluation/security-and-trust)
- [Deployment Security Assessment](../evaluation/deployment-security-assessment)
