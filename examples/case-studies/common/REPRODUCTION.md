<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# External reproduction notes

This is a **blank checklist**, not a completed reproduction or a claim of
savings. The #423 gate requires a person outside Metrum to follow a case-study
guide unaided and provide notes. Establish that independence with a maintainer;
do not publish personal information unnecessarily.

## Before running

1. Read the chosen workload README and the [shared setup](README.md). For the
   documented five-task document-Q&A run, start with
   [document-qa/README.md](../document-qa/README.md).
2. Obtain approval for provider charges and use only synthetic input. Check
   current model availability, price sources and prices. The committed rates
   are historical, not live quotes. Stop on errors; failed or retried requests
   may still incur charges.
3. Use private credentials and private router state. Do not put API keys,
   caller tokens, real request IDs, raw provider logs or invoices in a public
   issue or artifact. Arrange an approved private channel for any billing
   records a reviewer needs to verify.
4. Record the repository commit, operating-system/architecture, tool versions,
   exact commands with all secret values removed, and any deviation from the
   guide. Separate setup failures and retries from the successful run; do not
   discard failed trials or requests.

## Notes to return

Fill in the following with observations, not estimates presented as bills:

- **Reviewer independence:** confirmed privately with maintainer: yes / no.
- **Workload, repository commit and date (UTC):**
- **Environment and tool versions:**
- **Commands and setup changes:**
- **Completeness:** planned and completed trials in each arm, plus all failures,
  retries, fallbacks and their treatment. The supplied extractor rejects an
  incomplete run; reconcile such a run manually rather than presenting it as
  complete.
- **Quality:** verifier, per-arm passes/trials, interval where applicable, and
  paired wins/losses. Explicitly say if routed quality falls; small samples and
  overlapping intervals do not establish parity.
- **Routing and latency:** per-arm request/model mix and p50/p95 request
  latency, with sample size and source.
- **Usage-derived price calculation:** per arm, token counts, dated price
  sources and calculated amount. Router usage/cost fields are not a provider
  invoice.
- **Actually billed amount:** per arm and provider, only if independently
  attributable to the exact request set, including failures and retries.
  Identify the privately retained billing source, period, currency, scope and
  reconciliation method, including credits or adjustments. Otherwise write
  **unknown**. Aggregated account charges do not establish per-arm billing.
- **Sanitized evidence and blockers:** links to reviewed summaries, and what an
  external reviewer must inspect privately. Never publish the private state.

The runner's mock mode and a review of this checklist do not count as an
external paid reproduction. A reproduction by itself also does not establish
that two consecutive scheduled weekly results exist. Review each acceptance
criterion separately.
