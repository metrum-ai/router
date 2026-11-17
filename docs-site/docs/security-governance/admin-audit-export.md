---
title: Admin audit export and policy simulation
doc_type: reference
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Admin audit export and policy simulation

The v5 Admin API provides domain-scoped audit export and read-only candidate
policy simulation. Both require an authenticated admin identity and the
matching `/admin/v1` permission.

## Export audit events

`GET /admin/v1/audit-events/export` requires `admin:audit` `read`. It returns
cursor-paginated control-plane, usage, and credential events for the
authenticated credential domain. Continue with the signed cursor until no page
remains. Rows carry previous and current SHA-256 hashes; verification detects
changed, missing, inserted, or reordered rows across the complete export.

Save every page in cursor order and verify the complete sequence offline:

```sh
metrum-ai-routerctl audit verify audit-export-page-1.json audit-export-page-2.json
```

Retention defaults to 90 days through
`server.admin_api.audit.retention_days`. Pruning runs in bounded batches and
advances the stored chain checkpoint. The checkpoint is held in the database
and is not externally signed in v5.0.0; keep an external copy of exports when
proof against database-level rewriting is required. Manual usage-audit deletion
requires separate `admin:audit` `delete` permission and can delete only the
oldest retained event in the caller's domain. It writes a deletion marker.

## Simulate a candidate policy

`POST /admin/v1/policy-sets/{set_id}/simulate` requires `admin:policy` `read`
in the global admin-policy domain. The candidate must be stored, valid, and
within policy row bounds. The request takes an object and action:

```json
{
  "object": "admin:audit",
  "action": "read"
}
```

The response includes the candidate set ID, current policy revision,
allow/deny decision, and a generic explanation. It does not expose matched
rules or mutate policy state. Use validate and diff endpoints to check a
candidate before activation. See the
[Admin API contract](https://github.com/metrum-ai/router/blob/main/docs/ADMIN_API_V1.md)
and [Admin authorization](../configuration/admin-authorization).
