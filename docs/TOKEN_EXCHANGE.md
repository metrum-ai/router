<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Workload token exchange

The optional `security.token_exchange` listener route exchanges a configured workload JWT for a short lived router token. It uses the admin plane's issuer discovery and JWKS validation rules. Each issuer and audience is explicit, and each configured subject maps to one caller and fixed maximum scope.

```yaml
security:
  token_exchange:
    enabled: true
    signing_key_env: ROUTER_MISSION_TOKEN_KEY
    default_ttl: 15m
    issuers:
      - issuer: https://identity.example.com
        audience: router-exchange
        task_claim: workflow.task.id # Optional dotted path in the verified workload JWT.
        algorithms: [RS256, ES256]
        allowed_jwks_hosts: [identity.example.com]
        subjects:
          - claim: sub
            value: system:serviceaccount:jobs:worker
            caller_id: batch-worker
            human_owner: team-ml-platform
            task_id: nightly-batch # Optional grant-pinned task; overrides task_claim.
            allowed_groups: [fast, accurate]
            max_spend_usd: 0.25
            max_tokens: 4096
            allowed_tools: [search, calculator]
```

Set `ROUTER_MISSION_TOKEN_KEY` to at least 32 random bytes through the deployment secret manager. The value is read at startup and is never accepted from configuration text. Keep the same key on every replica that must accept exchanged tokens. Key rotation is a hard cutover: change the key on every replica together; all outstanding tokens signed by the previous key immediately become invalid. This implementation does not support overlapping signing keys.

POST an `application/x-www-form-urlencoded` RFC 8693 request to `/oauth/token` with `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`, `subject_token`, `subject_token_type=urn:ietf:params:oauth:token-type:jwt`, and a space-separated `scope` naming allowed model groups. Task attribution is optional. Set issuer `task_claim` to a dotted path for a signed JWT string claim, or pin `task_id` on an individual subject grant; a grant-pinned value takes precedence. If neither source is configured, the exchange succeeds without a task ID. When a source exists, its value must be 1–128 ASCII letters, digits, `.`, `_`, `:`, or `-`, beginning with a letter or digit. A form `task_id` is only an assertion equal to the resolved value and is rejected when no source exists. This supports stock Kubernetes service-account tokens and SPIFFE JWT-SVIDs, which do not carry a custom task claim. The response contains a bearer access token and `expires_in`. Invalid workload credentials return a generic OAuth error. Successful exchanges are emitted as Authentication audit events naming the caller and grant; failures are recorded as security events without logging the subject token or issued bearer token. A configured subject grant is limited to 30 exchanges per minute.

The issued token inherits the current configured caller's account status and may select only the requested subset of configured groups. Caller status and subject grants are rechecked from the live configuration on every exchange and token use; a reload that suspends the caller or removes the grant invalidates its mission tokens. Mission tokens are accepted only on `POST /v1/chat/completions`, `POST /v1/responses`, `POST /v1/messages` (including the Anthropic alias and count-token routes), `POST /v1/responses/input_tokens`, `POST /v1/embeddings`, `GET /v1/models`, and `GET /v1/models/{model}`. Every other serving route returns 401 for a mission token, including `/mcp`, `/v1/security/leaked-keys`, content-capture operations, `/metrics`, and `/v1/usage`; a mission token cannot revoke its parent caller key.

Cumulative `max_tokens` and `max_spend_usd` apply per configured issuer/subject grant and are shared across tasks and token refreshes, so re-exchanging a workload token does not reset the cap. Each admitted request atomically reserves its estimated token and spend budget; request finish releases that reservation and settles the recorded usage and cost, even if the stream finishes after the bearer token expires. Concurrent requests are allowed when their combined reservations fit. Budget and exchange-rate state are bounded by configured subject grants and held per router process; budget entries remain for the grant until a config reload removes it or the process restarts. They are not shared across replicas, so configure equivalent mission limits on every router process. Embedding requests also consume the mission budget. The token lifetime is at most 15 minutes. Subject JWTs are bearer credentials and can be replayed during their lifetime; the router does not keep a subject-token `jti` replay store. Kubernetes service-account tokens, OIDC ID tokens, and SPIFFE JWT-SVIDs are accepted when their issuer, audience, signature, and subject grant match. Sender-bound tokens with `cnf` are rejected because this endpoint does not validate sender proof. Tool names are limited to `allowed_tools` on model requests. A configured task ID, when present, and the human owner are written to usage rows; task attribution remains empty when no source is configured. A project can set `forbid_long_lived_keys: true` to reject its configured static caller keys while still allowing exchanged tokens.

The usage database migration adds `task_id` and `human_owner` columns. Deploy it through the normal [data migration runbook](DATA_MIGRATIONS.md) before serving this release when using deployment-job migration policy. Exchange is disabled by default. Kubernetes service-account JWTs, OIDC ID tokens, and SPIFFE JWT-SVIDs can be exchanged; X.509 SVID exchange is not supported.
