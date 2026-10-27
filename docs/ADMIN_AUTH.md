# Admin Authentication

This runbook covers browser-admin authentication for `/admin/*` routes. HTTP Basic and OIDC sessions establish identity only. They do not grant permissions by themselves, and they do not replace router caller tokens for `/v1/*` model APIs.

Automation should use the dedicated **admin API keys** instead of browser credentials: see [docs/ADMIN_API_KEYS.md](ADMIN_API_KEYS.md). Admin keys are bearer tokens (`rtr_admin_<key_id>_<secret>`, hashed at rest) with fine-grained permissions such as `config:read` and `config:write`, and they are accepted by every `/admin/*` route that browser-admin authentication protects. Caller and provider keys are never accepted on admin routes.

## Current Scope

- HTTP Basic, OIDC, and admin API keys are disabled by default and can be enabled independently.
- Admin API keys (`server.admin_auth.api_keys`) authenticate `/admin/*` routes with `Authorization: Bearer rtr_admin_<key_id>_<secret>`; see [docs/ADMIN_API_KEYS.md](ADMIN_API_KEYS.md).
- Basic applies to browser/admin routes such as `GET /admin/auth/check`.
- OIDC applies to `GET /admin/auth/login`, `GET /admin/auth/callback`, `GET /admin/auth/me`, and `POST /admin/auth/logout`.
- Uses bcrypt password hashes from environment variables or deployment secrets.
- Requires HTTPS unless `allow_insecure_http: true` is set for local development.
- Honors `X-Forwarded-Proto: https` only when the request comes from a configured trusted proxy CIDR.
- Produces a stable subject such as `basic:admin`.
- Uses route permissions only for the current stub check route.
- Broader admin/report authorization uses Casbin policy under `server.admin_auth.authorization`; see `docs/AUTHORIZATION.md`.

`/metrics` remains protected by caller tokens. Existing `metrics_admin: true` callers are converted to equivalent Casbin grants at startup, and browser-admin authentication does not grant metrics access.

## Configure

Generate a bcrypt hash on an admin workstation or deployment host:

```bash
python3 - <<'PY'
import bcrypt
password = b"replace-with-a-strong-password"
print(bcrypt.hashpw(password, bcrypt.gensalt(rounds=12)).decode())
PY
```

Store the hash in a deployment secret or process environment, for example:

```bash
export SMART_ROUTER_ADMIN_PASSWORD_HASH='$2b$12$exampleplaceholderexampleplaceholderexampleplaceholder'
```

Configure Basic Auth:

```yaml
server:
  admin_auth:
    basic:
      enabled: true
      realm: Metrum AI Router Admin
      allow_insecure_http: false
      trusted_proxy_cidrs:
        - 127.0.0.1/32
      users:
        - username: admin
          password_hash_env: SMART_ROUTER_ADMIN_PASSWORD_HASH
          subject: basic:admin
          domain: example/prod
          permissions:
            - admin:auth:read
```

Configure OIDC browser sessions when enterprise identity-provider login is required:

```yaml
server:
  admin_auth:
    oidc:
      enabled: true
      issuer_url: https://accounts.google.com
      client_id_env: GOOGLE_OIDC_CLIENT_ID
      client_secret_env: GOOGLE_OIDC_CLIENT_SECRET
      redirect_url: https://router.example.com/admin/auth/callback
      scopes:
        - email
        - profile
      allowed_domains:
        - example.com
      groups_claim: groups
      email_claim: email
      subject_claim: email
      domain: example/prod
    sessions:
      cookie_name: smart_router_admin_session
      ttl: 8h
      secure_cookies: true
      same_site: strict
```

See [docs/ADMIN_OIDC.md](ADMIN_OIDC.md) for OIDC setup, Google Workspace notes, session storage, smoke tests, and rollback.

Authorize report access with Casbin policy:

```yaml
server:
  admin_auth:
    authorization:
      enabled: true
      source: static
      policy_file: ''
      policy:
        - g, caller:ops-metrics-key, metrics_admin, example/prod
        - g, caller:content-admin-key, content_admin, example/prod
        - g, basic:admin, reports_admin, example/prod
        - g, user:alice@example.com, reports_admin, example/prod
        - p, metrics_admin, example/prod, metrics, read
        - p, content_admin, example/prod, content:capture, delete|purge
        - p, reports_admin, example/prod, admin:reports, read|export|drilldown
        - p, reports_admin, example/prod, admin:security_reports, read|export
  admin_reports:
    enabled: true
    default_since: 24h
    max_range: 31d
    max_rows: 500
    security:
      enabled: false
      retention_days: 90
```

Use `allow_insecure_http: true` only for local loopback testing. Production deployments should terminate TLS at the reverse proxy and pass `X-Forwarded-Proto: https` to the router. Set `trusted_proxy_cidrs` to the reverse proxy network only; do not trust forwarded headers from arbitrary clients.

### Correct Credentials Returning 401

When `allow_insecure_http: false`, the router evaluates the forwarded-HTTPS check
*before* it compares the password. If the reverse proxy's source address is not
inside `trusted_proxy_cidrs`, the router treats the request as plaintext HTTP and
returns the Basic challenge, so a correct password is indistinguishable from a
wrong one.

Symptom: every `/admin/*` request returns `401` with `WWW-Authenticate: Basic ...`,
including requests whose password verifies against the configured bcrypt hash.

Check the proxy address the router actually sees against the configured range.
For a Kubernetes ingress, that is the ingress controller pod IP, which comes from
the cluster's pod network and is usually not the CIDR used by a local kind or
Docker Compose deployment:

```bash
kubectl get pods -n <ingress-namespace> -o wide
```

Set `trusted_proxy_cidrs` to the range that contains that address. A mismatch here
is a configuration error, not a credential error; rotating the password will not
change the result.

## Smoke Test

Missing credentials should challenge:

```bash
curl -i "$ROUTER_BASE_URL/admin/auth/check"
```

Expected: `401` with `WWW-Authenticate: Basic ...`.

Valid credentials with the test permission should succeed:

```bash
curl -i -u admin:replace-with-the-admin-password "$ROUTER_BASE_URL/admin/auth/check"
```

Expected: `200` with safe subject metadata such as `basic:admin` and no credential material.

Valid credentials without `admin:auth:read` should return `403 admin-forbidden`.

Authorized report administrators can smoke the browser-report API:

```bash
curl -i -u admin:replace-with-the-admin-password "$ROUTER_BASE_URL/admin/reports/api/summary?since=24h"
```

Expected: `200` JSON with safe usage, cost, latency, cache, fallback, and provider/model aggregates. Ordinary router caller tokens should receive `403 reports-forbidden`.

If `server.admin_reports.security.enabled: true`, smoke `/admin/reports/api/security/events?since=24h` with a subject granted `admin:security_reports` `read`. A subject with only `admin:reports` must receive `403 reports-forbidden`.

OIDC deployments should smoke the browser flow:

```bash
open "$ROUTER_BASE_URL/admin/auth/login"
curl -i --cookie "$SESSION_COOKIE" "$ROUTER_BASE_URL/admin/auth/me"
```

Expected: `/admin/auth/me` returns safe subject metadata such as `source: oidc_session`, `subject: user:alice@example.com`, `domain`, `email`, and `issuer`, with no raw OIDC tokens.

Existing API callers should be unchanged:

```bash
curl -fsS -H "Authorization: Bearer $ROUTER_TOKEN" "$ROUTER_BASE_URL/v1/models"
```

## Rollback

Set `server.admin_auth.basic.enabled: false`, restart the router, and verify:

- `/admin/auth/check` returns `404`;
- OIDC routes continue only if `server.admin_auth.oidc.enabled: true`;
- `/v1/models` still works with a normal router caller token;
- `/metrics` still requires a caller subject authorized for `metrics` `read`.

## Security Notes

- Do not store plaintext admin passwords in config, docs, logs, shell history, or tickets.
- Do not commit password hashes for real deployments. Keep them in environment variables or a deployment secret manager.
- Failed login responses intentionally do not reveal whether the username or password was wrong.
- Protected admin responses use `Cache-Control: no-store`.
- OIDC sessions are server-side and use HttpOnly cookies. Do not store raw OIDC tokens in browser localStorage, logs, policy, docs, or tickets.
