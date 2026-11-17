---
title: Admin client identities
doc_type: reference
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Admin client identities

Register machine identities and public credential metadata through the
authenticated `/admin/v1/clients` API. The registry supports `adminkey`,
`mtls`, `oauth`, and `federated` credentials. Registration stores identity
bindings, never credential secrets. Machine clients use the dedicated admin
listener and still require admin authorization.

## Lifecycle

Use these endpoints:

| Operation | Endpoint |
| --- | --- |
| List or register clients | `GET` or `POST /admin/v1/clients` |
| Read or update client | `GET` or `PUT /admin/v1/clients/{client}` |
| Disable, enable, or delete client | `POST /admin/v1/clients/{client}/disable`, `/enable`, or `DELETE /admin/v1/clients/{client}` |
| Register credential metadata | `POST /admin/v1/clients/{client}/credentials` |
| Revoke credential | `DELETE /admin/v1/clients/{client}/credentials/{credential}` |
| Review effective grants and credential use | `GET /admin/v1/clients/access-review` |

Credential IDs and subjects must exactly match the identity returned by the
authenticator. OAuth and federated JWTs mapped from a `client_id` claim use the
ID `client_id:<value>`. mTLS bindings use stable SAN/SPIFFE identity and subject,
not a certificate fingerprint. See the
[Admin API contract](https://github.com/metrum-ai/router/blob/main/docs/ADMIN_API_V1.md#machine-clients)
for request schemas and endpoint details.

The router checks active client and credential rows on every admin request, so
disable, delete, expiry, and revocation take effect across replicas immediately.
Once any credential is registered for a subject, only credentials registered
to that subject can authenticate. Register replacement administrator
credentials before disabling old ones; the API refuses changes that would
remove the last usable global administrator.

## Access review and recovery

The access-review endpoint reports registered metadata, last-use state, and
effective grants. Disabled clients and disabled, revoked, or expired credentials
have no grants. Unused means no successful use; the durable unused warning is
emitted after 24 hours. Expiry warnings begin within 30 days of expiration.
Review failures return `503 admin-client-review-unavailable` when grants cannot
be resolved.

To recover from a disabled client, enable it or register a replacement
credential, subject to the administrator lockout guard. A deleted client ID
cannot be enabled; create a new client ID. Credential revocation is permanent
for that credential; register a new binding to restore that identity.
