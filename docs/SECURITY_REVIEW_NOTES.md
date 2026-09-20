# Security Review Notes

Source-only internal security-review notes. Do not add this file to `scripts/package_docs_allowlist.txt`; package-safe administrator bootstrap guidance belongs in `docs/PACKAGE_VALIDATION.md` and the router-served Docusaurus docs.

This document records product security expectations for implementation and operations.

## Secrets

Never commit or print provider API keys, raw router tokens, token hashes, full production config, `env.json`, `ROUTER_TOKEN*.txt`, or production DB dumps.
Keep `env.example.json` placeholder-only and run `make secret-check` before publishing changes that touch runtime configuration or package contents. Real provider keys belong in ignored `env.json`, process environment, or the deployment secret manager.

Release package review must include `scripts/validate_package_contents.py` on every binary and Docker artifact. The validator rejects AppleDouble `._*` metadata, non-allowlisted docs, internal production runbooks, private host/IP/SSH/live-compose markers, raw provider keys, raw router tokens, GitHub tokens, token-like filenames, real license files/state, signing material, local DBs, logs, JSONL state, source-checkout files, and unexpected package paths. Binary packages also validate `linux-amd64` and `linux-arm64` ELF machine types; Docker packages validate the saved image tar path and required runtime binaries in the image layers.

Retired local agent integrations may leave credential-bearing files under
`.metrum-agents/` in older checkouts. Its tombstone ignore rules must remain
even when the integration code is removed. Before cleanup, stop any process
that could still use the files, verify the paths with `git check-ignore
--no-index`, and remove only the retired directory without reading, printing,
archiving, or force-staging its contents. Do not use `git clean -x`; it can
delete unrelated ignored credentials and local state.

Provider keys stay server-side. Caller tokens authenticate to the router and are checked before provider calls.

Browser-admin authentication is separate from router caller tokens. HTTP Basic and OIDC sessions are disabled by default. Basic must use bcrypt password hashes from deployment secrets or environment variables and HTTPS in production. OIDC must use IdP client credentials from environment variables, Authorization Code with PKCE, server-side sessions, HttpOnly cookies, production HTTPS redirect URLs, and bounded pending-login state with per-client limits. If TLS terminates at a reverse proxy, `X-Forwarded-Proto: https` is trusted only from configured proxy CIDRs. Browser-admin authentication establishes subjects such as `basic:admin` or `user:alice@example.com`; it does not grant broader admin permissions by itself. `/metrics` remains caller-token protected and requires Casbin authorization for `metrics` `read`; existing `metrics_admin: true` callers are converted to equivalent startup grants.

Admin browser reports under `/admin/reports/*` are disabled by default. When enabled, they require browser-admin identity and Casbin authorization for `admin:reports` on every page, aggregate JSON API, static asset request, request drilldown, request evidence, Markdown export, and `/admin/reports/api/version`. Report data is scoped to the admin's Casbin domain by default; deployment-wide visibility requires an explicit `*` policy domain. Request detail and `/admin/reports/api/request-evidence?request_id=<request_id>` use the separate `drilldown` action so deployments can grant aggregate report reads without per-request diagnostic access, and cross-domain request IDs return `404` for domain-scoped admins. Optional security access reports require separate `admin:security_reports` read/export policy and persist safe scalar access events only when `server.admin_reports.security.enabled: true`. Ordinary router caller tokens receive `403 reports-forbidden`. Report responses expose safe scalar usage, cost, latency, cache, fallback, provider/model, public token ID, caller metadata, access-event metadata, trusted-proxy-derived IP classification, sanitized diagnostic fields, diagnostic completeness states, decision-telemetry policy/fallback buckets, non-secret routing fingerprints, and safe build metadata only. Markdown detail export is bounded to recent matching request rows by `limit`; Markdown summary export uses SQL-backed full-window totals plus bounded top-N aggregate sections and does not include raw request rows. Report responses and exports must not include raw provider keys, raw router tokens, token hashes, raw prompts, raw images, raw image URLs, raw tool schemas, raw tool outputs, policy request/response JSON, raw policy headers, raw URLs, raw cookies, OIDC tokens, full config, raw spreadsheet formulas, raw HTML/active Markdown in exports, or unsanitized upstream bodies. Routing/config/model-group/policy/pricing fingerprints are hashes over redacted canonical metadata and must not include secret values or full config material. The browser shell uses embedded local Metrum brand assets and a dark operational theme.

Security access events may record authorized calls, unauthorized caller-token attempts, model access denials, report/metrics/content authorization failures, Basic admin auth checks, and admin report reads/exports. Safe fields include timestamp, surface, endpoint, method, status, reason, caller/admin subject, owner user, project, public token ID, client or user agent, source IP or trusted-proxy-derived IP metadata, and coarse location enrichment when a deployment adds it. They must not store raw bearer tokens, token hashes, provider keys, prompts, images, raw tool outputs, raw cookies, OIDC tokens, full config, or unsanitized upstream responses.

Signed-license enforcement uses detached Ed25519 signatures over canonical JSON payloads. Normal release builds require license enforcement and cannot be made unlicensed through runtime YAML; temporary no-license operation is limited to explicit internal dev builds. The router embeds public verification keys only and must not shell out to OpenSSL at runtime. Private signing keys, real license files, customer-specific payloads, and license state files are deployment secrets and must not be committed, logged, embedded in public docs, copied into diagnostics, or placed in container images beyond the issued runtime `license.json`. Runtime logs, usage rows, metrics, and admin status expose only safe scalar license metadata.

Runtime licensing was removed in 3.0.0. Security review should confirm public docs do not reintroduce license issuance/signing guidance or real license payloads.

Commercial/control-plane systems must remain separated from router runtime secrets. Payment-provider credentials, webhook secrets, commercial-system administration, and fulfillment replay records belong in the approved control-plane environment, not router config or deployment packages. An approved commercial event may authorize license issuance or online-lease renewal, but it must not grant access to raw license-signing keys. The router must not store or process card details.

Payment and commercial fulfillment are out of band for this public tree. Public docs must not claim an in-tree commercial checkout mechanism.

## Tenant And Caller Isolation

Caller tokens carry allow lists and quota policy, while identity is validated through explicit `users`, `projects`, and `project_memberships` config sections. Each key references an `owner_user`, project, and environment. `/v1/models` is filtered to the presented token's allowed model groups, and inactive keys are rejected after token match with safe status-specific `403` errors such as `key-disabled`, `key-suspended`, `key-expired`, or `key-rotated`.

`/metrics` is global operational telemetry. It must only be accessible to caller subjects authorized for `metrics` `read`; ordinary caller tokens must use `/v1/usage` or generated usage reports.

Content-capture maintenance is separate from metrics access. Delete-by-request and retention purge endpoints require Casbin authorization for `content:capture` `delete` or `purge`; delete-by-request is evaluated against the captured row's caller project/environment domain. Existing `content_admin: true` callers receive compatible startup grants for their own domain. Metrics-admin tokens do not imply content-admin privileges.

Public docs responses set CSP, HSTS, nosniff, no-referrer, frame/object
restrictions, and a minimum Permissions Policy in both the router docs handler
and Caddy. `microphone=(self)` is the only enabled sensitive capability because
the optional ElevenLabs voice control may request microphone access after
affirmative consent; camera, location, payment, USB, and browsing topics are
disabled. Public docs omit router build-identity response headers and expose
security contact information at `/.well-known/security.txt`.

Commercial retention is a separate governed maintenance path under `server.retention`. It records policy versions, rules, jobs, per-table status counts, legal holds, and legal-hold audit rows as scalar relational data. Legal holds are matched by data class, optional request ID, and timestamp range during dry-run counts and batch deletion. The current delete slice covers usage diagnostics, governed content capture, and rollup-gated usage detail; it does not archive data, run a scheduler, delete the remaining unsupported classes through the generic runner, or expose full legal-hold admin APIs.

## Diagnostics And Redaction

Request diagnostics may contain request ID, caller metadata, selected route/provider/model, status/error class, token counts, latency, sanitized upstream error class/message, and cost fields.

Diagnostics must not contain raw prompts, raw images, raw router tokens, token hashes, provider API keys, full upstream headers, or unsanitized upstream response bodies.

Model-group `pii_filter` may redact configured request text before routing policy, cache keys, and upstream calls. TypeScript routing scripts are operator-trusted deployment code that run inside the router process and receive request text plus normalized request fields; when `pii_filter` is enabled, TypeScript request contexts are built from the redacted request, including raw payload mirrors. External policy services receive safe derived request context by default; raw/redacted `request` and `text` mirrors are sent only when `external_policy.include_request: true` is explicitly configured for a trusted service. Script and external-policy HTTP egress enforce allowlisted hosts, default-deny ports for non-local destinations, redirect allowlisting, and connect-time rejection of private/link-local/metadata resolved addresses. Requests that exceed `max_replacements_per_request` fail closed with `pii-filter-blocked` before upstream routing. PII-filter usage metadata must stay scalar and safe: applied flag, mode, replacement count, and matched-rule count only. Raw matched values and placeholder mappings must remain in memory for the request lifecycle unless a separate governed content-capture feature explicitly enables durable storage.

Governed content capture is opt-in and disabled by default. When enabled, captured request, response, and upstream-error content is stored in separate relational tables keyed by `request_id`, with retention timestamps and audit rows for delete and purge operations. Capture enablement fails closed unless `encryption.enabled` is true and `local_key_id` is configured. Content and allowed header values are redacted and then encrypted with AES-256-GCM using a deployment-local 32-byte key; nonce, key identifier, and encrypted status are separate scalar columns. Production supplies the local AES key material out of band through `CONTENT_CAPTURE_LOCAL_KEY` in the deployment secret boundary. `redact_and_restore` response capture stores the pre-restore placeholder response rather than restored caller PII. Delete-by-request authorization is scoped to the captured row's caller project/environment domain. Header capture is allowlist-only and must not include authorization, API-key, token, secret, cookie, or key-like headers. Content export/read APIs remain out of scope.

`security_access_events` remain admin-authenticated reports only, and public
examples remain anonymized. The 2026-08-19 employment-privacy review of this
public surface found no employee identifiers published; access reporting stays
inside authenticated admin reports with retention class
`security_access_events`.

## Docs And Examples

Public docs must not hardcode the current Metrum-managed production URL as the product endpoint. Use deployment placeholders except for historical case studies or explicitly labeled hosted-deployment examples.

Model group names are deployment-defined. Public docs may show names such as `default`, `fast`, `small`, `medium`, `high`, `big-coder`, or `vision` only as examples or historical deployment names.

## Package Content Threat Model

Release packages are external administrator artifacts. They may be forwarded to customer operators, scanned by customer security teams, and unpacked on deployment hosts before the router is running. Package contents therefore must be safe without relying on repository context.

Threats to review for every package change:

- accidental inclusion of private production runbooks, SSH procedures, private hostnames, live config paths, source-maintenance notes, or issue-triage material;
- accidental inclusion of raw provider keys, raw router tokens, token hashes, GitHub tokens, signing keys, signing-service credentials, real license files, license state, DB files, logs, or JSONL state;
- accidental inclusion of source checkout directories, build artifacts, sourcemaps, platform archive metadata, or Docker image layers containing source-only paths;
- stale bootstrap docs that tell external admins to use source checkout commands instead of packaged binaries, Compose files, or embedded `/docs/`;
- package docs that omit validation, upgrade, rollback, or support paths and cause administrators to depend on internal runbooks.

Required controls:

- keep `scripts/package_docs_allowlist.txt` limited to Tier 2 bootstrap docs;
- keep full external administrator guidance in Docusaurus Tier 1 docs embedded under `/docs/`;
- mark Tier 3 source-only runbooks explicitly when they are likely to be confused with package docs;
- run `python3 scripts/validate_package_contents_test.py` after validator or allowlist changes;
- run package validation against every release tarball and inspect packaged `docs/` against the allowlist.

## Production Change Safety

For production changes, take timestamped backups, use structured config edits, keep local production snapshot synchronized, verify local and remote config hashes, run relevant real smokes, update deployment notes, and clean temporary files and stale packages.

## Dependency Scans

Release Docker builds must use the pinned Go builder image from `Dockerfile`; do not replace it with a floating `golang:<minor>-alpine` tag during packaging. When Go standard-library advisories are reported by `govulncheck`, verify both the local toolchain and the Docker builder image patch version.

For hosted docs, run `npm audit --prefix docs-site --audit-level=moderate` after dependency updates. As of 2026-06-24, remaining moderate npm audit findings are Docusaurus-transitive `gray-matter` usage of `js-yaml@3` with no patched Docusaurus dependency path available. The docs dependency tree is used to build trusted repository documentation and is not part of the router API request path.

## Kubernetes Deployment Artifacts

Kubernetes manifests under `deploy/kubernetes/` must stay placeholder-only. They may show example Secret keys and file names, but must never include real provider credentials, router tokens, token hashes, signed license payloads, Postgres passwords, private registry names, private hostnames, private IPs, or full production config. Do not include `secret.example.yaml` in applied kustomizations because it can overwrite deployment-owned Secrets with placeholders. Review ConfigMaps and examples for secret interpolation before packaging or publishing docs.

The default manifests should preserve least-privilege posture: service account token automounting disabled, non-root container, read-only root filesystem, dropped Linux capabilities, read-only config/license mounts, readiness/liveness probes, and explicit ingress/egress NetworkPolicy examples. The generic SQLite base must have no database credential or TCP/5432 egress; an explicit PostgreSQL overlay must narrowly permit its reviewed database endpoint. Any production rollout must validate CNI enforcement and that `/metrics` and `/admin/reports/` remain restricted to authorized subjects.
