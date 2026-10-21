---
title: Cache And Usage Store
doc_type: reference
---

# Cache And Usage Store

The cache, usage database, decision telemetry, diagnostics, content capture, and retention settings determine what the router stores about requests. These features must remain safe for operations, reports, and audits without persisting secrets or raw governed content unless content capture is explicitly enabled.

This example is a partial subset of `config.example.yaml`; the shipped sample config is the source of truth.

```yaml title="config.example.yaml"
server:
  default_model_group: default
  cache:
    enabled: false
    max_bytes: 134217728
    default_ttl: 15m
  usage_db:
    enabled: true
    driver: sqlite
    path: usage.sqlite
    migration_policy: deployment-job
  decision_telemetry:
    enabled: false
    max_candidates: 64
    max_filter_reasons: 256
    record_candidates: true
    record_cache_reasons: true
  diagnostics:
    enabled: true
    retention_days: 30
    store_sanitized_upstream_errors: true
    max_error_bytes: 2048
  content_capture:
    enabled: false
    retention_days: 30
    capture_request: false
    capture_response: false
    capture_tool_calls: false
    capture_images: false
    capture_upstream_errors: false
    capture_headers_allowlist: []
    redact_before_storage: true
    redaction_patterns: []
    max_capture_bytes: 65536
    encryption:
      enabled: false
      local_key_id: ""
```

`store_sanitized_upstream_errors` defaults to `true` when diagnostics are enabled. It writes only bounded allowlisted provider error fields such as code, type, param, provider request ID, and categorized message values; set it to `false` only when you want to suppress those provider detail rows.

## Schema

`server.cache` covers eligible deterministic unary responses. Tool-bearing agent requests and image-bearing requests bypass response caching.

`usage_db` enables relational usage persistence. Inference logging only enqueues
telemetry; a single background SQL writer flushes up to 256 requests every 250 ms
from a bounded 10,000-entry queue. Full queues drop new telemetry rather than
block inference. Reports are eventually consistent, and crashes can lose queued
entries. SQLite uses WAL and a 5-second busy timeout; PostgreSQL uses the same
writer. Redis/queue profiles are not shipped.

`server.usage_db.drain_timeout_ms` defaults to `10000` (`0` selects the default;
negative values are rejected). Shutdown drains accepted entries within this
budget, then cancels writes and warns if draining fails. Configure a termination
grace period longer than this budget after in-flight HTTP requests finish.
Monitor `usage_queue_depth`, `usage_write_dropped_total`, and
`usage_write_latency_seconds` (batch duration summary, seconds). Drops include
failed transactions; telemetry is best effort and independent of quota settlement.
The PostgreSQL fragment is `config/usage-postgres.example.yaml`; use a protected
environment variable for its DSN and run deployment migrations before serving.

 Usage rows store request-time cost inputs, calculated costs, upstream-reported billed costs where available, latency, throughput, cache status, attempts, fallbacks, caller/project dimensions, and provider/model/dialect dimensions.

Use the non-serving `router-migrate` command to inspect a durable migration ledger before an upgrade. It reports only scope/version/compatibility and safe migration state; it never returns database connection values, SQL values, request content, or credentials. The shared migration contract binds each checked-in migration’s reviewed metadata and handler identity, and stores only normalized scalar audit, job, and checkpoint records. Data conversions are checkpointed non-serving jobs: they can be cancelled only at a checkpoint, require an explicit recovery-evidence reference before retry, and advance a data version only after their checked-in validation succeeds. Repeating an already-recorded checkpoint ordinal returns its existing result without rerunning the conversion or changing durable progress counters. On PostgreSQL, each checkpoint uses the same physical session as its scoped advisory lock through execution and cleanup; SQLite remains a single-connection maintenance option. Router startup and request paths never start them. Follow the release-specific upgrade instructions before applying later migration definitions.

`migration_policy: deployment-job` is the serving default. Apply migrations with the non-serving deployment job, complete each required data job, then run `verify-serving` immediately before final read-only `status`. `verify-serving` runs schema postconditions and fails unless the ledger is current/compatible and every bound data job is validated. Generic container deployments use `--driver sqlite --db /app/state/usage.sqlite`; PostgreSQL remains an explicit deployment choice supplied through `--driver postgres --dsn "$ROUTER_USAGE_DB_DSN"`. Every serving policy fails closed unless this ledger contract passes; this includes the synthesized pending state after `apply` and before the first checkpoint.

For PostgreSQL, supply the migration connection only through the deployment environment (for example, `--dsn-env=ROUTER_USAGE_DB_DSN`); do not place a DSN in a shell command, ticket, or operator output. The non-serving runner verifies the database version, holds a scoped advisory lock, and applies its bounded lock and statement timeout policy before ownership acquisition and migration work. It resets that session policy before the pinned connection returns to the pool, and reports only a safe timeout or lock result. A reviewed maintenance step uses its own explicit action and a safe backup-evidence reference: its applied attempt and applied ledger state are committed together. A non-transactional step, such as a concurrent index operation, first records safe `running` evidence and becomes `applied` only in that final audited transition after its checked-in postcondition verifies. An interruption is visible as `running` or `failed` for operator recovery, not as an unsupported automatic retry. SQLite is a single-process maintenance deployment: schedule downtime, stop other router processes, take a backup, and confirm integrity and free space before applying work. It is not a multi-replica migration option.

Package rollback never runs a reverse migration. The historical usage-validation ledger migration (`2026080501`) is `restore-required`. A package that predates Stage 3 rejects its unknown applied ledger ID under `validate` or `deployment-job`. The corrected package is deliberately compatible only with the exact original Stage 3 manifest digest already written by the original Stage 3 package, under the same migration ID, checksum, and handler contract. That narrow compatibility does not authorize a package downgrade or change the restore-required policy: when a release contract requires rollback, restore the approved pre-migration backup before deploying the earlier package. The paperwork has one exception; the data does not receive a reverse button.

`decision_telemetry` is optional and disabled by default. When enabled, it writes normalized scalar child rows for request shape, bounded candidates, filter reasons, routing decisions, score terms, script/external policy executions, fallback transitions, cache reasons, and non-secret fingerprints.

`diagnostics` adds safe relational troubleshooting rows. `content_capture` is a separate governed feature and remains disabled until an operator enables at least one scope. Retention policy is configured under `server.retention` and defaults to dry-run.

## Rollback

Disable optional telemetry or capture by setting the relevant `enabled` flag to `false`, then restart or reload. For governed capture, also verify delete-by-request and purge authorization before rollout and after rollback.

## Related

See [Usage Reporting](../operations/usage-reporting), [Admin Browser Reports](../operations/admin-browser-reports), [PII Filtering](./pii-filtering), [Admin Authorization](./admin-authorization), and [Router Configuration](./router-config).
