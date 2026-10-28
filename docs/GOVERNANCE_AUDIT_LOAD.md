<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance load: SQLite security export (#431)

This bounded localhost test covers SQLite security-CSV export. It is not a
usage-billing export or a PostgreSQL/distributed acceptance test. Run it with
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
run independently with the `go test` command above. These tests use SQLite and
synthetic Basic admin credentials.
They do not cover PostgreSQL, multiple router processes, durable crash/restart,
provider failures after response commitment, retention, or audit event
completeness for all endpoint/error types. #431 remains open.
