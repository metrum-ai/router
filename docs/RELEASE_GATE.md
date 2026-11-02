# Release gate

`make release-gate` runs the release-candidate checks that need real providers and writes one table with the actual cost from the router's usage rows.

```bash
set -a; . /path/to/env; set +a
export RELEASE_GATE_CONFIG=/path/to/full-capability.yaml
export ROUTER_CALLER_TOKEN=...
make release-gate                 # RELEASE_GATE_CAP defaults to 15
```

Stages, all bounded and all reported even when a stage cannot run:

| Stage | What it runs | Needs |
|---|---|---|
| A | `pi` end to end in OpenAI Responses and Anthropic Messages style, against a GPT target and a Claude target, plus direct baselines; tasks: create-and-verify a file, edit code until its test passes, quote a tool result | provider keys, full-capability config |
| B | Harbor task set, direct and routed, both styles | a container runtime |
| C | Pinned live SDK compatibility suites (`scripts/compat_live.py`) | provider keys, clean worktree |
| D | Live parallel tool-call gate (`scripts/parallel_tool_live.py`) | provider keys |

Rules the script enforces:

- the worktree must be clean, so the table names an exact commit;
- keys and the caller token come from the environment and are never printed, logged or written to the output directory;
- the router's usage rows are the cost source of record (list price, not an invoice); the run stops at `--cap`;
- a stage that cannot run is reported as `skipped` with its reason — never as a pass.

Output lands in `.out/release-gate/<commit>/`: `RESULT.md`, `results.json`, per-lane pi events, and router logs. The table is safe to paste into a PR or an issue; it contains model names, results and cost, not credentials or request bodies.

Notes on the current shape:

- Pass rule for stage A is the same as for a release candidate: every task that passes directly must also pass through the router in every lane. A task that fails directly is a provider or agent limit and is reported, not hidden.
- Harbor needs a container runtime. Without one the stage reports `skipped`; the gate does not pretend to have run it.
- The config is the release candidate's own; cross-format lanes need the bridges the configuration declares as validated, and the table shows exactly which lanes were eligible.

Copyright 2026 Metrum AI. SPDX-License-Identifier: Apache-2.0
