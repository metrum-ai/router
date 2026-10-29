# Real agent tool compatibility (partial #429)

`make agent-tool-mock` installs the pinned npm and Python clients, builds a
localhost router, and runs real pi, LangGraph, OpenAI Agents SDK, and Claude
Code clients against deterministic loopback SSE upstreams. Each client must
execute its declared tool and send the actual result in a second model request.
For pi, LangGraph, and OpenAI Agents SDK, it also exercises a Chat caller
through a Chat-to-Responses bridge to the Responses mock. They also receive
two distinct streamed Chat tool calls in one assistant turn, execute both,
and send both results in the continuation. This does not claim the tools run
simultaneously. The OpenAI Agents SDK additionally requests strict JSON output
while using a tool at depths 0, 1 and 3; the mock validates both requests'
`response_format` and the SDK validates the final typed output. The mock checks
request order and tool-result identifiers. Each test uses a
fresh temporary working directory, dummy caller token, scoped tool access,
non-production router, and high caller limits. No hosted model calls are made.
Dependencies are provisioned first; the test processes then have a restricted
environment, and Go dependency resolution is disabled during the smoke.

`make agent-tool-mock-codex` runs the pinned Codex CLI through the same style
of Responses fixture, with `workspace-write` sandboxing and a fixed local tool
command. The host must permit Codex's unprivileged user namespace; a denied
namespace fails the check, not a pass. Never disable the sandbox to make it
pass. The CI VM also runs a five-cell same-Responses chain matrix at depths 0,
1, 2, 3 and 5 using `run_codex_chain.py`; it writes a separate JSON artifact
and a denied sandbox or failed cell makes the job fail. The new matrix has not
yet accumulated acceptance evidence; inspect the CI result rather than assuming
a pass. Codex is configured with `web_search = "disabled"` because the router
rejects provider-hosted tools; this does not certify default web-search mode.

The Claude Code fixture supplies the `context-management-2025-06-27` beta in
the target allowlist for its Messages request. Without an allowed beta, a
Claude request containing `context_management: null` was rejected; see #459.
Its CLI runs with `Read` as the only allowed tool against one fixed synthetic
file. The mock verifies the streamed tool input and actual read result in the
continuation. A separate deterministic case streams a synthetic thinking block
and opaque signature before the tool use; the client must send both back with
the tool result. For depths 0, 1, 2 and 3, the mock compares the received
thinking type/budget and cache-control type/TTL and locations against the
direct case. The pinned client emitted adaptive thinking and three ephemeral
cache annotations on each request in this fixture. This does not validate a
real provider's signatures, caching or other thinking modes. A separate
non-thinking Claude Code fixture translates Anthropic Messages into an OpenAI
Chat mock and checks split streamed `Read` arguments and the actual tool-result
continuation. It explicitly disables thinking; it does not claim thinking,
signature or cache-control preservation across dialects.

## Current coverage and limits

This is an initial, partial #429 suite. The checks are deterministic scenarios
per listed client and path, usually at one router hop; the separate Claude
thinking/cache case spans depths 0–3. These are not a provider certification
or a statistical pass-rate estimate. The locked
versions are in `package-lock.json` and `uv.lock`.

| Client | Surface and path | Local result |
|---|---|---|
| pi 1.4.0 | Chat, streamed tool call, one hop, same dialect | 1/1 pass |
| LangGraph 1.2.18 | Chat, streamed tool call, one hop, same dialect | 1/1 pass |
| OpenAI Agents SDK 0.24.0 | Chat, streamed tool call, one hop, same dialect | 1/1 pass |
| pi 1.4.0 | Chat, two streamed tool calls in one turn, one hop | 1/1 pass |
| LangGraph 1.2.18 | Chat, two streamed tool calls in one turn, one hop | 1/1 pass |
| OpenAI Agents SDK 0.24.0 | Chat, two streamed tool calls in one turn, one hop | 1/1 pass |
| OpenAI Agents SDK 0.24.0 | Chat, streamed tool continuation with strict JSON response schema, depths 0/1/3 | 3/3 mock passes |
| pi 1.4.0 | Chat caller → Responses upstream, streamed tool call, one hop | 1/1 pass |
| LangGraph 1.2.18 | Chat caller → Responses upstream, streamed tool call, one hop | 1/1 pass |
| OpenAI Agents SDK 0.24.0 | Chat caller → Responses upstream, streamed tool call with `strict` function flag, one hop | 1/1 pass |
| Claude Code 2.1.296 | Messages, streamed tool use, one hop, same dialect | 1/1 pass with allowed beta |
| Claude Code 2.1.296 | Messages thinking/signature + streamed tool continuation, cache controls, same dialect, depths 0–3 | 4/4 deterministic local cases with allowed beta |
| Claude Code 2.1.296 | Messages → Chat, streamed tool continuation, thinking disabled, depths 1/2/3/5 | 4/4 deterministic local cases |
| Codex 0.162.0 | Responses, streamed tool call, one hop, same dialect | 1/1 pass on the sandbox-capable CI VM; blocked locally because namespace creation was denied |

`make agent-chain-mock` extends the same fixtures to depths 0, 1, 2, 3 and
5, collecting a JSON result for each of these four clients. Depth 0 connects
the client directly to the scripted upstream; depth 5 is a limited stress
case. It also runs the Claude Messages → Chat fixture at depths 1, 2, 3 and 5;
there is no direct depth-0 cross-dialect cell. `AGENT_CHAIN_REPORT` overrides the default output path
`tests/agent_compat/.out/chain-matrix.json`. A case passes only when the agent
executes its tool and the scripted upstream receives the continuation. The
matrix runner records every selected case and exits nonzero if any fail. The
mock-only nightly workflow retains the JSON artifact after the branch is merged.
Operators must triage failed cells and file or update issues; automated issue
filing and release-PR integration are not yet implemented.

Other surface/client combinations, other strict-schema and broader parallel-tool
sweeps, broader reasoning/cache-control combinations, other cross-dialect paths, chain error
and accounting comparisons, hop protection, nightly live checks, and
release-updated pass-rate publication remain untested. Do not include those
cells in a pass-rate denominator. See #429 and #430.
