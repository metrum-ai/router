---
title: Tool-call compatibility proof
doc_type: explanation
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Tool-call compatibility: bounded proof

This is a partial result for [#429](https://github.com/metrum-ai/router/issues/429), not certification of every agent and API combination. The fixture uses real pinned clients, a local router chain and a scripted upstream, not a live provider.

## Results

The [2026-10-31 scheduled run](https://github.com/metrum-ai/router/actions/runs/41192854439) reports the following per-cell results, also archived as `router / v45-proof / 430`, snapshot `be5e58bf`. Each cell is one scripted streamed tool call and continuation, not a statistical pass rate.

| Client | Caller → upstream | Depths | Result |
|---|---|---|---|
| pi | Chat → Chat | 0, 1, 2, 3, 5 | 5/5 |
| LangGraph | Chat → Chat | 0, 1, 2, 3, 5 | 5/5 |
| OpenAI Agents SDK | Chat → Chat | 0, 1, 2, 3, 5 | 5/5 |
| Claude Code | Messages → Messages | 0, 1, 2, 3, 5 | 5/5 |
| Claude Code | Messages → Chat | 1, 2, 3, 5 | 4/4 |
| Codex | Responses → Responses | 0, 1, 2, 3, 5 | 5/5 |

The Codex cases run separately on a sandbox-capable CI worker. The first five rows are reproduced by the command below; Codex is not included in that command. Earlier [mock results and individual feature coverage](../reference/api-compatibility.md#real-agent-tool-loop-smoke-initial-coverage) include strict JSON, parallel tool shapes, and synthetic thinking and cache metadata.

The scheduled mock run reports zero hosted-model calls and $0 hosted-model cost. Separately, [bounded live SDK comparisons](https://github.com/metrum-ai/router/issues/429#issuecomment-6460078165) used reported provider usage; their actual billed cost is not reconciled. The captured comparison's $0.00009210 is an estimate at list rates, not an actual invoice; evidence is `router / v45-proof / 429`, snapshot `4de7a058`.

## Rerun

With Go, Node/npm, Python and `uv` installed, from a checkout run:

```sh
make agent-chain-mock
```

It installs pinned dependencies, runs local synthetic cases without provider credentials, and writes per-cell results to `tests/agent_compat/.out/chain-matrix.json`. Dependency downloads require network access. See the [suite README](https://github.com/metrum-ai/router/blob/main/tests/agent_compat/README.md) for prerequisites and the separate sandboxed Codex check.

## Limits

The table covers only the named fixtures and depths; unlisted combinations are untested, not passes. The Claude cross-dialect case has thinking disabled. Synthetic thinking/cache checks do not prove live-provider signature or cache behavior. The full client × surface × path matrix, broad schema and parallel coverage, live nightly/release checks, and release-updated results are not established. The local mock command does not run Codex. Remaining matrix and automation work is tracked in [#572](https://github.com/metrum-ai/router/issues/572); known Claude context-management behavior is tracked in [#459](https://github.com/metrum-ai/router/issues/459). Mock and live results cannot prove production reliability or a statistical failure rate.
