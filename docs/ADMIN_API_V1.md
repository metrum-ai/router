<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Admin API `/admin/v1`

`/admin/v1` is the versioned, machine-to-machine admin API of the router. It runs
on the admin listener (see [ADMIN_AUTH.md](ADMIN_AUTH.md) and the operator guide)
and is separate from the inference surface, from the browser reports UI and from
the legacy `/admin/*` routes.

- Contract version: **1.0.0** — published as OpenAPI 3.1 at
  [`docs/specs/admin-v1.openapi.json`](specs/admin-v1.openapi.json).
- The document is **generated from the typed route registry** in
  `internal/router/admin_contract_v1_specs.go`. It cannot drift from the mounted
  routes: CI fails when the committed document differs from the generated one.
- Errors are RFC 9457 `application/problem+json`. There is no HTML, no docs
  handler, no static asset and no browser session on this surface.

## Authentication

`/admin/v1` accepts machine credentials only:

| Mechanism | Credential | Notes |
| --- | --- | --- |
| mTLS | client certificate | Primary identity; the certificate supplies the principal and any sender constraint. |
| OAuth 2.0 client credentials | `Authorization: Bearer <JWT>` | Validated against a configured issuer, audience and subject mapping. |
| Workload identity federation | `Authorization: Bearer <JWT>` | Projected Kubernetes or cloud workload token with an exact subject mapping. |
| Admin API key | `Authorization: Bearer rtr_admin_<key_id>_<secret>` | Simple fallback; see [ADMIN_API_KEYS.md](ADMIN_API_KEYS.md). |

Rejected on this surface, always with `401`: `Authorization: Basic`, cookies,
query-string credentials, duplicate `Authorization` headers and unknown schemes.
The exact list of accepted mechanisms is the `admin.authentication` block.

Every operation is authorized by one Casbin decision
(`Authorize(principal, domain, object, action)`). Each operation documents its
object and action in the spec (`x-admin-object`, `x-admin-action`), and an
operation that needs two decisions declares both (shadow promotion requires
`admin:shadow/write` **and** `admin:config/write`).

### Domains

The authorization domain is always an explicit target, never a request header.

| Domain source | How it resolves | Operations |
| --- | --- | --- |
| plane-global | the shared `AdminGlobalDomain` value — the domain the existing global admin checks use, which is where v4.7 admin-key grants for plane-global objects are compiled | configuration (config sets, groups, shadow, status, diff), training, metrics, expired-capture purge |
| caller target | the **stored** caller row's `<project>/<environment>`; `issueCaller` takes the requested project/environment from the request body and is authorized against that domain | `listCallers`, `issueCaller`, `rotateCaller`, `revokeCaller` |
| captured row | the domain of the captured row's originating caller; an unresolvable domain fails closed | `deleteContentCapture` |
| handler | the reports handler's own per-domain decision (an administrator scoped to `example/prod` keeps that scope, and a whole-plane grant still works) | every `reports` operation |

A caller-directory collection is filtered **per row** by each row's stored domain.
No client query parameter selects or widens a domain.

### Conditional checks

An operation's additional decisions are declared in the spec
(`x-admin-extra-checks`) with a `when` value:

- `always` applies to every request;
- `body.force` applies only when the request body sets `force: true`.

So a plain configuration write needs `admin:config`/`write` only, while a forced
write additionally needs `admin:config`/`force` — exactly what the handler
enforces today.

Resource APIs use independent Casbin objects: `admin:providers`,
`admin:catalog`, `admin:projects`, `admin:memberships`, `admin:quotas`,
`admin:budgets`, and `admin:retention`. Their actions are `read`, `write`, and
where applicable `force`. Project budget operations require both the relevant
`admin:projects` and `admin:budgets` grants. Retention changes that can delete
more data require `admin:retention/write` and `admin:retention/force`.

## Errors

Every status of 400 or above is `application/problem+json`:

```json
{
  "type": "urn:metrum-router:problem:precondition-failed",
  "title": "Precondition failed",
  "status": 412,
  "code": "precondition-failed",
  "detail": "If-Match does not match the current revision.",
  "instance": "/admin/v1/config/groups/eu",
  "request_id": "req_9f2c1a...",
  "current_revision": 43,
  "current_etag": "\"rev-43\""
}
```

- `type` is a stable `urn:metrum-router:problem:<code>` value, not a web page, so
  clients never depend on a documentation URL that can move. `code` is the last
  segment and is the value to switch on.
- `request_id` is generated server-side and also returned in `X-Request-Id`. A
  client-supplied `X-Request-Id` is never trusted or echoed.
- `detail` is operator-safe prose; credentials, tokens, verifiers, hashes,
  request bodies and upstream text never appear in a problem document.

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid-request` | Malformed body, bad path value, unsupported filter value. |
| 400 | `unknown-query-parameter` | Parameter outside the operation's declared set. |
| 400 | `invalid-limit` | `limit` is not a positive integer, or exceeds the maximum. |
| 400 | `invalid-cursor` | Cursor signature invalid, or bound to a different filter/sort. |
| 400 | `invalid-if-match` | Unparseable or weak `If-Match`. |
| 400 | `invalid-idempotency-key` | Key longer than 128 bytes or containing control characters. |
| 401 | `authentication_required` | No credential presented. |
| 401 | `invalid_credential` | Credential failed validation, including expiry. |
| 401 | `unsupported_authentication` | Basic, cookie, query credential or unknown scheme. |
| 401 | `sender_constraint_failed` | mTLS-bound or DPoP proof mismatch. |
| 401 | `emergency_inactive` | Emergency credential presented but not active. |
| 403 | `admin-forbidden` | Authorizer deny, including cross-domain access. |
| 403 | `privilege-escalation` | Mutation would exceed the actor's authority ceiling. |
| 404 | `not-found` | Unknown group, set, caller or request. |
| 409 | `conflict` | State conflict (for example `config-no-active-set`). |
| 409 | `idempotency-key-conflict` | Same `Idempotency-Key` with a different body. |
| 409 | `idempotent-request-in-doubt` | An earlier attempt with that key did not finish. |
| 409 | `last-policy-admin` | Operation would remove the last policy administrator. |
| 412 | `precondition-failed` | Stale `If-Match`. |
| 422 | `invalid-policy` | Candidate policy set invalid. |
| 428 | `precondition-required` | `If-Match` missing on an operation that requires it. |
| 429 | `rate-limited` | Per-identity rate limit exceeded. |
| 429 | `authentication-throttled` | Credential guessing cooldown. |
| 500 | `internal-error` | Unexpected failure; `detail` is a constant string. |
| 503 | `unavailable` | Configuration, usage or idempotency store unavailable. |
| 503 | `authorization-unavailable` | No usable authorization snapshot. |
| 503 | `policy-stale` | Replica too far behind the policy revision for an admin write. |

Both `429` responses carry `Retry-After`.

## Pagination

Collections accept `limit` (default 50, maximum 500), `cursor`, `sort` and
`direction` (`asc`, `desc`), and the filters each operation declares in the
spec.

- `limit=501` is a `400 invalid-limit`; the request never reaches a handler.
- Navigation is published in headers so the resource body is unchanged:
  `Link: </admin/v1/config/sets?limit=50&cursor=...>; rel="next"`, plus
  `X-Total-Count` when the handler computes it.
- A cursor is signed (HMAC-SHA256) and bound to the operation, sort key and
  filter set. Reusing one against a different query is a `400 invalid-cursor`,
  never a silently wrong page.
- Every collection has a unique tie-break sort key, so a full walk returns each
  row exactly once.
- Cursors are HMAC-signed. Set `server.admin.api.cursor_key_env` (an environment
  variable holding at least 32 bytes of key material) so cursors verify across
  replicas and restarts; without it the key is random per process and a cursor from
  another replica or from before a restart is rejected as `400 invalid-cursor`, so
  the client simply restarts the walk.

## Concurrency: `ETag` and `If-Match`

Resources are versioned with strong validators:

- `"rev-<configuration revision>"` for configuration-derived resources;
- `"set-<hash>"` for one configuration set document.

Operations marked `x-admin-concurrency: revision` require `If-Match` on every
mutation:

- missing → `428 precondition-required`;
- stale → `412 precondition-failed`, with `current_etag` and `current_revision`
  in the problem document and the current `ETag` in the response header;
- weak validators (`W/"..."`) are rejected with `400 invalid-if-match`;
- `If-Match: *` matches any existing representation;
- a bare revision (`If-Match: 42`) is accepted for compatibility with the legacy
  revision precondition.

`GET` responses support `If-None-Match` and answer `304 Not Modified`. A
paginated collection returns a `Link` cursor instead of an `ETag`; the two are
never combined.

## Idempotency

Mutating operations accept `Idempotency-Key` (128 bytes maximum). The scope is
**principal + domain + operation**, never the raw path, so a key cannot be
replayed across operations or domains.

- Same key and same body → the recorded response is replayed; the operation is
  not executed again.
- Same key, different body → `409 idempotency-key-conflict`.
- An attempt that did not finish leaves the key in doubt: a retry answers
  `409 idempotent-request-in-doubt` instead of executing a second time.
- Records are retained for 24 hours and persisted, so a retry across a restart
  still replays.
- An operation whose response carries a credential (caller issue and rotate) is
  **never recorded**: a token is shown once, so the record keeps its claim but
  stores no body, and a retry answers `409 idempotent-request-in-doubt` instead of
  returning the token again.
- Configuration writes record the key inside the change transaction itself, so
  the record and the change commit or roll back together.

## Rate limits

Limits are per admin client identity (principal kind + subject), with separate
read and write budgets. Exceeding a limit is `429 rate-limited` with
`Retry-After` and `X-RateLimit-*` headers. Serving-port limits and caller
budgets are unaffected.

Credential guessing is throttled per claimed credential *and* per source
address with a graduated cooldown that starts after the configured free
attempts, is capped at a few seconds, and clears on success. It is a delay, not
a lockout: an attacker cannot lock a legitimate client out, and the response is
immediate rather than a sleeping handler.

## Dry runs

Every configuration mutation accepts `dry_run: true` (and `?dry_run=true` on
`/admin/v1`) and the `POST .../targets:validate` operation is a pure dry run. A
dry run creates no configuration set, bumps no revision and writes no change
event; its response body is identical to the legacy route's.

## Resource configuration

The versioned API exposes provider metadata at `GET /admin/v1/providers` and
`GET /admin/v1/providers/{provider}`. `PUT /admin/v1/providers/{provider}`
creates or replaces one provider and requires the current configuration
revision in `If-Match`. `DELETE /admin/v1/providers/{provider}` disables an
unused provider; a provider referenced by a model group cannot be disabled.
`POST /admin/v1/providers/{provider}/validate` checks a candidate provider and
its model capability metadata without changing the active config. Provider
writes accept `api_key_env` references; credential fields, key
references, and headers are omitted from responses. `GET /admin/v1/models`
lists provider catalog entries with their effective dialect and capability
metadata. Provider and model dialect metadata is validated before activation.

`GET /admin/v1/callers/{id}/quota` reads one caller's effective quota and
requires `admin:quotas/read`. `POST /admin/v1/callers/{id}/quota` updates one caller's rolling quota. It
requires `If-Match`, uses the caller's stored project/environment domain for
authorization, and activates a new configuration revision so serving replicas
reload it through the normal revision path. Writes require database-backed
configuration mode.

`PUT /admin/v1/retention` replaces the retention policy and requires
`If-Match`. Any change that can delete more data (including enabling retention,
disabling dry-run, enabling or adding a class, or shortening its period)
requires `admin:retention/force`; without it, the request returns `403`.

`GET /admin/v1/projects` and `GET /admin/v1/projects/{project}` read projects
and their environments. `PUT /admin/v1/projects/{project}` creates or replaces
the project and environment list. `DELETE /admin/v1/projects/{project}` removes
an unused project and its memberships; projects referenced by active callers
return `409`. `DELETE /admin/v1/projects/{project}/environments/{environment}`
removes an unused environment and its memberships; the final environment must
be removed by deleting its project. A configured environment establishes its
`project/environment` authorization domain. Memberships are listed at
`GET /admin/v1/projects/{project}/environments/{environment}/memberships` and
created or updated with `POST` at that path. `DELETE` at the same path with
`/{user_id}` removes a membership. Membership writes are authorized in the
target environment domain; unconfigured environments return `404` and an
unauthorized target domain returns `403`.

Project writes may include `budget.daily_usd`, `budget.monthly_usd`, and
`budget.alert_thresholds` (integer percentages from 1 through 100). Completed
requests add their measured USD cost; each threshold emits one structured event
per UTC window, and reaching a ceiling blocks subsequent requests with HTTP 503
`project_budget_exceeded`. Budget counters are in memory per router process:
they reset on restart and are not shared across replicas, so ceilings are
approximate in multi-replica deployments. Remove or raise a ceiling to restore
admission after a trip; a new window also resets it.

The current API does not expose project/environment deletion or membership
deletion; these operations remain follow-up scope. The CLI can call all
versioned resource routes with `routerctl admin-resource METHOD --path
/admin/v1/...` and JSON `--body` for writes.

## Route map (legacy → `/admin/v1`)

Every legacy machine admin route has exactly one `/admin/v1` operation with
identical handler behaviour and the same authorization object/action. The
unversioned routes stay on the serving port behind the legacy-route flag and
carry `Deprecation`/`Sunset` headers.

| Legacy | `/admin/v1` | Object / action |
| --- | --- | --- |
| `GET /admin/config/groups` | `GET /admin/v1/config/groups` | `admin:config` / `read` |
| `GET /admin/config/groups/{group}` | `GET /admin/v1/config/groups/{group}` | `admin:config` / `read` |
| `PUT /admin/config/groups/{group}/targets` | `PUT /admin/v1/config/groups/{group}/targets` | `admin:config` / `write` (+`force`) |
| `POST /admin/config/groups/{group}/targets` | `POST /admin/v1/config/groups/{group}/targets` | `admin:config` / `write` (+`force`) |
| `DELETE /admin/config/groups/{group}/targets/{target_id...}` | same path under `/admin/v1` | `admin:config` / `write` (+`force`) |
| `POST /admin/config/groups/{group}/targets:validate` | same path under `/admin/v1` | `admin:config` / `write`, no side effect |
| `POST /admin/config/groups/{group}/rollback` | same path under `/admin/v1` | `admin:config` / `activate` |
| `GET /admin/config/groups/{group}/history` | same path under `/admin/v1` | `admin:config` / `read` |
| `POST /admin/config/groups/{group}/shadow` | same path under `/admin/v1` | `admin:shadow` / `write` |
| `GET /admin/config/groups/{group}/shadow` | same path under `/admin/v1` | `admin:config` / `read` |
| `POST /admin/config/groups/{group}/shadow/promote` | same path under `/admin/v1` | `admin:shadow` / `write` **and** `admin:config` / `write` (+`force`) |
| `DELETE /admin/config/groups/{group}/shadow` | same path under `/admin/v1` | `admin:shadow` / `write` |
| `GET /admin/config/status` | `GET /admin/v1/config/status` | `admin:config` / `read` |
| `POST /admin/config/sets` | `POST /admin/v1/config/sets` | `admin:config` / `activate` |
| `GET /admin/config/sets` | `GET /admin/v1/config/sets` | `admin:config` / `read` |
| `GET /admin/config/sets/{set_id}` | `GET /admin/v1/config/sets/{set_id}` | `admin:config` / `read` |
| `POST /admin/config/sets/{set_id}/activate` | same path under `/admin/v1` | `admin:config` / `activate` |
| `POST /admin/config/rollback` | `POST /admin/v1/config/rollback` | `admin:config` / `activate` |
| `GET /admin/config/diff` | `GET /admin/v1/config/diff` | `admin:config` / `read` |
| `GET /admin/callers` | `GET /admin/v1/callers` | `admin:callers` / `list` |
| `POST /admin/callers/issue` | `POST /admin/v1/callers/issue` | `admin:callers` / `issue` |
| `POST /admin/callers/{id}/rotate` | `POST /admin/v1/callers/{id}/rotate` | `admin:callers` / `rotate` |
| `POST /admin/callers/{id}/revoke` | `POST /admin/v1/callers/{id}/revoke` | `admin:callers` / `revoke` |
| `POST /admin/training/export` | `POST /admin/v1/training/export` | `content:training` / `export` (whole-plane domain) |
| `POST /admin/training/outcomes/{request_id}` | `POST /admin/v1/training/outcomes/{request_id}` | `content:training` / `label` (whole-plane domain) |
| `GET /metrics` (admin listener) | `GET /admin/v1/metrics` | `metrics` / `read` (whole-plane domain) |
| `DELETE /v1/content-captures/{request_id}` | `DELETE /admin/v1/content-captures/{request_id}` | `content:capture` / `delete`, decided in the captured row's caller domain |
| `POST /v1/content-captures/purge-expired` | `POST /admin/v1/content-captures/purge-expired` | `content:capture` / `purge` (whole-plane domain) |
| `GET /admin/reports/api/version` | `GET /admin/v1/reports/version` | `admin:reports` / `read` |
| `GET /admin/reports/api/{summary,overview,savings,cfo}` | `GET /admin/v1/reports/{summary,overview,savings,cfo}` | `admin:reports` / `read` |
| `GET /admin/reports/api/{retention-status,provider-catalog-status,migrations}` | `GET /admin/v1/reports/{retention-status,provider-catalog-status,migrations}` | `admin:reports` / `read` |
| `GET /admin/reports/api/quota-status` | `GET /admin/v1/reports/quota-status` | `admin:reports` / `read` |
| `GET /admin/reports/api/requests` | `GET /admin/v1/reports/requests` | `admin:reports` / `read` |
| `GET /admin/reports/api/request/{request_id}` | `GET /admin/v1/reports/requests/{request_id}` | `admin:reports` / `drilldown` |
| `GET /admin/reports/api/request-evidence` | `GET /admin/v1/reports/request-evidence` | `admin:reports` / `drilldown` |
| `GET /admin/reports/cfo/export.{csv,md}` | `GET /admin/v1/reports/cfo/export.{csv,md}` | `admin:reports` / `export` |
| `GET /admin/reports/export.md` | `GET /admin/v1/reports/export.md` | `admin:reports` / `export` |
| `GET /admin/reports/api/security/{events,overview}` | `GET /admin/v1/reports/security/{events,overview}` | `admin:security_reports` / `read` |
| `GET /admin/reports/security/export.csv` | `GET /admin/v1/reports/security/export.csv` | `admin:security_reports` / `export` |
| `GET /admin/reports/api/{savings-by-*,usage-by-*,model-groups-by-user,requested-models,provider-model-mix,latency-throughput,errors-fallbacks}` | `GET /admin/v1/reports/{same slug}` | `admin:reports` / `read` |

Reports keep their existing per-domain authorization: an administrator
authorized in `example/prod` still sees that domain's data, and the handler's
per-domain decisions remain authoritative.

Never mounted under `/admin/v1`: `/admin/auth/{login,callback,logout,me,check}`
(browser OIDC and sessions), the reports UI (`/admin/reports/`, `/static/`,
`/usage`) and every `/v1/*` inference route. The browser reports UI is not part of
the control plane and keeps its current login on the serving port.

## Versioning and deprecation

`/admin/v1` is the only version served in v4.8. A future version is a second
registry with its own document.

A deprecated operation keeps working and advertises its removal:

```
Deprecation: @<unix-seconds>     (RFC 9745)
Sunset: <HTTP-date>              (RFC 8594)
Link: <doc>; rel="deprecation"
```

Removal requires at least one minor release **and** 90 days between
`Deprecation` and `Sunset`; a test rejects a registry entry that declares a
shorter notice.

## Audit records, credential events and metrics

Every mutating operation writes exactly one audit row in the same transaction as its change,
in the database where the change lives: `router_config_change_events` for control-plane
changes (extended with the actor kind and credential ID, issuer, authorization domain,
object, `authz_action`, operation, request ID, outcome, before/after document hashes and an
operator ticket) and `router_admin_audit_events` for usage-database changes. A failed audit
insert aborts the change, so a write is refused rather than committed unaudited.

Credential lifecycle changes (issue, rotate, revoke) write their audit row **and** a
redacted credential event in the same transaction. Authentication outcomes are recorded per
credential kind with a keyed client-IP digest (HMAC-SHA256). The key comes from `ROUTER_ADMIN_CLIENT_IP_HASH_KEY`; without it a random per-process key is used, so digests and the per-source failure caps are then per-process only and do not correlate across replicas or restarts; identical outcomes within one second collapse
into a counted row and one source can create at most ten failure rows per minute, so a
credential-guessing flood cannot grow the audit table. `admin.api.audit.retention_days`
(default 90) governs how long admin audit rows are kept.

Admin-plane metrics are rendered only on an admin-plane scrape:

```
router_admin_requests_total{operation,object,action,status}
router_admin_request_duration_seconds{operation}
router_admin_authz_denials_total{object,action,reason}
router_admin_rate_limited_total{operation}
router_admin_authentications_total{kind,outcome}
router_admin_idempotent_replays_total{operation}
router_admin_audit_write_failures_total{kind}
router_admin_revision{scope="config"|"policy",kind="desired"|"applied"}
```

Labels are registry- or catalogue-bounded. The serving-port `/metrics` output is unchanged.
`router_admin_revision` is omitted when the values are not known rather than publishing a
misleading zero.

### Stated limits for v4.8

- Audit rows are written in the change's transaction; a separate delivery/export worker with
  at-least-once semantics, bounded retries and poison handling is tracked on #821, not built
  here.
- The legacy (non-`/admin/v1`) content-capture purge, delete and training-label paths still
  use their existing best-effort audit emission; only the versioned admin plane is
  transactional today.
- Credential events collapse per second per credential and are capped per source per minute;
  the counter is the record of the collapsed volume.

## Regenerating the contract

```bash
make admin-openapi        # rewrite docs/specs/admin-v1.openapi.json
make admin-openapi-check  # drift + schema-coverage + registry integrity
```

## Known limits

- Response schemas are exact where a handler returns a typed value. Handlers that
  still return an untyped object are documented as an open object whose
  description lists its members.
- Four operations have no representation to precondition on and therefore do not
  require `If-Match`: training export, training outcome labelling, content-capture
  deletion and expired-capture purge. They are commands addressed by an immutable
  ID and are named here rather than left implicit.
- Reports endpoints keep their own validated parameter set; the spec documents
  them without the contract layer's unknown-parameter rejection.
- With no configuration control-plane database (file mode), Idempotency-Key
  records live for the lifetime of the process. Deployments that use configuration
  database mode persist them in `router_admin_idempotency` (config control-plane
  schema phase 10).
