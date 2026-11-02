<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# results/

Measured evidence for this tier. Empty until a run happens; the tier README
keeps `pending` in **Measured results** and **Actual cost** until a directory
here backs the numbers.

## What a run commits

One directory per run, named `<YYYY-MM-DD>-<commit>`:

| File | Contents |
|---|---|
| `ENV.md` | Commit, hardware, driver and runtime versions, image digests and model revisions actually served, context length, `gpu_memory_utilization`. No credentials, tokens, host names or request bodies. |
| `functional.json` | Output of `harness/functional_smoke.sh` for this run: one row per model group, with status and status codes. |
| `sweep.json` | Output of `harness/concurrency_sweep.sh`: p95 latency and zero-failure worker counts for the router lane and the direct vLLM lane, per phase. |
| `cost.md` | The rental window and the actual cost from the provider's billing or usage API, or `estimate (hours × list rate), invoice pending` while the invoice is outstanding. |
| `FAILURES.md` | Every failing or skipped check, as it happened, with the reason. Absent only when there were none. |

## Rules

- Results come from a run **on the commit they are committed with**. A rerun
  after a change produces a new directory; results are never copied forward.
- A check that could not run is recorded as failed or skipped with its reason.
  It is never dropped from the table and never counted as a pass.
- Raw driver output too large to commit goes to the shared artifact library and
  the snapshot id goes in `ENV.md`; the summary that the README cites stays in
  git.
- Nothing in this directory may contain credentials, caller tokens, prompts,
  provider response bodies, internal host names or machine identifiers.
