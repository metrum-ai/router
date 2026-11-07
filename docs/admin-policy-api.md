<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Policy resource integration

The policy resource implementation defines 19 typed operations: policy-set list/create/get/replace/delete, nested rule and membership list/create/get/replace/delete, and validate/diff/activate/rollback. The operation table composes with the shared registry and OpenAPI generator; production global registration and constructor bindings remain integration work. These handlers do not create another authorization system, problem renderer, cursor protocol or notification transport.

## Mutation boundary

A local governance mutex and the database runtime singleton exclusive lock cover the complete current policy, published configuration, enabled normal credentials, complete candidate, safety guard, persistence, revision and required audit callback. Real actor identity and both operation and immutable management-capability authorization precede revision/resource/inventory/candidate work. Unknown storage, builder and cancellation errors become safe authorization-unavailable problems.

Draft children have stable parent-scoped record IDs. Active and retired content is immutable. Rollback accepts retired history and creates a fresh set ID, next same-name version and fresh child IDs. It does not reactivate or rewrite history. Clone creation, activation/retirement, active-set metadata, monotonic revision and audit callback are transactional. Notification is queued through the existing transport only after audit acknowledgement, in the same transaction.

The change-audit callback is now the real same-transaction binding (#869): the request's authenticated `AdminAuditMeta` (recorded by the `/admin/v1` mount, never from a header) becomes exactly one `router_admin_audit_events` row through `insertAdminAuditUsageTx`, carrying the actor, operation, request ID, ticket, revision, set ID and immutable hashes. A request without authenticated audit metadata fails closed with the fixed `503 unavailable` problem before any policy work, and a rejected audit insert aborts the whole change, so no revision, active-set pointer or notification can advance without its audit row.

Read-only validation and diff do not advance the mutation revision or write change audit. Validation can invoke real authorization-attempt audit; that callback is not the change-audit callback. Production integration must test it under a bounded deadline and a single-connection pool, because it runs while the read transaction retains a shared runtime lock. Diff compares both normalized bounded views in one coherent transaction, preserving duplicate multiplicity and deterministic ordering. Request contexts propagate into resource read transactions.

## Freshness and migration

Only trusted frozen operation-effect metadata marks a mutation. Reads and validation remain outside the policy mutation-age gate; changing method, action or headers cannot bypass the conservative mutation default. PostgreSQL unknown/expired synchronization age fails mutations closed, including emergency credentials. Static/SQLite authority remains age-exempt without fabricated observations.

The runtime migration is registered in the usage migration ledger as `2026110704` (schema 15, after the audit migrations `2026110701`-`2026110703`), with immutable checksum, handler/postcondition keys, transactional online execution and an explicit dependency on the audit denial-columns migration. The handler seeds legacy active state only for a newly created runtime table, refuses ambiguous active policies, inconsistent active IDs and missing/invalid singleton data, and preserves revisions on reapplication instead of resetting them. No serving constructor, request path or reload runs AutoMigrate; before the migration runs, PostgreSQL mutations stay stale while reads continue.

The PostgreSQL revision notifier owns its connection from a single goroutine and shutdown cancels then joins the listener; connection cleanup is awaited through pgx `CleanupDone` with a bounded five-second wait that logs and continues instead of letting a stuck socket hang shutdown.

## Required before production acceptance

- Complete published-generation credential inventory and reverse guard before configuration/key persistence and live publication.
- Rejected-attempt audit and admin-plane metrics using the existing audit infrastructure, plus production listener wiring of the operations.
- Global route composition and production dependency bindings; listener maximum-age wiring.
- Real upgrade acceptance of the registered migration on PostgreSQL, including revision preservation.
- CLI, real PostgreSQL locking/cancellation/LISTEN and multi-replica convergence, coherent-generation compatibility and release smoke.

The focused mounted/backend tests, independent bounded code reviews, and full repository tests are component evidence, not completion of these gates or of the authorization issues.
