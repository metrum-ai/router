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

## GPT targets are pinned to the Responses dialect

The gate refuses a config whose `gpt` group lists any target outside the
`openai-responses` dialect. An OpenAI Chat target rejects a request that pairs
`reasoning_effort` with tools (see the API compatibility known limitation), so a
Messages-style lane that fell back to Chat would report an opaque
`upstream-rejected` instead of exercising the translation the gate is meant to
check. Pin the group to Responses-only targets; the gate names any stray target.

## Running the Harbor stage on a Docker host

Stage B runs the bounded Harbor promotion matrix (`scripts/harbor_promotion.py`,
at most four cells). It needs a container runtime:

```bash
export RELEASE_GATE_HARBOR_HOST=root@203.0.113.10   # a Docker host with ~/router checked out
make release-gate
```

Without `RELEASE_GATE_HARBOR_HOST` the stage runs locally when Docker exists and
reports `skipped` with the reason when it does not. A Harbor matrix is bounded
evidence; the gate never upgrades it to a certification.

## Harbor CLI version and Compose

Stage B pins the Harbor CLI at **0.24.0**, the version the v4.5 gate was
validated against, and runs it through `uvx --from harbor==0.24.0 harbor` so the
host's `harbor` on `PATH` is irrelevant. Override with
`RELEASE_GATE_HARBOR_VERSION` only when the example agent adapter has been
updated for the newer CLI: Harbor 0.25 removed the Codex agent hook the example
adapter builds on, and the matrix fails inside the adapter rather than at the
router.

The host also needs Docker Compose **v2** (`docker compose`); Harbor invokes it
directly, and a host with only legacy `docker-compose` fails with
`unknown flag: --project-name`.
