<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance load: security export (#431)

This bounded localhost test covers SQLite and optional PostgreSQL security-CSV
export and per-key request-usage reports. It is not a usage-billing export or a
distributed acceptance test. Run
the SQLite case with
`go test ./internal/router -run '^TestGovernanceLoadAuditExport$' -count=1 -v`.

## Local observation

On 2026-10-28 UTC, using router production-code base
`306c38d95c7896baab36c58e94923ecd90a7770c`, Linux x86_64, 16 visible CPUs,
AMD Ryzen 7 7730U and Go 1.27.3, the test launched 64 concurrent requests
through a localhost router using two synthetic caller tokens. The scripted
primary saw 64 attempts and returned HTTP 503 each time; the fallback saw 64
attempts. Of the 64 responses, 32 streamed and 32 were unary. Eight clients
closed the
response after receiving the first SSE line; the synthetic upstream observed
all eight cancellations. These eight cutoffs were on the first synthetic key.

After draining the asynchronous writer, the security CSV was queried for each
public token ID. Each key's 32 request IDs appeared exactly once, without a
cross-key row; the export did not contain the synthetic credentials or request
text. The observation includes the cancelled requests, but does not prove their
billing settlement, event status semantics, or every possible retry/cancellation
race. Calls were local and cost $0.

Commands run:

```sh
go test -race ./internal/router -run '^TestGovernanceLoadAuditExport$' -count=3 -v
make governance-load
```

Both passed on the original stacked test branch; `make governance-load` is
available once the separate reservation-baseline work is merged. This test can
run independently with the `go test` command above. The SQLite case uses synthetic Basic admin credentials.

## Per-key request-usage observation

On 2026-10-30 UTC, production-code base
`566fd81edf9f790c31e24abfb9630a29b3c2d4e5` on Linux x86_64, 16 visible
CPUs (AMD Ryzen 7 7730U), Go 1.27.3, passed the expanded scenario four times
under the race detector with SQLite and four times with disposable local
PostgreSQL 16.15 UTF-8 over a private Unix socket. Each run matched all 64
client request IDs once in the per-key admin request report: 32 per synthetic
key, no duplicates or cross-key rows, each with two attempts and fallback
recorded. That includes eight deliberately cut streams whose upstreams observed
cancellation. The existing security CSV checks also passed. PostgreSQL test
schemas were absent afterward and the private server was stopped.

This verifies bounded request-row/accounting presence, not usage-token
correctness, provider charges or billing settlement: no monetary totals were
reconciled. It also does not establish at-most-once behavior across multiple
router processes or crashes. The PostgreSQL test remains opt-in.

```sh
go test -race ./internal/router -run '^TestGovernanceLoadAuditExport$' -count=4 -v
# With a disposable PostgreSQL DSN, use the PostgreSQL command below with -count=4.
```

## Completed-response token and price fixture

The audit workload also supplies a synthetic fallback price of $2 per million
input tokens and $5 per million output tokens. For each of the 56 completed
requests per run, the request report must match the mock upstream's terminal
usage: three input tokens, two output tokens, five total, and $0.000016 in
configured-price cost. The failed primary returns no usage. Eight intentionally
cut streams are excluded from this token/cost assertion because they do not
receive a terminal usage frame; their report-row isolation and fallback metadata
are still checked. This is deterministic synthetic price arithmetic, not a
provider invoice, a reconciliation of canceled streams, or proof for other
pricing shapes.

On 2026-10-30 UTC, the expanded test passed twice under the race
detector on SQLite and twice on disposable local PostgreSQL 16.15 UTF-8, on
production-code base `6ab2d35822d2004271a24c4f3aa56e5212514dbd`. The 56
asserted rows split into 24 for the first key and 32 for the second. PostgreSQL
schemas were absent afterward and its private server stopped. No hosted or
paid services were used.

```sh
go test -race ./internal/router -run '^TestGovernanceLoadAuditExport$' -count=2 -v
# With the disposable PostgreSQL DSN below:
go test -race ./internal/router -run '^TestGovernanceLoadAuditExportPostgres$' -count=2 -v
```

## PostgreSQL observation

On 2026-10-29 UTC, the same 64-worker scenario passed three times under the
race detector against a disposable local PostgreSQL 16.15 UTF-8 cluster using a
private Unix socket and synthetic credentials. Each run observed all eight
upstream cancellations; the two filtered exports each contained exactly 32
unique request IDs, with no cross-key rows, synthetic bearer tokens or prompt
text. The tests used a fresh isolated schema, explicitly applied maintenance
migrations and the historical-validation job, and verified serving
compatibility before starting the router. This is a bounded security CSV result,
not usage-billing reconciliation.

To reproduce, provide a **disposable database** on a trusted local server; the
test creates and drops a schema and must never target production:

```sh
GOVERNANCE_LOAD_POSTGRES_DSN='host=/path/to/private/socket port=5432 user=test dbname=disposable sslmode=disable' \
  go test -race ./internal/router -run '^TestGovernanceLoadAuditExportPostgres$' -count=1 -v
```

With no `GOVERNANCE_LOAD_POSTGRES_DSN` the PostgreSQL case skips; CI without an
explicit disposable database does not establish PostgreSQL coverage. Neither
case covers multiple router processes, durable crash/restart, provider failures
after response commitment, retention, or audit-event completeness for all
endpoint/error types. #431 remains open.
