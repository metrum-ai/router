---
title: Caller Tokens
doc_type: reference
---

# Caller Tokens

Caller token configuration binds router-issued bearer tokens to explicit users, projects, environments, allowed model groups, rate limits, quotas, and administrative roles. `/v1/models` is the caller-facing source of truth for the model groups allowed by the presented caller token.

This example is a partial subset of `config.example.yaml`; the shipped sample config is the source of truth.

```yaml
security:
  key_hygiene:
    min_entropy_bits: 128
    enforce: false
users:
  - id: example-standard
    name: Example Standard Caller
    type: service_account
    status: active
projects:
  - id: example-project
    name: Example Project
    status: active
project_memberships:
  - user_id: example-standard
    project: example-project
    role: developer
    status: active
callers:
  - id: example-standard-prod
    owner_user: example-standard
    project: example-project
    environment: prod
    status: active
    token_hash: TOKEN_HASH_OF_STANDARD_ROUTER_TOKEN
    token_id: rtr_metrum_example-standard_example-project_prod_k20260614
    token_format_version: 2
    token_entropy_bits: 256
    metrics_admin: false
    content_admin: false
    allow:
    - default
    - fast
    - small
    rate:
      rpm: 120
      tpm: 200000
      concurrent: 8
```

## Schema

Users and projects are account records, not values inferred from token names. Project memberships bind active users to active projects. A caller token config references an owner user, project, environment, token hash, public token ID, allow-list, and limit policy.

User IDs, project IDs, membership pairs, caller IDs, token hashes, and non-empty `token_id` values must be unique after normalization. Legacy `callers[].user` is accepted as a deprecated alias for `owner_user` only when both normalize to the same ID. Duplicate-hash validation errors identify caller IDs without printing hash values.

## Token Verifier

`token_hash` is a salted, computationally expensive verifier, `pbkdf2-sha3-512:i=<iterations>:<salt>:<key>`: PBKDF2-HMAC-SHA3-512 (pure Go `crypto/pbkdf2` and `crypto/sha3`) with unpadded base64url salt (16 to 64 bytes) and 64-byte key, and 10000 to 1000000 iterations (default 20000). SHA-3 is the Keccak family that the ML-KEM and ML-DSA post-quantum standards build on. The separator is `:` because config files are expanded with environment variables, which would rewrite `$name` sequences.

A `token_hash` caller needs a `token_id`, and the presented token must start with `<token_id>_`, which generated `rtr_metrum_` tokens always do. The router uses that public prefix to find the one candidate caller, runs the expensive verification once per process, and then authenticates later requests with a constant-time HMAC-SHA3-512 digest under a per-process random key. Expensive first-use verifications run in a small bounded pool. No caller `token_id` may be an underscore-delimited prefix of another.

New tokens use `rtr_metrum_<user>_<project>_<environment>_<key>_<secret>_v2_<crc32>`. The final eight lowercase hex digits are CRC32/IEEE over the token body before `_v2_`. The router checks this checksum before caller lookup. It detects malformed or mistyped tokens; it is not a cryptographic signature. Previously issued tokens without the v2 marker keep authenticating.

`security.key_hygiene.min_entropy_bits` defaults to 128; setting it to `0` selects that default, while explicit values must be 1–256. The generator sets `token_format_version: 2` and `token_entropy_bits: 256` for its 32-byte random secret. Keep that generated metadata with the matching token ID. Older token formats have unknown entropy because the router stores only a verifier; they warn by default. Caller environments `dev`, `development`, `test`, and `testing` also warn. Set `enforce: true` to reject unknown or below-floor tokens and development-environment tokens during config load. The token generator defaults an omitted environment to `prod`. Rotate old tokens before enabling enforcement.

To revoke the presented token after a leak, send it as the caller credential to `POST /v1/security/leaked-keys`. The route is available only when callers are served from the configuration control-plane database; YAML-only/file mode has no shared revocation store, so the route is not mounted there. The router records the verifier in a separate denylist keyed by token hash, independent of config sets and revisions, checks it during authentication on every endpoint, and publishes updates through the existing configuration revision reload path. Rollback and YAML import do not clear a denylist entry. The denylist write, revision notification and audit records commit in one transaction. Success returns `200`; bad, malformed, or already-revoked credentials return `401`; unavailable storage returns `503`. The raw token and verifier are never stored in usage rows or logged. Rotate the caller token to install a new verifier and restore access; only rotation restores access, and the revoked verifier stays refused.

For v5.0.0, a non-empty unsalted SHA-256 verifier fails at load and names the affected caller. Migrate a held token in place with `metrum-ai-routerctl callers migrate-legacy --config <config.yaml> --id <caller-id> --token-file <private-file>`; the command checks the token against the old verifier and updates the config. `callers hash` only prints verifier material. If the raw token is unavailable or cannot be used for in-place migration, rotate it with `metrum-ai-routerctl callers rotate --config <config.yaml> --id <caller-id> --token-out <private-file>` and distribute the new token once. Verify each migrated token with `metrum-ai-routerctl callers verify --token-file <file> --config <file> --id <caller>` before deploying.

Disallowed model-group requests return `403 model-not-allowed`. Inactive caller tokens return status-specific safe errors such as `key-disabled`, `key-suspended`, `key-expired`, or `key-rotated`. `/metrics` remains global operational telemetry and is restricted to metrics-authorized subjects.

## Rollback

Disable or rotate a caller token by changing caller `status`, removing the model group from `allow`, or replacing the token hash with a new generated router token. Keep old public token IDs available in historical reports; never reuse a token hash or expose raw router tokens in docs, tickets, or logs.

## Related

See [Available Models And Access](../getting-started/available-models), [Key Generation And Rotation](../operations/key-generation), [Admin Authorization](./admin-authorization), and [Router Configuration](./router-config).
