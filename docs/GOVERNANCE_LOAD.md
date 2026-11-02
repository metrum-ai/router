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

# Shipped-binary governance harness (#573)

`benchmarks/governance-load/` extends the bounded in-process evidence above by
starting real `metrum-ai-router` processes with PBKDF2 caller tokens, driving
HTTP load against a deterministic loopback OpenAI-Chat upstream, and reading the
usage and security-access rows back from the configured store. It covers
sustained limits, current authentication, PII on streaming/retry/failover paths,
cancelled-stream settlement, dollar-budget conversion, crash/restart and several
router processes. It runs with SQLite or PostgreSQL. `make governance-load-v47`
runs every scenario locally against SQLite; see the harness README for the
Postgres invocation and the scenario inventory.

This is a local dry run with synthetic usage, not the complete #573 acceptance
evidence. It makes no hosted calls and costs $0.

## Local dry run

- Date: 2026-11-02 UTC.
- Router: `0860943cc7` (harness commit; no product code changed by it).
- Host: AMD Ryzen 7 7730U, 16 visible CPUs, Linux x86_64; Go 1.27.3.
- Backend: SQLite (`auto-safe` migration), loopback upstream returning synthetic
  usage of 12 prompt + 8 completion tokens (240 for the dollar-budget case).
- Command: `make governance-load-v47` (duration 70 s, 16 workers).
- Result: 37 of 37 harness checks pass, $0 hosted cost.

| Scenario | Measurement |
|---|---|
| sustained | 11,960 tokens settled in 73.4 s; highest rolling 60 s window 5,980 tokens against a 6,000 TPM limit; peak 214 admitted requests/s; 35,704 rejections, all `tpm-exceeded`; admitted p50 44 ms, p99 840 ms |
| auth | missing token 401, wrong secret 401, disabled caller 403, both valid callers 200; 2 upstream calls total; 2 `api_auth_failed` audit rows; per-key `caller_id`/`token_id` attribution exact |
| cancel | stream cancelled after 2 events (TCP reset); cancelled request settled 15 tokens as `usage_source=estimated` with status 499; the concurrency slot was released (second attempt admitted after a first 429) |
| pii-failover | 16 requests, 16 primary failures, 16 fallback deliveries; no raw email or phone in either upstream, the request log or the usage rows; 16 `pii_filter_applied` rows with placeholders at the upstream |
| dollar-budget | $0.01 budget as a 44,400-token budget; 199 admitted, 11,286 rejected; actual list-price cost $0.010746, a 7.46% overspend, when the upstream reports 240 tokens against a local estimate of about 20 |
| crash-restart | `SIGKILL` after 80 admitted requests, restart in 0.90 s; 119 further admitted, then `quota-exhausted`; 3,940 of a 4,000-token day budget settled; 197 of 199 admitted requests have usage rows (async writer lost 2) |
| multiprocess | 2 processes, 2,000-token budget each: 198 admitted / 3,960 tokens, 1.98× the per-process budget; each process's own state file is accurate (1,980 + 1,980). With one shared state path the same 3,960 tokens settle but the persisted counter reads 1,980 |

## What changed in the picture

- The TPM limit is a rolling-window allowance, not a smooth refill: the whole
  minute allowance can be admitted in the first seconds, and a client that keeps
  retrying sees only rejections until the window frees capacity.
- A dollar budget is a token budget at a pinned price. There is no cumulative
  USD budget control. Overspend comes from settlement above the local
  reservation estimate, and is bounded by the number of reservations in flight
  when the budget boundary is crossed: 7.46% here with 16 in flight.
- The quota state is one JSON file per process. Two processes multiply the
  configured budget (1.98× for two), and sharing the file makes the persisted
  counter understate spend because the last writer wins. Run one state file per
  process, and use PostgreSQL for accounting across replicas.
- Async usage persistence is best effort: a `SIGKILL` lost 2 of 199 admitted
  rows while the quota counters stayed correct.
- Rejected requests also write usage rows; the sustained run wrote more than
  35,000 rows for rejections, which is a real write-amplification cost of
  auditing every rejection.

## Limits

This is a single-host SQLite run with a synthetic upstream. It does not cover
PostgreSQL serving, several hosts, real provider usage, network partitions,
provider tokenizers, or concurrent migrations, and it is not a universal
overspend bound. PostgreSQL runs, the cloud-hosted variant and the complete
#573 acceptance are still open.
