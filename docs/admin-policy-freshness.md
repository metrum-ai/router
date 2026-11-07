<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Administrative policy freshness

A PostgreSQL-backed authorization policy is refreshed through complete, transaction-bound authorizer builds. The reader locks the policy runtime singleton in shared mode while loading the policy. Writers of that singleton row take the same row exclusively (`FOR UPDATE`) before changing any policy data or revision; policy writers are delivered by the guarded policy-resource slice, and this freshness slice contains none, so the shared clause protects the read against such a writer rather than against another reader. The new immutable authorizer is published only after the read transaction succeeds; the observation time is sampled before the shared-locked read, so a slow read can only make the recorded synchronization look older than the data, never fresher. A successful observation is recorded after publication. Failed reads, builds and transactions keep the previous authorizer and observation time. Desired and applied revisions are distinct, and revision rollback is refused.

The default poll interval is five seconds, and `admin_auth.authorization.max_staleness` (a positive Go duration, default `30s`) sets the maximum accepted age. An empty value keeps the default; an unparseable or non-positive value fails configuration validation at startup, before the listener serves, and never becomes a request-time `422 invalid-policy`. `Service.ConfigureAdminPolicyFreshness` accepts only a positive duration, is safe to call concurrently, never creates a successful observation, and reports a bad duration with its own configuration error. `Service.PolicyFreshness` exposes the observation/revision state.

Only a trusted in-process adapter may mark a request using `WithAdminPolicyMutation`. The shared `/admin/v1` mount derives that mark from the frozen registry entry: only the operation's declared method is consulted (`GET`/`HEAD` are reads; every other method is a mutation). A request header, the request URL, the permission action, the principal kind or the replica count never selects this gate, and the mark is applied before any decision so every non-read operation and its always-checks — including emergency requests — are covered. A conservative consequence: a trusted POST that only validates is treated as a mutation by this slice; the guarded policy-resource slice adds an explicit no-side-effect marking for those. Authentication as an emergency principal does not bypass the gate. Missing or expired PostgreSQL observations produce the fixed `503 policy-stale` problem. Static and SQLite policies are age-exempt; this does not fabricate a successful synchronization. Other authority failures produce the fixed unavailable problem, never a database diagnostic. Wrapped authorization errors are mapped with `errors.As` through their `AdminProblemCode` method, so a `%w`-wrapped `ErrPolicyStale`, `ErrLastPolicyAdmin` or `ErrInvalidPolicy` keeps its documented status and code.

## One publisher, and what a reload may publish

The machine authorizer has exactly one publisher: the synchronized path that reads the authoritative singleton revision under the governance lock and stores the built snapshot. A configuration reload does **not** build and store an authorizer of its own. It asks the synchronized path to publish, passing the not-yet-published configuration, and only then exposes replacement credentials and the new configuration snapshot. If that publication fails, the reload publishes no authorizer, no credentials and no configuration; the caller runtimes it had already rebuilt are restored, and the successful-synchronization marker is cleared so mutating administrator requests fail closed instead of being authorized by an authorizer that no longer matches the published configuration. A deployment without the policy runtime table (before the migration has run) still publishes an authorizer built from the store, but claims no freshness, so PostgreSQL mutations stay stale until the migration seeds the singleton.

## Revision rollback (database restore) recovery

Revisions are monotonic in normal operation: the synchronization path refuses a revision lower than the last one it observed, so a restored database cannot silently lower the policy generation and have it accepted. Recovering from a restore therefore needs an explicit operator action rather than a quiet downgrade. **Today the only supported recovery is a restart**; there is no operator reseed command in this change.

1. Restore the usage database (the `authz_policy_runtime` singleton and the policy tables together) as one consistent snapshot.
2. Restart the routers. A restart rebuilds the freshness state from scratch: the first successful complete synchronization after startup seeds the observation, so the restored revision is accepted once and monotonic checking resumes from it.
3. Until that restart, replicas whose last applied revision is higher than the restored one keep the previous authorizer and refuse mutations (fail closed) — they do not roll back silently. A guarded statement that reseeds the singleton revision without a restart is planned with the policy CLI surface in the guarded policy-resource change (#866); it is not available here and must not be assumed by an operator runbook.

## Integration boundaries

These primitives do not independently complete the policy API or the control-plane release:

- The explicit policy runtime schema migration and singleton seed must run before PostgreSQL synchronization can succeed. Serving startup never creates the table. An absent table leaves PostgreSQL mutations stale while reads continue.

  **Deployment consequence:** with PostgreSQL authority and DB-sourced policy, every `/admin/v1` mutation answers `503 policy-stale` until the runtime table exists, because no successful synchronization can be observed yet. The table and its ledger migration are delivered by the guarded policy-resource change (#866), which ships in the same release as this freshness gate. Reads, and static/SQLite authority, are unaffected.
- The guarded policy-resource slice reuses this transactional synchronization and marks its own read-only validation as no-side-effect.
- All policy writers must use the singleton locking protocol, complete published-configuration credential inventory, and same-transaction audit/replay integration.
- Configuration and credential changes need a reverse last-admin guard before persistence and live publication. A post-commit reload callback or a local mutex alone is insufficient.
- Replicas must use identical authoritative administrative identity configuration. Revision mismatch diagnostics do not provide distributed atomicity across separate stores.
- The authentication slice owns the single published identity bundle; the synchronization path calls that publish rather than introducing a second one.

## Local verification

Focused race coverage includes typed safe error rendering, trusted mutation classification, real-actor context preservation, complete synchronization and revocation, old request pins, failed-read/build clock preservation, monotonic revisions, unchanged polling, cancellation, and the shared configuration poller. Mount-level tests drive the real `Service.Authorize` behind `/admin/v1` with an expired PostgreSQL clock and require every non-read operation (emergency included) to answer `503 policy-stale` while every read still works, and require wrapped `ErrPolicyStale`/`ErrLastPolicyAdmin`/`ErrInvalidPolicy` to keep their statuses. The poller fixture's cross-goroutine counter uses atomic accesses; its existing waits and assertions are retained.

The SQLite-backed synchronization fixtures do not establish PostgreSQL locking, LISTEN behavior, contention cancellation or multi-replica convergence. Those remain separate integration acceptance tests.
