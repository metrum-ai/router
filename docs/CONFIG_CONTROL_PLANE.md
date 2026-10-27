# Configuration control-plane foundation

Phase 1 introduces a migration-scoped, relational configuration projection for
future managed deployments. It is not enabled by router startup and does not
replace `config.yaml`; YAML remains the only production configuration source
in this phase.

Phase 6 adds the optional DB-backed serving mode. When
`server.config_source.mode` is `database`, the active configuration set stored
in the `router_config` scope is the serving source of truth. YAML is then an
import/export and bootstrap artifact only: it seeds a new deployment, moves a
reviewed document between environments, and provides an offline recovery
input, but the running router does not read it for serving decisions. When
`server.config_source.mode` is `file` (the default), nothing in this section
applies and YAML remains the only serving source.

The `router_config` migration scope creates scalar, foreign-keyed records for:

- versioned configuration-set metadata and one active set per runtime scope;
- server settings, providers, provider headers and model catalog metadata;
- one required scalar capability record per provider model, plus normalized
  rows for tool support, input/output modalities, supported inbound dialects,
  unsupported request features, and explicit dialect bridges;
- model groups and ordered weighted targets; and
- caller identities, token hashes, rate limits, and allowed groups.

Raw provider credentials and raw caller tokens have no column in this schema.
Provider credentials remain environment/secret references; caller rows retain
only an existing SHA-256 token hash and public token ID.

The database enforces the provider-header boundary on both inserts and
updates. Only `HTTP-Referer`, `User-Agent`, and `X-Title` metadata headers are
representable; standard or provider-specific credential headers are rejected.
Each allowed header name is unique case-insensitively for a provider, so a
configuration set cannot contain conflicting casing variants. Values in these
metadata fields must remain non-secret. Use `api_key_env` or a
deployment-managed secret reference for every upstream credential.

The read path independently rejects case-insensitive duplicate provider-header
names. That guard remains active when a database has only the phase-2 prefix or
an unavailable migration ledger, so header-map iteration can never select an
ambiguous upstream value. Phase 3 verifies the named unique expression index
semantically on both SQLite and PostgreSQL: it must be unique and contain
exactly `config_set_id`, `provider_name`, and `LOWER(header_name)` in that
order.

The active-set uniqueness boundary is verified semantically on SQLite and
PostgreSQL, not only by index name. The named index must be unique, have
exactly the `runtime_scope` key, and use the partial predicate `status =
'active'`; a drifted index cannot make a configuration scope appear current.

`LoadActiveConfigFromDB` is deliberately read-only. It loads only a validated
active set and reuses `Config.Validate`; missing active state and invalid
relational data fail closed. It reads at most two matching active rows and
refuses to choose when integrity drift leaves more than one validated active
set in a runtime scope. For a provider with `api_key_env`, it resolves the
named deployment environment variable only into the returned in-memory runtime
configuration; it never writes or logs that value. An unset reference remains
unconfigured so catalog-only providers can stay cataloged without an entitled
credential. PostgREST, Caddy, and secret persistence remain later issue #7
phases; the write APIs and activation/rollback actions landed in #395 (see
"Phase 8" below), and hot reload is the phase-2 poller/notifier.

## Provider-model capability projection

Migration phase 4 preserves provider-model eligibility metadata that an active
`model_ref` target inherits through the existing typed config resolver. The
one-to-one `router_config_provider_model_capabilities` record stores scalar
metadata: image pricing, tier/rate/cost, reasoning controls, nullable
`honors_max_tokens`, `force_store_false`, output-token encoding, request-size
limits and validation status/notes, and legacy Responses-to-Chat bridge flags.
The following child tables hold each repeated value as an independently
foreign-keyed row:

- `router_config_provider_model_tool_support`;
- `router_config_provider_model_modalities`;
- `router_config_provider_model_request_inbound_dialects`;
- `router_config_provider_model_request_unsupported_features`; and
- `router_config_provider_model_bridges`, including non-secret stateful Redis
  address and environment-reference fields.

The phase has no JSON, JSONB, array, packed list, raw provider key, or raw
Redis credential column. A provider model requires exactly one scalar
capability record. The migration backfills an all-default record for every
existing phase-3 catalog row; a newly inserted model without that record makes
`LoadActiveConfigFromDB` fail closed. Add the model, its scalar capability
record, and child rows in one reviewed write transaction, then run `Verify`
before any later activation workflow exists.

This bounded projection supports catalog-derived metadata only: capability
values flow to targets that use `model_ref`. Target-specific capability
overrides, traffic-shaping overrides, write APIs, import/export, and
activation remain outside this phase and must not be represented with ad hoc
columns or packed values. Until a later #7 phase adds typed target override
records and an activation workflow, keep those overrides in the reviewed YAML
bootstrap path.

Migration phase 5 adds nullable
`router_config_provider_models.cached_input_price_per_million_usd` so the
catalog projection can retain optional cached-input pricing. Omit the value
when unknown; store `0` when cache reads are known free. `LoadActiveConfigFromDB`
maps the scalar into `ProviderModel.CachedInputPricePerMillionUSD`. Historical
catalog rows keep SQL NULL rather than inventing cache pricing. Phase 5 is a
transactional **maintenance** migration in the same reviewed batch as phases
2 through 4.

For migration tests, use `ConfigControlPlaneMigrationRunner` with a dedicated
database. Do not point it at a production usage database until a reviewed
deployment migration and backup/restore procedure are available. The initial
table creation is an online migration, but the provider-header hardening,
capability projection, and cached-input catalog price phases are transactional
**maintenance** migrations: PostgreSQL must lock the existing header table
while adding the allowlist constraint and building the unique expression index,
while SQLite rebuilds that table. Phases 4 and 5 are bundled into the same
reviewed maintenance batch so operators do not leave a current schema with
catalog rows that lack required capability identities or silently drop cached
pricing. `ApplyPending` stops before those phases. A non-serving deployment
job must first apply the online prefix, take the approved backup, then
explicitly invoke `ApplyMaintenancePending` in a scheduled maintenance window
and finish with `Verify`; it must never be run by router startup.

## Phase 6: DB-backed serving source of truth

Phase 6 raises the `router_config` compatibility window to `MaxSchema: 6` and
adds three **online** tables through `ConfigControlPlaneMigrationRunner`:

- `router_config_documents` stores the canonical configuration document for
  each versioned set. The relational projection remains the queryable form;
  the document is the reviewed, round-trippable representation used for
  import, export, diff, and audit.
- `router_config_runtime` records which document is active per runtime scope
  and the activation/rollback lineage, so a restart or a second replica
  resolves the same active set without guessing from history rows.
- `router_config_change_events` is the append-only audit log for import,
  activate, rollback, and caller issue/rotate/revoke actions. It records
  actor, action, target identifiers, and safe scalar metadata only — never
  raw tokens, provider credentials, DSNs, or request content.

All three are online migrations: `ApplyPending` applies them without a
maintenance window, and no serving downtime is required to add them to an
existing phase-5 schema.

### Canonical document and relational projection

The configuration set has two coordinated representations:

1. The **canonical document** in `router_config_documents` is the complete,
   versioned, human-reviewable form. It is what `import` accepts, what
   `export` emits, and what a change review diffs.
2. The **relational projection** in the phase-1 through phase-5 tables is the
   normalized, foreign-keyed form the router reads on the serving path via
   `LoadActiveConfigFromDB`.

Import writes both representations in one reviewed transaction; activation
flips `router_config_runtime` to point at the new document. The serving read
path stays relational and read-only; the document is never parsed per
request.

### Secrets boundary

The DB-backed mode does not change the secrets contract:

- Provider credentials remain `api_key_env` environment/secret references.
  `LoadActiveConfigFromDB` resolves the named variable only into the returned
  in-memory runtime configuration and never writes or logs the value.
- Caller tokens are stored only as a verifier plus the public `token_id`.
  New and rotated callers use `token_hash` (salted PBKDF2-HMAC-SHA3-512);
  legacy callers may still carry deprecated `token_sha256`. Both forms share
  the existing `router_config_callers.token_sha256` column, distinguished by
  the self-describing `pbkdf2-sha3-512:` prefix, so no schema migration is
  needed. Raw caller tokens have no column in any `router_config` table
  and are never stored in `router_config_documents` or
  `router_config_change_events`.

### Import, export, activate, rollback

The intended operator flows, exposed through the admin API and the
`metrum-ai-routerctl` CLI at a high level, are:

- **Import** — validate a YAML or canonical-document payload, write the
  canonical document to `router_config_documents`, project it into the
  relational tables, and record an `import` change event. Import does not
  activate; the running set is unchanged.
- **Export** — read the canonical document for a specified set (or the active
  set) and emit it as YAML for review, backup, or transfer. Export is
  read-only and never includes resolved secrets.
- **Activate** — atomically repoint `router_config_runtime` for a runtime
  scope at an imported, validated set, record an `activate` change event, and
  have serving replicas pick up the new active set on their next load.
- **Rollback** — repoint `router_config_runtime` at a previously active set,
  record a `rollback` change event, and leave the superseded document intact
  for audit. Rollback is a runtime-pointer change, not a reverse migration.

Exact CLI subcommand names and flags are owned by the CLI phase and follow the
existing `metrum-ai-routerctl` style; this document intentionally does not
specify them.

### Caller issue, rotate, and revoke against the active set

When `server.config_source.mode=database`, caller lifecycle operations act
directly on the active relational projection and take effect without a
router restart or a YAML edit:

- **Issue** — insert a new caller row with its `token_hash`, public
  `token_id`, owner/project references, rate limits, and allowed groups, then
  record an `issue` change event. The raw token is shown to the operator once
  and never stored.
- **Rotate** — insert the replacement caller row, mark the prior row
  `rotated`, and record a `rotate` change event.
- **Revoke** — mark the caller row `disabled`, `suspended`, or `expired` and
  record a `revoke` change event. Revocation is effective on the next
  authenticated request; there is no config reload step.

These operations are available through the admin API and the
`metrum-ai-routerctl callers` family. They are only meaningful in DB-backed
mode; in file mode, caller changes remain a YAML edit plus reload/restart as
documented in `docs-site/docs/operations/key-generation.md`.

## Phase 8: Admin API write surface (#395)

Phase 8 delivers the control-plane write APIs as authenticated Admin API
endpoints (`/admin/config/*`), protected by the dedicated admin credential
from #350 with the fine-grained permissions `config:read`, `config:write`,
`config:activate`, and `config:force`. In file mode every write returns `409
config_source_mode_file` and reads continue to work from the live
configuration.

Group writes (`PUT/POST/DELETE /admin/config/groups/{group}/targets…`,
`POST …/rollback`) are copy-on-write: the active canonical document is
loaded, patched for the one group, validated (`Config.Validate` plus a
per-target catalog/contract eligibility report), imported as a new set, and
activated in one transaction. The guarded `router_config_runtime` update
makes concurrent writers safe (exactly one wins; the loser gets `409
config-revision-conflict`); `If-Match` carries the revision precondition and
`Idempotency-Key` makes retries return the recorded result without a second
set. `dry_run` and `POST …/targets:validate` run every check without
persisting anything. A change that would empty a group or leave a contract
API shape without an eligible target is refused, and the optional
`server.config_source.require_validation_status: passed` policy refuses
unvalidated targets unless the caller holds `config:force` and sets `force`.

The generic set flow is available over HTTP as well:
`POST /admin/config/sets` (import a canonical document as a draft),
`GET /admin/config/sets[/{set_id}]` (list/export; export never includes
resolved secrets), `POST /admin/config/sets/{set_id}/activate`,
`POST /admin/config/rollback`, and `GET /admin/config/diff?from=&to=`.
`GET /admin/config/status` reports the runtime scope, driver, active set,
current revision, and this replica's loaded revision.

The phase-8 migration (online, transactional, idempotent) extends
`router_config_change_events` with `group_name`, `prev_config_set_id`,
`reason`, `forced`, `idempotency_key`, `request_hash`, and `detail`, adds
`router_config_change_event_targets` (the before/after ordered target lists
as one foreign-keyed row per target — no packed or JSON audit values), and
adds a partial unique index making an idempotency key usable at most once
per runtime scope. Group history is served from these tables at
`GET /admin/config/groups/{group}/history`. Audit rows never contain raw
tokens, provider credentials, or request content.

Propagation: the writing replica reloads synchronously after commit; other
replicas converge through the phase-2 revision poller (SQLite,
`configControlPlaneReloadInterval`) or the PostgreSQL notifier. In-flight
requests finish on the target they started with; the group name and
`/v1/models` identity never change across a swap.

### Expected propagation window (Refs #411)

- **SQLite:** replicas converge through the revision poller at
  `configControlPlaneReloadInterval` (2 s). Worst-case propagation is one
  poll interval plus a reload; expect about 2–4 s.
- **PostgreSQL:** activation sends `NOTIFY router_config, '<runtime_scope>'`
  after commit. Every replica LISTENs on that channel on a dedicated
  connection (`StartConfigRevisionNotifier`) and reloads on a matching
  payload, so replicas converge in about 1 s (measured by
  `TestConfigRevisionNotifierReloadsOnActivate` and the `e2e/admin-api`
  multi-replica notify-window cases, which assert <= 1.5 s on PostgreSQL).
  Notifications whose payload names another scope are ignored.
- **Fallback:** if the listener connection cannot be opened at startup the
  router logs a line and starts anyway, and if the connection drops later
  the notifier goroutine logs and exits — the revision poller keeps
  running in both cases, so PostgreSQL replicas still converge within the
  poll window (covered by
  `TestConfigRevisionNotifierPollerFallbackAfterNotifierStop`).

## Phase 9: shadow runs (#395 §5)

Phase 9 adds `router_config_shadow_runs`, the shared registry behind
`POST/GET/DELETE /admin/config/groups/{group}/shadow[/promote]`. A shadow run
puts one catalog candidate in shadow against a group's served traffic at a
bounded sample rate (`sample_rate`, with `max_requests`, `duration_seconds`,
and `cost_cap_usd` bounds; one active run per group). Sampled requests are
replayed to the candidate off the caller's critical path — non-streaming,
no caller identity, no request content retained — and fold into aggregate
counters (requests, errors, latency, tokens, estimated cost) on the run row.
`GET …/shadow` compares those counters with the group's served baseline from
the existing usage telemetry for the run window.

Lifecycle changes bump the runtime revision (poller/notifier propagation,
same as config writes) and are audited as `shadow_start`, `shadow_stop`, and
`shadow_promote` change events; promotion goes through the copy-on-write
group write path and its event links the run via `detail=shadow_run=<id>`.
The migration is online, transactional, and idempotent, matching phase 8.
