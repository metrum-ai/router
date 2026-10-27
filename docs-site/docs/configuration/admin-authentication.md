---
title: Admin Authentication
doc_type: howto
---

# Admin Authentication

Metrum AI Router separates caller API authentication from browser-admin authentication.

- Router caller tokens authenticate applications and CLI users for `/v1/*` model APIs.
- Metrics-admin caller tokens read `/metrics`.
- HTTP Basic can establish a simple browser-admin identity for `/admin/*` routes.
- OIDC can establish browser-admin identity through an enterprise identity provider and a server-side session cookie.
- Admin API keys (`rtr_admin_*` bearer tokens, hashed at rest) authenticate automation for `/admin/*` routes with fine-grained permissions such as `config:read`, `config:write`, and `shadow:write`; see the in-repo runbook `docs/ADMIN_API_KEYS.md` and the [Admin Config API](../reference/admin-config-api).
- Policy-based authorization controls metrics and admin-report permissions. See [Admin Authorization](./admin-authorization).

HTTP Basic, OIDC, and admin API keys are disabled by default and can be enabled independently. Basic is useful for simple self-hosted deployments and bootstrap access. OIDC is intended for deployments that want browser administrators to authenticate through an identity provider such as Google Workspace.

## Basic Auth Configuration

Use a bcrypt password hash stored in deployment secrets or environment variables:

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

`password_hash_env` names an environment variable that contains the bcrypt hash. Do not put plaintext passwords in router config.

`trusted_proxy_cidrs` controls whether the router honors `X-Forwarded-Proto: https` from a reverse proxy. Leave it empty unless the router is behind a proxy you control, and set it only to that proxy's network or host address.

For local loopback testing only, set `allow_insecure_http: true`:

```yaml
server:
  admin_auth:
    basic:
      enabled: true
      realm: Local Router Admin
      allow_insecure_http: true
      trusted_proxy_cidrs: []
      users:
        - username: admin
          password_hash_env: SMART_ROUTER_ADMIN_PASSWORD_HASH
          subject: basic:admin
          domain: local/dev
          permissions:
            - admin:auth:read
```

Generate a hash with an operating-system package or a trusted admin tool. One Python example:

```bash
python3 - <<'PY'
import bcrypt
password = b"replace-with-a-strong-password"
print(bcrypt.hashpw(password, bcrypt.gensalt(rounds=12)).decode())
PY
```

Install the hash as a deployment secret or environment variable before the router starts:

```bash
export SMART_ROUTER_ADMIN_PASSWORD_HASH='$2b$12$replaceWithTheGeneratedHash'
```

## OIDC Session Configuration

OIDC uses Authorization Code flow with PKCE. The router verifies the ID token with provider discovery and JWKS, validates state and nonce, then creates a server-side session cookie. The browser receives only the session cookie; raw OIDC tokens are not returned by admin auth responses.

```yaml
server:
  admin_auth:
    oidc:
      enabled: true
      issuer_url: https://accounts.google.com
      client_id_env: GOOGLE_OIDC_CLIENT_ID
      client_secret_env: GOOGLE_OIDC_CLIENT_SECRET
      redirect_url: https://<router-host>/admin/auth/callback
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

Register `https://<router-host>/admin/auth/callback` with the IdP. Keep `client_id_env` and `client_secret_env` as environment-variable names and store the actual values in deployment secrets.

For Google Workspace, use issuer `https://accounts.google.com`, restrict `allowed_domains` to the approved Workspace domains, and grant authorization roles to stable subjects such as `user:alice@example.com`. Standard Google ID tokens do not always include group membership; use explicit policy unless a governed groups claim is configured and validated for the deployment.

## Runtime Behavior

`GET /admin/auth/check` is a protected stub endpoint for validating the first admin identity path.

| Condition | Result |
| --- | --- |
| Basic Auth disabled | `404` |
| Missing or invalid credentials | `401` with `WWW-Authenticate` |
| Valid credentials without route permission | `403 admin-forbidden` |
| Valid credentials with `admin:auth:read` | `200` with safe subject metadata |

Responses for protected admin routes use no-store cache headers. Passwords and password hashes are not returned.

OIDC browser routes:

| Route | Behavior |
| --- | --- |
| `GET /admin/auth/login` | Redirects to the IdP with state, nonce, and PKCE. |
| `GET /admin/auth/callback` | Verifies the OIDC response and creates a server-side session. |
| `GET /admin/auth/me` | Returns safe metadata for the active Basic or OIDC admin identity. |
| `POST /admin/auth/logout` | Deletes the server-side session and expires the admin cookie. |

Invalid or missing sessions return `401`. Valid sessions without a matching authorization policy receive endpoint-specific `403` responses such as `reports-forbidden`.

## Smoke Tests

Missing credentials should challenge:

```bash
curl -i "$ROUTER_BASE_URL/admin/auth/check"
```

Example response:

```http
HTTP/1.1 401 Unauthorized
Cache-Control: no-store
WWW-Authenticate: Basic realm="Metrum AI Router Admin", charset="UTF-8"
```

Invalid credentials return the same status without revealing which field was wrong:

```bash
curl -i -u admin:wrong-password "$ROUTER_BASE_URL/admin/auth/check"
```

Valid credentials with the current stub permission return safe subject metadata:

```bash
curl -i -u admin:replace-with-the-admin-password "$ROUTER_BASE_URL/admin/auth/check"
```

Example response:

```json
{
  "ok": true,
  "source": "basic",
  "subject": "basic:admin",
  "domain": "example/prod"
}
```

If the password is valid but `admin:auth:read` is not granted, the endpoint returns:

```json
{
  "error": {
    "type": "admin-forbidden",
    "message": "admin-forbidden"
  }
}
```

## Authorization Policy For Admin Reports

Browser-admin Basic Auth and OIDC sessions establish identity only. Admin reports use authorization policy under `server.admin_auth.authorization` for authorization decisions. The same authorization layer checks `/metrics` and content-capture maintenance for caller-token subjects.

```yaml
server:
  admin_auth:
    authorization:
      enabled: true
      source: static
      policy_file: ""
      policy:
        - g, caller:ops-metrics-key, metrics_admin, example/prod
        - g, caller:content-admin-key, content_admin, example/prod
        - g, basic:admin, reports_admin, example/prod
        - g, user:alice@example.com, reports_admin, example/prod
        - p, metrics_admin, example/prod, metrics, read
        - p, content_admin, example/prod, content:capture, delete|purge
        - p, reports_admin, example/prod, admin:reports, read|export|drilldown
```

The report surface checks object `admin:reports` with action `read` for pages, aggregate JSON APIs, and static assets. Markdown export checks action `export`. Request detail under `/admin/reports/api/request/<request_id>` and request evidence under `/admin/reports/api/request-evidence?request_id=<request_id>` check action `drilldown`.

Content-capture maintenance endpoints check object `content:capture`; delete-by-request uses action `delete` in the captured row's caller project/environment domain, and retention purge uses action `purge`. Existing `content_admin: true` caller entries receive compatible authorization grants for their own domain at startup.

When reports are enabled under `server.admin_reports`, ordinary router caller tokens are rejected with `403 reports-forbidden` rather than being treated as browser-admin credentials.

Normal model APIs continue to use router caller tokens:

```bash
curl "$ROUTER_BASE_URL/v1/models" \
  -H "Authorization: Bearer $ROUTER_TOKEN"
```

Metrics continue to use caller tokens. Existing `metrics_admin: true` callers receive equivalent authorization grants at startup:

```bash
curl "$ROUTER_BASE_URL/metrics" \
  -H "Authorization: Bearer $METRICS_ADMIN_ROUTER_TOKEN"
```

## TLS Requirement

Production deployments should set `allow_insecure_http: false`. The router accepts Basic credentials only when the incoming request is HTTPS or when a configured trusted reverse proxy forwards `X-Forwarded-Proto: https`.

Only trusted proxy CIDRs are allowed to assert forwarded HTTPS:

```yaml
server:
  admin_auth:
    basic:
      allow_insecure_http: false
      trusted_proxy_cidrs:
        - 127.0.0.1/32
        - 172.18.0.0/16
```

Use the actual reverse-proxy subnet for the deployment. Do not add broad networks unless the router is isolated from direct client traffic on those networks. The values above are placeholders: `172.18.0.0/16` is a common local kind or Docker bridge range and is normally wrong for a cloud Kubernetes cluster, whose ingress controller pods take addresses from the cluster pod network.

Use `allow_insecure_http: true` only for local development and loopback smoke tests.

### Troubleshooting: Correct Credentials Return 401

The forwarded-HTTPS check runs before the password comparison. When the reverse
proxy's source address falls outside `trusted_proxy_cidrs`, the router treats the
request as plaintext HTTP and answers with the Basic challenge, so a valid password
looks identical to an invalid one.

Symptom: every `/admin/*` request returns `401` with `WWW-Authenticate: Basic ...`,
even with credentials that match the configured bcrypt hash.

Confirm the address the router receives, then widen or correct the configured range
to cover it. For a Kubernetes ingress, compare `trusted_proxy_cidrs` against the
ingress controller pod IP:

```bash
kubectl get pods -n <ingress-namespace> -o wide
```

Rotating the admin password does not affect this failure.

## Authorization Boundary

Basic Auth establishes a subject such as `basic:admin`; OIDC with `subject_claim: email` establishes a subject such as `user:alice@example.com`. Neither decides what that subject may do. Current stub-route permissions are intentionally small. Metrics and admin report authorization are expressed through the deployment's authorization policy.

Equivalent policy expressed as separate authorization permission lines:

```text
g, basic:admin, reports_admin, example/prod
g, user:alice@example.com, reports_admin, example/prod
p, reports_admin, example/prod, admin:reports, read
p, reports_admin, example/prod, admin:reports, export
p, reports_admin, example/prod, admin:reports, drilldown
```

The username is not the authorization rule. The stable subject, domain, object, and action should be the policy inputs.

## Rollback

Disable the affected browser identity method and restart the router:

```yaml
server:
  admin_auth:
    basic:
      enabled: false
      users: []
    oidc:
      enabled: false
```

After Basic rollback, `/admin/auth/check` should return `404`. After OIDC rollback, `/admin/auth/login`, `/admin/auth/callback`, `/admin/auth/me`, and `/admin/auth/logout` should return `404` unless another admin identity method handles the route. `/v1/*` model APIs and `/metrics` keep their existing token behavior.
