<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Weekly document-Q&A rerun

`.github/workflows/weekly-case-study.yml` schedules one paired five-task
synthetic document-Q&A run every Monday at 04:17 UTC, only from this repository's
`main`. **It is not live until merged and configured.** The `case-study-weekly`
GitHub environment must be created by an authorized maintainer with appropriately
scoped `OPENAI_API_KEY` and `FIREWORKS_API_KEY` secrets, spending controls, and
reviewed environment-protection settings. Do not place credentials in the repo.
The workflow uses issue-write permission solely to post an audit comment to
[#423](https://github.com/metrum-ai/router/issues/423); checkout credentials are
not persisted. These permissions and secret references require security review.

If either provider secret is absent, the job logs one skip line, succeeds and
posts **no issue comment** or artifact. It does not count as a scheduled result.
No secrets are configured by this workflow; Chetan decides whether to add them.
When both secrets are present, each execution adds a new issue comment and uploads
sanitized summary, per-task results and usage evidence as a 90-day Actions artifact.
The issue comment thread
is the chronological audit log, **not an immutable or cryptographically signed
ledger**. If a step fails before the runner can write a summary, a fallback
comment marks the run incomplete and cost unknown. If comment posting fails,
the action fails; use the artifact to investigate and post a reviewed entry.
Do not treat a missing comment as a successful run. Actions artifacts expire;
archive any evidence required beyond the retention period in an approved store.

The runner stops on the first request error. Failed, timed-out or retried calls
can incur unknown charges. An incomplete audit shows any known successful
subset, never a total; reconcile provider charges before any manual retry. No
raw router logs, credentials, provider response bodies or real request IDs are
published. Review sanitized artifacts before broader distribution.

The runner uses fresh private tmpfs state, loopback-only routing, a 10-request /
25,000-token router quota, and a fixed 128-token output maximum per request.
It refuses to start when price dates are more than 31 days old, from unexpected
sources, or outside conservative per-token bounds. These controls **are not a
hard provider billing cap**: retries, failures, provider-side billing, invoices,
taxes and model-price changes may differ. Verify prices and adjust the reviewed
config before it expires. The reported amounts are request-time usage multiplied
by configured dated list prices, **not reconciled invoices**. The workflow uses
five fixed tasks per arm; routing is weighted and requests are not randomized.

For a credential-free, real-router mock exercise from the repository root:

```sh
go build -o /tmp/case-weekly-router ./cmd/metrum-ai-router
uv run --with pyyaml python examples/case-studies/common/weekly.py \
  --router /tmp/case-weekly-router --out-dir /tmp/case-weekly-mock --mock
```

The output directory must not exist. The mock uses synthetic upstream responses
and labels its cost figures as non-billed. A mock run is **not** a scheduled
result. For an independent paid reproduction, follow
[`document-qa/README.md`](../document-qa/README.md) and the setup guide, obtain
approval for the provider costs, verify current prices, and record independent
reproduction notes. Do not claim the #423 acceptance gate until two consecutive
actual scheduled results and an external engineer's reproduction exist.
