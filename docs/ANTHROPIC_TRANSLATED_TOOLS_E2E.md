<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Anthropic translated client-tool validation (#295)

Metrum AI working-tree validation on 2026-10-21. This is deployment evidence,
not a default promotion of every target. Provider accounts were supplied by the
VM's `OPENAI_API_KEY` and `FIREWORKS_API_KEY` environment entries; no account
identifier or credential is included here.

## Local setup and commands run

- `git fetch origin main` and `git rebase origin/main`.
- Generated ignored `env.json` from `env.example.json` using Python `os.environ`;
  `git check-ignore env.json` confirmed exclusion.
- `uv run --with pyyaml python scripts/local_pi_instance.py --out-dir tmp/a295 --listen 127.0.0.1:18295`.
- In the ignored generated config, added `anthropic` to each target's validated
  inbound dialects, `validation_status: passed`, and
  `tool_support.anthropic_messages: [client_tools]`. Added isolated single-target
  copies alongside the 70/30 weighted `big-coder` group. No production example
  or default target was promoted.
- `go build -o tmp/a295/router ./cmd/metrum-ai-router` and
  `tmp/a295/router --config tmp/a295/config.yaml` (background localhost process).
- `curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18295/readyz`: 200.
- `claude --version`: 2.1.306; `codex --version`: 0.163.0.

The Python subprocess driver invoked `claude -p --dangerously-skip-permissions`
from a disposable fixture directory, with a file-write/read instruction. The
caller key was passed only as the `ANTHROPIC_API_KEY` environment entry;
`ANTHROPIC_AUTH_TOKEN` was unset. `ANTHROPIC_BASE_URL` was
`http://127.0.0.1:18295/anthropic`. Main and subagent models were pinned to the
allowed group via environment. Every created file was checked with Python
`Path.read_bytes()` against the exact expected token and one newline.
The generated router uses identifier rewrite mode.

## Actual Claude CLI results

| Run | Group | Serving target(s), in request order | Outcome |
| --- | --- | --- | --- |
| 0 | isolated-0 | OpenAI x3 | exit 0, exact bytes, readback |
| 1 | isolated-1 | Fireworks x3 | exit 0, exact bytes, readback |
| 2 | big-coder | Fireworks, OpenAI, Fireworks, Fireworks | exit 0, exact bytes, readback |
| 3 | big-coder | OpenAI, Fireworks, OpenAI | exit 0, exact bytes, readback |

OpenAI target: `gpt-6-luna`, native `openai-responses`.
Fireworks target: `accounts/fireworks/models/deepseek-v4p1-flash`, native
`openai-chat`. All four runs used streamed `anthropic` inbound. SQLite usage
confirmed status 200, one attempt and no fallback for every request. Shape rows
confirmed 12 client tools on the first run and subsequent `tool_result` turns.
Selected sanitized continuation request IDs:

- OpenAI: `req_ce5bb48af15c8a064c25072859033b82`.
- Fireworks: `req_c094bad279db750bc51d149a8c656613`.
- Weighted cross-target continuation: `req_3271e6a1d430f143aa29296a265e5ed8`.

After rebuilding the final bridge, repeated isolated CLI runs also asked a
read-only subagent to independently verify the file. Both exited 0 with exact
bytes and final confirmation of the subagent check. This additionally exercises
the real coding-agent tool schema rather than a single minimal function schema.
This evidence does not claim arbitrary long-context or multimodal promotion.

## Regression checks

- `go test ./internal/router ./internal/stream`: passed.
- `go test ./internal/router ./internal/stream -run Anthropic -count=1`: passed,
  including dual-target HTTP continuation, omitted/auto/forced choice,
  multiple calls, identifier rewrite, streamed admission and interrupted tools.
- `make local-router-pi-smoke LOCAL_ROUTER_OUT=tmp/a295 LOCAL_ROUTER_LISTEN=127.0.0.1:18295`:
  passed, no failures; OpenAI Chat caller to Responses tool continuation.
- Actual Codex `exec --ignore-user-config --ephemeral --ignore-rules
  --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox` against
  isolated OpenAI `/v1`, using the existing Compose smoke's provider settings
  and environment-only `METRUM_ROUTER_KEY`: exit 0, exact `codex-tool-ok` newline
  file and readback. Disposable fixture only.
- `make api-compat-mock`: 212 passed.

The router and CLI logs, SQLite and generated configuration remain ignored under
`tmp/a295`; raw payloads and credentials are not part of this evidence.
