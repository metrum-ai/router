<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance load: authenticated HTTP reservations (#431)

This local, synthetic probe exercises the router's HTTP admission and settlement
path with mixed streaming and unary requests. It is included in `make
governance-load`; to run just this case:

```sh
go test -race ./internal/router -run '^TestGovernanceLoadHTTPTokenReservations$' -count=10 -v
```

On 2026-10-28 UTC, with router base `d2e9126dd8752e0c52be6cb4962790bf75972cee`,
Go 1.27.3 on Linux x86_64, 16 visible CPUs and an AMD Ryzen 7 7730U, ten
race-detector repetitions passed; after adding client idle-connection cleanup,
three further repetitions passed. Each scenario uses one valid synthetic caller
credential; an invalid credential receives HTTP 401 without reaching the
synthetic upstream. The fixture uses the legacy test `token_sha256` config,
not a separate modern caller-token authentication test.

For each of the day-token, month-token, lifetime-token and TPM limits, requests
are held at the local upstream until every admission attempt completes. The
first two admissions are deliberately one streaming and one unary request;
remaining successful admissions may have either mode. The fixture request's
router reservation estimate is 13 tokens. Each upstream completion reports 12
input plus 8 output tokens, deliberately exceeding that estimate. Admission
rejections return 429 for day/month/TPM and 403 for lifetime; all accepted
responses complete and the token counters, reservations and concurrency slots
settle. No hosted calls or cloud resources were used; cost $0.

| Concurrency | Threshold | Admitted | Rejected | Settled tokens | Settlement above threshold |
|---:|---:|---:|---:|---:|---:|
| 32 | 104 | 8 | 24 | 160 | 56 |
| 128 | 416 | 32 | 96 | 640 | 224 |

The same numbers held separately for each of the four limits in this controlled
fixture; the TPM threshold is a rolling token-rate threshold, not a budget.
The observed excess equals admitted requests × (20 reported − 13 reserved)
only because this fixture fixes every reported usage and holds all admissions
until the race ends. It is **not a universal overspend bound**. Real input
estimation, provider usage and the number of outstanding requests can differ.

## Additional admission limits

On 2026-10-28 UTC, an expanded fixture on router base
`e216ac6351c0ceff16bf8954f59b83e478a0a1c1`, Go 1.27.3 on the same Linux
x86_64 host (16 visible CPUs, AMD Ryzen 7 7730U), passed five race-detector
repetitions of all six HTTP scenarios at concurrency 8, 32 and 128, followed
by one `make governance-load` run. In addition to the four token limits above,
it tests RPM and in-flight concurrency **separately**, using the same held,
mixed-stream/unary admissions:

| Workers | Limit | Admitted | Rejected |
|---:|---:|---:|---:|
| 8 | 2 | 2 | 6 |
| 32 | 8 | 8 | 24 |
| 128 | 32 | 32 | 96 |

RPM rejections carried `Retry-After: 60`; concurrency rejections carried
`Retry-After: 1`. After all admitted responses and deferred accounting settle,
a new request in the concurrency case succeeds, proving slot release in this
fixture. The 8-worker token cases admitted 2 and settled 40 against a
26-token threshold (14 excess) for each token dimension. These are finite
synthetic results, not a sustained-rate benchmark or universal overspend bound;
the HTTP case does not cover a single worker. No hosted calls or cloud
resources were used.

This exercises one router process and its test quota store. It does not prove
SQLite/PostgreSQL persistence, multiple replicas, dollar-denominated budgets,
all key configurations, all error modes, or general rate-limit/PII/audit
acceptance. #431 remains open.
