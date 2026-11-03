<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# What did not pass, and what did not run

| # | Item | Result |
|---|---|---|
| 1 | Concurrency sweep | **completed**: six cells (1/8/32 workers × streaming and non-streaming × router and direct), 3 936 samples, zero failed requests. Sustained concurrency **32 workers** at p95 ≤ 8 000 ms. |
| 2 | Escalation group | **not run on this box** (policy: no provider keys on rented hardware); proven by the v47-hier #434 run. |
| 3 | Caller group grants in the suite | **not proven by the suite**: minting the restricted caller dropped the harness caller's entry (#755), so the suite's own token answered `401`. Verified by hand on the same box: `404 model_not_found` for a caller without the group. |
| 4 | Admin UI | pass: basic auth `200` with credentials, `401` without. |
| 5 | Functional suite | 8 passed, 0 failed, 2 skipped (rows 2 and 3). |

## Findings from this window

1. The complete sweep on real vLLM proves two earlier diagnoses: the local 32-worker
   abort was the mock test double, and the first H100 window's abort was the caller's
   rate ceilings. Neither was the router.
2. `callers generate --write` replaces the caller table instead of merging (#755).
3. The sweep wrapper compared every cell's request count against the one-worker figure
   and reported `sustained_workers: 1` even though all cells were full; fixed, and the
   verdict here was recomputed from the same samples (no re-measurement).
