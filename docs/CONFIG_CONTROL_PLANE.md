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
credential. Runtime DB mode, YAML import/export, write APIs,
activation/rollback actions, PostgREST, Caddy, secret persistence, and hot
reload remain later issue #7 phases.

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
- Caller tokens remain SHA-256 `token_sha256` hashes plus the public
  `token_id`. Raw caller tokens have no column in any `router_config` table
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

- **Issue** — insert a new caller row with its `token_sha256`, public
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
