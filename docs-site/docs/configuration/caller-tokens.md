---
title: Caller Tokens
doc_type: reference
---

# Caller Tokens

Caller token configuration binds router-issued bearer tokens to explicit users, projects, environments, allowed model groups, rate limits, quotas, and administrative roles. `/v1/models` is the caller-facing source of truth for the model groups allowed by the presented caller token.

This example is a partial subset of `config.example.yaml`; the shipped sample config is the source of truth.

```yaml
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

`token_sha256` (unsalted SHA-256 hex) is deprecated and will be removed in a future major release (#281). It still authenticates, and the router logs `caller <id> uses deprecated token_sha256` at startup and on control-plane reload. Set exactly one of `token_hash` or `token_sha256` per caller. Use `metrum-ai-routerctl callers hash --token-file <file>` to convert a token you still hold (it prints both `token_hash` and `token_id`), or `callers rotate` to issue a new one. Only tokens of the form `<token_id>_<secret>` can be converted in place; any other token must be rotated. `callers verify --config <file> --id <caller>` passes only when the router would authenticate the token, including the `token_id` prefix check.

To inventory legacy verifier fields in a local, unexpanded YAML file before migration, run `uv run --with pyyaml python scripts/legacy_caller_inventory.py config.yaml`. It prints caller IDs and a count, not verifier values. **This only inspects the specified file**; it does not inventory active or historical database-backed control-plane configurations. Use the control-plane's administrative records and deprecation logs to audit those separately.

Disallowed model-group requests return `403 model-not-allowed`. Inactive caller tokens return status-specific safe errors such as `key-disabled`, `key-suspended`, `key-expired`, or `key-rotated`. `/metrics` remains global operational telemetry and is restricted to metrics-authorized subjects.

## Rollback

Disable or rotate a caller token by changing caller `status`, removing the model group from `allow`, or replacing the token hash with a new generated router token. Keep old public token IDs available in historical reports; never reuse a token hash or expose raw router tokens in docs, tickets, or logs.

## Related

See [Available Models And Access](../getting-started/available-models), [Key Generation And Rotation](../operations/key-generation), [Admin Authorization](./admin-authorization), and [Router Configuration](./router-config).
