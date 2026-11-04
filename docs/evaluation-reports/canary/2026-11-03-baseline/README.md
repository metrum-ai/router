<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Pinned-baseline and current-stable canary rows — 2026-11-03

AGENT-01 live rows for the v4.7.0 evaluation scope of #330: the ARCH-05
direct-provider differential canary ran twice and was merged, so the **pinned
baseline** and the **current-stable canary** are reported side by side.

| run | direct model | router group | baseline role |
|---|---|---|---|
| pinned | `gpt-4o-mini-2024-07-18` | `pinned` (same exact model) | `pinned` |
| current-stable | `gpt-4o-mini` (alias) | `stable` (same alias) | `current-stable`, `baseline_of` the pinned cells |

Cell ids are `<pinned cell id>-current-stable`: `canary-direct` and
`canary-router-pinned` are the pinned-baseline cells, and the `-current-stable`
suffix marks the canary re-run, which links back through
`provenance.baseline_of`. The suffix is independent of the deployment's model
group names, so `canary-router-pinned-current-stable` is a canary row on the
pinned-router path (in this run the router group was called `stable`), not a
second "stable" baseline.

Each run sends the four fixed canary probes direct and through the router.
Result: **8/8 probes pass on both sides across both runs, 0 router regressions,
0 provider-only failures**, cost **$0.000164** usage-derived from the listed
$0.15/$0.60 per-million rates (not invoice-reconciled). `report.json` is the
merged report; it validates against the preregistered matrix and the report
schema, and each `*-current-stable` cell carries `provenance.baseline_of` naming
its pinned cell, which the #749 rule enforces.

Reproduce:

```sh
... CANARY_DIRECT_MODEL=gpt-4o-mini-2024-07-18 ... \
  python3 scripts/direct_provider_canary.py run --baseline-kind pinned --report-out pinned.json
... CANARY_DIRECT_MODEL=gpt-4o-mini ... \
  python3 scripts/direct_provider_canary.py run --baseline-kind current-stable --report-out stable.json
python3 scripts/direct_provider_canary.py merge --pinned pinned.json --current-stable stable.json \
  --report-out report.json
python3 scripts/direct_provider_canary.py validate report.json
```

Limits: four probes per run on one model family; the current-stable alias
resolved to the same dated model on the run date, so this run shows no drift —
it establishes the reporting pair and the gate, not a stability claim.
