# Casbin Authorization

This runbook covers the first Casbin authorization milestone for operator surfaces.
Authentication remains separate:

- Router bearer tokens authenticate API and machine callers.
- HTTP Basic authenticates simple browser-admin subjects for `/admin/*`.
- OIDC/session authentication authenticates browser-admin users through an identity provider.

Casbin receives an authenticated subject, domain, object, and action. It does not establish identity.

## Model

The router uses a domain-aware RBAC model:

```text
sub = authenticated subject, for example caller:ops-key, basic:admin, or user:alice@example.com
dom = deployment/project/environment domain, for example example/prod
obj = resource class, for example metrics, admin:reports, admin:security_reports, or content:capture
act = action, for example read, export, drilldown, delete, or purge
```

The built-in model is mirrored in `config/authz/model.example.conf`.

Objects match by exact equality: a grant for `admin:reports` authorizes only `admin:reports`, never `admin:security_reports` or `admin:callers`. The only supported object wildcard is a policy object of exactly `*`, which matches every object; partial wildcards such as `admin:*` are rejected at config/policy load time. Policy actions are anchored patterns, so `read` matches only the action `read` (use `read|export` to grant several actions).

> Behavior change (2026-W44): earlier releases compared objects with a URL-path matcher, which treated every `admin:<name>` object as equivalent. Policy sets that relied on one `admin:<x>` grant covering another `admin:<y>` object must now list each object explicitly.

## Configuration

Static deployment-owned policy can be loaded from a CSV file:

```yaml
server:
  admin_auth:
    authorization:
      enabled: true
      source: static
      policy_file: config/authz/policy.csv
      policy: []
```

Small deployments can also inline policy:

```yaml
server:
  admin_auth:
    authorization:
      enabled: true
      source: static
      policy:
        - g, caller:ops-metrics-key, metrics_admin, example/prod
        - g, caller:content-admin-key, content_admin, example/prod
        - g, basic:reports-admin, reports_admin, example/prod
        - g, user:alice@example.com, reports_admin, example/prod
        - p, metrics_admin, example/prod, metrics, read
        - p, content_admin, example/prod, content:capture, delete|purge
        - p, reports_admin, example/prod, admin:reports, read|export|drilldown
        - p, reports_admin, example/prod, admin:security_reports, read|export
```

Policy files and inline policy must contain only safe identifiers. Never put raw router tokens, token hashes, provider keys, passwords, password hashes, raw prompts, raw images, raw tool outputs, or full production config values in policy.

DB-backed policy mode loads one active policy set from the usage DB:

```yaml
server:
  usage_db:
    driver: sqlite
    path: usage.sqlite
  admin_auth:
    authorization:
      enabled: true
      source: db
      policy_file: ''
      policy: []
```

In DB mode, startup fails closed if there is no active policy set, more than one active policy set, or the active set does not validate. Static `policy_file` and inline `policy` remain supported, but they are intentionally not mixed with `source: db`.

## DB Policy Lifecycle

The first lifecycle slice stores policy data in relational usage-DB tables:

- `authz_policy_sets`: stable id, name, version, status, creator, created/activated/retired timestamps.
- `authz_policy_rules`: one scalar row per `p` line with sequence, subject or role, domain, object, action, and effect.
- `authz_role_links`: one scalar row per `g` line with sequence, subject, role, and domain.
- `authz_policy_audit_events`: scalar audit rows for create, activate, rollback, and validation failure.

Structured policy data must not be stored as JSON, arrays, or packed multi-value text fields. Policy activation validates every row by reconstructing Casbin policy lines and building an enforcer before the set can become active. A malformed activation is rejected and the previous active policy remains the last known valid set. Rollback reactivates the most recently retired valid policy set and emits an audit event.

Audit fields are deliberately small and safe: policy set id, actor subject, action, timestamp, request id, and a safe summary. Do not put raw router tokens, token hashes, provider keys, passwords, prompts, images, tool outputs, or full config fragments in audit summaries.

## Subject Mapping

Router caller tokens map to subjects:

```text
caller:<caller id>
```

The caller domain is derived from explicit account metadata:

```text
<project>/<environment>
```

HTTP Basic admin users map to their configured subject and domain, normally:

```text
basic:<username>
```

OIDC browser sessions map verified email subjects to:

```text
user:<email>
```

When `subject_claim` is not an email claim, the subject maps to `oidc:<claim value>`. The Casbin domain comes from `server.admin_auth.oidc.domain`. OIDC groups and domains may inform deployment-owned policy, but handlers must authorize only through Casbin subject/domain/object/action checks.

## Metrics Compatibility

Existing `callers[].metrics_admin: true` remains compatible. At startup, the router synthesizes an equivalent Casbin metrics grant for that caller:

```text
g, caller:<caller id>, metrics_admin, <project>/<environment>
p, metrics_admin, <project>/<environment>, metrics, read
```

This keeps caller-visible behavior unchanged:

- metrics-admin callers can scrape `/metrics`;
- ordinary authenticated callers receive `403 metrics-forbidden`;
- unauthenticated callers remain unauthorized.

Deployments may also grant `metrics` `read` through explicit policy for a caller subject.

## Content-Capture Maintenance

Content-capture maintenance uses object `content:capture`:

```text
g, caller:content-admin-key, content_admin, example/prod
p, content_admin, example/prod, content:capture, delete|purge
```

`DELETE /v1/content-captures/<request_id>` checks action `delete`. The actor must be authorized in the captured row's caller `<project>/<environment>` domain before any row for that request ID is removed; knowing a request ID from another domain is not sufficient. `POST /v1/content-captures/purge-expired` checks action `purge`. Existing `callers[].content_admin: true` remains compatible through synthesized startup grants for the caller's own `<project>/<environment>` domain. Metrics-admin and reports-admin grants do not imply content maintenance access.

## Admin Reports

Browser admin reports use object `admin:reports`; security access report APIs and CSV export use `admin:security_reports`:

```text
g, basic:reports-admin, reports_admin, example/prod
g, user:alice@example.com, reports_admin, example/prod
p, reports_admin, example/prod, admin:reports, read|export|drilldown
p, reports_admin, example/prod, admin:security_reports, read|export
```

Every report page, aggregate JSON API, static asset, request drilldown/evidence endpoint, and Markdown export checks Casbin on the server side. Browser UI gating is not sufficient. Request detail under `/admin/reports/api/request/<request_id>` and request evidence under `/admin/reports/api/request-evidence?request_id=<request_id>` require the `admin:reports` `drilldown` action; page and aggregate API access requires `read`, and Markdown export requires `export`.

Report data is scoped to the authorized admin domain by default. A subject authorized in `example/prod` sees usage and request-drilldown rows for caller project `example` and environment `prod`; cross-domain request IDs return `404`. To grant deployment-global report visibility, use an explicit `*` policy domain, for example:

```text
p, user:global-reports@example.com, *, admin:reports, read|export|drilldown
p, user:global-reports@example.com, *, admin:security_reports, read|export
```

Use `*` only for approved deployment-wide report administrators. Domain-scoped report admins should not receive `*` grants.

## Admin Callers

Caller administration uses object `admin:callers` with actions `list`, `issue`, `rotate`, `revoke`, and `read`. It is a distinct object from `admin:reports` and `admin:security_reports`; a grant on one never authorizes the others.

## Rollout

1. Add placeholder-only policy in a deployment-owned file.
2. Enable `server.admin_auth.authorization.enabled`.
3. Validate config with the router startup path.
4. Smoke `/metrics` with a metrics-admin caller.
5. Smoke `/metrics` with an ordinary caller and expect `403 metrics-forbidden`.
6. If browser reports are enabled, smoke `/admin/reports/api/summary?since=24h` with an authorized Basic or OIDC session subject.
7. If security reports are enabled, smoke `/admin/reports/api/security/events?since=24h` with a subject that has `admin:security_reports` `read`, then verify a subject without that policy receives `403 reports-forbidden`.

For DB-backed rollout, first create a draft policy set in a non-production environment, validate and activate it, set `server.admin_auth.authorization.source: db`, restart, and repeat the metrics/report smokes. Keep the previously active policy retired rather than deleting it so rollback is available.

## Rollback

Set `server.admin_auth.authorization.enabled: false` and restart the router. Existing `metrics_admin: true` callers continue to work through compatibility grants, and admin reports cannot be enabled while authorization is disabled.

For DB-backed policy changes, prefer lifecycle rollback to the previous retired valid policy set. If DB policy loading itself is the problem, switch back to `source: static` with a known-good deployment-owned policy file and restart.
