# Real agent tool compatibility (partial #429)

`make agent-tool-mock` installs the pinned npm and Python clients, builds a
localhost router, and runs real pi, LangGraph, OpenAI Agents SDK, and Claude
Code clients against deterministic loopback SSE upstreams. Each client must
execute its declared tool and send the actual result in a second model request.
For pi, LangGraph, and OpenAI Agents SDK, it also exercises a Chat caller
through a Chat-to-Responses bridge to the Responses mock. The mock checks
request order and the tool-result identifier. Each test uses a
fresh temporary working directory, dummy caller token, scoped tool access,
non-production router, and high caller limits. No hosted model calls are made.
Dependencies are provisioned first; the test processes then have a restricted
environment, and Go dependency resolution is disabled during the smoke.

`make agent-tool-mock-codex` runs the pinned Codex CLI through the same style
of Responses fixture, with `workspace-write` sandboxing and a fixed local tool
command. The host must permit Codex's unprivileged user namespace; a denied
namespace fails the check, not a pass. Never disable the sandbox to make it
pass. Codex is configured with `web_search = "disabled"` because the router
rejects provider-hosted tools; this does not certify default web-search mode.

The Claude Code fixture supplies the `context-management-2025-06-27` beta in
the target allowlist for its Messages request. Without an allowed beta, a
Claude request containing `context_management: null` was rejected; see #459.
Its CLI runs with `Read` as the only allowed tool against one fixed synthetic
file. The mock verifies the streamed tool input and actual read result in the
continuation.

## Current coverage and limits

This is an initial, partial #429 suite. The checks are one deterministic
scenario per listed client and path, all at one router hop; they are not a
provider certification or a statistical pass-rate estimate. The locked
versions are in `package-lock.json` and `uv.lock`.

| Client | Surface and path | Local result |
|---|---|---|
| pi 1.4.0 | Chat, streamed tool call, one hop, same dialect | 1/1 pass |
| LangGraph 1.2.18 | Chat, streamed tool call, one hop, same dialect | 1/1 pass |
| OpenAI Agents SDK 0.24.0 | Chat, streamed tool call, one hop, same dialect | 1/1 pass |
| pi 1.4.0 | Chat caller → Responses upstream, streamed tool call, one hop | 1/1 pass |
| LangGraph 1.2.18 | Chat caller → Responses upstream, streamed tool call, one hop | 1/1 pass |
| OpenAI Agents SDK 0.24.0 | Chat caller → Responses upstream, streamed tool call with `strict` function flag, one hop | 1/1 pass |
| Claude Code 2.1.296 | Messages, streamed tool use, one hop, same dialect | 1/1 pass with allowed beta |
| Codex 0.162.0 | Responses, streamed tool call, one hop, same dialect | 1/1 pass on the sandbox-capable CI VM; blocked locally because namespace creation was denied |

`make agent-chain-mock` extends the same fixtures to depths 0, 1, 2, 3 and
5, collecting a JSON result for each of these four clients. Depth 0 connects
the client directly to the scripted upstream; depth 5 is a limited stress
case. `AGENT_CHAIN_REPORT` overrides the default output path
`tests/agent_compat/.out/chain-matrix.json`. A case passes only when the agent
executes its tool and the scripted upstream receives the continuation. The
matrix runner records every selected case and exits nonzero if any fail. The
mock-only nightly workflow retains the JSON artifact after the branch is merged.
Operators must triage failed cells and file or update issues; automated issue
filing and release-PR integration are not yet implemented.

Other surface/client combinations, strict-schema and parallel-tool sweeps,
reasoning/cache-control combinations, other cross-dialect paths, chain error
and accounting comparisons, hop protection, nightly live checks, and
release-updated pass-rate publication remain untested. Do not include those
cells in a pass-rate denominator. See #429 and #430.
