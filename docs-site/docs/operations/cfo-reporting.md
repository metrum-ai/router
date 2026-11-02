---
title: CFO usage reporting
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# CFO report

The CFO report aggregates retained request-usage rows for a selected UTC period, with a start-inclusive and end-exclusive boundary. It reports recorded cost and a hypothetical baseline, not provider invoices or a proof of model quality. A single repeatable-read snapshot keeps its usage totals, team allocations and trends consistent with one another. The token-budget snapshot is separate and live.

Enable authenticated admin reports and map project IDs to teams explicitly:

```yaml
server:
  admin_reports:
    enabled: true
    cfo_teams:
      support-production: customer-support
      support-staging: customer-support
```

Mapping keys must be canonical caller project IDs: lowercase slugified IDs (for example, `Finance Team` is recorded as `finance-team`). Configuration rejects noncanonical keys; at most 500 projects and 64 teams may be mapped. The mapping is current configuration, not a historical record. Projects with no mapping are **Unassigned**; no team is inferred from a user or project name. Missing project IDs share the `unknown` bucket with a literal `unknown` project; leave it unmapped if missing attribution must remain unassigned. A scoped report only includes authorized project/environment data. An authorized administrator can select a configured, source-dated baseline in the browser's **Savings → CFO report**. Baseline prices apply uniformly to the selected window; this is not a historical price feed. CFO reports require a configured baseline with a source and date; ad-hoc `custom` session baselines are not accepted. Revalidate prices before external use.

The JSON endpoint requires `admin:reports` read; CSV and Markdown require export permission. Markdown export also requires `server.admin_reports.export_markdown: true`:

```sh
metrum-ai-routerctl reports cfo --router-url "$ROUTER_URL" --since 24h --baseline gpt-5.5 --format json
metrum-ai-routerctl reports cfo --router-url "$ROUTER_URL" --since 24h --baseline gpt-5.5 --format csv > cfo.csv
metrum-ai-routerctl reports cfo --router-url "$ROUTER_URL" --since 24h --baseline gpt-5.5 --format markdown > cfo.md
```

Use a dedicated admin key in `ROUTER_ADMIN_KEY` or a protected `--admin-key-file`; do not put credentials in arguments. `--from` and `--to` accept RFC3339 timestamps. The browser API is `/admin/reports/api/cfo`, and CSV and Markdown exports are `/admin/reports/cfo/export.csv` and `/admin/reports/cfo/export.md`. A configured child reports prefix replaces `/admin/reports` in those paths.

Summary and team totals cover the filtered retained rows; project, key, user and model-group breakdowns are top-N by recorded cost, capped at 500 rows per dimension with an explicit `has_more` flag. The top-five-percent metric uses `ceil(user buckets / 20)` buckets ranked by tokens; missing caller-user identities share one `unknown` bucket. It is not a count of distinct people when user attribution is missing. Daily, monthly and lifetime **token** quotas reflect live current state, not the report period or a dollar budget. The snapshot applies authorization and caller, exact token ID, project and environment filters; other usage filters do not restrict it. CFO reports reject `token_id_prefix` filters rather than applying inconsistent wildcard semantics to quota rows. CFO key breakdowns use opaque, per-report labels rather than stored token IDs; labels can change between reports. Missing token IDs and a literal `unknown` ID share one usage bucket. Quota rows are identified by caller and are not linked to key labels; legacy labels and missing key IDs cannot always be matched reliably. Rotate any credentials stored in historical token IDs and remove unsafe history under your retention policy. The quota snapshot lists at most 500 authorized callers and marks truncation. Raw-row retention can make older periods incomplete; finalized daily rollup history is not supported by this report. All values depend on recorded usage and pricing provenance.

## Worked example

The routed support-agent case-study [usage evidence](https://github.com/metrum-ai/router/blob/main/examples/case-studies/support-agent/evidence/usage.json) contains a request with 678 input and 47 output tokens, and a recorded router cost of $0.0002598. For illustration, the case-study's frontier price of $2 per million input tokens and $10 per million output tokens, dated 2026-10-11 in the same evidence, gives `(678 × 2 + 47 × 10) / 1,000,000 = $0.001826` for the same token counts. The hypothetical savings are `$0.001826 − $0.0002598 = $0.0015662`. This is arithmetic on one recorded request, not a bill, current price quote, quality comparison or independently verified invoice.

To rerun the case-study arithmetic and the seeded SQLite reconciliation and boundary check from a checkout, run:

```sh
go test ./internal/router -run '^TestCFOReport(CaseStudyExample|ReconcilesAdminOverview)$' -count=1
```
