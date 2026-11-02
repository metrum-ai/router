<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance under load: shipped-binary harness (#573)

This harness extends the bounded v4.5.0 governance load evidence (#431) from
in-process tests to the shipped binaries. It starts real `metrum-ai-router`
processes with PBKDF2 caller tokens, drives real HTTP load against a
deterministic loopback OpenAI-Chat upstream, and reads the usage and
security-access rows back from the configured store.

It is a measurement harness, not a CI gate. A run costs $0 and makes no hosted
model calls.

## What it covers

| Scenario | Measures |
|---|---|
| `sustained` | Mixed streamed/unary load under a TPM limit: settled tokens in any rolling 60 s window, burst profile, reject reasons, latency |
| `auth` | PBKDF2 caller authentication: missing, wrong and disabled keys; per-key usage attribution; `api_auth_failed` audit rows |
| `cancel` | Caller cancels a stream mid-flight (TCP reset): client-canceled settlement, committed token estimate, concurrency-slot release |
| `pii-failover` | PII redaction on the primary, the retried and the failed-over upstream; raw values absent from upstream bodies, request log and usage rows |
| `dollar-budget` | A token budget converted to US dollars at pinned prices, with concurrent settlement of requests whose upstream usage exceeds the local reservation estimate |
| `crash-restart` | `SIGKILL` mid-load, then restart: quota state continuity, recovery time, usage-row durability |
| `multiprocess` | Several router processes against one caller budget: per-process budget multiplication, and lost updates when a quota state file is shared |

Every scenario also reports its own checks. A failed check is evidence and is
reported as `FAIL`; `--strict` turns a failed check into a non-zero exit.

## Running

```sh
# SQLite, all scenarios, ~2 minutes of load plus a Go build
make governance-load-v47

# One scenario, longer sustained window
python3 benchmarks/governance-load/harness.py \
  --scenario sustained --backend sqlite --duration 120 --workers 32 --name sustained

# PostgreSQL on a dedicated node (DSN stays in the environment)
export ROUTER_USAGE_DB_DSN='postgres://...'
python3 benchmarks/governance-load/harness.py \
  --scenario sustained --scenario auth --scenario cancel --scenario pii-failover \
  --scenario dollar-budget --scenario crash-restart --scenario multiprocess \
  --backend postgres --duration 120 --workers 32 --name postgres
```

Requirements: Go (to build the three binaries once), Python 3.10+, and `psql`
for the Postgres backend. The harness writes `report.json` and `RESULT.md` under
`.out/governance-load/<utc>-<name>/`.

Nothing is printed that contains a caller token or a DSN. Caller tokens are
generated into mode-0600 files and referenced by path. A Postgres DSN is read
from the environment variable named by `--dsn-env` and handed to `psql` through
libpq `PG*` variables, never on a command line.

## What the numbers mean, and what they do not

- The upstream is a loopback fixture with fixed synthetic usage. These are
  governance and accounting measurements, not provider-behaviour or latency
  benchmarks. Latency figures describe this host and this fixture only.
- The dollar budget is a **token budget converted at pinned prices**. The router
  has no cumulative USD budget control; token and request budgets are the
  enforcement mechanism. Every dollar figure in the report says which prices it
  used.
- A bounded run of one build and one config is not a universal overspend bound.
  The analytic shape is stated in the report: settlement above the reservation
  estimate is bounded by the number of reservations in flight at the budget
  boundary.
- SQLite is a single-writer store. A multiprocess SQLite run can only show what
  breaks; it is not a deployment recommendation. PostgreSQL is the
  multi-process store.
- The quota state is a per-process JSON file with an integrity envelope. Two
  processes must not share it: the last writer wins and the persisted counters
  understate spend. The `multiprocess` scenario measures exactly that.
- Async usage persistence is best effort. A `SIGKILL` can lose rows that the
  quota state already counted; the crash scenario reports both numbers.
