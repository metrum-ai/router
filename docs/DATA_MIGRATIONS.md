# Data migration framework

This is the canonical operator runbook for durable usage-data migrations. `router-migrate` is the non-serving entrypoint for inspecting migration state and applying reviewed work. It opens the selected usage database without starting the router and emits only safe scalar metadata. Routine deployment guidance links here rather than reproducing a second migration procedure.

## Operator visibility and release contract

Metrics-admin callers can scrape aggregate migration schema/data version, compatibility, pending count, and data-job state. If the serving process cannot read the migration ledger, the authorized scrape remains available and emits the bounded `smart_llmrouter_migration_status_available{scope="usage"} 0` signal while preserving the router, license, traffic-shaping, and build families; it exposes no database error text, SQL, or connection detail. The authenticated **Operations / Data migrations** browser report exposes the same safe contract plus release, execution, maintenance, lock, timeout, rollback, duration, and safe error-class metadata. For a schema migration bound to a data job, its effective report state is derived from the durable job only after the schema ledger is `applied`: a missing, pending, paused, or cancelled job is pending/not-validated; running is in-progress; failed is failed; and only validated is applied/verified. A ledger `failed` or `running` state remains authoritative (with validation `failed` or `in-progress`) even when a job is absent or reports a conflicting state. The report retains scalar data-job state and aggregate progress so neither an applied schema row nor contradictory job paperwork can falsely certify unfinished work. Both surfaces are strictly read-only: they contain no SQL, DSNs, raw configuration, credentials, request content, or migration controls. `/metrics` requires a metrics-admin caller; the browser report requires authenticated browser-admin identity plus `admin:reports` `read`. Ordinary caller tokens cannot use either surface as a migration control plane. Apply/retry/recovery remains the non-serving CLI deployment-job workflow with the recorded backup/recovery evidence required by the migration definition.

## Required deployment-job gate

Before a fresh serving startup or package upgrade using `migration_policy: deployment-job`, run **plan → approved backup → apply → all data jobs → verify-serving → status → serve** while the router is stopped or drained. A deployment job owns the database change; the serving process only validates the compatible ledger. `auto-safe` is not a production deployment procedure.

When a Compose PostgreSQL usage schema cannot be adopted and the operator chooses to discard historical usage rather than repair the live catalog, do not hand-delete volumes. Use `scripts/compose_clean_cutover.py` (`plan`, then `apply --confirm-reset-usage reset-postgres-data`) as documented in `docs/DOCKER_DEPLOYMENT.md`. That CLI restic-archives a `pg_dump`, preserves caller config/tokens/Caddy, recreates only the Compose `postgres_data` volume, and runs this document's PostgreSQL deployment-job gate on the empty database before serving.

New generic Compose/Kubernetes installations use SQLite at `/app/state/usage.sqlite` and run:

```sh
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=plan --driver=sqlite --db=/app/state/usage.sqlite --json
# Take and approve one offline atomic copy/snapshot of usage.sqlite and any -wal/-shm sidecars.
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router --action=apply --driver=sqlite --db=/app/state/usage.sqlite --json
```

Version-check the same packaged runner before this gate. Complete every data job named by the release contract; do not start serving if plan, apply, or any job is incompatible, pending, running, paused, cancelled, failed, or unrecognized. `verify-serving` runs schema postconditions and fails unless the ledger is current/compatible and every bound data job is validated. PostgreSQL is explicit: use `--driver=postgres --dsn-env=ROUTER_USAGE_DB_DSN` with a deployment-owned DSN.

### Checkpointed data-job procedure

`router-migrate --action=resume` executes **one** checkpoint. Its ordinal is a durable idempotency key, not an estimate of job completion. The shipped `historical-usage-validation-v1` handler reads at most 100 usage rows per checkpoint; therefore ordinal `0` is not evidence that a database with 100 or more rows is ready to serve.

For every release-defined job, start at ordinal `0`, then use only the safe job state from the returned `status --json` document to decide the next action:

| Safe job state | Operator action |
| --- | --- |
| `running` | Honor the job's configured throttle, then run exactly the next ordinal (`n + 1`) when the runner permits it; inspect `status --json` again. If a throttle response occurs, wait and retry that same next ordinal—do not skip ahead. |
| `validated` | The job is complete. Continue only after every required job is `validated`. |
| `pending`, `paused`, `cancelled`, `failed`, `incompatible`, absent, or any unrecognized result | Stop. Do not advance the ordinal or start serving. Follow the recorded recovery/backup procedure; retry requires the declared safe recovery-evidence reference. |

For the current package, the controlled repeat is:

```sh
# n starts at 0. Run one checkpoint, then inspect only the returned safe status.
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router \
  --driver=sqlite --db=/app/state/usage.sqlite --action=resume \
  --job=historical-usage-validation-v1 --checkpoint-ordinal="$n" --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router \
  --driver=sqlite --db=/app/state/usage.sqlite --action=status --json
```

Record the ordinal and safe state in the approved change record. If the job state is `running`, set `n` to the next integer and repeat the two commands. If it is `validated`, continue with the final non-serving checks:

```sh
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router \
  --driver=sqlite --db=/app/state/usage.sqlite --action=verify-serving --json
docker compose run --rm --no-deps --entrypoint /app/bin/metrum-ai-router-migrate router \
  --driver=sqlite --db=/app/state/usage.sqlite --action=status --json
# Only after compatible/current final status and every required job is validated:
docker compose up -d
```

Do not infer an ordinal from a row count, cursor, prior release, or a successful command alone. A repeated existing ordinal is safe and returns its durable result; it does not run the checkpoint again. Advance only after the observed result is `running`. The JSON status contains safe aggregate job fields, not a permission to copy database contents into the change record.

## Backup, restore, and rollback

Package rollback never runs a reverse migration. Read the release migration contract before changing a package: if it is `restore-required`, restore the approved pre-migration database snapshot before deploying the earlier package. If it is compatible without restore, preserve the usage database and deploy only the approved package/config rollback. A ledger records history; it does not provide time travel as a service.

For PostgreSQL, take and verify a consistent logical or storage snapshot with the approved backup system before `apply`; retain its safe identifier in the change journal. Confirm restore permissions, backup storage, target free space, database-version compatibility, and a rehearsed restore path before the maintenance window. Restore only into the approved recovery target, then run `router-migrate --action=verify` and `--action=status` before serving traffic.

For SQLite, schedule exclusive downtime, stop every router and migration process, use an SQLite-safe backup method, verify integrity with approved SQLite tooling, and confirm source and backup free space before applying work. Restore while the database remains exclusively offline, verify integrity again, then run the same verify/status gate. SQLite is not a multi-replica migration option.

On file-owned installs, `metrum-ai-routerctl usage backup` and `usage restore`
perform an SQLite-safe copy of the configured `server.usage_db.path` plus `-wal`/`-shm`
sidecars when `--confirm-offline` is set. They refuse PostgreSQL and point operators at
this document. CLI backup/restore is an approved SQLite method; it does **not** replace
the `router-migrate` plan/apply/verify-serving gate.

Each release must publish its migration contract: migration ID and scope, online or maintenance execution mode, lock/timeout class, data-job requirement, backup evidence requirement, compatible schema/data window, and rollback class. Package rollback never performs a reverse migration; follow the release-specific restore requirement.

The target-region diagnostics increment adds usage migration `2026090901`
(`usage`, schema version 4). It adds the scalar, non-null
`request_usage.target_region` column with an empty default. The migration is
transactional, online, bounded, has no data job, and is `restore-required` for
package rollback. Existing rows remain empty; new requests record only the
deployment-defined region of the target that actually served the request.

The cached-input pricing increment adds usage migration `2026091301`
(`usage`, schema version 5). It adds nullable
`request_usage.cached_input_tokens` and
`request_usage.cached_input_price_per_million_usd`. The migration is
transactional, online, bounded, has no data job, and is `restore-required` for
package rollback. Historical rows keep null cache evidence rather than inventing
prompt-cache savings.

```sh
router-migrate --driver=sqlite --db=/app/state/usage.sqlite --action=verify-serving --json
router-migrate --driver=sqlite --db=/app/state/usage.sqlite --action=status
# Explicit PostgreSQL substitution:
router-migrate --driver=postgres --dsn-env=ROUTER_USAGE_DB_DSN --action=verify-serving --json
```

Stage 3 activates the framework's normalized scalar job records without putting a backfill in a router request path or startup. A checked-in data-job definition is bound to an applied immutable migration by scope, migration ID, data version, key, execution and validation mode, bounded throttle, restart-safe declaration, and handler identity. `schema_data_jobs` records only aggregate counters and safe state; `schema_data_job_checkpoints` holds bounded scalar shard/range/cursor progress. No JSON, SQL values, DSNs, request content, credentials, or configuration is recorded.

The non-serving runner processes one checkpoint per `resume` invocation. A checkpoint ordinal is a durable idempotency key: repeating an already-recorded ordinal returns its existing job result without rerunning the handler, overwriting its scalar progress, or adding aggregate counters. A job is `pending`, `running`, `paused`, `cancelled`, `failed`, or `validated`; cancellation is observed only between checkpoints. A failed or cancelled job never resumes implicitly: it needs an explicit retry with a bounded recovery-evidence reference. On PostgreSQL, each checkpoint's handler, durable checkpoint/job updates, and cleanup share the physical session that owns the scoped advisory lock; SQLite retains its single-connection exclusive-maintenance behavior. A migration's data version advances only after its job's checked-in validation succeeds, so a schema expansion cannot impersonate a completed historical conversion. This is intentionally useful even when a release has no active usage backfill; bureaucracy has finally learned to distinguish a table from its data.

Durable signed quota and license state use the common versioned integrity envelope and an owner-only atomic-write contract. Each update writes through a private temporary file and retains one private last-known-good `.bak` file. A missing primary can recover from that backup; a present but invalid primary is never masked by it and fails integrity validation. The legacy unsigned import remains an explicit one-time migration under its documented operator control, after which the signed versioned format is written. Do not edit either state file or its backup by hand.

Applied ledgers bind both checksum and canonical manifest digest. Unknown future IDs, checksum changes, and a bound digest mismatch are incompatible and fail closed. Every atomic failure is recorded after rollback as a bounded failed ledger/attempt record with a fenced owner generation; it does not retain SQL, DSNs, values, or request data. `ApplyPending` allows only transactional online migrations. `ApplyMaintenancePending` is an explicit non-serving operation for transactional maintenance definitions after the online prefix has completed. `ApplyNonTransactionalMaintenancePending` is separately required for a reviewed non-transactional definition such as a PostgreSQL concurrent index build: it commits a durable `running` record before the operation, marks a safe `failed` state on error, and marks `applied` only after its postcondition verifies. Neither path belongs in router startup.

The lock ledger is a durable, scope-level fenced lease. PostgreSQL pins a session advisory lock to the non-serving runner connection, verifies a supported server version, and applies the configured bounded lock and statement timeouts before advisory ownership and fenced generation/lease DML, as well as during migration work. Those settings are reset before the pinned connection returns to its pool. A lock/statement timeout returns only a safe acquisition or migration diagnostic; it is a recovery signal, not an invitation to rerun arbitrary DDL. Inspect safe status, preserve the approved backup evidence, and resume only through the declared recovery procedure. SQLite is explicitly a single-process maintenance option: the runner performs integrity and exclusive-lock checks, but operators must first take and retain a file backup, confirm free space for the copy and journal, stop other router processes, and schedule downtime. It provides no multi-replica or zero-downtime SQLite migration promise; the committee has declined to issue one on behalf of physics.

For a maintenance action, use a bounded non-secret backup reference rather than putting a filesystem path or database detail in a ticket:

```sh
router-migrate --driver=sqlite --db=usage.sqlite --action=maintenance \
  --backup-evidence-ref=change-approval-20260805
router-migrate --driver=postgres --dsn-env=ROUTER_USAGE_DB_DSN \
  --action=non-transactional-maintenance \
  --backup-evidence-ref=change-approval-20260805
```

`router-migrate` rejects a maintenance command without that reference. For a transactional maintenance definition, the normalized applied-attempt row (including that bounded reference) and the ledger's `applied` transition commit together; an audit-write failure rolls back both the migration work and the transition. For a non-transactional definition, the runner first durably records `running` with the bounded reference, then records the applied attempt and `running` → `applied` transition together after the checked-in postcondition succeeds. A crash or final-audit failure after independently committed work therefore remains `running` or `failed`, never falsely `applied`; inspect the safe status and use the declared recovery procedure rather than rerunning arbitrary DDL. The command does not create a backup or perform a restore; backup/restore approval remains an operator action. Package rollback never runs reverse migrations.

The usage manifest starts with immutable, transaction-safe explicit baseline creation/adoption (`2026071901`, schema version `1`, data version `0`), followed by online reasoning-telemetry expansion (`2026072301`, schema version `2`) and historical usage validation (`2026080501`, data version `1`). The baseline creates missing checked-in usage tables and indexes for a fresh install, or adopts an existing schema only after strict verification. The online expansion adds nullable `reasoning_tokens` fields and scalar request coverage counters; the Stage 3 migration records the checked-in historical-validation data-job ledger entry. Verification remains strict about scalar defaults, including on PostgreSQL: its catalog spelling of a quoted text literal with text-type casts (for example `''::text`) is accepted as the equivalent configured literal, but functions, operators, non-text casts, and different literal values fail closed.

Both later migrations are `restore-required`; package rollback never runs a reverse migration. For `2026072301`, a pre-expansion package rejects the newer ledger entry under `validate` or `deployment-job`. For `2026080501`, a package that predates Stage 3 likewise rejects the unknown ledger ID. The corrected package has one bounded exception in the forward direction: it accepts only the exact original Stage 3 manifest digest already recorded by the original Stage 3 package, with the same migration ID, checksum, and handler contract. This compatibility correction neither makes `2026080501` package-only nor authorizes a downgrade. When the release contract requires a rollback, restore the approved pre-migration database snapshot before deploying the earlier package; the ledger is a record, not a time machine. Run `router-migrate --action=plan` first, take the approved backup, then run `--action=apply` as the non-serving deployment job. `--action=verify` rechecks applied postconditions without changing application tables. Future schema and data changes must add immutable definitions, use expand/migrate/validate/contract phases, and state the package/config/database rollback class in release notes.

`server.usage_db.migration_policy` controls serving-process behavior. `deployment-job` is the default. `validate` and `deployment-job` never apply application-schema DDL or run data jobs at startup; both require a current, compatible ledger and fail startup otherwise. `auto-safe` applies only checked-in transactional online definitions and is intended only for a reviewed small/single-node SQLite deployment. On a fresh SQLite database, after `ApplyPending` it may validate exactly ordinal `0` of the synthesized pending job when that checked-in job is explicitly marked zero-row-safe, and opens only when that checkpoint validates with zero scanned, updated, skipped, and failed rows. While that synthesized checkpoint still requires validation, existing usage rows fail closed. It never resumes, retries, loops through batches, bypasses validation, or accepts a pre-existing durable `pending`, `running`, `paused`, `cancelled`, `failed`, or unrecognized job state; those states use the non-serving deployment-job workflow. When the required job is already `validated`, restart skips the zero-row preflight and preserves the durable checkpoint result and counters, including after usage rows accumulate. For PostgreSQL production, use a non-serving deployment job and `deployment-job` (or `validate` after that job); do not rely on a serving replica to perform a migration.

The CLI intentionally accepts a PostgreSQL DSN only through a named environment variable, never a command-line flag. For a checked-in data job, use `--action=resume --job=<key> --checkpoint-ordinal=<n>` for one bounded checkpoint, `--action=cancel --job=<key>` to request checkpoint-bound cancellation, or `--action=retry --job=<key> --recovery-evidence-ref=<safe-reference>` after recovery. Its ledger and output exclude SQL values, DSNs, raw configuration, request content, images, tool payloads, tokens, hashes, provider credentials, and payment data.
