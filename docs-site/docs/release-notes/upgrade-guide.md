---
title: Upgrade Guide
doc_type: reference
---

# Upgrade Guide

## Governed fine-tuning workflow

Training capture remains disabled unless an operator explicitly enables it for
an approved caller, group, or server scope. Captured content passes the DLP
filter before encrypted storage. Review the
[domain fine-tuning operator guide](../operations/domain-fine-tuning) before
enabling capture or training export. Exported datasets and trained artifacts
have retention independent from router capture deletion. Use bounded shadow
and audited promotion; retain the previous group set for one-call rollback.

## v5.0.0 request trace correlation

Inbound trace acceptance is opt-in. Existing callers and deployments that do not
configure a trusted parent receive a new trace for every inbound request. To
preserve a trace across routers, configure `server.trace.trusted_parent_cidrs`
for the immediate parent network, or enable `trust_traceparent` only on an
authenticated child-router caller. Outbound `traceparent` and `X-Request-Id`
propagation is on by default for every provider. Set
`providers.<name>.propagate_trace: false` to disable both headers for one
provider. Inbound `tracestate` is
deliberately dropped and never forwarded. A trusted proxy or ingress CIDR trusts
every client behind it, so trust only the immediate router network. Schema 18
adds scalar trace, span, and
parent-span columns to the usage table and an index on `trace_id`; existing rows
remain valid with empty values. Schema 19 adds mission task and owner
attribution after trace-correlation schema 18. Schema 20 records recomputed
input tokens and rollup coverage after schema 19; migration `2026111614` depends
on mission-attribution migration `2026111607`. The migration is additive and is
applied by the deployment migration job. See [router configuration](../configuration/router-config#request-trace-correlation)
for trust boundaries and examples.

## v5.0.0 tool-routing usage fields

Usage schema 21 adds six scalar fields to `request_usage` for tool calls, first
failure class, retries, escalations, per-request reliability outcome, and its
source. Existing rows keep empty/default values. The migration is additive,
online, and applied by the deployment migration job. Migration `2026111623` depends
on main's recomputed-input migration `2026111614`. Raw tool arguments,
schemas, and outputs are not stored. The reliability source records the evidence
that affected ordering (`compat-suite` or `live`); it reports `live` for observed
outcomes when no scored evidence changed the order. The compatibility matrix
currently has no provider/model-attributed failure counts, so its version 1 seed
is empty, routing starts neutral, and observed usage supplies live counts. To
load a future seed, add provider/model-attributed failure counts and matrix cell
links to the versioned seed manifest, then validate it with the existing seed
loader tests. Binary rollback across this schema requires restoring a compatible
pre-upgrade usage database backup; package rollback does not reverse the migration.

## Security hardening

Review the [Router Hardening Guide](../security-governance/hardening), [Threat Summary](../security-governance/threat-model-v5), and [Compromise Runbook](../security-governance/compromise-runbook) before production rollout. These docs use existing v5.0.0 settings; no config migration is required.

## v5.0.0 Security Console

The admin reports UI adds a Security Console to the Security events report. It ranks caller risk using detector results and caller authentication failures, and shows detector, authentication, and containment access events in the timeline. It links events to request evidence and provides JSON and Markdown incident bundles using the selected time window. Users with the domain-scoped containment permission can suspend a caller from the reports UI; the request requires same-origin CSRF protection and an audit reason. Release remains on the admin control plane and is shown as a `routerctl release` command and `/admin/v1/containment` request only for a caller suspended by containment. No new configuration is required.

The console only displays signals returned by the selected time range and report page. A missing signal does not prove that a caller is safe. Request evidence remains subject to the existing report drilldown permissions.

## v5.0.0 security posture self-test

`metrum-ai-routerctl doctor --security --config config.yaml` reads the existing
configuration and makes no config changes. Use `--format json` and `--fail-on`
to gate CI. Plain HTTP `--target` values must be loopback. The check reports caller
key format, recorded entropy, and key age against a 90-day maximum. Caller entries now accept optional
`token_issued_at` UTC RFC3339 timestamps; token generation and admin issue or
rotation write this field automatically. Add original issue times to manually
managed caller keys. Existing configs remain valid; keys without a timestamp
warn until updated or rotated, and keys older than 90 days fail.

The `/metrics` endpoint remains on the serving port and requires an active
caller configured with `metrics_admin: true`. The doctor checks this setting;
it does not suggest moving metrics to the admin listener. DB-backed config mode
reports that database-stored caller keys were not inspected. Engine image pins
are compared with the release's `engine-advisories.json` snapshot; use
`--advisories <file>` to select one explicitly. Without a snapshot, the
advisory result is informational `not checked` and makes no network request.

To display a result in admin reports, use `--report-latest --router-url` with
`--admin-key-file` or the existing admin-key environment variables. The router
stores only check IDs, outcomes, severities, timestamp, and aggregate status.
Apply usage migration 2026111602 (usage schema version 17) first. Grant `admin:security_reports` / `write`
only to the self-test operator; read and export grants do not include write.


## Honeytokens and canary groups (v5.0.0)

Honeytoken caller metadata and canary group allowlists are optional. Existing
configurations need no changes. A honeytoken caller's `allow` list can contain
only canary groups; config validation rejects real model groups, so decoy keys
cannot inherit production access. New canary groups must use a targetless
`static` strategy and explicitly list selected callers. Authenticated use emits
a metadata-only critical event before dispatch; it contains no token, prompt,
or user message. Known allowed canary completions return a canned response,
while unknown/disallowed models and unsupported features retain ordinary
caller errors. The config-control-plane migration advances schema 17 → 18
with ID `2026111611`, depending on admin-client migration `2026111610`, to
persist honeytoken label and planting time. Database-mode issuance and canary
group grants use the active canonical config store. See
[Honeytokens and canary groups](/docs/security-governance/honeytokens) for
setup, reporting, and key handling.

Caller routes authenticate before returning an identifier-rewrite availability
error. Invalid credentials therefore receive 401, while authenticated
honeytoken use is recorded before the route returns the unavailable response.

## Detector service boundary (v5.0.0)

Custom detector strategies run as separate services behind the versioned
`metrum.detector.v1` HTTPS contract. Detector transports require an exact
allowlisted hostname and a bounded timeout. The router rejects unlisted
redirects, URL userinfo, and malformed or oversized responses. Detector events
carry metadata by default. Text segments are sent only when an operator opts in
per detector with `include_segments`; prompts and user messages are never sent.
Configured detectors default to `mode: report`; set `mode: off` explicitly to
disable one. The example service's rule thresholds are configurable flags with
documented defaults.
See [External detectors](/docs/security-governance/detectors).

For v5.0.0, `metrum-ai-router-migrate --json` writes one JSON document to stdout. Database diagnostics are sent to stderr.

Release candidate validation includes the package upgrade-and-rollback procedure in the [release checks](https://github.com/metrum-ai/router/blob/main/docs/RELEASE_CHECKS.md).

Targets that ignore caller sampling controls can declare `temperature` or `top_p` in `request_shape_support.unsupported_request_features`; the router omits each declared field from both the translated request and its raw field map, and records the omission on the attempt. OpenAI Chat Completions translations now forward `top_p`. Pre-commit HTTP 400/422 refusals fall back only when the structured provider error identifies `temperature` or `top_p` with a supported unsupported-value/parameter code (or no code). Free-form message text and other 4xx responses do not trigger this fallback.

The NVIDIA and AMD reference pins moved to the official `vllm/vllm-openai:v0.33.0` and `vllm/vllm-openai-rocm:v0.33.0` images, respectively, for security advisories. AMD tiers were hardware-validated on the previous v0.25.0 image; revalidation on v0.33.0 is pending. Confirm the host's AMDGPU kernel driver supports the container runtime before deploying.

NVIDIA and AMD Kubernetes deployment examples and provisioning intents use the
digest-pinned official vLLM v0.33.0 images, matching the reference
configurations. Update copied examples from older releases before deploying them.
### Tenant cache isolation

Response-cache entries and vLLM prefix-cache salts remain isolated by caller and project by default. Project-level sharing is opt-in with `projects[].share_cache_within_project`; enabling it lets callers in that project share response entries and vLLM prefix-cache entries. Self-hosted vLLM providers receive salts when `providers[].upstream_kind: vllm` and `server.cache.upstream_salt_key_env` are configured; provide the environment key through the deployment secret manager on every router replica. SGLang prefix-cache isolation is not supported in v5.0.0. See [Tenant cache isolation](/docs/security-governance/cache-isolation) for the config example, compatibility behavior and key requirements.

### Workload token exchange

The opt-in `security.token_exchange` feature adds the `POST /oauth/token` route and short-lived, scoped bearer tokens. Configure the issuer, audience and subject grants before enabling it, and provide `signing_key_env` through the deployment secret manager with at least 32 random bytes. Grant-wide cumulative limits reserve estimated tokens and spend atomically, allowing concurrent requests only when their combined reservations fit; re-exchanging a token does not reset the cap. Budgets persist per grant across token expiry and are pruned when a reload removes the grant. Exchange is limited to 30 requests per configured subject per minute. Budget and rate state is process-local, shared across tasks and token refreshes, persists until grant removal or process restart, and is not shared across replicas. Mission tokens are accepted only on the scoped chat, Responses, Messages, count-token, embeddings, and model list/detail routes; all other serving routes reject them with 401, including leaked-key revocation, content-capture operations, `/metrics`, `/v1/usage` and `/mcp`. Task attribution is optional: use issuer `task_claim` with a dotted JWT claim path or pin `task_id` on a subject grant; if neither is configured, exchange succeeds without task attribution. When `task_claim` is configured and no grant task is pinned, the claim must exist and be a string; a missing or invalid claim returns HTTP 401 from `/oauth/token`. This accepts stock Kubernetes service-account and SPIFFE JWT-SVID tokens without custom task claims. Subject tokens remain replayable until expiry. A mission token cannot revoke its parent caller key. Changing the HMAC signing key is a hard cutover that invalidates all outstanding mission tokens; all replicas must use the same new key. Successful exchanges produce Authentication audit events naming the caller and grant. Review [Workload token exchange](/docs/security-governance/token-exchange) for the request format, limits and rotation behavior. Static caller keys are rejected for projects with `forbid_long_lived_keys: true`.

Usage schema version 19 adds `task_id` and `human_owner` columns. With the default deployment-job migration policy, run the usage migration plan, apply and serving verification steps before starting v5.0.0. Keep the database backup and rollback procedure in the [data migration runbook](https://github.com/metrum-ai/router/blob/main/docs/DATA_MIGRATIONS.md) available for the upgrade window.

Use this guide for customer-managed package upgrades. The exact maintenance window, approval process, and rollback policy belong to the deployment operator.

## v5.0.0 bounded tool-call streaming hold

Tool-aware model groups can opt in to `tool_routing.streaming.hold_until_validated` with
`hold_timeout_ms` (1–60000) and `hold_max_bytes` (1–33554432). Tool-call frames are withheld until
the complete call has passed inspection; ordinary text frames still stream immediately. For
OpenAI Chat Completions, OpenAI Responses, and Anthropic Messages, the first tool-call frame incurs
the remaining upstream tool-call generation time plus router inspection time. The hold limit applies per stream, and held bytes also count against the process-wide
`server.streaming.max_inflight_line_bytes` budget shared with native and Responses SSE parsing. This
budget is per router process, so configure each replica to cover its expected concurrent streams.
The timeout is an absolute deadline from the first held tool event; upstream byte activity does not
extend it. Responses argument delta/done events remain private through inspection, and Anthropic
tool-use frames are released before the terminal `message_delta`.

## Virtual models

The optional `virtual_model` block adds capability thresholds to a model
group. Existing groups are unchanged. Import matching eval-infrastructure
reports with `metrum-ai-routerctl groups import-eval`; vectors and provenance
are applied through the config Admin API's copy-on-write group update. The
group list/show API now reports each target's measured vector, membership and
exclusion reason. Missing or ambiguous report data fails without changing the
active config. Reported context tokens are the maximum observed in the eval
workload, not a model's theoretical context limit.
## CPU-first small-work reference group

The hierarchy reference now includes an optional `agent-small` model group,
backed by the official vLLM CPU image and `Qwen/Qwen2.5-0.5B-Instruct`. The
reference caller can request this group explicitly. Its CPU candidate is
eligible only when estimated input is at most 800 tokens and the requested
output cap is at most 128 tokens; other requests use the configured frontier
target. The full CPU attempt has a 15-second timeout sized for the 128-token
output cap; `thresholds.max_ttfb_ms: 1000` supplies the dynamic latency budget,
and CPU errors/timeouts retry the frontier before a response begins. Configure
`CPU_BASE_URL`, `CPU_MODEL`, and the vLLM
CPU resource settings in the hierarchy `.env` file. Existing groups and
existing caller behavior are unchanged.

This is reference configuration only. Operators should establish workload
quality and saturation limits on their own CPU hardware before granting callers
access to `agent-small`. The edge and datacenter hierarchy reference configs set
`admin.enabled: false` because each router uses one listener; deployments that
need the authenticated admin listener should enable and configure it explicitly.

## v5.0.0 tool-call firewall

`server.tool_firewall` is off by default. Review the [Tool Call Firewall
configuration](../configuration/tool-firewall) before enabling `shadow` or
`enforce`. Sequence counters and pending approvals are process-local; with
multiple replicas, route each conversation and its approval request to the same
router process. Configure `admin:tool_approvals:resolve` only for principals
that may release held tool calls. Hold rules return an immediate pending
result; after an administrator releases the hold, the agent retries the same
request. Enforced streamed tool calls are held automatically until inspection completes,
using 30-second and 1 MiB per-stream bounds when the model group does not set
explicit bounds. Schema validation still runs only when the group opts in to
`streaming.hold_until_validated`.


## v5.0.0 caller-token hygiene

Newly generated caller tokens append a `v2` marker and checksum to the existing
`rtr_metrum_` format.
Previously issued tokens continue to authenticate. Config load warns when token
entropy is unknown or a caller uses a development environment. The token
generator defaults an omitted environment to `prod`. The
`security.key_hygiene.min_entropy_bits` default is 128; `0` selects that default
and explicit values must be 1–256. Rotate old keys before setting
`security.key_hygiene.enforce: true`; enforcement rejects unknown or below-floor
entropy and development-environment tokens. A leaked token can revoke itself
through `POST /v1/security/leaked-keys` only in database-backed configuration
mode. YAML-only/file mode has no shared revocation store, so the route is not
mounted. The verifier is recorded in a separate token-hash denylist, independent
of config sets and revisions, and checked during authentication on every
serving endpoint. Rollback and YAML import do not clear the denylist. Database-
backed replicas receive changes through the configuration revision reload path.
The denylist write, revision notification, and audit records commit together.
Rotate the caller token to issue a new verifier and restore access; then update
clients with the new bearer token. Only rotation restores access; the old
verifier remains refused.
## v5.0.0 streaming line-buffer budget

Streaming now caps total in-flight SSE line-buffer capacity per router process with `server.streaming.max_inflight_line_bytes` (default 64 MiB). Set an explicit value from 64 KiB to 1 GiB if needed. Exhaustion fails a stream with `upstream_stream_buffer_limit`; pre-commit requests can use existing fallback. Monitor `metrum_ai_router_sse_line_buffer_bytes`, `metrum_ai_router_sse_line_buffer_limit_bytes` and `metrum_ai_router_sse_line_buffer_rejections_total`.

## Security dependency update

The v5.0.0 build pins `golang.org/x/crypto` to the upstream fix for GO-2026-5932. The fix removes the unused OpenPGP packages from the module; router configuration and runtime behavior do not change. When the next tagged `x/crypto` release includes this fix, routine dependency maintenance should replace the temporary pseudo-version.

Direct `model` targets inherit provider-catalog capabilities, such as reasoning, tools, modalities and request-shape support, when their model ID matches a catalog entry. Their configured dialect, pricing and routing values remain unchanged. A weighted or failover group can therefore select an OpenAI reasoning model even when the target is written with `model:` instead of `model_ref:`. When mixed eligibility rejections leave the reasoning error parameter unset but removing requested reasoning makes a target eligible, `param` identifies the caller field (`reasoning_effort` for Chat Completions, `reasoning` for Responses, or `thinking` for Anthropic). Anthropic output reasoning requires `control: token_budget` or active default thinking.

## v5.0.0 provider spend breaker

Provider `spend_breaker` is optional. Existing configurations keep their prior
routing behavior. Set `hourly_usd` and/or `daily_usd` under a provider to cap
estimated spend; each window resets at the next UTC boundary. The counters are
in memory and reset on router restart, so configure ceilings with that limit
in mind. Counters are per router process; with `N` replicas the effective
ceiling can reach `N` times the configured value. The router records only safe
scalar trip/reset events and does not write provider keys to logs, events or
usage rows. With `on_trip: stop`, a request whose group includes the tripped
provider returns HTTP 503 with error code `provider_spend_breaker`; the router
does not fail over to another provider until the window resets. Use
`on_trip: failover` to reroute to another eligible provider. Trip/reset records
also enter security access events when
`server.admin_reports.security.enabled` is true; structured log events remain
available otherwise.

## v5.0.0 Bedrock reasoning history

Anthropic Messages requests routed through Bedrock Converse now forward the
`thinking` request option for supported Claude models and retain signed
`thinking` and `redacted_thinking` history through unary and streaming tool
continuations. No router configuration change is required; callers opt in with
their normal Anthropic `thinking` object. The router rejects missing thinking
signatures and malformed redacted data before dispatch. Opaque signatures are
returned in Anthropic continuation blocks for provider validation, are excluded
from usage rows and request logs, and are not returned in OpenAI Chat responses.
Bedrock still validates signature authenticity, and opaque signatures may be
scoped to a provider account. Keep reasoning history on the same Bedrock target
and account; cross-provider and cross-account replay is unsupported.

## v5.0.0 secret DLP

Model groups can set `pii_filter.secrets_enabled: true` to apply the built-in
credential detector pack to inbound text, buffered responses, and streamed
text. `mode: redact_only` replaces matches with placeholders; `mode:
fail_on_match` blocks matching requests. Existing custom rules and replacement
limits continue to apply. Configure `apply_to.tool_results` for tool-result
coverage. Streamed text keeps a 512-byte ordinary rolling tail, holds plausible
JWTs from their start (up to 4,096 bytes per segment), and tracks every PEM
BEGIN marker through its end marker, up to 64 KiB. The PEM cap and overlong
provider-token or credential-assignment candidates fail closed. Unary response
replacement-limit exhaustion also fails closed. Response redaction requires
`secrets_enabled` and `apply_to.messages`; custom-only PII configurations keep
their prior response behavior. JSON-encoded tool-call arguments are decoded
before inspection, with string values checked alongside their property names as
`key=value`; header-shaped values are also checked as `key: value`.
JSON-escaped credential characters are normalized before matching. Object-valued
Chat arguments and Anthropic `tool_use.input` objects receive the same key/value inspection.
Malformed or truncated argument strings are scanned in best-effort `key=` and
`key: ` forms so partial JSON cannot hide credentials. Responses function-call
arguments and custom tool input are redacted in output snapshots.

When tool routing enables `streaming.hold_until_validated`, streamed tool-call
arguments are held until inspection completes. Secret matches are redacted in
`redact_only` mode and block the held call in `fail_on_match` mode. Without the
bounded hold, tool-bearing streams on a DLP-enabled group fail with HTTP 400
`secret-dlp-stream-inspection-required`. Streams with custom rules remain
fail-closed because the rolling text filter cannot infer a safe match bound.
Completed streamed arguments use the same JSON-aware inspection as buffered
tool calls, including JSON-escaped credentials.

## v5.0.0 KV-cache routing and metering

The additive usage-schema migration adds nullable `request_usage.recomputed_input_tokens` and scalar coverage fields to usage rollups. It backfills recomputed tokens only for historical rows with reported cached-token counts; rows without that evidence remain unknown. The router computes new values as `max(input_tokens - cached_input_tokens, 0)` when input tokens are nonnegative; negative input counts remain unknown. Reports now include cached and recomputed token totals and coverage.

Existing routing config keeps its behavior unless a target sets `tiered_kv_cache: true` or a dynamic-score request supplies `metadata.session_id`. Tiered-cache preference applies at an estimated input length of 32,000 tokens and only among targets already eligible for the request. Dynamic-score affinity remains process-local and bounded; restarting clears pins. Remove the target attribute or restore the prior group config to roll back routing behavior. Schema rollback follows the restore-required migration contract.

## v5.0.0 trusted proxy client IP resolution

When `server.client_ip.trusted_proxy_cidrs` is configured, the router now walks
`X-Forwarded-For` from the socket peer toward the client and chooses the first
untrusted hop. A client-supplied leftmost value no longer overrides the address
appended by a trusted proxy. If the socket peer is not in a trusted CIDR, the
router ignores `X-Forwarded-For` and uses the socket peer. If every forwarded
hop is trusted, the router uses the leftmost address. `X-Real-IP` keeps its
existing single-value behavior.

This can change client addresses recorded in security access events and used by
admin OIDC rate limiting for proxied deployments. Review the trusted proxy
CIDRs and verify that each proxy appends its connecting peer to
`X-Forwarded-For`; do not trust client-controlled forwarding headers directly.

## v5.0.0 admin listener and serving-port routes

The authenticated admin listener starts by default at `127.0.0.1:8081`. Move machine automation
from legacy `/admin/*` paths on the serving port to `/admin/v1/*` on the admin listener before
upgrading. Set `admin.enabled: false` only when the deployment needs a single listener; remote binds
require TLS or the explicit plaintext opt-in. Browser reports and `/admin/auth/*` remain on the
serving port. Remove `admin.legacy_routes_on_serving_port` and `admin.legacy_sunset` from file
configurations; either key fails config loading with migration guidance.

For database-mode configurations, back up the router-config database and run the non-serving
`router-migrate --scope router-config --action apply` followed by
`router-migrate --scope router-config --action maintenance --backup-evidence-ref <backup-reference>`
before starting v5.0.0. The maintenance migration removes those two v4-only keys from stored
canonical documents and refreshes their content hashes. File-mode configurations need only have the
keys removed before restart.

## v5.0.0 routerctl Admin API changes

The online `metrum-ai-routerctl` commands now use the versioned API on the dedicated admin
listener. Set either `--router-url`/`ROUTER_URL` for TCP or `--socket` for a Unix socket; do not
combine them. When neither is set, routerctl uses the restored default
`http://127.0.0.1:8081`. Unix sockets can run alone or alongside TCP, and their filesystem mode controls which
local users can connect. Remote TCP endpoints require HTTPS. Credentials remain in `ROUTER_ADMIN_KEY`,
`ROUTER_ADMIN_KEY_FILE`, or a mode-0600 `--admin-key-file`. Endpoint values must be origins with
no path (a single trailing slash is tolerated), query, fragment, or embedded credentials; use only
the scheme, host, and optional port. Non-2xx responses are written to stderr as concise
`admin API <status> <type>: <message>` diagnostics. Connection and timeout failures use
`admin API request failed: <cause>` and include a hint to check `admin.enabled`, `admin.listen`,
and the admin credential setting; response bodies and credentials are not printed.

When `admin.unix_socket.path` is configured without `admin.listen`, the socket is the only admin
listener and the default TCP listener is disabled. Set `admin.listen` explicitly to keep TCP access.

| Command | v5 behavior and flags |
| --- | --- |
| `config diff` | `--api` selects `/admin/v1/config/diff`; it remains optional for local file comparison. |
| `config import` | Online by default through `POST /admin/v1/config/sets`; `--offline` explicitly selects direct database access. `--yaml` and `--name` remain required. |
| `config export` | Online by default through `/admin/v1/config/sets/{id}`; `--offline` explicitly selects direct database access. `--set-id` and `--out` remain optional. |
| `config activate` | Online by default through `/admin/v1/config/sets/{id}/activate`; use `--offline` for direct database access. |
| `config rollback` | Online by default through `/admin/v1/config/rollback`; use `--offline` for direct database access. |
| `groups list`, `show`, `set-targets`, `add-target`, `remove-target`, `rollback`, `history`, `shadow start|status|promote|stop` | Use `/admin/v1/config/groups/...`; `--router-url` is optional when the listener is at its default. |
| `contain`, `release` | Use `/admin/v1/containment`; `--router-url` is optional at the default listener. |
| `policies list|get|draft|validate|diff|activate|rollback|replace|delete` | Use `/admin/v1/policy-sets`; `--router-url` is optional at the default listener. |
| `reports cfo` | Uses `/admin/v1/reports`; `--reports-prefix` now defaults to `/admin/v1/reports`. The old `/admin/reports` value is rejected. |

For config import/export/activate/rollback, the historical `--api` flag remains accepted as a
redundant online-mode selector and cannot be combined with `--offline`. Add `--offline` only for
deliberate direct database operations while the router is stopped.

## Pre-Upgrade Checklist

1. Read the release notes for operator impact, database changes, license changes, and caller-visible behavior.
2. Confirm the page banner and `/version` identify the expected router version and build timestamp.
3. Confirm the package architecture matches the host.
4. Back up runtime config, provider env file, license file, license state, and usage database; verify the backup can be restored within the planned window and has sufficient free space.
5. Record the current router version and build timestamp from `/version`.
6. Confirm `/readyz`, `/v1/models`, metrics, and admin reports are healthy before the change.
7. Prepare a rollback package and the previous reviewed config.
8. For v2.0.0+, inventory every install path, systemd unit, Compose service,
   Kubernetes image, and client `model_provider` that still names `metrum-router*`
   or `metrum-genai-smartrouter-*` CLIs.

## v5.0.0 Configuration Changes

### Caller token verifier removal

v5 rejects a non-empty `callers[].token_sha256`; an empty value written by v4.8
alongside `token_hash` is ignored. `callers migrate-legacy` is shipped only in
the v5 `metrum-ai-routerctl`; run that v5 binary against a v4.8 config before
upgrading the router. Back up the configuration and token files, then migrate
every active legacy caller:

```sh
metrum-ai-routerctl callers migrate-legacy --config config.yaml --id <caller-id> --token-file <private-token-file>
# Or pass the token on stdin without placing it in an argument:
cat <private-token-file> | metrum-ai-routerctl callers migrate-legacy --config config.yaml --id <caller-id> --token-file -
```

The command verifies that the supplied token matches the stored SHA-256 value,
derives the salted verifier, preserves the token, removes `token_sha256`, and
keeps a configuration backup. The raw token is read from the private file; do
not pass it as a command-line argument. If the token is unavailable or does not
have the supported `<token_id>_<secret>` format, use
`metrum-ai-routerctl callers rotate --config config.yaml --id <caller-id>
--token-out <private-file>` and distribute the replacement once. Rotation
preserves caller status and revocation metadata; add `--reactivate` only when
you intend to restore access. For disabled or revoked legacy callers, use
`migrate-legacy --remove-disabled`; this deletes the entire caller entry without
issuing a token. v4.8 rejects a caller with neither verifier even when it is
disabled, so do not leave a verifier-free caller entry in the file. The removed
caller must be recreated explicitly if access is needed later. Run:

```sh
metrum-ai-routerctl callers migrate-legacy --config config.yaml --id <caller-id> --remove-disabled
```

Migrate every active legacy caller before running `callers verify`; verify each
token after all migrations are complete.
`callers hash` only prints hash material; it does not edit a config and is not
the migration command.

The migrated active caller remains readable by v4.8 because it contains the
supported salted `token_hash` field and no non-empty legacy verifier. Rotated
callers also remain readable; revoked callers stay disabled after rotation.
Disabled or revoked callers removed with `--remove-disabled` are absent from
the migrated file. Retain the original backup and recreate removed callers only
through a reviewed, explicit provisioning step.

For database-backed config, export the active document, edit a local copy or
run the CLI against the exported document to migrate each legacy caller, import
the updated YAML as a new config set, review its diff, then activate that set.
Test startup from the stored document before completing rollout. The recommended
v4.8 DB migration is a maintenance-window procedure: stop every router replica
before the `--offline` export, import, and activate commands below, keep them
stopped through activation, then start all replicas on v4.8 and verify startup
before upgrading. This avoids a mixed fleet reading incompatible config. For
file-mode deployments, migrate a copy while v4.8 serves its unchanged file;
replace the file and restart v4.8 to verify it before upgrading.

For a SQLite control-plane database, the command sequence is:

```sh
metrum-ai-routerctl config export --offline --db <config-db> --scope prod --out config.yaml
metrum-ai-routerctl callers migrate-legacy --config config.yaml --id <caller-id> --token-file <private-token-file>
metrum-ai-routerctl config import --offline --db <config-db> --scope prod --name v5-caller-migration --yaml config.yaml
metrum-ai-routerctl config activate --offline --db <config-db> --scope prod --set-id <imported-set-id>
```

Repeat migration for every active legacy caller before running verify, import,
review, and activation. Run these `--offline` commands only while every router
replica is stopped. Use the v5 binary for each step. For PostgreSQL, add
`--driver postgres --dsn-env <environment-variable>` to each `--offline`
database command. Review the
imported set and diff before activation; retain the exported original and
database backup for rollback.

The load error names `callers migrate-legacy`; use `callers rotate` when no
usable raw token is available.

### TypeScript Routing Policy Removal

The in-process `strategy: script` policy and its non-empty `script`,
`script_max_concurrent`, and `script_http` settings are removed. User code
never runs inside the router process. Configurations selecting `strategy:
script` or setting non-empty legacy values fail during configuration loading
with guidance to migrate to `strategy: external`. Empty legacy fields written
by v4.8 are ignored and removed from canonical documents on import.

Move the policy to a standalone service and configure `external_policy.url`,
`allow_hosts`, and a bounded `timeout_ms`. The service receives safe derived
request context by default. Enable `external_policy.include_request: true` only
when the service is trusted to receive request content. The request-shape and
PII-aware examples under `examples/typescript-request-shape/` and
`examples/typescript-pii-policy/` show the external policy contract. Use
`pii_filter` when request redaction is required before upstream calls.

## v5.0.0 caller binding

Caller binding is opt-in; existing callers keep `binding.mode: off`. To enable
enforcement, add `callers[].binding` and validate client network paths first.
CIDR and ASN allow lists can be enforced behind an ingress. ASN checks require
an operator-maintained `server.client_ip.asn_prefixes` map. For mTLS identity,
terminate TLS at the router or pass TLS through unchanged, then configure
`server.tls.client_auth: optional` (or `require`) and `server.tls.client_ca_file`. The router
does not accept forwarded certificate headers. Config validation rejects
enforced mTLS binding without verified client-certificate TLS or the client-auth
extended key usage. Enforced binding failures return the same generic HTTP 401
response as invalid tokens; the security event retains the specific failure
reason.

## Compatibility and operations (v4.6.0)

### No usage-database migration

v4.6.0 adds no usage-schema migration; the schema stays at version 11. The
Required Migration Gate does not apply to this upgrade, and rolling back to
4.5.0 needs no database restore for schema reasons. Back up the usage database
as usual before any package upgrade.

### Reasoning provenance is opt-in

`server.reasoning_provenance` defaults to `mode: off`, where the router changes
no bytes and reasoning history passes through exactly as in 4.5.0. Enabling
`observe` or `enforce` requires a secret in the environment variable named by
`key_env`, at least 32 bytes, plus a log-safe `key_id`; a configuration that
sets `key_env`, `key_id` or `ttl` while the mode is `off` is rejected so a
dormant block cannot look active. In `enforce`, history minted by another
provider account, model or group is dropped rather than replayed, which can cost
one continuation at cutover. Rotation, diagnostics and the full mismatch table
are documented in [Reasoning provenance](/docs/operations/reasoning-provenance).

### Embeddings groups

`POST /v1/embeddings` is served only for model groups configured as embeddings
groups. Each target declares `embedding_space` and `embedding_dimensions`
(inherited from `model_ref` like other target fields);
`default_embedding_dimensions` is required when a group serves more than one
dimension set. Load-time validation rejects a group that mixes embedding and
non-embedding targets, targets that disagree on space or dimension sets,
non-text inputs, and the chat-shaped strategies (`semantic`, `dynamic_score`,
`external`, `script`, `intelligent`). A request whose `dimensions` no target
serves, or that omits `dimensions` where no default exists, fails closed. Adding
embeddings to an existing chat group is not supported; create a separate group.

### Error bodies for capability failures

`400 unsupported_capability` (and the Anthropic-shaped equivalents) now carry
the recorded eligibility reason and a `param` that is set only when every
rejected candidate shares that field; otherwise `param` is `null`. Anthropic
error envelopes gain `error.code` and `error.param`. This changes the body of an
error that previously used a generic message and a bare `param`: review any
caller, dashboard or test that matches the old text, and match `code` and
`param` instead.

### PII-enabled groups

Three changes apply to requests evaluated against a PII-enabled model group.

- Response-cache reads and writes are bypassed while target-bound cache identity
  is reconciled, and otherwise-cacheable requests report `cache-pii-filter`.
  Upstream call volume and provider spend can rise for those groups. The guard
  uses the policy each request captured; it is not an instantaneous activation
  barrier, so pause and drain requests and confirm every replica loaded the
  enabled policy before a strict cutover. Existing cache entries are not purged
  and can become eligible again if filtering is disabled, until expiry or cache
  reset.
- Candidate shadow replay is skipped, with the policy rechecked before dispatch.
  A replay already past that check can still finish after an in-flight change,
  so stop and drain shadow runs before a strict cutover.
- Responses system-message histories fail closed with `system_pii_unsupported`
  when the only eligible translation target is Anthropic or Bedrock Messages,
  which can make such a request return `400` where it previously dispatched.

These guards are safety mitigations, not a target-bound filtering identity; the
broader reconciliation remains tracked.

### Anthropic Messages context management

A null `context_management` is treated as absent and is not forwarded upstream.
Non-null values still require both the caller's matching beta and the target
allowlist; other beta-gated fields are unchanged.

### Usage rows and unsupported surfaces

New usage rows carry a bounded endpoint label; the schema-11 column is unchanged
and historical rows are not backfilled, so usage reports keep a mix of populated
and empty labels until old rows age out. `POST /v1/content_provenance_checks`
answers an authenticated `501 unsupported_endpoint` with no upstream dispatch;
automation that previously treated that surface as generically unsupported should
match the `501` code.

### Example infrastructure

The example Kubernetes overlays are validated as rendered manifests in CI with a
reviewed ignore list, and the example vLLM serving workloads and example runner
image now run as non-root with dropped capabilities, a read-only root filesystem
and explicit writable cache and temporary mounts. The example runner also
expects to start as its non-root user. Re-validate device access, model startup
and runner registration in your own environment before adopting those examples;
the offline checks in CI do not exercise a GPU or a runner image.

## v5.0.0 admin resource API

The versioned admin API adds provider and model-catalog reads, provider writes,
project environments, project-environment memberships, project USD budgets and
alert thresholds, caller quota updates,
and retention policy writes. Mutations require
database-backed configuration mode and a current `If-Match` revision. Provider
API-key environment references and headers are write-only and do not appear in
responses. Shortening retention requires the separate `config:force`
permission. The existing config revision notifier and poller propagate changes
to other serving replicas. The provider, project, quota and retention routes do
not add schema; the operations endpoints described below do.
Project budget counters are per process and reset on restart; ceilings are
approximate across replicas. Removing or raising a ceiling, or waiting for its
UTC budget window to reset, restores admission after a trip.

Admin operation tracking and control-plane backup/restore add the configuration
database schema version 16 operations table. Before enabling these endpoints,
run the non-serving migration CLI (host or the packaged Kubernetes Job) and
verify the configuration database is current. The admin API never applies
migrations. Backups contain key verifiers and audit records; store files with
restricted access. `routerctl control-plane backup --out FILE` creates a new
mode-0600 output file, and `routerctl control-plane restore --in FILE` restores
only to a clean migrated replica. Grant `admin:backup/read` only to backup
operators and keep `admin:operations/read` separate; operation status reads do
not expose the backup bundle. Atomic target batches are available through
`routerctl groups bulk-targets GROUP --change add=provider/model_ref` and commit
one configuration revision or none.

## Compatibility and operations (v4.4.0)

### Authorization matching and tenant RBAC

Admin authorization policy objects now match by exact equality; the only
wildcard is the standalone `*`. Actions are anchored patterns, and
partial-wildcard objects (for example `admin:*`) are rejected at policy load
time. A grant for one admin object no longer covers a different admin object:
policies that relied on that implicit coverage must list each object
explicitly (`admin:reports`, `admin:security_reports`, `admin:callers`, …).
See `docs/AUTHORIZATION.md`.

The tenant provisioner Role example is least-privilege: write access only to
ResourceQuota, LimitRange, NetworkPolicy and namespace-scoped Linkerd
Server/ServerAuthorization, read-only Ingress, no delete verbs, no wildcards.
Re-apply `deploy/kubernetes/bootstrap/tenant-provisioner-rbac.yaml` and verify
the `kubectl auth can-i` matrix in `docs/EKS_IDENTITY_BOOTSTRAP.md` (including
explicit denials for secrets, deployments, serviceaccounts, clusterroles,
other namespaces and `pods/exec`). Teardown removes the tenant namespace
rather than granting delete verbs.

### Admin Config API and group management (v4.4.0)

Group swap/shadow/promote/rollback and config-set writes require
database-backed configuration mode (`config_source_mode_file` deployments keep
serving and answer writes with `409 config_source_mode_file`). Admin config
sets and the shadow run registry are phase-9 tables in the config database:
apply the config migrations, keep the `router_config_change_events` audit
chain and back up the config database alongside the usage database. Promotions
are copy-on-write; rollback restores the exact prior target set. Admin API
calls require a dedicated admin API key with the matching permissions
(`config:read`, `config:write`, `config:activate`, `config:force`,
`shadow:write`, …); caller and provider keys are rejected.

Validate with the `e2e/admin-api/` suite (builds the router, stands up a
3-replica DB-backed deployment plus a file-mode router, and verifies
swap/shadow/promote/rollback against real providers), or follow the worked
example in the "Switch models when prices change" guide: dry-run, shadow,
promote, rollback.

### Admin API key (new configurations)

New configurations use `server.admin_auth.api_keys` with hashed keys and
fine-grained permissions; `metrum-ai-routerctl adminkeys generate` creates
them. Caller and provider keys are rejected on `/admin/*`. Rotate keys without
downtime by adding the new key, switching consumers, then removing the old
one. See `docs/ADMIN_API_KEYS.md`.

### Tooling and CI

Dependency updates: docs site (body-parser, serialize-javascript, katex,
postcss-selector-parser), learned routing policy (filelock 4.x, setuptools),
test projects (pytest 9), LiveCodeBench evaluator (datasets 5.0.1 with an
fsspec floor; `contract.json` tracks the pin). No router-service Go dependency
changed. Dependabot version updates now observe a cooldown period and cover
evaluator/test pip projects. Release workflow dispatch inputs are validated
against a strict semver tag allowlist before any build step.

Rollback: keep the previous binary and reviewed configuration; the hardening
changes introduce no database migration. Restore the previous RBAC and
authorization policy files if you must restore the previous matching behavior.

## Compatibility and operations (v4.3.0)

### Migrations and rollback

Back up and rehearse restore before upgrading. Use the **Required Migration
Gate** below, including the non-serving PostgreSQL migration job; do not bypass
the ledger or manually add columns. Usage migrations `2026102202` (schema v7),
`2026102301` (v8), and `2026102302` (v9) retain provenance, cache-write usage and
pricing, and unbilled count-event identity respectively. The configuration
control plane has its own `2026102301` cache-write pricing migration (v7);
identical IDs in separate scopes are not the same migration.

Usage migration `2026102303` (schema v10, dependent on v9) adds reasoning,
cache-read/write coverage, attempt counters and cost allocation to daily, hourly
and monthly aggregate tables. Existing aggregates retain their ordinary totals
but start with zero **known evidence**, not proof that no reasoning/cache usage
occurred. Regenerate draft windows only while raw rows remain; finalized windows
are immutable through the generator. Recomputing finalized history requires a
separately reviewed maintenance/restore workflow, not an automatic backfill.

Usage migration `2026103001` (schema v11, dependent on v10, online) adds
`request_usage.endpoint`. In v4.5, only Responses input-token and compact
requests populate this column; other rows may be empty. Starting with v4.6,
new request-usage rows receive bounded surface labels (including Chat,
Responses and Messages); historical rows remain unchanged and are not
backfilled. No new migration is needed for these values. Like the earlier
expansions schema v11 is `restore-required`: package rollback never runs a
reverse migration, so downgrading to a package that predates schema v11
requires restoring a pre-upgrade database backup.

Usage migration `2026110201` (schema v12, dependent on v11, online) adds the
dormant `response_affinity` table for owner-bound stored Responses. It is
metadata only and enables nothing: stored Responses affinity stays off by
default and is enabled per model group in later reviewed changes, the agreed
local affinity TTL is 30 days (configurable, integrated with retention and
legal holds in that later work), an unavailable pinned target will return `409`
`response_target_unavailable` instead of re-routing, and only native Responses
targets are in scope. The table has no bearer-token, prompt or response-content
column and the migration writes no rows.

The upstream-id envelope this table stores is introduced with schema v12 and has
no earlier released format, so there is no stored-response metadata to migrate.

Back up the usage database before applying the Required Migration Gate. This
backup is an operator requirement: online `auto-safe` migration does not verify
backup evidence. A downgrade to a pre-v12 package requires restoring a
compatible pre-upgrade backup; package rollback never runs a reverse migration.
Migration tests exercise a reverse step that drops the table and its ledger
rows on disposable SQLite and PostgreSQL databases; that step is destructive and
is not a supported production downgrade. Future stored-response writes must
first implement authenticated encryption and key lifecycle, caller-token
ownership with tenant/project authorization, and retention and legal-hold
integration with this table.

Historical provenance remains empty/unknown. New estimated rollup counters
start at zero, not a reconstruction of historical estimates. Historical nullable
cache-write counts/prices remain unknown. Historical count calls cannot be
reliably distinguished from inference and are not retroactively refunded.
These expansions require restoring the pre-upgrade snapshot for binary rollback.
Retain the reviewed old configuration as well as the database backup.

### Empty modalities and launcher preflight

An empty legacy `modalities: []` now behaves as unset rather than overriding
`input_modalities` with an empty capability set. Explicit nonempty `modalities`
still overrides input metadata; unset/empty legacy metadata falls back to the
existing input metadata/default text. This repairs serialized config round trips
that previously made text targets ineligible. If you intentionally used an
empty array to disable a route, remove/disable the target explicitly instead;
do not rely on an empty capability array as an access control.

Future training exports collapse canonical-JSON-identical repeated tool declarations by
name and report normalization counts in the manifest. Conflicting declarations
are rejected, not silently selected; previously published exports/hashes are not
rewritten. Fine-tuning launchers now require successful authenticated route,
static-target and (for semantic runs) grader preflight before batch dispatch.
Preflight is chargeable warm-up traffic, not a scored case. Missing access or
pre-route errors abort the whole batch; supply every target alias and sufficient
local capacity, not privileged replacement keys or selective retries.

### Accounting and client behavior

Review dashboards that assumed every token count was reported. Inference token
metrics now include `usage_source`; aggregate across this label when comparing
with older totals. Missing stream usage settles using observed text rather than
the requested maximum; partial reported usage can survive committed stream
failure. Explicit Chat `stream_options` are preserved; when absent the router
requests final usage unless the target declares that feature unsupported.

Gemini thinking tokens count as output, and Anthropic input includes uncached,
cache-read and cache-write tokens. Reasoning is already included in output and
must not be charged twice. Configure optional
`cache_write_price_per_million_usd` for the actual cache policy; missing write
pricing uses base input pricing and warns. Validate refreshed catalog prices
before promoting targets. See [Usage, Cost, And Reports](../usage-cost-reports/overview)
for fields, provenance and cache-cost distinctions.

Native Anthropic `count_tokens` now proxies the provider count and errors;
other transports return marked estimates. Both paths are unbilled and excluded
from inference quotas/aggregates. Request reports still show count events.
Clients must not assume counts are always local or tokenizer-exact.

Reporting now preserves reasoning/cache token subsets and known reporting
coverage through rollups, API tables and CLI Markdown output. Cost/savings
composition distinguishes routing, provider prompt caching and free router
response caching; cache-write premiums can make prompt-cache savings negative.
Additional counters are `metrum_ai_router_reasoning_tokens_total`,
`metrum_ai_router_cached_input_tokens_total`, and
`metrum_ai_router_cache_write_input_tokens_total`. Cost metric components now
include `cached_input` and `cache_write`; `input` excludes these, while `output`
already includes reasoning. Review dashboards that selected only old component
values. No separate reasoning cost counter should be added.

The admin history selector is opt-in: `history_source=rollup` reads finalized
daily aggregates rather than raw rows. Use complete closed UTC-day windows;
unsupported predicates and report types return HTTP 400 rather than silently
mixing sources. The retained-history reader now excludes current/future days,
partial manually finalized days, and snapshots finalized before their window
closed; an early snapshot does not become complete merely with elapsed time. Keep raw retention long enough for request-level investigation
and Markdown exports, which cannot be reconstructed from aggregates.

### Optional upstreams and fine-tuning

Bedrock is opt-in: use `auth_scheme: bedrock`, internal `dialect: openai-chat`,
and a target `region` (or the AWS SDK region chain). Use the default AWS
credential chain, never keys in YAML; grant only the validated model/profile
resources. Cross-region profiles require destination permissions and a residency
review. Existing providers need no Bedrock configuration. See
[Providers And Models](../providers-models/overview) for the validated matrix.

The governed training surface adds `POST /admin/training/outcomes/{request_id}`
and `POST /admin/training/export`. Both require opt-in encrypted content capture,
explicit deployment-wide `content:training` grants (`label`/`export`) and a
durable security audit; report-view/export grants do not imply training access.
No new schema migration is introduced for these endpoints: labels reuse
encrypted content-capture records. Existing IR-only captures lack canonical
chat/tool history and cannot be retroactively converted. Keep encryption keys
available while captures remain exportable. Exported datasets have independent
access/retention obligations; deleting captures cannot recall downloaded copies.
Review teacher model license, provider terms, consent and output-training rights
before enabling this optional surface. The synthetic example authorization is
not permission for customer-data training. See
[Fine-tune And Route Your Own Model](../configuration/fine-tune-own-model).

The fine-tuning example is an operator-owned training/deployment recipe, not
a default routing change or a quality promotion. Run 1, run 2 and AMD all
**failed** the preregistered gate and were not promoted; the router kept the
teacher in place. Training completion and quoted throughput/unit costs are not
API parity or quality-equivalent savings evidence. Review the original gates
and measured-comparison limits before using an adapter; enforce rental spend
limits and verify teardown independently of token billing. The example's
localhost proxies now validate allowlisted response headers against CRLF/header
injection.

For release acceptance, exercise a real client tool/result continuation on each
enabled route, confirm count events leave inference spend/quota unchanged, and
compare persisted usage, reports and metrics after queued writes drain. For
optional training export, include an uncaptured caller control and validate the
manifest and privacy/scope checks. For Bedrock, smoke the exact model/profile,
region and capabilities granted by IAM. Retain the tested main SHA and build
metadata with the operator's validation evidence.

### CI operations

Contributor workflows now target the repository-restricted `router-arc` Actions
Runner Controller scale set on DigitalOcean Kubernetes. Jobs use ephemeral
single-use pods and must tolerate cold HOME/tool/cache state. The legacy
`runner-cache-trim.yml` host-maintenance job remains separate. See the
[ARC runner runbook](https://github.com/metrum-ai/router/blob/main/docs/ci-arc-runners.md)
for caching and acceptance; this does not change serving-router deployment.

## Compatibility and operations (v4.2.0)

### Translation eligibility and beta policy

`eligibility.implicit_openai_to_anthropic` defaults to `true` for this
release and flips to `false` next minor. Inventory startup warnings for each
target relying on implicit translation. Preview the migration with
`metrum-ai-routerctl config patch-eligibility --config config.yaml --dry-run --diff`;
review the explicit inbound dialects and validation status before applying
with `--output reviewed.yaml`. The patcher preserves existing behavior, not
new validation evidence: smoke each exact target before deployment. Nonempty
inbound lists remain restrictive. Disable the legacy flag after review.

Set target/provider-model `allowed_betas` explicitly (default empty). Only the
intersection with caller `anthropic-beta` values is forwarded; provider headers
cannot bypass the list. Known gated fields without their matching permitted
beta return HTTP 400 `unsupported_parameter` naming the field. Beta requests
bypass normalized caching. Adaptive thinking now requires reasoning support.

Claude Code tools can use OpenAI Chat/Responses only with explicit, passed
Anthropic inbound validation and distinct `anthropic_messages: [client_tools]`
evidence, plus upstream tool support. Explicit tool choice needs separate
choice capability on both surfaces. Text success alone does not prove tools.

### Caller-visible validation and continuation

CHAT-09 now rejects present `max_tokens` values that are not integers >= 1
with HTTP 400 `invalid-request` before upstream, on text and tools paths:
`0`, negative numbers, strings and fractional numbers are invalid. There is
no silent truncation or model-specific validation cap.

Native same-dialect Chat preserves `seed`, `n`, sampling controls, user,
all choices (`n>1`), refusal and logprobs. Multiple-choice, refusal, tool-call
and logprob responses bypass the normalized text cache rather than losing
information on replay. This does not promise lossless cross-dialect mapping.

Continuation requests exclude `force_store_false` targets; if none qualify,
the router returns HTTP 400 `continuation_unsupported` before upstream.
Chat-to-Responses always sends full history with `store:false`, never
`previous_response_id`; legacy session config cannot re-enable upstream state.
Streams now disable proxy buffering, reject non-SSE upstream responses with
`502 upstream_protocol_error` and cap SSE lines at 64 KiB
(`upstream_frame_too_large`). After commitment an error terminates the stream.

### Usage writer, database and billing

Back up the usage database and follow the required migration gate below for
schema v6. `cost_list_usd` retains estimated list cost; `cost_billed_usd` is
zero for router-cache hits. Legacy total cost aliases billed cost. Cache
savings are list minus billed; routing savings remain baseline minus billed.
Retained request rows are backfilled, not previously audited rollup snapshots;
unknown cache states retain their historical charge.

OPS-09 queues telemetry without waiting for SQL. The bounded queue holds
10,000 entries and batches up to 256 entries every 250 ms. Configure
`server.usage_db.drain_timeout_ms` (default 10000; zero selects default) and
allow sufficient service shutdown grace. Full/closed queues and failed
transactions drop telemetry rather than delaying inference or retrying forever.
Reports, security telemetry and optional encrypted content captures are now
eventually consistent; poll with a bound rather than reading immediately
after the response. Capture encryption occurs before enqueueing.

Monitor `usage_queue_depth`, `usage_write_dropped_total` and
`usage_write_latency_seconds` (batch SQL duration summary sum/count). SQLite
uses WAL and a 5000 ms busy timeout. The PostgreSQL profile supplies a config
fragment (`config/usage-postgres.example.yaml`) and disposable-database
integration test (`OPS09_POSTGRES_TEST_DSN`); production PostgreSQL still
requires the non-serving migration job. Redis/queue profiles remain deferred.
Test a held database lock locally and verify inference does not wait for it.

### Packages, local setup and security exceptions

Windows amd64/arm64 ZIP packages join Linux tarballs. Compose upgrades use
`METRUM_AI_ROUTER_VERSION`, not the old `SMART_LLMROUTER_VERSION` variable.
Fresh default/local groups use Fireworks DeepSeek Flash 1017 70% / Luna 30%
for eligible Chat, with Responses on Luna. Existing local config and callers
are preserved; explicit regeneration requires `--force` and creates a backup.

Grype exceptions added 2026-10-21 cover the upstream-blocked TypeScript 7 native
`tsc` compiler only, pinned by vulnerability, module version and
`**/node_modules/@typescript/typescript-*/lib/tsc` path. These build-time Go
compiler dependencies are not shipped in the router binary or image; the
exceptions do not waive router runtime findings. Reassess when upstream ships
a compiler built with patched dependencies.

### Media, output formats and hosted tools (W2b #329)

#271 MEDIA-07, ANTH-12 and ANTH-09 are **in v4.2.0**, not deferred.
Targets may declare `modalities: [text, image, document, audio]`; an explicit
list overrides `input_modalities`, while absence retains existing metadata
(default text). Declare only capabilities validated on the exact provider.
Unsupported media returns `400 unsupported_modality`; translated unknown
Anthropic blocks return `400 unsupported_block`, rather than a successful
response with content silently dropped. Native opaque blocks remain native.
Supported document/image parts survive unary, streaming and tool bridges.
Provider file IDs remain provider-owned: capability declarations do not
migrate files between providers.

`structured_output` defaults false and gates Anthropic `output_config.format`
on native as well as translated targets. Set it only after validating the
provider/skin; unsupported formats return `400 unsupported_parameter`.
Anthropic JSON-schema formats map to OpenAI Chat/Responses; existing OpenAI
capability declarations remain supported.

Anthropic server search tools and Responses hosted tools both reject before
upstream with `provider-hosted-tools-forbidden`. Generic search/image descriptors
now reject too instead of being silently stripped; clients must remove them
or use supported client tools. This is not a hosted-tool enablement feature.
For Claude tool-only smokes on non-reasoning translated routes, disable CLI
adaptive thinking explicitly (`MAX_THINKING_TOKENS=0` and
`CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING=1`) or choose a separately validated
reasoning route. Do not infer arbitrary opaque-item or system-role translation
fidelity from document/tool success.

SSE framing now recognizes bare CR, LF and CRLF consistently, even across
reads (#331, fixes #327). Completed bare-CR events do not wait
for another upstream byte; byte limits and incomplete-frame errors remain
in force. Recheck translated tool streams if an upstream emits non-LF endings.
#281 (v5),
#287 follow-up work and #326 remain out of scope; already-merged #289 evidence
is included, not a claim that all #287 work is complete.

For rollback, keep the pre-upgrade config and database backup. Older routers
do not understand `token_hash`; restore compatible caller verifiers before
rolling back. Rehearse schema rollback/restore rather than assuming old
binaries can consume the migrated usage database.

## Caller token_hash and deprecated token_sha256 (v4.2.0)

Caller tokens now use `callers[].token_hash`, a salted PBKDF2-HMAC-SHA3-512
verifier (`pbkdf2-sha3-512:i=<iterations>:<salt>:<key>`). Existing
`token_sha256` callers keep authenticating; the router logs
`caller <id> uses deprecated token_sha256` at startup and on control-plane
reload. `token_sha256` will be removed in a future major release (#281), so
plan the conversion now. No usage-database or control-plane schema migration.

1. Upgrade the router first. Older routers reject `token_hash`, so do not
   write it to a shared config until every replica runs this release.
2. For each legacy caller, either convert the token you still hold with
   `metrum-ai-routerctl callers hash --token-file <file>`, replacing
   `token_sha256` with the printed `token_hash` and keeping `token_id`, or
   rotate it with `metrum-ai-routerctl callers rotate`. A `token_hash`
   caller needs `token_id`, and set exactly one of the two fields. Only
   tokens of the form `<token_id>_<secret>` can be converted in place
   (`callers hash` prints the `token_id` and refuses anything else); any
   other token must be rotated.
3. Check with `metrum-ai-routerctl callers verify --token-file <file>
   --config <config.yaml> --id <caller-id>` and restart the router.
4. `metrum-ai-routerctl callers generate`, `metrum-ai-router-token-gen` and
   the database-mode caller admin API issue `token_hash` by default.

Redis-backed bridge sessions move to a SHA3-512 key layout
(`metrum-ai-router:bridge-session-v2:...`). For one session TTL plus one hour
after startup, the router also reads, writes and deletes the old layout, so
in-flight sessions and mixed-version replicas keep working during a rolling
upgrade. Rolling back within that window keeps sessions; after it, sessions
created by the new version reset on the old one.

## Requested-group model identity and Go 1.26.9 (v4.1.0)

v4.1.0 adds opt-in `server.responses.model_identity: requested_group`, which
reports the requested router group as the caller-facing `model` and redacts
upstream provider and model names from errors. The default (`upstream`) keeps
v4.0.2 behavior. Release binaries are built with Go 1.26.9. The GitHub Release
no longer attaches Docker packages; build them from the tag with
`make package-docker`. No usage-database migration. v4.0.2 ignores
`model_identity`, so a rollback returns callers to upstream model names.

## Concurrent rewrite-mode stability (v4.0.2)

v4.0.2 replaces the abandoned `secure-io/siv-go` AES-SIV library with in-tree
pure-Go AES-SIV-CMAC so `identifiers.mode: rewrite` no longer SIGSEGV under
concurrent streaming tool-call id rewrite. No config change. Existing `mr_`
tokens remain valid. No usage-database migration.

## Identifier rewrite and optional DB config (v4.0.1)

v4.0.1 fixes tool-call identifier decode under `identifiers.mode: rewrite` so
OpenAI Chat `tool_calls[].id` and Anthropic `tool_use` ids round-trip on
ingress. No config change is required for that fix.

Optional: `server.config_source.mode: database` serves canonical config from
the control-plane database. File-mode installs (the default) need no config
or usage-database migration. Do not enable database mode in the same change
window as the package upgrade unless you have already rehearsed import/
activate and rollback.

## Fleet removal and license config (v4.0.0)

v4.0.0 removes the Fleet lifecycle subsystem. These names are gone, not renamed:

- `metrum-ai-router-fleetctl` (including plan, deploy, status, and licenses)
- `metrum-ai-router-fleet-sign`
- packaged rename stubs for those CLIs

Self-managed installs use the binary, Docker Compose, or Kubernetes manifests
with `metrum-ai-routerctl`. Delete systemd units, Compose services, Kubernetes
Jobs, and CI steps that invoke Fleet CLIs before cutover.

`server.license` is rejected at startup. Remove the block. Do not mount
`license.json`. v3.0.0 still accepts the key with one warning; rollback to
v3.0.0 does not require a license file. This release has no usage-database
migration.

## Licensing removal (v3.0.0)

v3.0.0 does not enforce a license and does not read `license.json`.
`server.license` is ignored with one startup warning in this release and will
be rejected in 4.0.0.

These packaged names from the v2.0.0 table below are **removed**, not renamed:

- `metrum-ai-router-license`
- `metrum-ai-router-customer-lifecycle`

Also removed: `metrum-ai-routerctl license` and `metrum-ai-router-fleetctl licenses`.
Fleet plan/deploy/status and `metrum-ai-router-fleet-sign` stayed through the
v3.0.0 licensing removal, and are removed from current packages.

Before cutover, delete systemd units, Compose services, Kubernetes Jobs, and
CI steps that invoke those binaries. License file and license-state backups
matter only if you may roll back to v2.2.0. This release has no
usage-database migration. Drop dashboards and alerts for the removed license
Prometheus series. License volume mounts are unused.

## Breaking rename (v2.0.0)

v2.0.0 packages ship **canonical binaries only**. There are no packaged rename
stub binaries. Update operators and automation before cutover:

| Old packaged name | New packaged name |
| --- | --- |
| `metrum-router` | `metrum-ai-router` |
| `metrum-router-token-gen` | `metrum-ai-router-token-gen` |
| `metrum-router-usage-report` | `metrum-ai-router-usage-report` |
| `metrum-router-migrate` | `metrum-ai-router-migrate` |
| `metrum-routerctl` | `metrum-ai-routerctl` |
| `metrum-genai-smartrouter-fleetctl` | `metrum-ai-router-fleetctl` |
| `metrum-genai-smartrouter-fleet-sign` | `metrum-ai-router-fleet-sign` |
| `metrum-genai-smartrouter-license` | `metrum-ai-router-license` |
| `metrum-genai-customer-lifecycle` | `metrum-ai-router-customer-lifecycle` |

Also removed from packages and the runtime image: `router`, `router-token-gen`,
`router-usage-report`, `router-migrate`, `smartrouterctl`,
`metrum-genai-smartrouterctl`, `metrum-fleetctl`, `metrum-smartrouterctl`,
`metrum-fleet-sign`, and `router-license`.

Release archives and image tags move from `metrum-router-*` to
`metrum-ai-router-*`. Client examples that set `model_provider="metrum-router"`
must use `metrum-ai-router`.

## Required Migration Gate

For a package whose release contract includes a migration, stop or drain the serving router and follow the packaged `docs/DATA_MIGRATIONS.md` runbook: `metrum-ai-router-migrate plan → approved backup → apply → all required data jobs validated → verify → status → serve`. `deployment-job` never applies application schema at startup; it validates the ledger and fails closed until the contract is current, compatible, and every required bound data job is validated—including the post-apply, pre-resume pending state. PostgreSQL production uses this non-serving job, not `auto-safe`; SQLite requires exclusive downtime, SQLite-safe backup and integrity verification, and free-space checks. Checkpoint ordinal `0` is not completion evidence.

Use `--dsn-env=ROUTER_USAGE_DB_DSN` for PostgreSQL and the documented non-serving container `--entrypoint` for Compose. Do not put a database connection string in commands, tickets, screenshots, or logs.

The admin migration report reads the existing ledger without changing it. Run
migration plans and upgrades through `metrum-ai-router-migrate` on the host or
in a non-serving Kubernetes Job; the API cannot execute migrations.

Releases containing usage migration `2026090901` add optional diagnostics
through the non-null text column
`request_usage.target_region` with an empty default for historical and
unlabelled rows. Plan and verify this migration through the same non-serving
gate; do not hand-add or backfill location claims. Package rollback follows
the migration contract and may require restoring the approved pre-migration
database snapshot.

## Docker Compose Upgrade

```bash
docker load -i images/metrum-ai-router-<version>-linux-<arch>.tar

cd compose
cp .env .env.backup
# Pin the image tag env var to the exact loaded tag (metrum-ai-router:<tag>).
# Compose reads METRUM_AI_ROUTER_VERSION. Rename a legacy SMART_LLMROUTER_VERSION
# line if your .env still has one (the package upgrade script does this for you).
docker compose config >/dev/null
docker compose up -d
docker compose ps
```

Validate:

```bash
export ROUTER_BASE_URL="https://<router-host>"
curl -fsS "$ROUTER_BASE_URL/readyz"
curl -fsS "$ROUTER_BASE_URL/version"
curl -fsS "$ROUTER_BASE_URL/docs/" | head
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-routerctl router version
```

## Binary Host Upgrade

1. Extract `metrum-ai-router-<version>-linux-<arch>.tar.gz`.
2. Install the new `bin/metrum-ai-router*` binaries over the previous names
   (or update PATH/service unit ExecStart paths).
3. Diff `config.example.yaml` against the reviewed runtime config; apply only
   approved changes.
4. Run the migration gate when the release contract requires it.
5. Restart the supervised process and validate `/readyz`, `/version`, and one
   authenticated `/v1/models` call.

## Kubernetes Upgrade

Update the Deployment/Job image to `…/metrum-ai-router:<version>-linux-<arch>`
(or the digest from the release inventory). Migration Jobs must use
`/app/bin/metrum-ai-router-migrate`. Confirm the service account, mounts, and
license/config secrets still match the signed intent.

## Rollback

Prefer rolling back to the previous known-good GitHub Release package. For the
v2.0.0 cutover, that is **v1.4.4** (`metrum-router-*` artifact and image names).
Restore the prior service unit / Compose / Kubernetes references and any
pre-upgrade config or usage DB backup required by the migration contract.
Re-check `/readyz` and `/version` before closing the change.

## v5.0.0 caller containment

The new caller containment endpoints require the `admin:containment` / `contain`
Casbin grant in the global `*` domain. Add that narrow grant to the SecOps identity that needs to suspend
or resume keys. Supply a short reason code, not free text. Do not combine this
grant with caller issue, rotation, or revoke unless
the same identity needs those separate operations. The optional signed webhook
is off by default and requires `admin.enabled: true` with
`server.config_source.mode: database`. To enable it, configure
`server.containment_webhook.enabled` and `secret_env`, then provide a 32-byte-or-longer secret through the named
environment variable. See [Admin Authentication](/docs/configuration/admin-authentication)
for headers, signing input, timestamp bounds, and CLI examples. A suspended
caller with a TTL becomes active when the TTL expires; no database migration is
required.
The containment role can resume only keys it suspended; keys suspended by an
administrator or by legacy configuration require all caller write permissions
(issue, rotate, and revoke) to resume.
Signed webhook callers do not need an ETag or idempotency header. Their nonce
reservation is durable for at least twice the configured timestamp window and
expired reservations are pruned hourly.

## Provider usage reconciliation

Daily report-mode reconciliation is disabled by default. To enable it, configure
`server.provider_usage_reconciliation`, the usage database, and
`server.admin_reports.security.enabled`. OpenAI and Anthropic require a
read-only usage API credential environment reference using the
`ROUTER_PROVIDER_USAGE_` prefix and a provider key ID. Keep the value in the
deployment secret environment; config stores only the reference.
Bedrock uses CloudWatch metrics in its configured AWS region and compares at
account scope; its USD total is estimated from the router model catalog prices.
The job runs at 06:00 UTC by default, supports bounded restart catch-up with
`max_catch_up_days`, and waits for provider `data_refreshed_at` watermarks when
available. Use one Bedrock entry per AWS account and region to avoid counting
the same CloudWatch metrics more than once.
See [Provider usage reconciliation](../security-governance/provider-usage-reconciliation.md)
for configuration and operating details.

## MCP gateway

MCP is disabled by default, so existing v4.8 configurations continue to load
without changes. To enable it, add an `mcp` block, set each server's explicit
`allow_hosts`, configure server-side credential environment variables with
the `ROUTER_MCP_` prefix, and enable the usage database for durable tool
invocation audit records. MCP has a per-caller request-rate limit and supports
per-caller and per-tool request and estimated-spend limits. See the
[MCP gateway guide](../configuration/mcp-gateway.md) for configuration fields
and policy behavior.

Automatic detector containment is a separate opt-in feature. It is off by
default. Before setting `server.auto_containment.mode` to `shadow` or
`enforce`, run `router-migrate` to create the automatic action tables. Start
in `shadow` mode, set a low `max_actions_per_minute`, and list emergency
caller IDs under `exempt_callers`. Throttle rules must set
`unlimited_throttle_limit` to provide a finite base for callers with unlimited
configured quotas. Enforced suspend rules require database config mode and
are recorded as containment-made so the containment role or an administrator
can resume them before their TTL. Actions expire at their configured TTL.

## Security event stream

The OCSF 1.4.0 JSON Lines stream is disabled by default. To enable it, set `server.security.events.enabled`, choose stdout or file output, and optionally configure an HTTPS webhook with `webhook_allow_hosts` and a 32-byte-or-longer secret supplied through the environment. Webhook signatures cover the timestamp, a random nonce, and the exact body; receivers must enforce a timestamp window and reject nonce replay. Detector signals and failures use the configured detector ID and bounded signal metadata; evidence request IDs use the finding UID, capped at 256 characters. Containment events use Account Change Disable/Enable and include the target caller and acting principal. Database-backed changes to this config require a router restart. Firewall-hold (#891) and honeytoken (#884) source integrations follow when those PRs merge; keep #885 open until then. See [Security event stream](../security-governance/security-events) for full verification and queue behavior.
The prompt-injection detector is an optional external example. It is off unless a detector is configured, and it receives no segment text unless its own `include_segments` list explicitly opts in. Prompts and user messages are never sent in v5.0.0.

## v5.0.0 admin client identities

Registering any credential makes its policy subject managed immediately. Only
credentials registered to that subject authenticate; all unregistered credentials
for it are refused from that point onward, including before disable, revoke, or
expiry. Ensure registration leaves at least one usable global administrator.

The router-config migration `2026111610` advances schema 16 to 17 and adds the admin client and
credential-binding tables and extends credential lifecycle audit records with client ID and actor. Apply it with the
packaged non-serving migration job before starting serving replicas; follow the backup, apply,
verify, and status steps in [Data Migrations](https://github.com/metrum-ai/router/blob/main/docs/DATA_MIGRATIONS.md). Client endpoints return
unavailable until the migration is applied. Bindings are checked against the active configured
admin identity, including its subject and stable mTLS SAN or SPIFFE identity. Disabled/deleted
clients and revoked/expired credentials lose access. Re-register replacement credentials to restore
a disabled client; deleted IDs cannot be re-enabled, so create a new ID.
