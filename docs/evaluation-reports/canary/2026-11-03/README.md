<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Direct-provider differential canary — 2026-11-03

First recorded run of the ARCH-05 canary layer
(`scripts/direct_provider_canary.py`, tracked on #330). Four fixed probes went
once to the exact provider/model and once to the same model pinned through a
local router, with the router built from the PR branch.

| item | value |
|---|---|
| upstream model (both sides) | `gpt-4o-mini`, OpenAI Chat Completions |
| direct endpoint | `https://api.openai.com/v1` |
| router | local listener, `identifiers.mode: passthrough`, static single-target group `canary` pinned to `gpt-4o-mini` |
| prices used | $0.15 / $0.60 per million input / output tokens (listed rates) |
| probes | `evaluators/eval-infra/canary-probes.json` (nonce echo, JSON arithmetic, bounded answer, nonce in sentence) |
| result | both pass 4/4, router regression 0, provider-only fail 0, both fail 0 |
| recorded cost | $0.000083, provider usage × listed rates; not invoice-reconciled |
| spend including the one discarded attempt | ≈$0.0002 (16 requests total) |

`report.json` is validated by `scripts/eval_report_schema.py` with the
preregistered matrix (`canary-direct`, `canary-router-pinned`; sample size 4,
`allowed_error_rate 0.10`). The one-off attempt before this run is not present
in the report: it was discarded when the report writer needed the optional YAML
dependency, no gate was evaluated from it, and its cost is counted in the spend
line above.

Reproduce:

```sh
CANARY_DIRECT_BASE_URL=https://api.openai.com/v1 CANARY_DIRECT_API_KEY=... CANARY_DIRECT_MODEL=gpt-4o-mini \
CANARY_ROUTER_BASE_URL=http://127.0.0.1:PORT/v1 CANARY_ROUTER_API_KEY=... CANARY_ROUTER_MODEL_GROUP=canary \
uv run --with pyyaml python scripts/direct_provider_canary.py run --report-out report.json
uv run --with pyyaml python scripts/direct_provider_canary.py validate report.json
```

This is a smoke of the canary layer, not statistical certification: four probes
on one model prove the differential path works and found no divergence in this
run.
