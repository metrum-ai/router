<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Escalation evidence

- [AMD MI300X synthetic CPU / GPU / frontier run](mi300x-2026-10-30/README.md):
  real backends, small synthetic fixture, infrastructure cost is an estimate
  (hours × list rate) with the invoice pending, not reconciled billing. Raw
  session logs and teardown: shared artifact library, `router / v45-proof /
  427`, snapshot `a371eee3`.
- [NVIDIA A6000 synthetic CPU / GPU / frontier run](a6000-2026-10-30/README.md):
  real backends, small synthetic fixture, no invoice reconciliation
  (snapshot `c2d3e27f`).
- `mock-output.json`, `mixed-output.json`, `mixed-usage.json`: earlier mock
  and mixed-smoke examples described in the parent README.

These outputs are not production quality, privacy, latency or cost claims. The
published summary of both GPU runs, with every number linked to these folders
and a Limits section, is the docs-site page
`docs-site/docs/evaluation/escalation-and-decision-evidence.md`.
