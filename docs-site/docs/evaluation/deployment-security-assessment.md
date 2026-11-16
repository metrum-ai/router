---
title: Deployment Security Assessment
doc_type: explanation
---

# Deployment Security Assessment

Use this checklist to assess a Metrum AI Router deployment before production rollout or after a major routing, provider, auth, or telemetry change.

## Secrets And Authentication

Confirm:

- provider API keys are loaded server-side from the deployment environment or protected config files;
- licensed deployments mount only the issued runtime license file; private signing keys, full license payloads, and detached signatures are excluded from images, logs, reports, browser docs, tickets, shared configuration systems, and shared support bundles;
- caller tokens are distributed only to approved users, services, or validation jobs;
- runtime config stores caller token hashes, not raw caller token secrets;
- raw provider keys, raw router tokens, token hashes, and full production config are excluded from browser docs, logs, tickets, and announcements;
- metrics-admin access uses separate caller subjects authorized for `metrics` `read`, with existing `metrics_admin: true` callers converted to compatible grants;
- content-capture maintenance access uses authorization policy `content:capture` `delete`/`purge` policy, delete-by-request is scoped to the captured row's caller project/environment domain, and existing `content_admin: true` callers are converted to compatible grants for their own domain.
- browser-admin Basic Auth, when enabled, uses bcrypt hashes from deployment secrets, requires HTTPS in production, trusts forwarded HTTPS state only from configured proxy CIDRs, and maps to stable subjects such as `basic:admin`; see [Admin Authentication](../configuration/admin-authentication).
- browser admin reports, when enabled, require authorization policy `admin:reports` policy in addition to browser-admin identity, and reject ordinary router caller tokens with `403 reports-forbidden`.

Acceptance checks:

- ordinary caller token can access allowed API paths;
- ordinary caller token receives `403 metrics-forbidden` on `/metrics`;
- metrics-admin token can scrape `/metrics`;
- ordinary caller token receives `403 content-forbidden` on content-capture maintenance endpoints;
- ordinary caller token receives `403 reports-forbidden` on `/admin/reports/*` when reports are enabled;
- authorized Basic Auth or OIDC session report subject can read `/admin/reports/api/summary?since=24h`;
- `/v1/models` returns only groups allowed for the presented token.

## Diagnostics And Data Handling

Confirm diagnostic records exclude:

- raw prompts;
- raw image payloads;
- raw router tokens;
- token hashes;
- provider API keys;
- full upstream headers;
- unsanitized upstream response bodies.

Expected diagnostics include request IDs, selected provider/model, attempt summaries, status, latency, sanitized errors, token counts, image counters, cost fields, cache behavior, and fallback events.

Governed content capture is disabled by default. If a deployment enables it, confirm captured rows are redacted before storage, keyed by `request_id`, subject to retention purge, and maintained through audited `content:capture` delete/purge operations scoped to the captured row's caller project/environment domain.

## Network And Private Upstreams

For private vLLM, SGLang, Baseten-style, or other OpenAI-compatible upstreams:

- keep upstream endpoints on private network names where possible;
- expose the router as the governed ingress point;
- validate direct upstream reachability from the router host or network;
- validate router-level access from approved clients;
- keep private upstream tokens in the deployment environment;
- configure media-domain restrictions for VLM services that fetch image URLs.

For TypeScript `router.fetchJSON` and `strategy: external` policy egress:

- keep policy services inside trusted infrastructure because they receive routing context and eligible target metadata;
- use exact hostname allowlists, not suffixes or wildcards;
- prefer HTTPS for all non-local policy services;
- approve `script_http.allow_http: true` or `external_policy.allow_http: true` only for trusted internal endpoints that cannot use HTTPS;
- verify redirects are blocked when they point to non-allowlisted hosts, including loopback addresses that were not listed.
- enforce private-network and CIDR restrictions with deployment network policy until native router CIDR egress controls are added.

## API Surface Review

Validate:

- `/readyz` and `/version` expose build metadata without secrets;
- `/v1/chat/completions`, `/v1/responses`, and `/v1/messages` enforce caller auth, allow lists, quotas, and target eligibility;
- `/v1/usage` returns caller-appropriate usage visibility;
- `/admin/reports/*`, when enabled, returns only safe scalar report data and local embedded assets after browser-admin identity plus policy-based authorization;
- `/metrics` is restricted to metrics-admin tokens;
- no private host paths, SSH details, provider keys, or raw tokens appear in hosted docs.

## Dependency, Container, And Package Scans

Each deployment should follow the organization's security process for:

- container image vulnerability scanning;
- dependency scanning for packaged docs and runtime dependencies;
- verification that release images are built with the pinned Docker builder Go patch version, not a floating language image tag;
- secret scanning of release artifacts;
- static analysis or policy review for TypeScript routing scripts;
- review of third-party script dependencies before packaging;
- verification that the router does not install packages or fetch dependency code at runtime.

The Docker builder image is pinned to a patched Go toolchain tag so reachable Go standard-library advisories are controlled by the image patch version used for the release build.

Release reviews should include dependency and artifact scan evidence for the shipped package. If a release has a residual documentation-build or packaging advisory that does not affect the router request path, record the residual in the private release evidence and keep authored documentation inputs trusted until a patched dependency path is available.

## Security Sign-Off Record

Record:

- deployment version and build timestamp;
- config backup path;
- token policy summary;
- provider key storage method;
- metrics-admin token owner;
- private upstream network policy;
- scan tooling and result summary;
- accepted residual risks;
- rollback owner and rollback command path.
