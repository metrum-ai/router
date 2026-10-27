<!-- Copyright (c) 2026 Metrum AI. All rights reserved. -->
# Admin API Keys

This runbook covers the dedicated admin credential for the router Admin API
(issue #350). Admin keys are a third credential class, separate from caller
API keys (`rtr_metrum_*`, used on `/v1/*` model APIs) and from
provider/upstream credentials (`api_key_env` references, which are never
accepted on inbound requests).

- Admin keys authenticate `/admin/*` routes only. They can never call the
  caller-facing model APIs, and caller or provider keys are never accepted on
  admin routes.
- The token shape is `rtr_admin_<key_id>_<secret>`. The `key_id` segment is
  public metadata used to select the verifier; the secret is 256 bits of
  randomness.
- At rest the router stores only a salted PBKDF2-HMAC-SHA3-512 verifier (the
  same `token_hash` format as caller tokens), read from the environment
  variable named by `key_hash_env`. Raw keys are never logged, stored in
  config, or returned after the one-time generation output. Errors and audit
  events carry only the safe `adminkey:<key_id>` subject.
- Every key carries an explicit permission list. There is no implicit
  admin-wide access.

## Provision a key

Generate a key on an operator workstation. The raw key is written once to a
new mode-0600 file; only the verifier hash is printed:

```bash
metrum-ai-routerctl adminkeys generate \
  --key-id ops-primary \
  --permissions config:read,config:write,config:activate,shadow:write \
  --domain example/prod \
  --token-out ./ops-primary.key
```

Store the raw key file in your secret manager. Put the printed `token_hash`
in a deployment secret or environment variable, and reference it by name in
`config.yaml`:

```bash
export ROUTER_ADMIN_KEY_OPS_PRIMARY_HASH='pbkdf2-sha3-512:i=20000:...'  # from a secret manager
```

```yaml
server:
  admin_auth:
    api_keys:
      - key_id: ops-primary
        key_hash_env: ROUTER_ADMIN_KEY_OPS_PRIMARY_HASH
        domain: example/prod
        permissions:
          - config:read
          - config:write
          - config:activate
          - shadow:write
```

The router refuses to start when an active key's `key_hash_env` is unset or
holds a malformed verifier; a typo fails closed at boot rather than at the
first admin call. `subject` defaults to `adminkey:<key_id>` and appears in
audit events and security reports.

Use the key as a bearer token on admin routes:

```bash
curl -fsS -H "Authorization: Bearer $(cat ops-primary.key)" \
  "$ROUTER_BASE_URL/admin/config/groups"
```

Never pass the raw key as a CLI flag value; the `routerctl groups … --api`
commands read it from `ROUTER_ADMIN_KEY` or a `--admin-key-file`.

## Permissions

Fine-grained permissions keep read-only operators and on-call roles away from
mutations:

| Permission | Grants |
|---|---|
| `config:read` | `GET /admin/config/...` (groups, sets, diff, status, history) |
| `config:write` | Group target writes: swap, add, remove, `:validate`, dry runs |
| `config:activate` | Set import/activate and rollback operations |
| `config:force` | `force: true` overrides of the `require_validation_status` policy |
| `shadow:write` | Shadow start/promote/stop |
| `admin:auth:read` | `GET /admin/auth/check` |

Admin keys may also hold the pre-existing `object:action` permissions used by
the other admin endpoints: `admin:callers:list|issue|rotate|revoke`,
`admin:reports:read|export|drilldown`, `admin:security_reports:read|export`,
`admin:training:export|label`, and `metrics:read`. Config validation rejects
unknown permission strings at startup.

The same permission names apply to browser-admin (HTTP Basic) users through
their `permissions` list, and to OIDC users through Casbin policy. The OIDC
mapping is `object/action`: `config:read` → `admin:config`/`read`,
`config:write` → `admin:config`/`write`, `config:activate` →
`admin:config`/`activate`, `config:force` → `admin:config`/`force`, and
`shadow:write` → `admin:shadow`/`write`. Example policy for an OIDC
read-only operator:

```yaml
server:
  admin_auth:
    authorization:
      enabled: true
      policy:
        - p, user:ops@example.com, example/prod, admin:config, read
```

## Rotate a key

Rotation is zero-downtime because multiple keys may be active at once:

1. Generate the replacement (`metrum-ai-routerctl adminkeys generate
   --key-id ops-2026-11 …`) and deploy it alongside the old key.
2. Move automation to the new raw key.
3. Remove the old key from `api_keys` (or set `status: disabled` for an
   immediate cutoff) and reload/restart.
4. Delete the old raw key and old verifier from the secret manager.

Every admin write is audited in `router_config_change_events` with the
`adminkey:<key_id>` subject, never the raw key, so you can confirm the old
key is unused before deleting it.

## Migration from browser admin only

Existing deployments keep working: HTTP Basic and OIDC behave exactly as
before when `api_keys` is empty, and `/admin/auth/check`, `/admin/reports`,
`/admin/callers`, and `/admin/training` are unchanged. To adopt admin keys,
add one `api_keys` entry with the permissions your automation needs and point
automation at `Authorization: Bearer rtr_admin_…`. No caller tokens change.

Note: `GET /admin/auth/check` accepts admin keys that hold
`admin:auth:read`; the Basic challenge behavior for browsers is unchanged.

## Security notes

- Caller keys (`rtr_metrum_*`) and provider keys are rejected on `/admin/*`
  with `401`; admin keys are rejected on `/v1/*`. The token namespaces are
  disjoint by construction.
- Failed admin-key authentication is logged without any token material; only
  the outcome code is recorded.
- Admin responses use `Cache-Control: no-store`.
- Keep raw keys out of tickets, docs, shell history, CI logs, and the config
  database. Only the verifier ever touches a config file or environment.
