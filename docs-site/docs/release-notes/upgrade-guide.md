---
title: Upgrade Guide
doc_type: reference
---

# Upgrade Guide

Use this guide for customer-managed package upgrades. The exact maintenance window, approval process, and rollback policy belong to the deployment operator.

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

## Compatibility and operations (v4.3.0)

### Migrations and rollback

Back up and rehearse restore before upgrading. Use the **Required Migration
Gate** below, including the non-serving PostgreSQL migration job; do not bypass
the ledger or manually add columns. Usage migrations `2026102202` (schema v7),
`2026102301` (v8), and `2026102302` (v9) retain provenance, cache-write usage and
pricing, and unbilled count-event identity respectively. The configuration
control plane has its own `2026102301` cache-write pricing migration (v7);
identical IDs in separate scopes are not the same migration.

Historical provenance remains empty/unknown. New estimated rollup counters
start at zero, not a reconstruction of historical estimates. Historical nullable
cache-write counts/prices remain unknown. Historical count calls cannot be
reliably distinguished from inference and are not retroactively refunded.
These expansions require restoring the pre-upgrade snapshot for binary rollback.
Retain the reviewed old configuration as well as the database backup.

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

### Optional upstreams and fine-tuning

Bedrock is opt-in: use `auth_scheme: bedrock`, internal `dialect: openai-chat`,
and a target `region` (or the AWS SDK region chain). Use the default AWS
credential chain, never keys in YAML; grant only the validated model/profile
resources. Cross-region profiles require destination permissions and a residency
review. Existing providers need no Bedrock configuration. See
[Providers And Models](../providers-models/overview) for the validated matrix.

The fine-tuning example is an operator-owned training/deployment recipe, not
a default routing change or a quality promotion. Review evidence and the
outcome gate before using an adapter; enforce rental spend limits and verify
teardown independently of token billing.

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
   the database-mode caller admin API now issue `token_hash` by default. Use
   `--hash-format legacy-sha256` only when issuing tokens for older routers.

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
