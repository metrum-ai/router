<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Router hierarchy: bounded chain proof

The [#430](https://github.com/metrum-ai/router/issues/430) harness replays real agent clients against a local scripted SSE upstream directly and through router chains. The table records scheduled mock runs, not a complete API compatibility matrix.

| Scheduled run | Agent chain cells | Sandboxed Codex Responses cells | Evidence |
|---|---:|---:|---|
| [2026-10-28](https://github.com/metrum-ai/router/actions/runs/40673999064) | 20/20 | 5/5 | `router / v45-proof / 430`, snapshot `1e3af31b` |
| [2026-10-29](https://github.com/metrum-ai/router/actions/runs/40861651773) | 20/20 | 5/5 | snapshot `1e3af31b` |
| [2026-10-30](https://github.com/metrum-ai/router/actions/runs/41044327781) | 24/24 | 5/5 | snapshot `1e3af31b` |
| [2026-10-31](https://github.com/metrum-ai/router/actions/runs/41192854439) | 24/24 | 5/5 | snapshot `be5e58bf` |

The first 20 cells cover pi, LangGraph, OpenAI Agents SDK and Claude Code at depths 0, 1, 2, 3 and 5 in their same dialect. Four Claude Messages → Chat cells at depths 1, 2, 3 and 5 were added by [#508](https://github.com/metrum-ai/router/pull/508), with thinking disabled. Codex is a separate sandboxed Responses matrix. Each cell checks a scripted tool continuation, not complete stream equivalence. The runs report zero hosted calls and $0 hosted cost.

Separately, a [small live comparison](https://github.com/metrum-ai/router/issues/430#issuecomment-6461977263) sampled depths 2 and 3 against a provider; evidence is `router / v45-proof / 430`, snapshot `dcb4d3d3`. Its $0.00009210 list-rate calculation is an estimate; actual billed cost is not reconciled. It is not a latency or accounting benchmark.

## Rerun

With Go, Node/npm, Python and `uv` installed, run from a checkout:

```sh
make agent-chain-mock
```

This credential-free local command installs pinned dependencies and writes per-cell results to `tests/agent_compat/.out/chain-matrix.json`; dependency downloads may require network access. It covers the 24 agent cells, not the separate sandboxed Codex cells. See the [suite README](https://github.com/metrum-ai/router/blob/main/tests/agent_compat/README.md) for prerequisites and Codex.

## Limits

Four scheduled observations are not a week of green or triaged nightly runs. Unlisted cells, complete streaming, live-provider and billing behavior, per-hop latency (p50, p99 and TTFT), trace propagation and complete per-hop accounting are not proven. The [loop protection implementation](https://github.com/metrum-ai/router/pull/471) uses best-effort normalized Via entries and excludes Bedrock. The observed trace correlation gap is tracked in [#492](https://github.com/metrum-ai/router/issues/492); the layered deployment and hierarchy release gate continue in [#434](https://github.com/metrum-ai/router/issues/434). See [#430](https://github.com/metrum-ai/router/issues/430) for other observations and limitations.
