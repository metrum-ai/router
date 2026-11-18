---
title: Release Notes
doc_type: reference
---

# Release Notes

## v5.0.0 - 2026-11-18

v5.0.0 is a security release. Review the [v4.8 to v5.0.0 upgrade guide](/docs/release-notes/upgrade-guide) before upgrading.

### Security

- Use one shared placeholder counter per built-in namespace so different secret detectors cannot collide during restoration.
- Hold PEM private-key blocks through only a matching END label and fail closed when the stream ends unresolved.
- Inspect streamed Chat refusal deltas with per-choice secret DLP, redacting split credentials or blocking them under `fail_on_match`.
- Invalidate pending and released tool-firewall approvals when the effective policy changes, so retries are evaluated and approved under the current rules.
- Inspect Anthropic text and thinking carried by streamed block-start events before forwarding them when secret DLP is enabled.
- Keep independent secret-scanning buffers for each Responses text item and content part so interleaved deltas cannot hide credentials.
- Mark honeytoken and canary OCSF findings Critical and include the caller ID and source IP for SIEM correlation.
- Include firewall decisions and honeytoken/canary use in the opt-in OCSF 1.4.0 security event stream.
- Rewrite inspected hosted Responses tool items and argument/code events when secret DLP redacts their payloads.
- Run Harbor task containers as UID/GID 10001 with a passwd entry, owned workspace, and writable verifier directory. Require digest-pinned image references in Kubernetes examples; add an opt-in Kyverno trusted-registry policy example and exact-file KSV-0125 suppressions for public deployment placeholders.
- Preserve project long-lived-key restrictions when updating project metadata through the admin API.
- Keep the version-pinned Grype exception for the TypeScript 7 build-time compiler: stable `typescript` 7.0.2 still embeds `golang.org/x/text` v0.38.0, which a fresh Grype database reports as GO-2026-5970 (High). The exception applies only to the compiler binary under `node_modules`; revisit when a stable release embeds fixed Go and x/text v0.41.0 or later.
- Evaluate published Casbin advisory ranges against the exact locked version, retain range evidence, and require reviewed exact-scope exceptions. Document fail-closed recovery for advisory service outages.
- Ship the example external detector's per-rule bounded-LRU auth-failure storm, tiny-request sweep, denied-model enumeration, and spend-spike rules. Signals retain the router's opaque key ID and bounded evidence IDs; source-IP-only signals have no key. Configured detectors default to report mode, and the example rule thresholds are flags with documented defaults.
- Partition response-cache entries and vLLM prefix-cache salts by caller and project by default, add opt-in sharing inside a project, and forward stable salts only to explicitly configured vLLM providers. SGLang prefix-cache isolation is not supported in v5.0.0.
- Add opt-in RFC 8693 workload token exchange with short-lived caller-scoped tokens, optional task attribution from an issuer claim or grant-pinned value, task/owner usage attribution, atomic per-process cumulative budget reservations (including embeddings), and per-project long-lived-key rejection. Grant budgets persist across token expiry and are pruned when the grant is removed on reload. Mission tokens are accepted only on the explicit inference route allow-list; all other serving routes, including `/mcp` and parent-key revocation, return 401.
- Add a required pull request and merge-group CI gate that checks every pinned vLLM and SGLang image in deployment YAML and lock files against GitHub Advisories and OSV, with tier-local documented exceptions and fail-closed behavior when advisory services are unavailable.
- Update NVIDIA and ROCm Kubernetes deployment and provisioning examples to digest-pinned official vLLM v0.33.0 images after scanning the full deployment tree.
- Move the AMD reference and edge images to the digest-pinned upstream vLLM ROCm 10.0 image at v0.33.0, which is outside the affected range of GHSA-v97p-wpxp-px84.
- Add `metrum-ai-routerctl doctor --security` with bounded probes, JSON output, configurable failure thresholds, and findings for admin exposure, caller limits, key format and recorded entropy, sensitive-group DLP, and serving-port metrics authorization. Engine pins use the release advisory snapshot; offline operation never executes config-adjacent scripts or makes advisory network calls.
- Add opt-in latest posture summary reporting through the authenticated admin security reports surface. Usage migration 2026111602 adds a singleton summary table at usage schema version 17.
- Record UTC issuance timestamps for generated and rotated caller keys; the security self-test warns when age is unknown and fails keys older than 90 days.
- Reject Responses tool snapshots that do not match the accumulated arguments inspected from streamed deltas.
- Route every config, usage, and credential audit writer through append-only chained inserts; serialize SQLite chain appends, backfill legacy chains in bounded batches, protect deletion markers from direct API deletion while allowing retention to prune expired markers and advance the chain checkpoint, export complete fields, and verify ordered export pages offline. The checkpoint remains in the database and is not externally signed in v5.0.0.
- Add the versioned external detector contract and bounded asynchronous detector queue. Operator detector code runs in separate services; see the detector security guide.
- Reject userinfo in external-policy and detector URLs, including redirect targets.
- Add a Security Console to admin reports with detector and authentication risk ranking, a timeline of detector/auth/containment events, scoped serving-port containment with CSRF and audit, admin-only release instructions, and time-filtered JSON/Markdown incident bundles.
- Generate checksummed v2 caller tokens, reject malformed checksums before lookup, add database-backed leaked-key self-revocation enforced by every authenticated route and propagated to replicas, and warn or enforce caller-token hygiene. Revoked verifiers live in a sticky denylist independent of config sets, so rollback and YAML import cannot restore a leaked key; rotation installs a new verifier.
- Add an opt-in `pii_filter.secrets_enabled` detector pack for common provider/router credentials, JWTs, bearer authorization values, private-key blocks, and credential assignments in inbound, buffered response, and built-in-only streamed text. Stream filtering tracks unresolved JWT and PEM candidates from their start, checks every PEM BEGIN marker through its END marker (capped at 64 KiB), and keeps a 512-byte ordinary rolling tail; unary responses fail closed when the shared replacement limit is exceeded. With #929 bounded tool-call inspection enabled, streamed tool arguments are inspected before release and redacted or blocked by the configured policy; custom-rule-enabled streams remain fail-closed.
- Decode JSON-encoded tool-call arguments before secret scanning, checking string values with their property names as `key=value` and header-shaped values as `key: value`; normalize malformed argument strings in both forms, inspect object-valued arguments and Anthropic `tool_use.input`, and redact function-call arguments and custom tool input in Responses output snapshots. Completed streamed tool arguments use the same JSON-aware scan before release.
- Pin `golang.org/x/crypto` to the upstream fix commit for GO-2026-5932, removing the unused `openpgp` packages from the dependency graph.
- Add opt-in `server.tool_firewall` deny rules, sequence checks, and admin approval holds. Enforcement inspects streamed tool calls at a bounded hold before releasing tool frames; groups can separately opt into schema validation.
- Add optional per-provider hourly and daily spend breakers. Breaker state uses estimated costs in memory, emits structured trip/reset events, and returns `503 provider_spend_breaker` when configured to stop.
- Raise the Go patch floor to 1.27.4 for GO-2026-6859 (CVE-2026-97054), an HTTP/1 server crash caused by a `MaxBytesReader` header race in `net/http`. Release binaries, locally built container images and the CI runner image use Go 1.27.4, and source builds require at least Go 1.27.4.
- Resolve proxied client addresses from the right through trusted proxy hops, so client-supplied leftmost `X-Forwarded-For` values cannot control caller binding, admin OIDC rate limits, or security access records.
- Add optional per-caller CIDR, ASN-prefix, and verified mTLS identity binding with off, report, and enforce modes. Enforced mismatches return the same generic 401 as invalid tokens; security events retain the specific reason.
- Add domain-scoped, hash-chained admin audit export and separately authorized usage-audit deletion. Apply configured audit retention to control-plane change events.
- Add candidate policy simulation in the authenticated caller's own domain. Responses include the decision and policy revision, with a generic explanation that does not expose matched rule fields or policy rules.

### Breaking

- v5.0.0 removes legacy machine `/admin/*` routes from the serving port. Use `/admin/v1` on the admin listener. Configs that still set `admin.legacy_routes_on_serving_port` or `admin.legacy_sunset` fail at load with migration guidance. `admin.enabled` defaults to `true`; the authenticated admin listener binds `127.0.0.1:8081`. Set it to `false` for a single-listener deployment. Browser reports and `/admin/auth/*` routes remain on the serving port. Database-mode operators must run the router-config maintenance migration before startup; it removes the v4-only keys from stored canonical configurations and refreshes their content hashes.
- `metrum-ai-routerctl config import|export|activate|rollback` now use the Admin API by default; use `--offline` for deliberate direct database access while the router is stopped. The default Admin API endpoint is `http://127.0.0.1:8081`; non-loopback endpoints require HTTPS. Reports use `/admin/v1/reports`, and the former `/admin/reports` `--reports-prefix` value is rejected. Offline mutations refuse while any v5 router holds the database runtime lock; stop every v5 replica first.
- Remove the in-process TypeScript routing strategy and its goja/esbuild dependencies. Configurations using `strategy: script`, `script`, `script_max_concurrent`, or `script_http` now fail at load with migration guidance to `strategy: external`. Port request-shape and PII-aware policy examples to standalone services.
- v5.0.0 removes caller `token_sha256` verifiers. Non-empty legacy fields fail at load; empty legacy fields are ignored; migrate held tokens with `metrum-ai-routerctl callers migrate-legacy --config <config.yaml> --id <caller-id> --token-file <private-file>` (or `--token-file -` for stdin) or rotate them. Migration preserves status and unknown entropy; disabled or revoked callers are cleaned without receiving a token. Rotation preserves status and revocation metadata; use `--reactivate` only to explicitly restore access. Token generation and rotation now emit only salted verifiers.

### Added

- Add an optional admin Unix socket with configurable owner/mode and `metrum-ai-routerctl --socket` transport; existing authentication and authorization stay in force.
- Governed per-domain fine-tuning operator workflow: opt-in encrypted capture, DLP-filtered export, external training and preregistered gates, bounded shadow, audited promotion, one-call rollback, and independent retention for copied datasets.
- Add release-only OpenAI Chat, OpenAI Responses, and Anthropic Messages SDK client cells to the bounded tool-call workflow. A manual `cell_set=release` dispatch on `main` attaches the sanitized report to release checks.
- Add `routerctl honeytoken issue` and `routerctl honeytoken report`, plus caller-scoped canary model groups. Issued decoy keys return a canned local completion and canary use raises a security event without contacting an upstream. Database-backed callers use router-config migration `2026111611` (schema 18).
- Define trusted W3C `traceparent` correlation across router chains, with a new span and request ID per hop and safe scalar usage fields for joining requests. Trust is opt-in through `server.trace.trusted_parent_cidrs` or an authenticated caller's `trust_traceparent`; malformed, duplicate, or untrusted input starts a new trace. Outbound trace headers are on by default and can be disabled per provider; inbound `tracestate` is deliberately dropped.
- Forward Anthropic `thinking` settings to supported Bedrock Claude models and preserve `thinking` and `redacted_thinking` history, signatures, ordering, and streamed reasoning blocks through Converse tool continuations. Malformed blocks fail before AWS dispatch.
- Add per-request tool-routing counts, first failure class, retries, escalations, outcome bucket, and reliability source to usage records and reports. Tool reliability matrix v1 has an empty seed because it contains no provider/model-attributed failure counts; routing starts neutral and learns from live usage. The additive migration is schema 21, following the schema-20 recomputed-input migration.
- Add nightly evaluation aggregation across the previous 30 runs, with expected-missing cells reported separately from gate failures.
- Add virtual model groups whose eligible targets are selected from per-family capability vectors imported from reproducible eval reports. The config Admin API and `routerctl groups` show vectors, membership and exclusion reasons.
- Added a release checklist and a bounded package upgrade-and-rollback check.
- Retain locally prepared Codex AI review findings by exact commit range and artifact digest, validate the committed artifact without a model call during publication, and attest release manifests and review evidence.
- Add machine-client registration, credential metadata and revocation, effective-grant access review, and expiring and unused-credential warnings. Client credentials are checked against the shared control-plane store on authentication; credential secrets are not stored or returned.
- Add an opt-in CPU-first small-work model group to the hierarchy reference. It uses the official pinned vLLM CPU image, limits the CPU candidate to 800 estimated input tokens and 128 requested output tokens, and falls back to the frontier candidate on upstream failure or timeout.
- Add a caller-authenticated MCP gateway with per-server egress allow-lists, `ROUTER_MCP_` credential forwarding, HTTPS upstreams (cleartext HTTP is limited to loopback), compositional tool allow-lists, bounded JSON-RPC traffic, persistent caller/tool rate and estimated-spend limits, and keyed invocation audit digests instead of raw arguments.
- Complete MCP governance by applying the selected model group's secret and PII masking to tool arguments and results, enforcing configured tool-firewall approvals before upstream calls, and mirroring safe invocation outcomes into the hash-chained admin audit export.
- Add a non-serving PostgreSQL Kubernetes migration Job example that plans or applies both scopes through router-config schema 18.
- Add delayed daily report-mode provider usage reconciliation for OpenAI, Anthropic, and Bedrock CloudWatch metrics, with bounded catch-up/retries, HTTPS-only allow-listed egress for provider APIs and Bedrock/STS, partial-data watermarks, auth-failure fast paths, and security-stream events.
- Add a standalone prompt-injection detector example with a commit-pinned model revision, downloaded-weight checksum verification, and overlapping chunk scoring for opted-in tool-result and document segments.
- Add an opt-in OCSF 1.4.0 JSON Lines security-event stream for stdout, append-only files, and allow-listed signed HTTPS webhooks. Security events use bounded sink and webhook queues, timestamp/nonce HMAC signatures, rotation-aware file output, and Prometheus drop/failure counters. Containment maps to Account Change Disable/Enable with target and actor identity; detector findings use configured IDs and evidence request UIDs capped at 256 characters.
- Add parent/child hierarchy governance with authenticated bounded policy snapshots, shared parent organization counters, inherited model-group/tool/PII restrictions, singleflight refresh and bounded stale-cache fallback. Hierarchy audit attribution adds `organization_id` and `team_id` in schema 22 via migration `2026111626`, after tool-routing schema 21 migration `2026111623`.
- Add caller key containment through `/admin/v1/containment`, `routerctl contain` and `routerctl release`, plus an optional HMAC-SHA256 SOAR/EDR webhook. Webhook writes use signed nonce replay protection without ETag headers; containment can resume only containment-originated suspensions.
- Record recomputed prompt tokens when upstream reports cached prompt tokens. Usage and cost reports now show cached and recomputed input tokens with reporting coverage.
- Prefer targets marked `tiered_kv_cache: true` for estimated prompts of at least 32,000 tokens. Dynamic-score affinity accepts `metadata.session_id` and remains bounded per process.
- Add opt-in shadow/enforce mapping from detector signals to temporary key throttles or suspensions, with a per-minute action cap, emergency caller exemptions, and persistent TTL state.
- Route detector suspensions through the canonical containment API so containment and admin roles can resume them; require a finite default throttle limit for unlimited keys.
- Add versioned provider and model-catalog reads, provider validation and create/update/disable, project and environment lifecycle, project-environment membership CRUD, project USD budgets with alert thresholds, caller quota reads and updates, and retention policy writes. Writes use configuration revisions; retention changes that can delete more data require `admin:retention:force`, and provider credential references and headers never appear in responses.
- Cap process-wide in-flight SSE line-buffer capacity and expose current usage and rejections in Prometheus metrics.
- Add durable asynchronous admin operation resources, versioned control-plane backup/restore through the Admin API and routerctl, and atomic model-target batches capped at 100 operations.
- Add an opt-in bounded stream hold for tool-call frames, with an absolute deadline, an ordered allow/block/redact inspector hook, and per-stream byte limits charged to the process-wide SSE buffer budget. Hold OpenAI Chat (including legacy `function_call`) and Responses argument events through inspection, fail closed when call-count or argument limits truncate inspection, and release Anthropic tool-use frames before terminal `message_delta`; ordinary text frames continue immediately.

### Changed

- Label direct-provider nightly spend as a list-rate estimate, retain measured Router usage as its own basis, and report gate violations from runs older than 30 days as historical.
- Align Harbor's provider-free Docker preflight with the live OpenAI Responses adapter and retain its sanitized summary on failure.
- Direct `model` targets inherit catalog capabilities (reasoning, tools, modalities, and request-shape/bridge support) for matching model IDs while retaining their configured dialect, pricing, and routing values. Ambiguous model IDs across dialects do not inherit capabilities unless one catalog entry matches the target dialect. Mixed reasoning rejections identify the caller field (such as `reasoning_effort` for Chat Completions) when the error parameter is otherwise unset.
- Raise the default upstream SSE line limit to a bounded 8 MiB and expose `server.streaming.max_sse_line_bytes` with validated 64 KiB–32 MiB bounds.
- Authenticate caller credentials before returning identifier-rewrite unavailability, preserving 401 for invalid credentials and recording honeytoken use before account-policy rejection on caller-authenticated router routes.

Migration entries below retain their scope, maintenance requirements, and rollback class. Follow the upgrade guide for the complete deployment-job sequence.

| Migration id | Scope | Schema version | Change | Maintenance | Data job | Rollback class |
| --- | --- | --- | --- | --- | --- | --- |
| `2026111601` | `router-config` | 13 | remove v4 serving-port admin settings from stored configs | maintenance | none | `restore-required` |
| `2026111602` | `usage` | 17 | store latest security posture summary | online | none | `restore-required` |
| `2026111603` | `usage` | 18 | add request trace correlation metadata | online | none | `restore-required` |
| `2026111607` | `usage` | 19 | add mission task and owner attribution | online | none | `restore-required` |
| `2026111610` | `router-config` | 17 | add admin client and credential registry, lifecycle audit fields, and deduplicated unused-warning state | online | none | `restore-required` |
| `2026111614` | `usage` | 20 | record recomputed prompt tokens and rollup coverage | online | none | `restore-required` |
| `2026111627` | `router-config` | 19 | add bounded offline-operation provenance markers | online | none | `restore-required` |
| `2026111627` | `usage` | 23 | add bounded offline-operation provenance markers | online | none | `restore-required` |
| `2026111628` | `router-config` | 20 | store chained admin audit hashes and prune checkpoints | online | none | `restore-required` |
| `2026111628` | `usage` | 24 | store chained admin audit hashes and prune checkpoints | online | none | `restore-required` |

### Fixed

- Fix audit retention pruning moving its checkpoint backwards under concurrent replicas.
- Fix security console resume instructions failing with HTTP 428. Routerctl now discovers the current configuration ETag for guarded mutations, and copied HTTP instructions fetch and send `If-Match`.
- Use the public, revision- and checksum-pinned ProtectAI DeBERTa-v3-base prompt-injection classifier in the reference detector example so setup requires no model-host access grant.
- Fix admin operations failing with database busy under concurrent audit writes on SQLite.
- Keep tool-firewall sequence holds active across retries and require each retry to consume a fresh released approval before forwarding a held call; concurrent retries cannot reuse one release.
- Fix PostgreSQL usage database upgrades and fresh installs rejecting the hierarchy audit index when its columns are reported out of order.
- Include boolean values when matching tool-firewall argument deny rules.
- Keep streamed tool calls from multiple Chat choices held until every choice with a tool call has finished.
- Inspect and redact output snapshots in failed and incomplete Responses stream events, honoring `fail_on_match` before forwarding them.
- Inspect populated Responses added text as rolling chunks, deduplicate repeated part snapshots, and inspect terminal snapshots before flushing held text.
- Flush held refusal and reasoning summary text when a Responses output item completes.
- Keep only `id`, `type`, `status`, and `error` on non-message and non-reasoning output items in failed and incomplete Responses snapshots, including unrecognized item kinds.
- Harden the packaged production reference configuration and ship the local development profile separately as `config/config.dev.example.yaml`.
- Keep pending provider usage windows inside the configured catch-up age bound.
- Read audit export rows and chain anchors from repeatable snapshots so concurrent writes and retention do not produce invalid pages.
- Preserve each provider's trace propagation setting when the admin provider update API replaces its other fields.
- Bound authentication audit writes with durable per-replica five-minute counters, chain one count-delta row at window close, and fall back to direct chained writes when the counter table is full; deny authentication if either audit write fails.
- Evaluate tool-firewall sequence rules in response order, including first and second steps returned in one response.
- Persist a denied same-response tool sequence so a retry cannot bypass its `then_tool` rule.
- Remove the unsupported legacy admin route setting from both hardening guides and direct operators to the separate `/admin/v1` listener.
- Align the release procedure with the committed AI review artifact gate and document the required artifact preparation path.
- Correct the upgrade guide to describe successful local sinkhole responses for unknown or disallowed honeytoken model names.
- Align OpenAI and Anthropic compatibility matrix versions with the pinned Python SDK lockfile and regenerate the published matrix page.
- Read control-plane backup tables from coordinated, repeatable database snapshots so related rows and their audit anchors cannot come from different writes.
- Align the standalone cache isolation guide with v5 caller-scoped response-cache and vLLM prefix-salt behavior; document that SGLang prefix isolation is unsupported.
- Keep disabled callers with intentionally empty verifiers visible in the admin caller list while rejecting active callers without verifiers.
- Include managed admin-client statuses, credential bindings, and emergency activation state in control-plane backups so restore preserves disablement and revocation state.
- Disable the unused admin listener on API compatibility test routers to avoid collisions when tests start several routers, and make plain `make` compile binaries without running the full test suite during CodeQL autobuild.
- Preserve sanitized per-cell tool compatibility results when live SDK cells fail, and ingest failures as live evidence.
- Require all three SDK cells in release reports and distinguish release details from nightly runs.
- Roll back failed automatic suspensions so they release their action-cap slot; exempting a caller during config refresh releases an active automatic throttle before its TTL.
- Migrate the API compatibility suite to Anthropic Python SDK 2.3.0 and test API-key and bearer authentication with separate clients.
- `metrum-ai-router-migrate --json` keeps stdout valid JSON, including on failures, by directing GORM diagnostics to stderr.
- Drop target-declared unsupported `temperature` and `top_p` fields before dispatch; classify structured 400/422 sampling-parameter refusals for pre-commit fallback; and forward `top_p` when translating to OpenAI Chat Completions.
- Keep the admin migration-status API read-only: it now refuses unavailable or pre-ledger databases without bootstrapping or upgrading migration tables.
- Generate OpenAPI concurrency and idempotency metadata from normalized runtime operations so the published contract omits `If-Match`, `Idempotency-Key`, and conflict responses for routes that do not enforce them.
- Bind client credential metadata to configured identities; enforce client status, expiry and revocation across replicas; refuse lifecycle writes that remove the last policy administrator; exclude expired and revoked bindings from access-review grants.
- Treat every credential registration as managing its entire subject when checking administrator lockout, return unavailable when access-review grants cannot be resolved, and delay unused-credential warnings until a binding has been registered for 24 hours.
- Reevaluate provider spend-breaker trips against current ceilings so raising or removing a limit takes effect on the next admission check.
- Keep SQL usage reports aligned with in-memory recomputed-token coverage when input counts are negative.
- Retry the earliest provider usage reconciliation window without a successful checkpoint, including partial and failed windows, before advancing catch-up.
- Preserve mission-token grant identity and cumulative budget when an unchanged issuer is reloaded.
- Admit honeytoken and canary sinkhole requests through caller rate limits, validate honeytoken groups on relational config writes, and bound repeated security-event writes with a 10,000-entry LRU and Prometheus suppression counter.
- Record canary-group use from embeddings and count-token routes, include canary usage in honeytoken reports, and prevent stale honeytoken config from reaching a real upstream.
- Re-evaluate project budget trips against current ceilings on every admission, preserve provider spend-breaker settings during admin updates, normalize project/environment identifiers before authorization, and require retention force for every change that can purge additional data.
- Allow oversized upstream lines to fall back before downstream commitment; retain reported usage from discarded attempts with explicit audit provenance.
- Cover large streaming tool calls, repeated Responses completion payloads, limit errors, fallback boundaries, and tool-result continuation.

### Known issues

- At eight concurrent clients, median plain-path completion overhead increased from 1.475 ms in v4.8.0 to 3.035 ms in rc.3. This remains an accepted v5.0.0 limitation; [#1193](https://github.com/metrum-ai/router/issues/1193) tracks the latency follow-up for v5.1.0.
- No prebuilt container image is published. Use a binary release archive; Docker and Compose installs are unsupported.

Release notes help customer operators decide whether to deploy, how to validate the release, and how to roll back if needed. Each packaged router build also displays its router version and build timestamp on every docs page.

For upgrade execution, see [Upgrade Guide](/docs/release-notes/upgrade-guide). For the docs package index, see [Releases](/docs/releases).

The entries below describe the validation contract for the package that embeds
this page. The version banner, `/docs/releases`, and `/version` are the
authoritative sources for its exact router version and build timestamp; do not
infer the running version from a date written in documentation.

## v4.8.0 - 2026-11-08

### Highlights

- **An admin control plane on its own listener, machine-to-machine only.** `admin.enabled: true`
  starts a second listener (`admin.listen`, default `127.0.0.1:8081`) that accepts mTLS client
  identities from configured trust domains, OAuth 2.0 client-credentials and workload identity
  federation tokens with DPoP or certificate sender constraints, configured admin API keys and
  offline emergency credentials. It accepts no HTTP Basic, cookies, browser sessions, HTML or CORS.
  See [Admin Listener](/docs/configuration/admin-listener) and the
  [operator guide](https://github.com/metrum-ai/router/blob/main/docs/ADMIN_PLANE_UPGRADE.md).
- **One authorization decision point.** Every `/admin/v1` operation goes through
  `Authorizer.Authorize(ctx, principal, domain, object, action)` backed by Casbin, with compatibility
  grants for the v4.7 permission maps and flags, escalation and last-normal-administrator guards,
  and a policy freshness gate that answers `503 policy-stale` on an expired revision clock while
  reads stay available.
- **A versioned API contract.** `/admin/v1` has a typed route registry with generated OpenAPI 3.1 and
  a CI drift check, problem+json errors, pagination with signed cursors, `ETag`/`If-Match`,
  `Idempotency-Key`, dry-run, rate limits and an authentication-failure throttle.
- **Admin keys, policy resources and audit.** Issue/list/get/rotate/revoke admin API keys with a
  one-time secret; 19 typed policy-resource operations with immutable active content and
  clone-rollback; audit written in the same transaction as the change.
- **Automated TLS for both listeners.** `server.tls` and `admin.tls` support router-served
  certificates from files or ACME DNS-01, proxy termination, an mTLS client-CA bundle and a TLS 1.2
  or 1.3 floor.
- **A v4.7 configuration is unaffected.** Without an `admin:` block the router behaves exactly like
  v4.7, and the defaults keep the compatibility surface: `legacy_routes_on_serving_port: true` and
  `/metrics` on the serving port.

### Operator Impact

| Area | Change |
| --- | --- |
| Admin transport | Opt-in second listener; loopback by default; a non-loopback bind needs TLS or `allow_plaintext_non_loopback: true`; the legacy `/admin/*` routes stay on the serving port with `Deprecation`, `Sunset` and `Link` headers |
| Admin credentials | Machine credentials on the admin listener: mTLS, OAuth 2.0 client credentials, workload identity federation, admin API keys and emergency credentials. The serving-port browser login for the reports UI is unchanged |
| Authorization | Casbin on every admin decision with v4.7 compatibility grants; `router-internal:` is reserved for internal identities and is refused in public subjects; Basic-auth membership and secret changes need a restart |
| Database | Two scopes move, six additive online migrations with no data job: usage schema 12 → 15 and router-config schema 9 → 12, all `restore-required` for rollback; `migration_policy: deployment-job` (the default) requires the non-serving migration job before serving |
| PostgreSQL with DB-sourced policy | `/admin/v1` mutations answer `503 policy-stale` until usage migration `2026110704` (schema 15) has created the policy runtime table; reads and static/SQLite authority are unaffected |
| `/metrics` | Stays on the serving port with the v4.7 caller-token scrape; `admin.metrics.on_serving_port: false` is refused until the admin-side handler ships |
| TLS | `server.tls` and `admin.tls` are new and off by default; certificate and key changes need a restart, the client CA bundle reloads live |
| Kubernetes | New admin-plane overlay publishing the admin port as a ClusterIP Service only, with a default-deny NetworkPolicy that admits labelled admin clients in a named namespace |

### Migrations

| Migration id | Scope | Schema version | Change | Maintenance | Data job | Rollback class |
| --- | --- | --- | --- | --- | --- | --- |
| `2026110601` | `router-config` | 10 | admin API Idempotency-Key store | online | none | `restore-required` |
| `2026110701` | `router-config` | 11 | admin audit columns and credential events | online | none | `restore-required` |
| `2026111629` | `router-config` | 21 | durable credential audit counters | online | none | `restore-required` |
| `2026111629` | `usage` | 25 | durable credential audit counters | online | none | `restore-required` |
| `2026110801` | `router-config` | 12 | admin API-issued key metadata and generations | online | none | `restore-required` |
| `2026110702` | `usage` | 13 | admin audit events for usage-database changes | online | none | `restore-required` |
| `2026110703` | `usage` | 14 | admin denial columns on the security access log | online | none | `restore-required` |
| `2026110704` | `usage` | 15 | policy runtime singleton revision | online | none | `restore-required` |

Every one is additive and transactional with no row rewrite and no data job. `2026110704` depends on
`2026110703`, which depends on `2026110702`; `2026110801` depends on `2026110701`, which depends on
`2026110601`. Package rollback never runs a reverse migration: restore the approved pre-migration
snapshot before deploying the earlier package. The deployment-job order for both scopes is in
[DATA_MIGRATIONS.md](https://github.com/metrum-ai/router/blob/main/docs/DATA_MIGRATIONS.md).

### New configuration

- `admin.*` (new, disabled by default): `enabled`, `listen`, `allow_plaintext_non_loopback`,
  `legacy_routes_on_serving_port` (default `true`), `legacy_sunset`, `external_url`,
  `tls.{mode, certificate_source, cert_file, key_file, min_version, client_ca_file, client_auth,
  trusted_proxy_cidrs, acme.*}`, `limits.{max_body_bytes, max_header_bytes, read_header_timeout_ms,
  read_timeout_ms, write_timeout_ms, idle_timeout_ms, shutdown_timeout_ms}`,
  `metrics.on_serving_port`, and `authentication.{mtls.*, issuers[], jwks.*, dpop.*, api_keys.*,
  emergency.credentials[]}` for the machine credentials. Defaults and every key are in
  [Admin Listener](/docs/configuration/admin-listener).
- `server.admin_api.*` (new, defaults apply when absent): `audit.retention_days`,
  `cursor_key_env`, `pagination.{default_limit, max_limit}`, `idempotency.{ttl, max_key_bytes,
  max_response_bytes}`, `rate_limit.{enabled, read_rps, read_burst, write_rps, write_burst,
  max_identities}`, `auth_failure_throttle.{enabled, window, free_attempts, base_delay, max_delay}`.
- `server.tls.*` (new): `mode` (`off`), `certificate_source` (`files` or `acme`), `cert_file`,
  `key_file`, `min_version` (`1.2`), `acme.*` with the same fields as `admin.tls.acme`.
- `server.admin_auth.authorization.max_staleness` (new): bounds how old the last complete
  authoritative policy synchronization may be before PostgreSQL-authority mutations fail closed.
- No existing field is removed or renamed, and every new block is optional.

### Fixed

- Configured admin API keys whose status is written in any case are disabled when they say disabled:
  `DISABLED` and `Disabled` keys no longer authenticate on the serving port
  ([#851](https://github.com/metrum-ai/router/issues/851)).
- `metrum-ai-router-migrate --json` prints the status document while a migration is pending instead
  of an empty line with a zero exit, and an encoding failure is a non-zero exit with a bounded error
  ([#807](https://github.com/metrum-ai/router/issues/807)). This fix is also in v4.7.1.

### Security

- v4.8.0 release binaries are built with Go 1.27.3. That toolchain carries the Go 1.27.2
  standard-library security fixes (net/http HTTP/1 and HTTP/2 request handling, `crypto/tls` ECH,
  `html/template` escaping, `mime/multipart` header parsing) and Go 1.27.3's `crypto/x509` chain
  filter for distrusted intermediate certificates. Source builds require at least Go 1.27.3; the CI,
  release and container build producers use the patched toolchain and assert the version they
  actually run. The Go 1.26.10 floor that first carried the standard-library security fixes for the
  37 package and symbol advisories recorded in
  [#854](https://github.com/metrum-ai/router/issues/854) (GO-2026-4599 through GO-2026-6809; the
  [Go vulnerability database](https://pkg.go.dev/vuln/) lists them) remains the security history of
  this release. Go 1.27 also removes the `asynctimerchan`, `gotypesalias`, `tlsunsafeekm`,
  `tlsrsakex`, `tls3des`, `tls10server` and `x509keypairleaf` GODEBUG settings; this tree sets none
  of them and relies on none of the removed behaviours.
- The entire `router-internal:` prefix is reserved for internal authorization identities: policies or
  public subject mappings that select it fail startup/reload validation instead of being read as
  public grants.
- Admin requests capture one immutable authentication/authorization generation, every credential
  reload publishes one complete replacement atomically, and a rejected reload changes nothing. Admin
  requests run with a finite 60-second deadline, and `cnf.jkt`-bound tokens must use the `DPoP`
  scheme (a `Bearer` presentation is refused with `sender_constraint_failed`).

### Known limitations

- `/metrics` stays on the serving port: `admin.metrics.on_serving_port: false` is refused until the
  admin-side handler ships.
- Certificate and key changes need a restart; the client CA bundle reloads live. A Unix-socket admin
  listener and an Ingress/NodePort/LoadBalancer for the admin plane are not available.
- Emergency activation is single-replica and DPoP replay state is node-local; multi-replica
  coordination is not part of this release.
- With file-mode configuration the machine runtime has no durable credential-event sink:
  authentication works, but no authentication or credential lifecycle events are written, and
  emergency enrollment is refused rather than backed by a no-op adapter.
- PostgreSQL authority with DB-sourced policy answers `503 policy-stale` for mutations until
  migration `2026110704` has run; restore recovery today is a restart.

### Release verification

The live release gate ran on the release commit `f2ae91d1` with the v4.7.0 gate configuration
unchanged, byte for byte, in one sequential run, with the router built with Go 1.27.3.

| Stage | Result on f2ae91d1 | v4.7.1 | v4.7.0 |
| --- | --- | --- | --- |
| A: 24 agent cells | 20/24 | 20/24 | 19-21/24 |
| B: Harbor matrix, both API styles | pass, 5/5 in every lane plus the offline replay | pass, 5/5 plus the replay | pass, 5/5 plus the replay |
| C: live SDK compatibility suites | fail, fully classified | fail, fully classified | fail, fully classified |
| D: parallel tool calls | pass, 6/6 | pass, 6/6 | pass, 6/6 |

The four red stage-A cells are the three `openai-chat -> gpt` cells (the documented
`reasoning_effort` plus tools limitation, identical on v4.7.0 and v4.7.1) and
`routed-openai-responses-claude/quote` on admission `429` at the unchanged caller quota. Stage C's
tester reports are byte-identical to the archived v4.7.0 reports apart from request ids and the local
port, so the classification is unchanged and the stage is not reported as a pass.

The upgrade path is proven with the released v4.7.1 binaries: the v4.7.1 usage database takes exactly
the three new usage migrations, `verify-serving` passes, the router starts and serves, and the
upgraded schema matches a fresh v4.8.0 install exactly (49 tables, 1082 columns, 227 indexes). A
v4.7.1 file-config deployment behaves identically on both versions, and a router-config
control-plane database moves from schema 9 to 12.

### Upgrade

A v4.7 configuration needs no change: without an `admin:` block the router behaves exactly like
v4.7. Under `migration_policy: deployment-job`, run the non-serving `plan` → `apply` →
`verify-serving` job for the usage scope and the router-config scope before starting the new package.
Adopting the control plane afterwards is reversible: enable `admin.enabled`, keep
`metrics.on_serving_port: true` until the scrape moves, issue one machine credential, move automation
to `/admin/v1`, then set `legacy_routes_on_serving_port: false`.

## v4.7.1 - 2026-11-08

### Highlights

- **Rebuilt with Go 1.26.10.** Every v4.7.1 binary is built with Go 1.26.10,
  which carries the Go standard-library security fixes for the
  37 advisories recorded in
  [#854](https://github.com/metrum-ai/router/issues/854). Source builds now
  require Go 1.26.10 or newer, and the CI, release and container build producers
  use the patched toolchain and assert the version they actually run.
- **Migration status is readable while work is pending.**
  `metrum-ai-router-migrate --json` prints the status document at the point the
  deployment-job procedure in
  [DATA_MIGRATIONS.md](https://github.com/metrum-ai/router/blob/main/docs/DATA_MIGRATIONS.md)
  asks for it, instead of an empty line with a zero exit
  ([#807](https://github.com/metrum-ai/router/issues/807)).

### Operator Impact

| Area | Change |
| --- | --- |
| Build | v4.7.1 binaries use Go 1.26.10; source builds require at least Go 1.26.10 |
| Migration tooling | `--action=plan --json` and `--action=status --json` return a document while a migration is pending: id, name, release, checksum, manifest digest, execution, maintenance, lock, timeout and rollback classes, data-job key, postcondition and dependencies. A JSON encoding failure is a non-zero exit, never an empty success |
| Database | Unchanged: the usage schema stays at version 12. An upgrade from v4.7.0 runs no migration, and a rollback to v4.7.0 needs no restore for schema reasons |
| Configuration and API | Unchanged: no field is added, renamed or removed, and no authorization decision changes |

### Fixed

- `metrum-ai-router-migrate --json` printed an empty line and exited 0 whenever a
  migration was pending. A pending migration is a migration definition that
  carries handler functions, which the encoder cannot represent; the status is
  now projected into a handler-free document whose pending entry carries only
  safe metadata. The rest of the document keeps its existing shape and field
  names, and an encoding failure now exits non-zero with a bounded error
  ([#807](https://github.com/metrum-ai/router/issues/807)).

### Security

- v4.7.1 release binaries are built with Go 1.26.10. The Go standard-library
  advisories that apply
  to Go 1.26.0, including the 37 package and symbol advisories recorded in
  [#854](https://github.com/metrum-ai/router/issues/854) (GO-2026-4599 through
  GO-2026-6809; the [Go vulnerability database](https://pkg.go.dev/vuln/) lists
  them), are fixed in that toolchain, so v4.7.1 binaries do not carry them. A `govulncheck` scan of this tree on exactly Go 1.26.10
  reports no package or symbol findings and one module-only advisory
  (GO-2026-5932, `golang.org/x/crypto/openpgp`) that router code does not call.

### Known limitations

- An OpenAI Chat target still rejects a request that pairs `reasoning_effort`
  with tools, and a Chat-dialect agent lane against such a target reports the
  provider's typed refusal. The release gate records those cells as red on this
  release and on v4.7.0.
- The live SDK stage is a scoped subset: provider-hosted tools and
  resource-creating endpoints are excluded by design and reported as such, not
  as unsupported passes.

### Release verification

The live release gate ran on the release commit `2deb954d791d` with the v4.7.0
gate configuration unchanged, byte for byte, in one sequential run.

| Stage | Result on 2deb954d | v4.7.0 result |
| --- | --- | --- |
| A: 24 agent cells (6 routed lanes and 2 direct baselines x file/edit/quote) | 20/24 | 19-21/24 across the replicated runs |
| B: Harbor matrix, routed vs direct in both API styles | skipped: the stage needs a Docker host and none was available | pass, 5/5 both styles |
| C: live SDK compatibility suites | fail, fully classified | fail, fully classified |
| D: parallel tool calls, direct vs routed | pass, 6/6 | pass, 6/6 |

The four red stage-A cells are the three `openai-chat -> gpt` cells, where the
provider answers `400 upstream-rejected` for the `reasoning_effort` plus tools
request shape (the documented limitation above, identical on v4.7.0), and
`routed-openai-responses-claude/quote`, which failed on admission `429
tpm-exceeded` at the unchanged caller quota. Stage C's tester reports are
identical to the archived v4.7.0 reports apart from request ids and the local
port: documented `501` surfaces (`/v1/complete`, moderations, message batches,
beta files, `responses compact` and `responses input_tokens` on the Claude
target) and typed `400` refusals (embeddings without an embeddings group,
document modality, thinking and chat on the GPT target). No router defect appears
in the classification, and the stage is not reported as a pass. Stage B passed
the bounded Harbor matrix on a dedicated Docker host: every lane 5/5, routed
equal to direct on every task, and the offline replay passed before any provider
call.

### Upgrade

A v4.7.0 usage database opens on v4.7.1 without a migration, every existing row
stays readable, writes continue, and the released v4.7.0 binaries still read the
database afterwards.

## v4.7.0 - 2026-11-06

### Highlights

- **Tool-aware routing, opt-in.** A model group can declare
  `strategy: tool_aware` with a `tool_routing` block. In `observe` mode the
  router validates the upstream's tool calls against the caller's arguments and
  records a failure class without changing behaviour; `enforce` adds one
  same-target retry and bounded escalation inside the request deadline and the
  effective spend ceiling. Groups without the block never run the code.
- **Configurable HTTP deadlines.** `server.read_timeout_ms`,
  `write_timeout_ms`, `idle_timeout_ms` and `stream_idle_timeout_ms`, with a
  stream progress deadline that every successful write extends, so a stall after
  the first chunk ends the stream with a clear `upstream_timeout` record while a
  slow first token stays bounded by the attempt and upstream timeouts.
- **Stored Responses affinity.** Opt-in, with an authenticated-encryption
  envelope for upstream ids, retention-aware metadata and the stored-response
  operation set for enabled groups. The envelope is new in this release, so
  there is no earlier stored-response metadata format to migrate.
- **PII filtering on translated histories.** Responses system-message histories
  served by Anthropic and Bedrock targets are filtered at their instruction
  boundaries instead of being replayed raw, and response-cache identity for
  translated targets includes the exact filter state.
- **Reference deployments.** An escalation-continuum set of reference
  configurations. Validated on hardware: `nvidia-datacenter` (1× H100 80 GB) and
  `amd-strix-halo-edge` (its measured 1-worker p95 is above the 8 s target, which
  its Limits state). Functional check only: `nvidia-gb10-edge`. Committed without
  a hardware run: `amd-mi350p-datacenter` (#785). The edge bundle ships alongside
  them, and the layered hierarchy reference carries measured per-hop results on
  AMD Instinct MI325X, Intel Xeon CPU and Strix Halo tiers.
- **Release discipline.** The router hierarchy compatibility matrix is a required
  step of the release workflow, and the layered edge → datacenter → frontier
  deployment ships as a reference configuration with per-hop usage rows and
  measured hop cost. Shared hierarchy governance is planned for v5.0.0.

### Operator Impact

| Area | Change |
| --- | --- |
| Database | Usage schema moves from 11 to 12; with the default `migration_policy: auto-safe` the migration is applied at startup, existing rows stay readable and writes continue. `migration_policy: deployment-job` deployments keep the reviewed `router-migrate apply` / `verify-serving` gate |
| HTTP deadlines | Four new `server.*_timeout_ms` settings, zero selects the default; the write default follows the worst-case attempt chain and the stream idle window starts at the first data byte |
| Tool-aware groups | New opt-in `strategy: tool_aware`; `observe` records only, `enforce` adds one same-target retry and bounded escalation, both bounded by the request deadline and spend ceiling |
| Stored Responses affinity | Opt-in per model group; enabling it requires `server.retention` on, and affinity rows follow the `responses_affinity` retention class and legal holds |
| Reference deployments | New reference configurations and an edge bundle; installed artifacts and their measured limits are documented with each tier |

### Known limitations

- An OpenAI Chat target still rejects a request that pairs `reasoning_effort`
  with tools, and a Chat-dialect agent lane against such a target reports the
  provider's typed refusal. The release gate records those cells as red on this
  and on the previous release.
- The live SDK stage is a scoped subset: provider-hosted tools and
  resource-creating endpoints are excluded by design and reported as such, not
  as unsupported passes.
- Layered deployment and shared hierarchy governance are reference
  configurations in this release; cross-hop request identifiers and a folded
  cross-tier cost total are not product features.
- `amd-mi350p-datacenter` ships its structure, pins and configuration without a
  hardware run (#785), and `nvidia-gb10-edge` has a functional check on hardware
  but no measured concurrency or latency. `amd-strix-halo-edge` has one measured
  run whose 1-worker p95 is above its own 8 s target.

### Release verification

The live release gate ran on the release candidate `5db5ac66a7e4` with the
v4.6.0 gate configuration unchanged, recorded as issue #805. Stage B passed the
bounded Harbor matrix (routed equals direct at 5/5 in both API styles plus the
offline replay) and stage D passed 6/6. Stage A's only non-quota red cells are
the three `openai-chat → gpt` cells described above, which fail identically on
the previous release with the same configuration; a replicated four-pair A/B on
the same configuration and harness shows the candidate's failure set equal to or
smaller than the previous release's, with mean tokens per request within 0.2%
and admission rejections inside the stated bound. Stage C is reported as a red
stage with a full classification of documented `501` surfaces and typed `400`
refusals, with no router defect in the classification.

This change set contains release documentation only:

```
 CHANGELOG.md                          | 54 ++++++++++++++++++++-
 deploy/reference/README.md            |  8 ++--
 docs-site/docs/release-notes/index.md | 88 +++++++++++++++++++++++++++++++++++
 3 files changed, 145 insertions(+), 5 deletions(-)
```

`git diff --stat 5db5ac66a7e4 v4.7.0 -- internal cmd go.mod go.sum` is
empty, so the router code is identical to the gated commit.

## v4.6.0 - 2026-11-02

### Highlights

- **Reasoning provenance, off by default.** When an operator enables it, opaque
  reasoning history is bound to the provider account, model and group that
  minted it, unwrapped only for that target and dropped after a fallback,
  instead of being replayed to a provider that never issued it. With
  `mode: off` nothing changes. See
  [Reasoning provenance](/docs/operations/reasoning-provenance).
- **Embeddings and reporting.** `POST /v1/embeddings` is served for
  embeddings-configured model groups, with admission control before any
  upstream call and usage metering on the same accounting path as chat; the
  configuration is fail-closed on embedding space, dimensions and selection
  strategy. A scoped CFO usage report adds spend and hypothetical savings by
  team, project, key and model group with JSON, CSV and Markdown exports.
- **Tool results are preserved exactly.** Empty tool results are no longer
  serialized as `null`, multi-part tool-result text is concatenated without
  inserted separators across the translated directions including Bedrock, and
  mixed text and image tool results survive Messages-to-Responses. Parallel
  tool calls ship with an offline fixture matrix in required CI and a
  budget-capped live gate whose passing and unproven cells are published.
- **Caller-visible changes:** `400` capability failures name the recorded
  eligibility reason and set `param` only when every rejected candidate shares
  that field, and Anthropic-shaped error envelopes gain `error.code` and
  `error.param`; requests against PII-enabled groups bypass the response cache
  and skip shadow replay, and Responses system-message histories on those groups
  fail closed for Anthropic and Bedrock targets; a null Messages
  `context_management` is treated as absent; `content_provenance_checks`
  answers `501 unsupported_endpoint`.
- **No usage-database migration.** The usage schema stays at version 11.

### Operator Impact

| Area | Change |
| --- | --- |
| Database | No usage-database migration in this release; schema stays at 11, so no migration gate applies and a rollback to 4.5.0 needs no schema restore |
| Reasoning provenance | New `server.reasoning_provenance` block, `mode: off` by default; `observe` and `enforce` require `key_env` with a secret of at least 32 bytes, and a configuration that supplies `key_env`, `key_id` or `ttl` while `off` is rejected (#622) |
| PII-enabled groups | Response-cache reads and writes are bypassed while target-bound cache identity is reconciled, so upstream calls and provider spend can rise for those groups; existing cache entries are not purged and can become eligible again if filtering is disabled, until expiry or cache reset (#670) |
| PII-enabled groups | Candidate shadow replay is skipped with a policy recheck before dispatch, and Responses system-message histories fail closed with `system_pii_unsupported` when only Anthropic or Bedrock Messages targets are eligible; a replay already past the check can finish after an in-flight change, so stop and drain shadow runs for a strict cutover (#664, #668) |
| Embeddings | New endpoint for embeddings groups only; groups declare `embedding_space`, serve one identical dimension set and may set `default_embedding_dimensions`; chat-shaped strategies are rejected for embeddings groups (#532) |
| Error bodies | `400` capability failures carry the eligibility reason and a `param` limited to fields shared by every rejected candidate; Anthropic-shaped envelopes add `error.code` and `error.param`. Callers matching the previous generic text must accept the specific reason (#655) |
| Unsupported surfaces | `POST /v1/content_provenance_checks` answers an authenticated `501 unsupported_endpoint` with no upstream dispatch, where it previously fell through to a generic unsupported response (#620) |
| Anthropic Messages | A null `context_management` is treated as absent and is not forwarded; non-null values still require both the caller's beta and the target allowlist (#459) |
| Usage rows | New rows carry a bounded endpoint label; the schema-11 column is unchanged and historical rows are not backfilled (#653) |
| Reporting | The scoped CFO report requires retained raw usage and current project-to-team mappings; recorded costs are usage-derived, not invoices, and unmapped usage stays separate (#435) |
| Examples and CI | Example overlays are scanned as rendered manifests, six example serving workloads and the example runner image run non-root, and the example dependency locks move to patched releases (#669, #682, #640) |

### Caller Impact

Routed model groups, prices and usage accounting are unchanged. Client-visible
differences are the new `POST /v1/embeddings` surface, the more specific `400`
bodies and Anthropic `error.code`/`error.param` fields, `501` for
`content_provenance_checks`, the treatment of a null Messages
`context_management` as absent, fewer eligible targets and no response caching
for PII-enabled groups, and exact tool-result text. Reasoning provenance is
invisible to callers unless an operator enables it; with `mode: off`, reasoning
history passes through exactly as before.

### Known limitations

- **Reasoning with tools on Chat Completions — Chat callers included.** A
  Chat Completions caller that sends tools together with `reasoning_effort` is
  affected, not only an Anthropic caller. Against a Chat-dialect target the
  provider rejects the request and the caller sees `400 upstream-rejected`.
  Routing that caller to a Responses target through the `chat_to_responses`
  bridge does not currently make it eligible either: the router reports
  `400 unsupported_endpoint_for_model` with `param: model` and the requirement
  `openai-chat_tool_passthrough` — that refusal comes from the target not
  declaring `openai-chat` in `request_shape_support.supported_inbound_dialects`,
  not from the bridge itself. Practical guidance until #703 lands: a Chat caller
  that sends tools needs a Responses target that declares the `openai-chat`
  inbound dialect; without that declaration the router refuses before dispatch.
  Calling the Responses surface directly also works.

### Release verification

The release gate (#712) ran on `0ea47c4ae10d4793a4afec56ad509e71634bac72`, with the four stages: agent tasks in six lanes plus a direct baseline, the Harbor matrix (five tasks, direct and routed in both API styles), the live SDK suites with a same-config A/B against v4.5.0, and the parallel tool-call gate. The candidate and v4.5.0 produced identical live-SDK failure sets on that one configuration.

This tagged commit differs from the gated commit only in the release notes and the release-gate scripts. Files changed between `0ea47c4a` and the tag commit:

```
CHANGELOG.md
docs-site/docs/release-notes/index.md
docs-site/docs/release-notes/upgrade-guide.md
scripts/release_gate.py
scripts/release_gate_test.py
```

`scripts/release_gate.py` and `scripts/release_gate_test.py` are the gate harness (#713, #715): the test-scope changes that restored the Chat-dialect lanes and let the live-SDK stage run with a validated-target config. Router code is identical: `git diff --stat 0ea47c4a <tag> -- internal cmd go.mod go.sum` is empty.

### Proof and Evidence

Tool-call compatibility is published with its limits: the offline fixture matrix
runs in required CI on every pull request and merge group, and the live gate
reports which provider and direction cells passed, which were not exercised and
what the run cost. The page `evaluation/parallel-tool-calls-live` is the
authoritative statement of that coverage; it does not claim comprehension of
tool results, and it names the translated directions that remain offline-only.

## v4.5.0 - 2026-11-04

### Highlights

- **Proof, with limits attached.** The router overhead benchmark, three savings
  case studies, expert routing, the escalation continuum, decision-model
  routing, agent tool-call compatibility and governance under load are published
  with their configurations, samples and limits, and each ships the command
  that reruns it. Results that came out negative — the coding-agent quality arm, the
  four-expert comparison, the escalation latency target — are published as
  negative.
- **OpenAI and Anthropic compatibility gates the merge.** The pinned official
  SDK testers run inside the required CI check on every pull request and merge
  group, the Go and TypeScript SDK suites run alongside as supplemental checks,
  and `GET /v1/models/{id}` serves caller-filtered model lookup.
- **Caller-visible changes:** chat streams no longer end with a usage-only
  chunk unless the caller sets `stream_options.include_usage`; errors now follow
  the caller's dialect, with an indistinguishable `404` for an unknown or
  disallowed model, `400` for a capability no target can serve, and `503` when a
  provider rejects the router's credentials; and endpoints the router does not
  serve answer `501 unsupported_endpoint` instead of a bare `405`.
- Release assets carry build-provenance attestations, Windows binaries are
  Authenticode-signed when signing secrets are provisioned, and the updated Go
  and Node dependencies close the reported advisories.

### Operator Impact

| Area | Change |
| --- | --- |
| Caller streams | Chat streams send no usage-only chunk unless `stream_options.include_usage` is true (#528); clients that consumed the trailing chunk must opt in |
| Router chains | Sanitized router-only `Via` with a hop limit (default 16) and HTTP 508 on a loop; intermediate proxies must preserve `Via` (#430) |
| Trusted upstreams | YAML providers may set `upstream_kind: router` to keep a 429 or typed 503 and `Retry-After`; database-backed config sets reject the field (#496) |
| Database | PostgreSQL-backed replicas learn configuration changes through LISTEN/NOTIFY, with the revision poller as the fallback (#443, #452) |
| Release artifacts | Provenance attestations on release assets; Windows signing is a logged no-op until signing secrets exist (#332) |
| Security | Tenant provisioner RBAC loses NetworkPolicy write access; re-apply `provisioner-rbac.yaml` when onboarding the next tenant (#414) |
| Error codes | A disallowed group no longer returns `403 model-not-allowed`: unknown and disallowed models both return `404 model_not_found`, disclosing nothing about which groups exist (#530) |
| Error codes | A capability or dialect no target can serve returns `400 unsupported_capability` with `param`; a terminal provider request error keeps its status as `upstream-rejected` instead of `502`; provider `401`/`402`/`403` returns `503 upstream-access-denied` (#530) |
| Unsupported surfaces | Endpoints outside v4.5 answer `501 unsupported_endpoint` in the caller's dialect, including the late-added OpenAI surfaces (videos, containers, conversations, agents, ChatKit threads, skills) that previously returned a bare `404`/`405`; automation matching `405` must accept `501` (#531, #580) |
| Database | Usage schema 11 (`2026103001`) adds `request_usage.endpoint`; apply it through the Required Migration Gate before serving, and restore a pre-upgrade database backup to downgrade to 4.4 (#608) |
| CI | Required CI fans out into six measured router shards, other Go and Windows, and compatibility, Python and example lanes behind one aggregate `ci` job; Go test wall time is guarded (#549, #569, #418) |

### Caller Impact

Routed model groups and usage accounting are unchanged. Client-visible
differences are the chat usage opt-in, the dialect-shaped error contract
(indistinguishable `404` for unknown or disallowed models, `400` for a
capability no target can serve, `503` when a provider rejects the router's
credentials, `501` for endpoints outside v4.5), and Anthropic model discovery
plus document input. Nothing in this release changes routing policy, model
groups or prices on its own.

### Proof and Evidence

Every number in this release links to the run that produced it. Start here:

| Proof | Where | Limit stated in the evidence |
| --- | --- | --- |
| Added latency, p50/p99 and streaming first content | `benchmarks/overhead/evidence/` | Shared hosts; CPU and memory per 1,000 requests/s, sustained throughput and edge results missing (#422 open) |
| Savings on three workloads, with scored quality | [Savings Case Studies](/docs/evaluation/savings-case-studies) | Three to five tasks per arm; one arm lost quality |
| Expert routing across four domains | `docs/evidence/expert-routing/` | Negative result on a fixed 88-case cohort; classifier and route accuracy differ |
| Escalation, local → GPU → frontier | [Escalation and decision evidence](/docs/evaluation/escalation-and-decision-evidence) | Illustrative latency target missed; hardware and energy excluded; invoices pending |
| Decision-model wiring and observation | [Escalation and decision evidence](/docs/evaluation/escalation-and-decision-evidence) | OpenJev excluded for licensing; edge results missing; no co-located added-latency claim |
| Agent tool calls and router chains | [Tool-call compatibility proof](/docs/evaluation/tool-call-proof), [API compatibility](/docs/reference/api-compatibility) | Deterministic mock cells, not live-provider certification |
| Governance under load | `docs/GOVERNANCE_LOAD.md`, `docs/GOVERNANCE_AUDIT_LOAD.md` | Bounded local probes, not the complete acceptance suite |

Paths are relative to the source distribution; the same files are linked from
the examples and benchmark READMEs in the repository.

### Upgrade

Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide). Three items need
attention before traffic moves: clients that consumed the chat usage-only chunk
must set `stream_options.include_usage`; automation that matched `405` for
endpoints outside v4.5 must accept `501`; and monitoring that treated provider
`401`/`402`/`403` as a caller authentication problem must expect
`503 upstream-access-denied` instead. Deployments that run router chains
behind another proxy must preserve `Via` for the loop protection to work, and
anyone using the tenant provisioner must re-apply its RBAC manifest.

v4.5.0 moves the usage database to schema 11 (`2026103001`, which adds
`request_usage.endpoint`) on both SQLite and PostgreSQL. Run the
[Upgrade Guide](/docs/release-notes/upgrade-guide) Required Migration Gate
before serving: the default `deployment-job` policy has the serving process
validate the ledger only, and `auto-safe` applies it at startup solely on a
reviewed single-node SQLite deployment. Existing rows cannot be classified
retroactively. **Downgrading to 4.4 requires restoring a pre-upgrade database
backup**; package rollback never runs a reverse migration.

### Validation

Check `/readyz`, `/version` and authenticated `/v1/models`, including
`GET /v1/models/{id}` with a caller-scoped token; a model the caller cannot use
must return the same 404 as one that does not exist, and the Anthropic catalog
must list only that caller's groups. Confirm a request the group cannot serve
returns `400` with a `param`, that a provider credential failure surfaces as
`503 upstream-access-denied`, and that an endpoint outside v4.5 returns `501`
in the caller's dialect. Confirm a chat stream
without `stream_options.include_usage` ends without a usage-only chunk and that
one with the opt-in carries usage. For compatibility, run the mock suites from
the source distribution
(`make api-compat-mock api-compat-ts agent-tool-mock agent-chain-mock`). These
are deployment checks, not a claim that a release artifact has passed final
launch validation. Confirm the usage schema ledger reports schema 11 applied and
compatible for the configured driver before serving.

### Security and Rollback

Keep the previous binary, reviewed configuration and restorable config and usage
database backups. Take a usage-database backup before upgrading and run the
[Upgrade Guide](/docs/release-notes/upgrade-guide) Required Migration Gate for the
schema level this build expects; package rollback never runs a reverse
migration. The PostgreSQL notifier change is additive and polling remains the
fallback. Windows artifacts are unsigned unless signing secrets are provisioned;
verify provenance attestations and `SHA256SUMS` before installing.

## v4.4.0 - 2026-10-27

### Highlights

- Swap the model behind a group with one API call: authenticated Admin API
  target swap with dry-run validation, optimistic concurrency, per-target
  eligibility reports and a full audit trail; one-call rollback restores the
  exact prior targets. Shadow a candidate against live traffic, read the
  cost/token/latency comparison and promote in one call. A `routerctl groups`
  CLI, Admin API docs and a repeatable live-upstream e2e suite cover the
  workflow end to end. Database-backed configuration mode is required for
  these writes; file mode keeps serving.
- The Admin API now has its own credential: dedicated admin API keys hashed at
  rest, rotatable without downtime, with fine-grained permissions. Caller and
  provider keys are rejected on admin routes.
- Security hardening across CI, authorization policy and example infrastructure:
  hardened release workflow input handling, exact admin authorization scope
  matching, least-privilege tenant provisioner RBAC, and refreshed scanner
  policy.
- Patched dependency updates across docs tooling, the learned routing policy
  service and test projects.

### Operator Impact

| Area | Change |
| --- | --- |
| Authorization | Policy objects match exactly; a grant for one admin object no longer covers another. Re-apply tenant provisioner RBAC manifests; review policies that relied on implicit cross-object grants |
| Admin API | Dedicated admin API keys required for `/admin/*` on new configurations; generate and rotate via `metrum-ai-routerctl adminkeys generate`. Group swap/shadow/config-set writes require database-backed configuration mode; file mode answers writes with 409 |
| Database | Admin config sets and the shadow run registry are phase-9 tables in the config database; apply the config migrations and keep the audit event chain |
| Tooling | Dependency updates in docs site, learned routing policy and test projects; Dependabot coverage now includes evaluator/test pip projects |
| CI | Release workflow inputs validated before use; test timeout headroom on shared-CPU nodes |

### Caller Impact

Model group target swaps, shadow runs and promotions are caller-transparent:
no restart, no client configuration change, and prior targets remain as
fallback after promotion. No change to caller-facing request or billing
semantics from the hardening items. Out-of-range Bedrock
`max_tokens`/`max_completion_tokens` values clamp instead of erroring or
wrapping.

### Upgrade

Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide). The
authorization matcher change is behavior-affecting for policies that relied on
one admin grant covering several admin objects; list each object explicitly.
Tenant provisioner RBAC manifests must be re-applied to upgrade existing
namespaces.

### Validation

Check `/readyz`, `/version` and authenticated `/v1/models`. Exercise admin
authorization with scoped grants (one admin object must not open another),
and for the tenant provisioner run the `kubectl auth can-i` verification matrix
from `docs/EKS_IDENTITY_BOOTSTRAP.md`. For the Admin API, verify caller and
provider keys are rejected on `/admin/*` and that key rotation does not
interrupt admin sessions.

These are deployment checks, not a claim that a release artifact has already
passed final launch validation. Dated evidence and the exact tested SHA identify
validation; this page's date does not identify the running build.

### Security and Rollback

Keep the previous binary, reviewed configuration and restorable config/usage
database backups: the Admin Config API adds config-database migrations (admin
config sets, shadow run registry, audit chain). The hardening changes alone
introduce no usage-database migration; rollback of the hardening items is
binary replacement plus re-applying the previous RBAC/authorization
configuration. Scanner policy exceptions now carry owners and expiry
conditions.

## v4.3.0 - 2026-10-24

### Highlights

- Governed training datasets from explicitly captured traces: outcome labels,
  privacy filters/redaction, deduplication, audited admin authorization,
  provenance manifests and frozen splits. Heldout failures remain in cohort;
  identical tool declarations normalize with manifest counts and conflicts
  reject rather than silently choosing a schema.
- Opt-in AWS Bedrock Converse/ConverseStream upstream with AWS default-chain
  credentials, model/profile IDs, tools, streaming and usage accounting for
  the validated capability matrix.
- Reported-versus-estimated usage, corrected Gemini thinking and Anthropic
  cache accounting, unbilled token counting, and reasoning/cache coverage
  across rollups, admin reports, CLI and Prometheus.
- Router-centric Qwen LoRA example with deterministic tools, key-scoped canary,
  NVIDIA/AMD evidence and conservative gates. **Run 1, run 2 and AMD all failed
  the preregistered gate and were not promoted; the router kept the teacher in
  place.** No API parity or quality-equivalent savings claim follows from
  training completion, throughput or quoted unit costs.

### Operator Impact

| Area | Change |
| --- | --- |
| Database | Usage schema v7–v10 and control-plane cache-write pricing migration; back up and apply the required migration gate before serving |
| Accounting | Historical provenance/coverage remains unknown; configure cache-write prices and review component/usage-source metric labels |
| Training | Capture and deployment-wide `content:training` grants are opt-in; durable audit, privacy/rights review and independent export retention required |
| Config | Empty legacy `modalities: []` means unset, not disabled; nonempty legacy metadata still overrides input metadata |
| Bedrock | Opt-in AWS credentials, IAM, region and model/profile capability review; no change to existing providers |
| CI | Ephemeral `router-arc` pods and Actions caches supersede active persistent-host workflows; strict parallel aggregate preserved, legacy host maintenance separate |

### Caller Impact

Native Anthropic `count_tokens` returns provider counts/errors; other transports
return marked estimates. Both are unbilled and outside inference quotas, with
separate visible count events. Usage preserves explicit zeros and stream totals;
missing stream output uses observed text rather than requested maximums.
Gemini thinking is included in output, not an extra billable token category.
Anthropic inclusive input includes cache reads/writes without double-counting.

### Upgrade

Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for exact migration
IDs, rollback backups, retained-history limits, pricing and metric changes.
Training export and Bedrock are optional, not automatic deployment changes.
Existing IR-only captures cannot be converted retroactively into canonical
chat/tool histories; old exports and finalized history are not silently rewritten.

### Validation

Check `/readyz`, `/version` and authenticated `/v1/models`, then exercise real
client tool/result continuations on enabled routes. Verify count calls do not
increase inference spend/quota; compare usage/report/metric components after
queued writes drain. For Bedrock, validate the exact region/profile and tool
capabilities. For training, test capture scope with an uncaptured control key,
admin authorization, privacy filtering and manifest validation before any export
leaves the operator boundary. Example batches now require readiness and
successful authenticated route/target/grader preflight; test callers have high
limits and deliberate limiter tests are separate.

These are deployment checks, not a claim that a release artifact has already
passed final launch validation. Dated evidence and the exact tested SHA identify
validation; this page's date does not identify the running build.

### Security and Rollback

Example localhost proxies reject CRLF/header injection in forwarded allowlisted
headers. Keep the previous binary, reviewed configuration and restorable
pre-upgrade databases: schema expansion rollback requires restoring the backup,
not merely replacing the binary. Do not promote the example adapters; use the
teacher and independently verify model-license and provider output-training
rights for any new data.

## v4.2.0 - 2026-10-21

### Highlights

- Claude Code client tools and continuations can translate to explicitly
  validated OpenAI Chat and Responses targets, including streaming and
  identifier rewriting. Unvalidated targets remain ineligible.
- Native Chat preserves sampling controls, all choices, refusals and logprobs;
  invalid `max_tokens` values return HTTP 400 before upstream.
- Asynchronous batched usage writes keep database locks off inference paths;
  SQLite and PostgreSQL are supported. Cache-hit billing is zero, with list
  estimates retained separately.
- Windows amd64/arm64 packages, d3 policy examples and measured evidence,
  dependency/security refreshes and nightly Go fuzz checks.

### Operator Impact

| Area | Change |
| --- | --- |
| Eligibility | `eligibility.implicit_openai_to_anthropic` defaults true now, false next minor; startup warnings and config patcher support migration |
| Anthropic | `allowed_betas` defaults empty; adaptive thinking requires reasoning support; translated tools need distinct validation evidence |
| Database | Usage schema v6 adds `cost_list_usd` / `cost_billed_usd`; follow migration runbook and back up first |
| Telemetry | Eventually consistent queued writes; monitor queue depth, drops and SQL latency; default shutdown drain 10 seconds |
| Local setup | Existing configs/callers preserved; fresh defaults use Flash 1017 70% / Luna 30% |
| Security | Salted caller verifiers, secret-safe diagnostics, scoped revoked-key attribution and dependency refresh |

### Caller Impact

- Native Chat retains `seed`, `n>1`, refusal and logprobs instead of silently
  normalizing them away. Present `max_tokens` must be an integer >= 1.
- Ineligible stored continuations return `400 continuation_unsupported`.
  Chat-to-Responses is stateless regardless of legacy session settings.
- Non-SSE upstream stream replies return `502 upstream_protocol_error`;
  SSE lines are bounded at 64 KiB and responses disable proxy buffering.
- SSE translation recognizes bare CR, LF and CRLF, including endings split
  across reads, without retaining or losing CR field values (#331, fixes #327).
- W2b (#329): explicit `modalities` and `structured_output`
  metadata gate media and Anthropic output formats; supported document/image
  parts survive streaming/tool bridges and JSON schemas translate to OpenAI.
  Unsupported media, translated unknown blocks and unsupported formats return
  explicit 400s. Anthropic server search and Responses hosted tools reject
  before upstream rather than silently dropping descriptors.

### Upgrade

Verify package checksums, back up config and usage data, and follow the
[Upgrade Guide](/docs/release-notes/upgrade-guide), including schema migration,
caller verifier conversion, eligibility review and beta allow-lists. Existing
local configuration is not regenerated automatically. Windows packages use ZIP;
Linux packages remain tarballs.

### Validation

For deployment validation, check `/readyz`, `/version` and authenticated
`/v1/models`. Exercise client-tool continuations on each enabled API skin,
native Chat sampling/multiple choices, media and output-format admission,
streaming first-event delivery and usage persistence after a bounded wait.
Monitor usage queue/drop metrics and verify inference remains responsive
under a usage-database write lock. The exact build metadata in `/version`
identifies the deployed artifact; CI and dated release evidence identify the
source SHA tested, not this documentation's date.

### Rollback

Keep the previous package, reviewed config and restorable database backup.
Older routers reject the new caller verifier field; use compatible verifiers
when rolling back.
Rehearse restore against the usage migration contract before the rollout.

## v4.1.0 - 2026-10-10

### Highlights

- Opt-in `server.responses.model_identity: requested_group` makes every
  caller-facing `model` field report the requested router group (for example
  `big-coder`) instead of the upstream model that served it, across Chat
  Completions, Responses and Anthropic Messages, unary and streaming. In that
  mode, upstream failure errors no longer name providers or upstream models.
  The default stays `upstream`, so existing deployments see no change.
- Release binaries are built with Go 1.26.9, which fixes standard-library
  vulnerabilities in the `net/http` HTTP/1 and HTTP/2 server and client,
  `net/textproto` and `crypto/tls` (GO-2026-6603 through GO-2026-6617).
- API compatibility fixes: verbatim passthrough for tool-less same-dialect
  Anthropic and Responses requests, the response cache key includes `n`,
  provider URLs no longer appear in network error details, images inside
  Anthropic `tool_result` blocks trigger image eligibility, and Codex
  continuations under `identifiers.mode: rewrite` decode replayed Responses
  item ids.
- Admin UI toolchain refresh (Vite 8, TypeScript 7, Tailwind CSS 4,
  React 19.3) with a rebuilt embedded admin dist.

### Operator Impact

| Area | Change |
| --- | --- |
| Config | New optional `server.responses.model_identity` (`upstream` default, or `requested_group`) |
| Errors | Under `requested_group`, `error.details.targets` becomes `{attempt, error_class}` and `last_error` is redacted |
| Usage and reports | Unchanged: usage rows, attempts, logs and admin reports keep upstream provider and model in both modes |
| Database | No usage-database migration |
| Packages | GitHub Release publishes binary packages, `SHA256SUMS` and `release-artifacts.json` only; Docker packages are built from a checkout with `make package-docker` |
| Runtime | Built with Go 1.26.9 |

### Caller Impact

- With the default `model_identity: upstream`, request and response shapes are
  unchanged from v4.0.2 except for the passthrough fidelity fixes above (for
  example, tool-less same-dialect Responses calls now return the upstream
  `resp_` id, so `previous_response_id` continuation works).
- With `requested_group`, callers see the group name in `model` and redacted
  upstream error details.

### Known limitation: Claude Code tools on translated targets

In v4.1.0, Anthropic Messages text can be translated to a validated OpenAI
Chat or Responses target, but Anthropic client-tool requests cannot use those
translated targets. If a group has no eligible native Anthropic Messages tool
target, a Claude Code tool request returns `502 no-eligible-target` before an
upstream attempt. A successful Claude text request does not prove that Claude
Code can call a tool and continue after its result. Adding
`tool_support.anthropic_messages` metadata to an OpenAI target does not enable
the missing tool round trip.

This was confirmed on the v4.1.0 Linux AMD64 release binary in a `big-coder`
deployment with OpenAI `gpt-6-luna` (Responses, also serving Chat through the
Chat-to-Responses bridge) and Fireworks
`accounts/fireworks/models/deepseek-v4p1-flash` (Chat only). Direct upstream
text, router Chat text and tools, router Responses and Anthropic text, and a
Codex CLI Responses file write/read continuation passed. The Anthropic tool
request returned `502 no-eligible-target`. This is an API-surface limitation,
not evidence of a provider credential failure or a general lack of model tool
support.

For Claude Code tool workflows, choose a separately validated Anthropic
Messages tool-capable route. Cross-dialect tool support requires its own
implementation and Claude Code continuation validation; track that work in
[#295](https://github.com/metrum-ai/router/issues/295).

**After v4.1.0:** the client-tool bridge is implemented for explicitly validated
Chat and Responses targets. See [Claude Code translated client tools](/docs/getting-started/claude-code-cli)
for the distinct per-target capability requirements and continuation validation.
The limitation above remains applicable to the v4.1.0 release binary.

### Upgrade

1. Download `metrum-ai-router-v4.1.0-linux-<arch>.tar.gz` from the GitHub
   Release and verify it against `SHA256SUMS` / `release-artifacts.json`. For
   Docker, build the package from the `v4.1.0` tag with `make package-docker`.
2. Replace the package; keep existing config. Optionally set
   `server.responses.model_identity: requested_group`.
3. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide).

### Validation

- `/readyz` and `/version` report v4.1.0, and `/version` reports `go1.26.9`
- One authenticated `/v1/models` call succeeds
- If `requested_group` is enabled: one Chat request reports the group name in
  `model`, and a tool-using client continuation completes

### Rollback

Roll back to GitHub Release **v4.0.2**. No usage-database restore is required.
v4.0.2 ignores `server.responses.model_identity`, so callers see upstream
model names again after rollback; remove the key to keep config tidy.

## v4.0.2 - 2026-10-01

### Highlights

- Rewrite-mode routers no longer crash under concurrent streaming tool-call
  id rewrite: abandoned `secure-io/siv-go` is replaced with in-tree pure-Go
  AES-SIV-CMAC (`internal/aessiv`). Existing `mr_` tokens stay valid (same
  RFC 5297 wire format). No config change.

### Operator Impact

| Area | Change |
| --- | --- |
| Identifiers | Concurrent SSE tool-id rewrite no longer SIGSEGV under rewrite mode |
| Config | Unchanged |
| Database | No usage-database migration |
| Packages | Same binary and Docker package layout as v4.0.1 |

### Caller Impact

- Request and response shapes are unchanged from v4.0.1.
- Prior `mr_` tool-call ids continue to decode.

### Upgrade

1. Download `metrum-ai-router-v4.0.2-linux-<arch>.tar.gz` (and the Docker
   package if used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Replace the package or image; keep existing config.
3. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide).

### Validation

- `/readyz` and `/version` report v4.0.2
- One authenticated `/v1/models` call succeeds
- If rewrite mode is enabled: concurrent Chat / Anthropic tool streams
  complete without router process exit; prior `mr_` tool-call ids still decode

### Rollback

Roll back to GitHub Release **v4.0.0** (v4.0.1 was tagged but never published
as a GitHub Release). No usage-database restore is required for file-mode
installs.

## v4.0.1 - 2026-10-01

### Highlights

- Tool-call identifier rewrite round-trips again: under
  `identifiers.mode: rewrite`, OpenAI Chat `tool_calls[].id` and Anthropic
  `tool_use` ids decode back to `mr_` values on ingress so multi-turn tool
  continuations keep matching.
- Optional database config source (`server.config_source.mode: database`)
  stores canonical YAML in the control plane with import/export/activate/
  rollback and caller issue/rotate/revoke against the active set. Default
  file-mode installs are unchanged.
- Docs include an OpenJev task-class external routing example. OpenJev is excluded from the v4.5 comparison for licensing; its prior result artifacts were removed.

### Operator Impact

| Area | Change |
| --- | --- |
| Identifiers | Rewrite-mode tool continuations no longer drop protocol tool ids |
| Config source | Optional `server.config_source.mode: database`; omit or keep file mode for existing installs |
| Database | No usage-database migration required for file-mode upgrades |
| Packages | Same binary and Docker package layout as v4.0.0 |

### Caller Impact

- Clients that send tool results referencing rewritten tool-call ids under
  rewrite mode succeed again.
- Request and response shapes are otherwise unchanged from v4.0.0.

### Upgrade

1. Download `metrum-ai-router-v4.0.1-linux-<arch>.tar.gz` (and the Docker
   package if used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Replace the package or image; keep existing file-mode config unless you
   intentionally enable `server.config_source.mode: database`.
3. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide).

### Validation

- `/readyz` and `/version` report v4.0.1
- One authenticated `/v1/models` call succeeds
- If rewrite mode is enabled: a Chat tool-call continuation that echoes the
  model-issued tool-call id completes without a tool-id mismatch

### Rollback

Roll back to GitHub Release **v4.0.0**. No usage-database restore is required
for file-mode installs. If you activated a database config set in v4.0.1,
restore the previous package and file config (or re-activate the prior DB set
only if you intentionally keep database mode).

## v4.0.0 - 2026-09-21

### Highlights

- The Fleet lifecycle subsystem is removed: `metrum-ai-router-fleetctl`,
  `metrum-ai-router-fleet-sign`, rename stubs, and `internal/fleet`.
  Self-managed installs use binary, Docker Compose, or Kubernetes manifests
  with `metrum-ai-routerctl`.
- `server.license` is a startup error. Remove the block before upgrade.
  `license.json` is still never read.

### Operator Impact

| Area | Change |
| --- | --- |
| Fleet | Fleet CLIs, Fleet signing, and Fleet deploy/plan/status are removed |
| Config | `server.license` fails startup; delete the block |
| License files | Existing `license.json` files remain inert and are not mounted by current manifests |
| Packages | No Fleet binaries are packaged |
| Database | No usage-database migration |

### Upgrade

1. Download `metrum-ai-router-v4.0.0-linux-<arch>.tar.gz` (and the Docker
   package if used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Delete `server.license` from router config and drop license file mounts.
3. Remove automation that calls `metrum-ai-router-fleetctl` or
   `metrum-ai-router-fleet-sign`.
4. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide).

### Validation

- `/readyz` and `/version` report v4.0.0
- Startup fails if `server.license` is still present, and succeeds after it is removed
- One authenticated `/v1/models` call succeeds without a license file

### Rollback

Roll back to GitHub Release **v3.0.0**. No usage-database restore is required.
v3.0.0 still ignores `server.license` with one startup warning and still
includes Fleet plan/deploy/status.

## v3.0.0 - 2026-09-19

### Highlights

- Runtime licensing is removed. The router no longer reads `license.json`,
  and request handling is not gated on a license.
- `server.license` is accepted and ignored, with one startup warning, through
  3.0.0. It will be rejected in 4.0.0.
- Packaged license binaries and license inventory commands are gone.
  `metrum-ai-router-fleetctl` plan/deploy/status and
  `metrum-ai-router-fleet-sign` deployment-intent signing remain.
- Learned Routing Policy records per-phase sidecar latency histograms
  (receive, tokenize, embed, featurize, predict, select, respond) without
  logging prompt text (#216).

### Operator Impact

| Area | Change |
| --- | --- |
| Config | `server.license` is ignored with one startup warning; remove it before 4.0.0 |
| License files | Existing `license.json` files are inert and are never read |
| Packages | `metrum-ai-router-license` and `metrum-ai-router-customer-lifecycle` are no longer packaged. `metrum-ai-routerctl license` and `metrum-ai-router-fleetctl licenses` are removed |
| Fleet | Plan, deploy, status, and deployment-intent signing are unchanged |
| Metrics | License Prometheus series and license fields on `/version`, readiness, and diagnostics are removed |
| Database | No usage-database migration (package-only rollback) |
| LRP | Opt-in sidecar exposes phase latency histograms. This release does not publish Harbor timings |

### Caller Impact

- Requests are no longer rejected with `license-*` error codes.
- `/version` and readiness metadata no longer include license fields.
- Identifier rewrite, streaming translators, and model routing behavior are
  unchanged from v2.2.0.

### Upgrade

1. Download `metrum-ai-router-v3.0.0-linux-<arch>.tar.gz` (and the Docker
   package if used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Remove automation that calls `metrum-ai-router-license`,
   `metrum-ai-router-customer-lifecycle`, `metrum-ai-routerctl license`, or
   `metrum-ai-router-fleetctl licenses`.
3. Drop license mounts and license alert rules that depend on the removed
   Prometheus series. Keep `server.license` only if you still need the 3.0.0
   ignore-and-warn behavior.
4. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide).

### Validation

- `/readyz` and `/version` report v3.0.0 and do not include license fields
- One authenticated `/v1/models` call succeeds without a license file
- Startup logs show a single warning if `server.license` is still set, and
  no warning after it is removed
- If LRP is enabled, phase latency histograms are present and contain no
  prompt text

### Rollback

Roll back to GitHub Release **v2.2.0**. No usage-database restore is required.
Licensing returns only with the v2.2.0 package; keep the previous license file
if you may need that rollback.

## v2.2.0 - 2026-09-19

### Highlights

- Caller-facing identifiers can be rewritten with AES-SIV so upstream IDs are
  not exposed on the wire, including field-level rewrite on native SSE (#205).
- Incremental SSE translation for same-dialect Responses and for Chat ↔
  Responses bridges (#206–#208).
- Incremental Anthropic ↔ OpenAI Chat/Responses text and tool streaming (#209);
  reasoning remains on native Anthropic routes.
- LRP synthetic CI no longer requires host AppArmor profile loads, so privileged
  Docker self-hosted runners can complete sandbox verification (#210).

### Operator Impact

| Area | Change |
| --- | --- |
| Config | Optional `server.identifiers` (`rewrite` / `passthrough`) and transform key material; default streaming translator remains `incremental` |
| Streaming | Bridge paths emit incremental SSE when eligible; `server.streaming.translator: synthesized` keeps unary upstream behavior |
| Database | No new usage migration in this release |
| Packages | Canonical `metrum-ai-router*` binaries unchanged from v2.1.0 naming |
| CI / runners | Self-hosted Docker runner pools that run LRP synthetic need privileged containers for bubblewrap; AppArmor host profiles are not required |

### Caller Impact

- Streaming Chat ↔ Responses and Anthropic ↔ OpenAI text/tool bridges return
  incremental SSE instead of failing closed when the bridge is enabled and
  `server.streaming.translator` is `incremental`.
- When identifier rewrite is enabled, caller-visible IDs are transformed;
  `passthrough` preserves prior exposure behavior for lab/mock configs.
- Reasoning workloads should continue to use native Anthropic routes; these
  bridges do not synthesize thinking blocks.

### Upgrade

1. Download `metrum-ai-router-v2.2.0-linux-<arch>.tar.gz` (and Docker package if
   used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Review `server.identifiers` and `server.streaming.translator` before enabling
   rewrite or changing translator mode in production.
3. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for Compose /
   Kubernetes procedures.

### Validation

- `make api-compat-mock-offline`
- `make sse-capture` (or package SSE harness checks from source)
- `make test` and `make lrp-test` when exercising LRP packages from source
- After deploy: `/readyz`, `/version` reports v2.2.0; smoke streaming bridges
  and confirm identifier rewrite or passthrough matches the reviewed config

### Rollback

Roll back to GitHub Release **v2.1.0** (`metrum-ai-router-*` artifacts). No
usage-database restore is required for this release. Revert identifier and
streaming config knobs with the prior package if rewrite or incremental bridges
were enabled.

## v2.1.0 - 2026-09-14

### Highlights

- Usage evidence can persist optional cached-input token counts and
  `cached_input_price_per_million_usd`, and can partition input cost when both
  cache evidence and a known cached-input price are present (#160).
- Learned Routing Policy gains offline seed-corpus tooling (#157), a
  multi-provider coding portfolio (OpenRouter Qwen + MiniMax, Fireworks kimi,
  Baseten GLM), a routing-benchmark harness (#159), and pluggable embedder
  backends with explicit CUDA/CPU device selection (#156 A/C).
- LRP serving/eval/signed-bundle gaps from #158 are closed. Public seed and
  benchmark evidence scalars are checked in under
  `docs/evidence/learned-routing-policy/`.

### Operator Impact

| Area | Change |
| --- | --- |
| Config | Optional `cached_input_price_per_million_usd` on catalog/target pricing |
| Database | Usage migration `2026091301` (schema version 5); `restore-required` rollback |
| LRP | Opt-in only; new seed/benchmark/embedder tooling does not auto-enable enforce |
| Packages | Canonical `metrum-ai-router*` binaries unchanged from v2.0.0 naming |

### Upgrade

1. Download `metrum-ai-router-v2.1.0-linux-<arch>.tar.gz` (and Docker package if
   used) from the GitHub Release; verify against `SHA256SUMS` /
   `release-artifacts.json`.
2. Apply usage migration `2026091301` per
   [DATA_MIGRATIONS.md](https://github.com/metrum-ai/router/blob/main/docs/DATA_MIGRATIONS.md)
   with an approved pre-migration backup (restore-required).
3. If using LRP, sync the locked uv project, refresh target pricing descriptors,
   and keep router `external_policy.mode` in `shadow` until workload gates pass.
4. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for Compose /
   Kubernetes procedures.

### Validation

- `python3 scripts/validate_package_contents_test.py`
- `python3 scripts/release_artifact_inventory_test.py`
- `make test` and `make lrp-test` when exercising LRP packages from source
- After deploy: `/readyz`, `/version` reports v2.1.0; confirm nullable cached
  input columns on new usage rows when providers report cache reads

### Rollback

Roll back to GitHub Release **v2.0.0** (`metrum-ai-router-*` artifacts). Because
`2026091301` is `restore-required`, restore the approved pre-migration usage DB
snapshot before deploying the earlier package. Disable new LRP knobs or revert
to the prior LRP bundle if enforce was enabled.

## v2.0.0 - 2026-09-12

### Highlights

- Breaking packaging rename to the canonical technical slug
  **`metrum-ai-router`**. Packages and the runtime image ship **canonical
  binaries only**; rename stub binaries are not packaged.
- Release archives, Docker image tags, and GitHub Release artifact names use
  `metrum-ai-router-*` / `metrum-ai-router:<tag>`.
- Go module path remains `github.com/metrum-ai/router`.

### Operator Impact

| Area | Change |
| --- | --- |
| Runtime binary | `metrum-router` → `metrum-ai-router` |
| Token / usage / migrate CLIs | `metrum-router-*` → `metrum-ai-router-*` |
| Customer CLI | `metrum-routerctl` → `metrum-ai-routerctl` |
| Fleet / license / lifecycle CLIs | `metrum-genai-smartrouter-*` / `metrum-genai-customer-lifecycle` → `metrum-ai-router-fleetctl`, `metrum-ai-router-fleet-sign`, `metrum-ai-router-license`, `metrum-ai-router-customer-lifecycle` |
| Packaged stubs | Removed (`router`, `smartrouterctl`, `metrum-fleetctl`, …) |
| Archives / image | `metrum-router-*` → `metrum-ai-router-*` |
| Client `model_provider` examples | `metrum-router` → `metrum-ai-router` |
| Docs URL | Still `https://llm-api.apps.metrum.ai/docs` until docs.metrum.ai is hosted |

### Upgrade

1. Download `metrum-ai-router-v2.0.0-linux-<arch>.tar.gz` (and Docker package if used)
   from the GitHub Release; verify against `SHA256SUMS` / `release-artifacts.json`.
2. Update service unit, Compose image name, Kubernetes image references, and PATH
   installs to the new binary names. Do not rely on packaged rename stubs.
3. Update client configs that set `model_provider="metrum-router"` to
   `metrum-ai-router`.
4. Follow the [Upgrade Guide](/docs/release-notes/upgrade-guide) for migration
   gate and Compose/Kubernetes procedures.

### Validation

- `python3 scripts/validate_package_contents_test.py`
- `python3 scripts/release_artifact_inventory_test.py`
- `python3 scripts/canonical_product_test.py`
- After deploy: `/readyz`, `/version` reports v2.0.0, binaries on PATH are
  `metrum-ai-router*`, `/docs/` names Metrum AI Router

### Rollback

Roll back to GitHub Release **v1.4.4** (`metrum-router-*` artifacts and image
tags) with the prior service unit / Compose / Kubernetes references. Restore
config and usage DB backups taken before the upgrade if any migration ran.

## v1.4.4 - 2026-09-11

Packaged republish of the canonical **Metrum AI Router** naming commit as
GitHub Release `v1.4.4`. Prefer these release assets for deploy. The `v1.4.3`
tag/ref was locked by repository immutable-release rules after the initial
asset-upload failure; product content matches the v1.4.3 notes below.

## v1.4.3 - 2026-09-11

### Highlights

- Public docs, site title, `/docs/llms.txt`, and the branding lint now use
  **Metrum AI Router** as the only current product name. "LLM smart router"
  remains the category description, not a second product title.
- Example configs and admin-auth docs align the Basic-auth realm example with
  the runtime default (`Metrum AI Router Admin`).

### Operator Impact

- No runtime config schema, strategy name, metric, CLI flag, or binary rename.
- Technical identifiers (`metrum-router`, `smartrouterctl`, Helm chart IDs,
  Kubernetes labels) are unchanged.
- Admin Basic-auth realm default was already `"Metrum AI Router Admin"`.

### Upgrade

Standard image upgrade. No configuration migration is required for this release.

### Validation

- `make docs-qa && make docs-build`
- `make test`
- After deploy: `/readyz`, `/version` reports v1.4.3, `/docs/` and
  `/docs/llms.txt` name Metrum AI Router

### Rollback

Redeploy the previous package/image (`v1.4.2`). No database or config rollback
is required for this documentation/branding release.

## v1.4.2 - 2026-09-11

Packaged republish of the merged smart-router positioning commit as GitHub Release
`v1.4.2`. Prefer these release assets for deploy when remaining on that tag. The
`v1.4.1` tag/ref was locked by repository immutable-release rules after the
initial asset-upload failure; product content matches the v1.4.1 notes below.

## v1.4.1 - 2026-09-11

### Highlights

- Public positioning leads with Metrum AI Router as an open-source LLM smart
  router. Gateway functions remain available underneath routing rather than as
  the leading product classifier.
- Learned Routing Policy is linked from the public routing strategy table and
  decision tree, with checked-in synthetic holdout figures labeled as
  non-promotable wiring evidence.
- Offline `make proof-routing` proves one `dynamic_score` group can select
  different upstreams for trivial vs complex fixtures via request-evidence.
- Docs embed `/docs/llms.txt` for machine-readable product classification.

### Operator Impact

- No runtime config schema, strategy name, metric, CLI flag, or binary rename.
- Package docs allowlist is unchanged; `llms.txt` ships in the embedded
  Docusaurus tree.

### Upgrade

Standard image upgrade. No configuration migration is required for this release.

### Validation

- `make docs-qa && make docs-build`
- `make proof-routing`
- `make test`
- After deploy: `/readyz`, `/version`, `/docs/`, `/docs/llms.txt`

### Rollback

Redeploy the previous package/image. No database or config rollback is required
for this documentation/positioning release.

## v1.2.0 - 2026-09-09

### Highlights

- Learned Routing Policy follow-ups (#21–#37) ship as opt-in LRP and external
  policy enhancements. Defaults preserve v1.1.0 cheapest-above-floor behavior.
  No live routing activation is authorized by installing this release.
- External policy context adds a pseudonymous `conversationKey`, opt-in
  post-completion feedback callbacks, and governed verifier-hint metadata for
  Chat, Responses, and Anthropic (#21–#23).
- LRP adds operator-signed Ed25519 bundles, read-only usage-DB explore import,
  near-duplicate evaluation splits, serving-provider variance reports, SQL and
  allowlisted plugin verifiers, and protected human-judge audit sampling
  (#29, #31–#34, #37).
- Selection constraints cover upstream latency gates, per-project quality
  floors, evidence-based prompt-cache cost estimates, and bounded explanation
  labels (#26, #27, #35, #36).
- Uncertainty abstention, Thompson sampling, PSI/embedding drift
  recommendations, and evidence-backed cold-start exploration are available
  behind explicit LRP group knobs (#24, #25, #28, #30).

### Operator Impact

- Review external-policy docs for conversation key, feedback URL, and
  verifier-hint configuration. Feedback never sends prompts, tools, or
  credentials.
- LRP operators can enable signed-bundle trust stores, usage import, eval
  split options, selection constraints, and uncertainty/explore knobs through
  LRP YAML and CLI. Cryptography for signing pins to a patched 50.x release.
- Synthetic wiring and local e2e evidence passed for these surfaces. They do
  not establish provider-backed quality, production latency, or live deployment
  readiness. Keep new knobs off until deployment-owned validation completes.
- No usage schema migration is introduced by this release beyond contracts
  already documented for v1.1.0.

### Caller Impact

- Ordinary Chat, Responses, and Anthropic callers continue to send model-group
  requests. New conversation-key and verifier-hint fields are operator-gated
  metadata; callers do not choose LRP internals.
- Class labels may use bounded abbreviations such as `lrp:caf:q0.90:c2`. Coarse
  Prometheus labels stay low-cardinality. Rich explanations remain on
  authenticated LRP `/explain`.

### Validation

- `/readyz` and `/version`: confirm `v1.2.0` and the package build timestamp.
- Browser docs: confirm the v1.2.0 release notes and LRP follow-up pages.
- `make lrp-test` and `make lrp-e2e` when exercising LRP packages from the
  source archive.
- Completion smoke for any deployment that enables new external-policy or LRP
  knobs; keep synthetic wiring separate from provider-backed evidence.

### Rollback

- Restore the previous router package and previous reviewed config/license.
- Disable new LRP and external-policy knobs or revert to the prior LRP bundle.
- No reverse migration is required for this release’s LRP follow-ups.

## v1.1.0 - 2026-09-09

### Highlights

- Release binaries use Go 1.26.8, including standard-library security fixes
  identified by the package vulnerability scan.
- Learned Routing Policy (LRP) adds a separate Python/uv service that trains
  calibrated quality and output-token models and selects the cheapest eligible
  target predicted to meet a workload's quality floor. Training, evaluation,
  bundle validation, authenticated inference and explanation are documented in
  the [worked case study](/docs/evaluation/learned-routing-case-study).
- The LRP deadline defaults to 200 ms and accepts 1–4,500 ms through YAML or
  `lrp serve --deadline-ms`; allow additional headroom in the router timeout.
- Same-dialect OpenAI Chat and Anthropic Messages requests proxy native
  upstream SSE, including tool and usage events. Responses and cross-dialect
  streaming remain router-encoded after a unary upstream call.
- `dynamic_score` adds caller-isolated, process-local conversation affinity
  with bounded fixed-TTL storage.
- The in-process TypeScript routing strategy is removed in v5.0.0;
  external policy adds explicit `baseline`, `shadow`, and `enforce` modes with
  reusable cancellation-aware HTTP clients.
- The outbound Gemini `generateContent` codec supports evidence-gated unary
  text targets. It is not a caller endpoint and does not enable tools, images,
  reasoning, structured output, or streaming.
- Targets may declare a bounded diagnostic `region` label. It records the
  actual serving target but does not enforce or prove processing location.
- Fleet lifecycle implementation is isolated from the serving request-path
  package, and package validation excludes Fleet binaries from runtime images.
- A second preregistered fixed-model OCR outcome gate demonstrates
  scalar-only quality, latency, cost, and regression decisions.

### Operator Impact

- LRP is opt-in trusted deployment infrastructure using `strategy: external`.
  Install its locked uv project from the v1.1.0 source archive, following the
  [LRP guide](/docs/routing/learned-routing-policy). Router binary and Docker
  packages do not start a policy service or contain trained bundles. No provider
  or model group is activated by installing this release.
- Synthetic training and routing evidence passed quality checks, but the
  controlled example's cost delta versus its anchor was only 0.685% and
  **failed** the cost promotion gate — that percentage is a failed-gate
  benchmark artifact, not a product or launch savings claim. Real BGE tests
  failed the embedding/full-feature budgets at 512 tokens on the shared test
  host. Default one-thread median full-feature latency exceeded 200 ms; size
  deadlines for the intended workload and expect fallback while timed-out
  inference retains its bounded worker slot. These results do not establish
  production quality, concurrency capacity, HTTP/router latency, or expected
  customer savings.
- Config: review `dynamic_score.affinity`,
  `external_policy.mode`, and optional `targets[].region`. New external
  policies should begin in `shadow`; `baseline` skips policy calls.
- Database: migration `2026090901` adds non-null text
  `request_usage.target_region` with an empty default.
- Streaming: reverse proxies must not buffer SSE. After the first native event,
  a later failure cannot change the committed `200`, append an error envelope,
  or fall back.
- Security: `allow_private_image_urls: true` bypasses all router image-URL
  admission and therefore requires independent egress controls.
- Script safety: concurrency limits do not preempt unbounded JavaScript after
  a VM starts.

### Caller Impact

- Native Chat/Messages streams can deliver lower time-to-first-event and
  preserve upstream event shapes. Clients must treat a missing terminal event
  as incomplete.
- Native PII-filtered streams preserve placeholders; buffered responses and
  router-generated Responses/bridge streams can restore them.
- Existing model-group authorization and request-shape eligibility remain
  authoritative. Affinity and external policy cannot widen access.

### Validation

- LRP validation records 79 Python tests with zero skips, 11 real-binary/HTTP
  synthetic checks and 526 Go tests. Sanitized training, inference and benchmark
  evidence is linked from the case study. Before enabling enforcement, run real
  workload quality/cost gates and staging shadow validation for your targets.
- Run the documented native Chat and Messages text/tool/usage/cancellation
  smokes, plus Responses and bridge regressions.
- Exercise affinity hit, miss, expiry, ineligible replacement, caller
  isolation, restart, and multi-replica behavior.
- Load-test external-policy
  `shadow` recommendation with the served baseline before promotion.
- Plan, apply, and verify migration `2026090901`; confirm serving-target region
  attribution after successful fallback.
- Keep Gemini targets catalog-only unless the exact provider/model/account has
  direct and restricted router text evidence.

### Rollback

- Disable affinity or restore the previous strategy; set external policy to
  `baseline`; restore the prior script/cap; remove Gemini or region-bearing
  targets from active groups; and restore the prior package/config.
- Native-stream rollback requires the prior package. Follow the migration
  contract before downgrading; restore the approved pre-migration database
  when the contract requires it.

## v1.0.2 - 2026-09-08

Metrum AI Router v1.0.2 is a documentation packaging release. Caller-facing
routing, model groups, and API behavior are unchanged from v1.0.1.

### Highlights

- Embedded Docusaurus docs now link the navbar GitHub control and Source License
  footer entry to the public Apache-2.0 home at
  `https://github.com/metrum-ai/router`.
- The legal software-licenses page uses the same public repository LICENSE URL.

### Operator Impact

- Config: unchanged.
- Database: no schema or data migration is introduced by this release.
- License: unchanged.
- Metrics and reports: unchanged.

### Caller Impact

- API behavior: unchanged.
- Model groups: unchanged.
- Errors: unchanged.
- Docs: `/docs/` GitHub and LICENSE links point at the public repository.

### Validation

- `/readyz` and `/version`: confirm `v1.0.2` and the new build timestamp.
- `/docs/`: confirm navbar and footer GitHub hrefs use
  `github.com/metrum-ai/router` and do not use
  `sysadmin-metrum-ai`.
- Completion smoke: run one request for an actively used model group.

### Rollback

- Restore the previous router package (`v1.0.1`) and the previous reviewed
  config.
- No reverse migration is required; preserve the usage database.

## v1.0.1 - 2026-09-08

Metrum AI Router v1.0.1 is a maintenance release focused on admin browser
report availability. Caller-facing routing, model groups, and API behavior are
unchanged from v1.0.0.

### Highlights

- Deployments that require admin browser reports can now declare the
  reverse-proxy networks in front of the router. A deployment whose
  `server.admin_auth.basic.trusted_proxy_cidrs` does not cover those networks is
  rejected before rollout instead of serving an admin surface that refuses every
  correct credential.
- Admin authentication documentation explains the failure mode where Basic Auth
  returns `401` for a valid password because the forwarded-HTTPS check runs
  before the password comparison.

### Operator Impact

- Config: when `allow_insecure_http` is `false`, confirm that
  `server.admin_auth.basic.trusted_proxy_cidrs` contains the network your reverse
  proxy or ingress controller connects from. In Kubernetes this is the cluster
  pod network, which usually differs from a local kind or Docker bridge range.
- Database: no schema or data migration is introduced by this release.
- License: unchanged.
- Metrics and reports: `/metrics` isolation and admin report authorization are
  unchanged.

### Caller Impact

- API behavior: unchanged.
- Model groups: unchanged.
- Errors: unchanged.

### Validation

- `/readyz` and `/version`: confirm the new router version and build timestamp.
- Admin reports: an unauthenticated request returns `401` with a Basic challenge,
  an incorrect password returns `401`, and an authorized request loads the report
  shell and a SQL-backed summary.
- Completion smoke: run one request per actively used model group.

### Rollback

- Restore the previous router package and the previous reviewed config.
- No reverse migration is required; preserve the usage database.

## v1.0.0 - 2026-09-07

Metrum AI Router v1.0.0 is the first public open-source release package.
Use the docs banner, `/docs/releases`, and `/version` for the exact router
version and build timestamp of the package that embeds this page.

### Highlights

- The package embeds documentation for its own runtime build and exposes the
  same version and build timestamp through the docs banner and `/version`.
- Caller-facing routing supports deployment-defined model groups across the
  configured OpenAI Chat, OpenAI Responses, and Anthropic Messages surfaces.
- Capability and request-shape eligibility keep tools, images, reasoning,
  structured outputs, output caps, bridges, and large payloads on targets
  validated for those exact surfaces.
- Usage, diagnostics, cost, latency, attempt, fallback, traffic-shaping, and
  governed admin-report surfaces use safe scalar operational evidence.
- Operations: `metrum-genai-smartrouterctl` is available for customer-local safe config,
  token-file, license, model, and aggregate-usage operations. Fleet lifecycle
  authority moved to binary-package-only `metrum-genai-smartrouter-fleetctl`; the old
  `metrum-fleetctl` / `metrum-smartrouterctl` / `smartrouterctl` names are
  one-release rename notices.
- Self-hosted serving: validated NVIDIA and AMD Instinct reference paths cover
  private in-cluster OpenAI-compatible model servers while keeping the router
  workload GPU-free.

### Operator Impact

- Config: compare the packaged `config.example.yaml` with the reviewed runtime
  config. Do not copy sample provider/model routes directly into production.
- Database: follow the package's migration policy and release-specific
  deployment record. The current migration contract is: `2026071901` creates
  schema version 1/data version 0 through an online explicit baseline;
  `2026072301` advances schema version 2/data version 0 and is
  `restore-required`; `2026080501` advances data version 1 and is also
  `restore-required` and requires its restart-safe
  `historical-usage-validation-v1` job to reach `validated` before service
  startup. Run the non-serving deployment-job gate and retain the approved
  pre-migration backup when required.
- License: verify the installed license permits the enabled features and
  deployment shape.
- Credentials: preserve the protected provider environment file or
  secret-manager state; never place provider keys in release evidence.
- Metrics and reports: retain `/metrics` isolation for metrics-admin subjects
  and verify report authorization after upgrade.
- CLI packaging: confirm customer Docker images include `metrum-genai-smartrouterctl` and
  exclude `metrum-genai-smartrouter-fleetctl`. Fleet RDS mutation remains fail-closed pending
  recorded non-production evidence and a qualified reviewer's approval, which a
  single-maintainer deployment may supply itself. Managed production profile
  authority is operator-owned and is not granted by the customer package.

### Caller Impact

- API behavior: validate every caller API skin and client workflow affected by
  the package or config change.
- Model groups: callers must discover their allowed deployment-defined groups
  through authenticated `/v1/models`.
- Errors: preserve structured caller-facing error types and request IDs; use
  sanitized attempt and diagnostic rows for root-cause analysis.
- Client compatibility: run the actual Codex and Claude Code CLIs when routing,
  tools, images, auth, or model metadata changed.

### Compatibility and Known Limitations

- Model groups are deployment-defined; callers discover their allowed groups
  through authenticated `/v1/models` rather than relying on fixed names.
- Capability eligibility is request-shape-specific. A target is used only for
  dialects, tools, modalities, bridges, and payload sizes validated for it.
- AMD and NVIDIA local-serving support depends on the exact operator, driver,
  serving image, model, parser, hardware, and request shape documented by the
  deployment's validation evidence.
- The docs build retains an unpatched build-time image metadata dependency risk.
  It is not executable in the published browser bundle. The v1.0.0 launch
  decision accepts this risk through 2026-10-04 with reviewed documentation
  inputs, bounded build jobs, and static generated docs as the serving boundary;
  the accountable owner must recheck or remediate it by that date.

### Validation

- Confirm the docs banner, `/docs/releases`, and `/version` agree on the
  expected version and build timestamp for the deployed package.
- Run `/readyz`.
- Run authenticated `/v1/models` with each affected caller class.
- Run representative Chat, Responses, Messages, streaming, tool, image, and
  output-cap smokes for every changed surface.
- Run metrics-admin and ordinary-caller `/metrics` authorization checks when
  metrics are enabled.
- Run admin report and license-status checks when those features are enabled.

### Rollback

- Restore the previous package and reviewed runtime config.
- Restore the previous license input only when the license changed.
- Never run a reverse migration. Preserve the current usage database only when
  the release contract allows package/config rollback without restore; for a
  `restore-required` contract, restore the approved pre-migration snapshot
  before deploying the earlier package.
- Repeat `/readyz`, `/version`, `/v1/models`, and the failed caller/client smoke
  before returning traffic.
