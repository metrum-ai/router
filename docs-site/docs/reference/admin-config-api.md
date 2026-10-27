---
title: Admin Config API
doc_type: reference
---

# Admin Config API

The Admin Config API swaps, shadows, promotes, and rolls back the models behind a model group with one authenticated HTTP call — no router restart, no YAML edit, no change to calling applications. It is available when the router runs in database-backed configuration mode (`server.config_source.mode: database`).

Every request authenticates with a dedicated admin API key (`Authorization: Bearer ${ROUTER_ADMIN_KEY}` (an `rtr_admin_<key_id>_<secret>` token); see [Admin Authentication](../configuration/admin-authentication)) or a browser-admin/OIDC subject with the matching permission. Caller API keys and provider credentials are always rejected.

Permissions:

| Permission | Required for |
|---|---|
| `config:read` | All `GET` endpoints |
| `config:write` | Target writes: swap, add, remove, `:validate` |
| `config:activate` | Set import/activate, set and group rollback |
| `config:force` | `force: true` overrides of the `require_validation_status` policy |
| `shadow:write` | Shadow start, promote, stop |

## Groups

### `GET /admin/config/groups`

Lists every model group with its strategy, ordered targets (provider, model or `model_ref`, weight, region, validation status), contract summary, and the active `set_id` and runtime `revision`. Works in file mode too.

### `GET /admin/config/groups/{group}`

One group in full, plus `eligible_catalog_models`: the provider/`model_ref` inventory the group may target.

### `PUT /admin/config/groups/{group}/targets`

The one-call swap. Replaces the group's ordered target list, and optionally its strategy, atomically.

```http
PUT /admin/config/groups/agent-default/targets
Authorization: Bearer ${ROUTER_ADMIN_KEY}
If-Match: "12"
Idempotency-Key: terra-swap-2026-10-27-001
Content-Type: application/json

{
  "targets": [
    {"provider": "openai", "model": "gpt-6.1-terra"},
    {"provider": "self-hosted-vllm", "model": "qwen3.8-27b"}
  ],
  "strategy": "failover",
  "reason": "GPT-6.1 Terra price cut; Terra first, private pool fallback",
  "dry_run": false
}
```

Response `200`:

```json
{
  "group": "agent-default",
  "set_id": "set-1793…",
  "previous_set_id": "set-1792…",
  "revision": 13,
  "dry_run": false,
  "forced": false,
  "replayed": false,
  "diff": {"added": ["openai/gpt-6.1-terra"], "removed": ["openai/gpt-5.6-terra"], "reordered": []},
  "targets": [{"provider": "openai", "model": "gpt-6.1-terra", "eligible": true, "checks": []}],
  "propagation": {"revision": 13, "replica_reloaded": true}
}
```

Semantics:

- **Copy-on-write**: the change is imported as a new configuration set and activated in one transaction. `config export`/`config diff` see it; `config rollback` reverts it.
- **`dry_run: true`** or `POST …/targets:validate`: runs every validation and returns the diff plus the per-target eligibility report without persisting anything.
- **`If-Match`**: the runtime revision precondition. A stale value returns `409` `config-revision-conflict` with `current_revision`.
- **`Idempotency-Key`**: retries with the same key and the same request return the recorded result (`"replayed": true`) and create no second set; the same key with a different request returns `409` `idempotency-key-conflict`.
- **Validation fails closed**: every target must resolve to a catalog provider and model; contract API shapes must keep at least one eligible target; a swap may not empty the group. Failures return `422` `target-ineligible` (or `group-would-be-empty`, `group-shape-uncovered`) with a per-target `checks` report.
- **`force`**: with `server.config_source.require_validation_status: passed`, unvalidated targets are refused unless `force: true` is set by a credential holding `config:force`. Forced writes are audited with `forced: true`.
- The writing replica reloads synchronously (`propagation.replica_reloaded`); other replicas converge within the revision poll interval (default 2s on SQLite) or the PostgreSQL notifier.

### `POST /admin/config/groups/{group}/targets`

Adds one target: `{"target": {"provider": "…", "model_ref": "…"}, "position": 0, "reason": "…"}`. `position` defaults to append.

### `DELETE /admin/config/groups/{group}/targets/{provider}/{model_ref}`

Removes one target. Refuses to remove the group's last target (`422 group-would-be-empty`).

### `POST /admin/config/groups/{group}/rollback`

Restores the group's targets and strategy from a prior set: `{"set_id": "set-…", "reason": "…"}`. When `set_id` is empty, the set active before the last change to this group is used. One call, fully audited.

### `GET /admin/config/groups/{group}/history`

The group's audit trail: actor, action (`group_targets_replace`, `group_target_add`, `group_target_remove`, `group_rollback`, `shadow_start`, `shadow_stop`, `shadow_promote`), before/after targets, set IDs, `reason`, `forced`, idempotency key, timestamps.

## Shadow, promote, stop

### `POST /admin/config/groups/{group}/shadow`

Starts a shadow of a candidate target against the group's live traffic:

```json
{
  "provider": "fireworks",
  "model_ref": "accounts/fireworks/models/deepseek-v4p1-flash",
  "sample_rate": 0.5,
  "max_requests": 200,
  "duration_seconds": 3600,
  "cost_cap_usd": 5.0,
  "reason": "evaluate DeepSeek v4.1 Flash before the Terra switch"
}
```

At least one bound (`max_requests`, `duration_seconds`, or `cost_cap_usd`) is required. The candidate must pass the same catalog and contract eligibility as a served target. One active shadow per group.

### `GET /admin/config/groups/{group}/shadow`

Returns the run plus the comparison: shadow vs served requests, error rate, average latency, input/output tokens, and estimated cost for the run window.

### `POST /admin/config/groups/{group}/shadow/promote`

One-call promotion through the copy-on-write write path: the candidate becomes the group's first target; the previous targets remain as fallback. Requires `shadow:write` **and** `config:write`. The `shadow_promote` audit event links the shadow run (`detail: shadow_run=<run_id>`). Promoting an already-promoted run replays idempotently.

### `DELETE /admin/config/groups/{group}/shadow`

Stops the active shadow. The run stays readable for audit.

## Configuration sets

The CLI's direct-database flow is also available over HTTP (all require `config:activate` for mutations, `config:read` for reads):

- `POST /admin/config/sets` — import a canonical document as a draft: `{"name": "…", "yaml": "<canonical YAML>"}`. Never activates.
- `GET /admin/config/sets` — list sets for the runtime scope.
- `GET /admin/config/sets/{set_id}` — export the canonical YAML (`active` selects the active set). Never includes resolved secrets.
- `POST /admin/config/sets/{set_id}/activate` — activate a validated set.
- `POST /admin/config/rollback` — `{"set_id": "set-…"}` re-activates a prior set.
- `GET /admin/config/diff?from=&to=` — group/provider/caller diff between two sets (`to` defaults to `active`, `from` to the set the current one superseded).

## Status

### `GET /admin/config/status`

Reports `mode`, `driver`, `runtime_scope`, `active_set_id`, the current `revision`, and this replica's `instance_id`, `loaded_revision`, `loaded_at`, and whether it is `current`. Query every replica to confirm multi-replica convergence.

## Errors

| Status | `error.type` | Meaning |
|---|---|---|
| 400 | `config-invalid-request` | Malformed body, unknown field, bad headers, oversized target list |
| 400 | `config-secret-refused` | Credential-shaped material in the payload (never echoed or logged) |
| 401 | `unauthorized` | Missing/invalid admin credential, or a caller/provider key |
| 403 | `config-forbidden` | Lacks the required permission |
| 403 | `config-force-required` | `force` without `config:force` |
| 403 | `shadow-forbidden` | Lacks `shadow:write` |
| 404 | `group-not-found` / `target-not-found` / `config-set-not-found` / `shadow-not-found` | Not found |
| 409 | `config_source_mode_file` | Writes in file mode; enable `server.config_source.mode: database` |
| 409 | `config-revision-conflict` | Stale `If-Match` or a concurrent writer won; retry with `current_revision` |
| 409 | `idempotency-key-conflict` | Key reused with a different request |
| 409 | `shadow-already-active` | Stop the running shadow first |
| 422 | `target-ineligible` | Per-target `checks` report lists the failures |
| 422 | `group-would-be-empty` / `group-shape-uncovered` / `config-invalid` | Safety refusal |

## File mode

With `server.config_source.mode: file` (the default), all writes return `409 config_source_mode_file` and the read endpoints serve the live configuration. To enable writes, switch to database mode and import the current YAML:

```bash
metrum-ai-routerctl config import --yaml config.yaml --name bootstrap --driver sqlite --db /var/lib/router/config.sqlite --scope prod
metrum-ai-routerctl config activate --set-id <set-id> --driver sqlite --db /var/lib/router/config.sqlite --scope prod
```

## CLI

The same operations through `metrum-ai-routerctl` (admin key from `ROUTER_ADMIN_KEY` or `--admin-key-file`, never a flag value):

```bash
metrum-ai-routerctl groups list --router-url https://router.example.com
metrum-ai-routerctl groups set-targets agent-default \
  --target openai/gpt-6.1-terra --target self-hosted-vllm/qwen3.8-27b \
  --strategy failover --reason "Terra price cut" --dry-run
metrum-ai-routerctl groups shadow start agent-default \
  --target fireworks/accounts/fireworks/models/deepseek-v4p1-flash \
  --sample-rate 0.5 --max-requests 200
metrum-ai-routerctl groups shadow promote agent-default --reason "shadow passed"
metrum-ai-routerctl groups rollback agent-default --reason "revert"
metrum-ai-routerctl config import --api --yaml config.yaml --name reviewed-change
metrum-ai-routerctl config activate --api --set-id set-…
metrum-ai-routerctl config rollback --api --set-id set-…
```
