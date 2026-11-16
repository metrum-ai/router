<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Router v5 Security Threat Summary

This summary covers the Router's main assets and trust boundaries. Controls reduce risk; they do not make a deployment or upstream provider risk-free.

| Asset or boundary | Threat | Controls |
| --- | --- | --- |
| Provider API keys; router to upstream provider | A stolen key is used outside the router, or a compromised upstream receives sensitive requests. | Keep provider secrets in the deployment secret store and reference them through `api_key_env`. Scope and rotate keys at the provider. Limit provider account permissions and review provider-side activity. |
| Caller keys; client to serving listener | Key guessing, sweeping, reuse, or access beyond the caller's intended groups. | Store salted `token_hash` verifiers and public `token_id` labels. Set caller `allow`, rate limits, quotas, and status. Use TLS on untrusted networks. Rotate or suspend exposed keys. |
| Admin credentials; operator to admin listener | Remote interception, credential theft, or unauthorized policy changes. | Keep the admin listener loopback-bound where possible. Use TLS, mTLS where appropriate, OIDC, and Casbin permissions. Keep legacy routes disabled after migration. |
| Prompts, tool schemas, outputs; router to logs and storage | Logs or captured content become a credential store, or content leaks across tenants. | Keep request-content capture disabled unless governed and encrypted. Use PII filtering where required. Keep logs and reports to safe metadata. Restrict log and database access and retention. |
| Audit and security records | An attacker alters or reads records to hide activity or discover deployment identifiers. | Restrict database and export access, enable authorized security reports when needed, set retention, and preserve records outside a compromised host. |
| Agent task boundary; caller to model and tool host | A model asks a client-side tool to act beyond the user's task. | Give callers only required model groups. Expose only least-privilege tools at the client/tool host. `tool_routing` validates some returned tool calls; it does not execute tools or authorize their side effects. |
| Shared self-hosted inference engine | Shared prompt or prefix caches expose cross-tenant data. | Configure the engine's cache isolation for the deployment's tenant model. Keep Router response caching partitioned by caller/project defaults. Verify isolation with a two-tenant test before sharing an engine. |
| Router to external detector or SOAR/EDR endpoint | Egress abuse, forged actions, or detector code compromise. | Keep detector code outside the Router process. Allowlist detector egress. Use TLS and the configured webhook signature secret. Give response credentials narrow scope and enforce containment through Casbin. |
| Router to upstream engine images | A vulnerable or misconfigured engine exposes requests or control surfaces. | Pin reviewed images, restrict engine network access, apply vendor fixes, and review security advisories before upgrades. |

## Related guidance

- [Hardening Guide](./hardening)
- [Compromise Runbook](./compromise-runbook)
- [Security And Governance](./overview)
