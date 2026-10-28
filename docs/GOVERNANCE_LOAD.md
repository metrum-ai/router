<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance load: reservation baseline (#431)

Run `make governance-load` to repeat the local governance load tests with the
Go race detector. They include this in-process reservation baseline, the
[HTTP token-reservation probe](GOVERNANCE_HTTP_LOAD.md), the
[PII/fallback probe](GOVERNANCE_PII_LOAD.md), and the
[SQLite security-export probe](GOVERNANCE_AUDIT_LOAD.md). They use synthetic
callers and no hosted requests; they do not require PostgreSQL. These bounded
cases are not the complete #431 acceptance suite.

## Local baseline

- Date: 2026-10-28 UTC.
- Router production-code base: `306c38d95c7896baab36c58e94923ecd90a7770c`,
  plus the test harness introduced with this document.
- Machine: Linux x86_64, 16 visible CPUs, AMD Ryzen 7 7730U; Go 1.27.3.
- Commands: `go test ./internal/router -run '^TestGovernanceLoad' -count=1 -v`
  and `go test -race ./internal/router -run '^TestGovernanceLoad' -count=10 -v`;
  both passed. The one-command target also runs these tests with `-race`.
- No hosted calls; cost $0.

Each worker attempts to reserve 10 tokens; all accepted reservations stay
outstanding until every admission attempt has completed. A synthetic response
then settles at 15 tokens. The tests check exact admission and rejection counts,
rejection reasons, counter settlement, slot release and reservation release.

| Concurrency | Admission limit | Accepted | Rejected | Settled tokens | Difference from reservations |
|---:|---:|---:|---:|---:|---:|
| 1 | 1 | 1 | 0 | 15 | 5 |
| 32 | 8 | 8 | 24 | 120 | 40 |
| 128 | 32 | 32 | 96 | 480 | 160 |

The same counts were observed independently for day tokens, month tokens,
lifetime tokens, tokens per minute and requests per minute. For the first
four cases, the configured token limit was 10 × the admission limit, so the
settlement exceeded that limit by the final column. For the RPM case that
column is a token-estimate difference, **not** an RPM overspend. A four-caller
in-process case with 32 attempts per caller and a separate 80-token day limit
admitted 8 and rejected 24 per caller; each settled 120 tokens, 40 over its
own limit, without cross-caller accounting or outstanding reservations. It
does not exercise API key authentication.

These numbers show the overshoot for this deliberately under-reserved
scenario only. They are not a general upper bound: actual upstream usage can
exceed the reservation, and a bound requires separate assumptions about
upstream output limits, input estimation, and outstanding work. The tests do
not cover HTTP requests, streaming, persistence, multiple processes, database
accounting, retries, PII filtering, exports, or cost-denominated budgets.
PostgreSQL and SQLite-backed acceptance, mixed streamed/unary requests, PII
and audit-export load tests, release security findings and the complete
overspend-bound analysis remain open under #431.
