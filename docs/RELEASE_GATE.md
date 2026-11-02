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

## Harbor stage: the Pi adapter matrix

Stage B runs the matrix the v4.5 gate used, ported into `scripts/harbor_e2e/`:

- the **Pi agent adapter** (`metrum_pi_tmpfs.py`), which reads the router token
  from a read-only tmpfs bind mount instead of any flag or environment value;
- **five fixed synthetic tasks** (`generate_tasks.py`);
- **four lanes**: direct and routed, in both API styles, against the GPT and
  Claude groups, driven by `run_matrix.py`;
- direct traffic goes through the spend-guarded relay (`guarded_relay.py`), so a
  direct baseline is bounded and recorded like a routed one.

It runs on a Docker host reachable over SSH; the gate ships the bundle and its
own router binary, runs `setup_remote.sh` (Docker CE with the Compose plugin and
a Harbor virtualenv), installs the provider keys and the caller token into
tmpfs, then executes the matrix and copies the result back.

```bash
export RELEASE_GATE_HARBOR_HOST=root@203.0.113.10
export RELEASE_GATE_HARBOR_KEY=~/.ssh/gate-key
export RELEASE_GATE_HARBOR_VERSION=0.25.0   # default; the CLI the v4.5 matrix ran 5/5 against
make release-gate
```

Without `RELEASE_GATE_HARBOR_HOST` the stage reports `skipped` with the reason. A
Harbor matrix is bounded evidence, never a certification, and the gate never
upgrades it to a pass.

Compose **v2** is required: Harbor invokes `docker compose` directly, and a host
with only the legacy `docker-compose` fails with `unknown flag: --project-name`.
The setup script installs the plugin, so this only matters for a hand-built host.

The earlier `examples/harbor-algotune-pca` Codex adapter is **not** used by the
gate: it calls an MCP-registration hook that Harbor's Codex agent does not
provide in the 0.25 line either, so a matrix built on it fails inside the adapter
before reaching the router. The Pi adapter above has no such dependency and is
the one the v4.5 matrix passed with.
