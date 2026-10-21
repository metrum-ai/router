# Coding-Agent E2E Matrix

Use this runbook before promoting routing changes that affect coding-agent clients, tool routing, Anthropic-compatible skins, OpenAI Responses skins, image-bearing agent requests, or model-group access.

Run coding-agent validation against a deployment-defined smoke or staging group before changing a stable group used by ordinary agents. Broad groups should receive a new provider/model/API skin only after the exact client surfaces pass: Codex/OpenAI Responses, Claude Code/Anthropic Messages, Cursor/opencode/aider OpenAI Chat where applicable, and any bridge direction being enabled. Do not use a successful simple text or tiny tool smoke as promotion evidence for large repository or IDE traffic.

The deterministic harness is:

```bash
python3 scripts/coding_agent_matrix.py --mode mock --output-dir tmp/coding-agent-matrix
```

Mock mode does not call live providers, the router, or installed client binaries. It creates isolated fixture repositories under `tmp/` by default, runs file-diff and unit-test verifiers, and writes:

- `tmp/coding-agent-matrix/coding-agent-matrix.json`
- `tmp/coding-agent-matrix/coding-agent-matrix.md`

Use live mode only with a scoped router caller token in an ignored env file:

```bash
python3 scripts/coding_agent_matrix.py \
  --mode live \
  --env-file tmp/router-client-smoke.env \
  --model-group '<allowed-coding-group>' \
  --output-dir tmp/coding-agent-matrix-live
```

The env file may contain `ROUTER_BASE_URL` and a client-specific router token variable such as `METRUM_ROUTER_KEY` or `ANTHROPIC_AUTH_TOKEN`. Do not commit it and do not print token values in logs.

## Required Clients

| Client | Router API shape | Automated status | Live validation path |
|---|---|---|---|
| Codex CLI | OpenAI Responses | Mock fixture verifier in `scripts/coding_agent_matrix.py` | `codex exec` against the router Responses provider config |
| Claude Code CLI | Anthropic Messages | Mock fixture verifier in `scripts/coding_agent_matrix.py` | `claude --bare --print` with `ANTHROPIC_BASE_URL=$ROUTER_BASE_URL/anthropic` and `ANTHROPIC_AUTH_TOKEN`; unset `ANTHROPIC_API_KEY` |
| opencode | Usually OpenAI-compatible Chat or Anthropic-compatible, depending on local config | Mock fixture verifier in `scripts/coding_agent_matrix.py` | Configure the router as the OpenAI or Anthropic provider and run the fixture edit task |
| aider | OpenAI-compatible Chat via LiteLLM-style model strings in common setups | Mock fixture verifier in `scripts/coding_agent_matrix.py` | Use `.aider.conf.yml` or env vars that point to the router base URL and a deployment-defined model group |

If a client is not installed in the runner, live mode records `client-not-installed` and the manual smoke path remains the acceptance path.

This matrix proves coding-agent compatibility: the client can authenticate, send the intended API shape, exercise tools or files, and receive a router response that passes the fixture verifier. It is not a full outcome benchmark for a customer workload. For promotion decisions, pair these smokes with Harbor or another workload-specific verifier and follow [Evaluation Evidence Playbook](EVALUATION_EVIDENCE_PLAYBOOK.md). Keep the public Harbor case study aligned with `docs/harbor-case-study.md` when publishing agent-eval evidence.

## Workload Matrix

Run each workload in an isolated temp checkout, never in the main router repository:

| Workload | Verifier |
|---|---|
| Simple text completion | Response contains the exact requested sentinel |
| Repository read/navigation | Output names the expected fixture files |
| Single-file edit | `python3 tests.py` passes after editing `app.py` |
| Multi-file edit | `python3 tests.py` passes and `CHANGELOG.md` contains the expected note |
| Tool-heavy task | Client writes or reads files in the isolated workspace only |
| Long-context task | Client summarizes the supplied long fixture without truncation errors |
| Image-bearing task | Client returns the expected image fixture answer or passes the deployment image verifier |
| Tiny output cap | Caller cap is forwarded and the selected target honors it |
| Forced model group | Request uses the configured allowed model group |
| Disallowed model group | Caller receives a safe auth/model error and no upstream provider call is attempted |

## Production Smoke Matrix

For production route changes, record client version, model group, request ID, selected provider/model/dialect from usage reports when available, status, verifier result, elapsed time, and token totals.

For large-agent payload promotion, also record safe request-shape metrics: request byte bucket, estimated input tokens, output cap field and value bucket, tool count, serialized tool-schema byte bucket, streaming flag, tool-choice mode, bridge direction, attempts, fallback state, and sanitized error class. If only one target has passed a large shape, gate other targets with `request_shape_support` or keep them in staging for that shape rather than removing them from all ordinary traffic.

| Smoke | Required when |
|---|---|
| Codex + deployment coding group | OpenAI Responses, Codex, tool, or coding-group target changes |
| Claude Code + deployment coding group | Anthropic Messages, Claude Code, thinking, or Anthropic-compatible target changes |
| opencode + deployment coding group | OpenAI-compatible Chat coding targets or opencode customer compatibility changes |
| aider + deployment coding group | Repository edit compatibility or OpenAI-compatible Chat coding target changes |
| One smoke through a temporary or restricted coding group | The change affects a group available only to selected callers |
| Image-bearing Codex or Claude Code smoke | VLM target, modality metadata, URL safety, or mixed code/image routing changes |

Use reusable non-secret caller token files on the host. Do not create throwaway production caller tokens for routine matrix runs unless an isolated investigation requires it; remove any temporary token and quota state immediately after the run.

The same model group can have different effective upstream pools for different clients. Codex uses OpenAI Responses, Claude Code uses Anthropic Messages, and Cursor/opencode/aider commonly use OpenAI Chat. Before declaring a group ready for a client, confirm the active target rows for that group expose the matching native `activeEligibilitySkin` and capability in provider catalog status. Catalog metadata for another skin is shown as `inactiveToolSupport` and does not make that target eligible for the client's request shape unless an explicitly validated bridge target is present.

## Client Setup Notes

### Codex CLI

- Use OpenAI Responses wire API.
- Set the router token in `METRUM_ROUTER_KEY`.
- Before each Codex run, fetch the caller-filtered, Responses-eligible `GET /v1/codex/models.json` catalog with that token to a local mode-0600 file and pass it through `model_catalog_json`; discover allowed group IDs from `/v1/models`, then choose one present in the Codex catalog. The catalog must never be synthesized from provider or target configuration.
- Use `codex exec` for non-interactive validation.
- Codex/Responses traffic can use native `openai-responses` targets and Chat-only targets only when the target explicitly enables `responses_to_chat` and the requested shape matches the validated bridge flags.
- For image validation, attach an image with `--image` and use a coding group that includes validated multimodal Responses targets.
- Provider-hosted tool descriptors should be stripped or rejected according to router Responses policy before upstream calls.
- Stateful `previous_response_id`, hosted tools, images, reasoning, structured-output, and streaming bridge behavior require separate validation; without it, expect a safe no-upstream eligibility failure for Chat-bridged targets.

### Claude Code CLI

- Set `ANTHROPIC_BASE_URL` to the router Anthropic-compatible namespace, for example `$ROUTER_BASE_URL/anthropic`, and `ANTHROPIC_AUTH_TOKEN` to the router token.
- Unset `ANTHROPIC_API_KEY`.
- Use a model group returned by `/v1/models` for the caller token.
- Pin subagent model selection to the same allowed group with `ANTHROPIC_MODEL=<allowed-coding-group>` and `CLAUDE_CODE_SUBAGENT_MODEL=<allowed-coding-group>` when validating a restricted single-group token. The smoke is not valid if usage rows show blank `requested_model`, blank `resolved_group`, or `403 model-not-allowed`.
- Legacy `$ROUTER_BASE_URL` setups that call `/v1/messages` should continue to work, but new validation should use `/anthropic` so OpenAI-compatible and Anthropic-compatible client failures can be separated cleanly.
- Use a restricted caller token whose `/v1/models` response contains only the intended group when proving access behavior. This catches accidental direct-provider fallback and subagent requests that omit the model group.
- For Kimi-style Anthropic-compatible targets, include thinking/default-thinking smoke coverage when that behavior changes.
- Do not infer Claude Code compatibility from OpenAI Chat or OpenAI Responses smokes. A target is eligible for Claude Code only when its active skin is Anthropic Messages or an explicitly documented bridge has passed Claude Code text, client-tool, subagent, output-cap, and large-context validation.

v4.1.0 known limitation ([#295](https://github.com/metrum-ai/router/issues/295)):
Anthropic Messages text can reach a validated OpenAI Chat or Responses target,
but translated Anthropic client-tool requests are ineligible. A group with only
those targets returns `502 no-eligible-target` for Claude Code tools before an
upstream attempt. `tool_support.anthropic_messages` metadata on an OpenAI
target does not enable the tool round trip. Use a separately validated native
Anthropic Messages tool route for Claude Code, and keep text, Chat-tool, and
Codex Responses-tool smoke results distinct. In the October 2026 v4.1.0
two-target deployment, Chat tool probes and an actual Codex CLI Responses file
write/read continuation passed while the Anthropic tool probe failed with that
eligibility error. Do not diagnose this result as a provider credential error.

Minimum Claude Code support matrix:

| Smoke | Acceptance evidence |
|---|---|
| Plain text | CLI returns the expected sentinel; usage row has inbound `anthropic`, requested and resolved model group, selected provider/model/dialect, terminal status, tokens, and no fallback unless fallback is under test. |
| Client tool or file edit | CLI creates or edits only the disposable fixture files; usage/report evidence shows `client_tools`-compatible target selection and safe tool-count telemetry. |
| Subagent | A prompt that triggers a subagent completes with `CLAUDE_CODE_SUBAGENT_MODEL` set to the allowed group; no blank-model `403` rows appear for that caller. |
| Long context and tool schema | A sanitized large fixture completes or fails with a classified router/provider error, bounded latency, request-shape buckets, attempts, and fallback evidence. It must not look like an unexplained client stop. |
| Tiny output cap | Caller `max_tokens: 1` or the CLI equivalent is forwarded and either honored by the selected target or filtered out by `honors_max_tokens`/request-shape metadata before upstream. |
| Target isolation | Each Anthropic Messages target or bridge candidate is tested through a dedicated smoke group before broad group eligibility changes. |

Record only safe scalar evidence: request ID, client/version, model group, inbound dialect, selected provider/model/dialect, status/error class, fallback flag, attempt count, timeout/client-canceled markers, latency, token totals, request-shape buckets, tool-count bucket, and stop/finish reason when persisted. Do not store or paste prompts, tool schemas, tool outputs, images, bearer tokens, token hashes, provider keys, upstream response bodies, or full production config.

Promotion rule: keep a target out of broad Claude Code routing when any required Claude Code smoke fails or has not been run for that exact provider/model/dialect/account. Use a dedicated deployment smoke group and grant the validation caller explicit access; do not change a broad production coding group just to run bridge or target-isolation fixtures.

### opencode

- Prefer a config that points the OpenAI-compatible provider base URL at the router `/v1` endpoint and uses a router token.
- If using an Anthropic-compatible opencode provider, use the router origin plus Anthropic Messages authentication variables.
- Verify both a text task and a fixture edit task; add image coverage only when the installed opencode workflow supports attachments.
- Use the opencode API capability matrix before declaring provider/model support for opencode-style requests. It sends synthetic text, client-tool, and image requests through OpenAI Chat and Anthropic Messages shapes, records sanitized pass/fail evidence, and writes JSON plus Markdown artifacts under `tmp/`:

```bash
python3 scripts/opencode_api_matrix.py \
  --base-url https://api.provider.example/v1 \
  --model provider-model-id \
  --api-key-env PROVIDER_API_KEY \
  --dialects openai-chat,anthropic \
  --tasks text,tools,image \
  --output-dir tmp/opencode-api-matrix
```

For direct Fireworks validation, use the Fireworks base URL and a protected `FIREWORKS_API_KEY` from the environment or ignored `env.json`:

```bash
python3 scripts/opencode_api_matrix.py \
  --base-url https://api.fireworks.ai/inference/v1 \
  --model accounts/fireworks/models/deepseek-v4-flash \
  --api-key-env FIREWORKS_API_KEY \
  --env-json env.json \
  --dialects openai-chat,anthropic \
  --tasks text,tools,image \
  --output-dir tmp/opencode-fireworks-deepseek
```

Use failed rows as capability evidence, not as a harness failure. A model that passes text/tools but rejects images must remain text-only in routing metadata until direct and router-level image smokes pass for that exact skin.
The command exits zero after writing evidence by default, even when a capability row fails. Add `--strict-exit` only for CI gates that should fail on any non-passing row. Text rows require the expected text, default `OK`; image rows require the expected receipt text, default `Rite Aid`, before they are marked as capability passes.

The opencode API matrix is direct capability evidence, not large-payload closeout evidence. When a route will serve Cursor/OpenCode-style OpenAI Chat traffic with large message history and tool schemas, also run `scripts/large_payload_chat_smoke.py` at the direct upstream, local router, and production router layers when a safe production caller is available. For production-derived regressions, run `go test ./internal/router -run 'ProductionDerived'` locally and `scripts/prod_smoke_regressions.py --fixture all` against the deployment so the same sanitized fixture set proves request-shape buckets, selected target, bridge direction, translated reasoning control, selected/must-not-select target behavior, and expected error classes. Use dedicated deployment smoke groups such as the reference `reasoning-bridge-smoke`, `responses-to-chat-bridge-smoke`, `large-openai-chat-tools-smoke`, or a scoped opencode stream-options smoke group; for caller-visible reference fixtures such as `high-gt1mb-openai-chat-tools`, use an existing scoped caller and confirm candidate filter evidence from safe reports. Grant one reusable, deployment-owned evaluation caller access before restricted smoke-group runs, and do not modify a broad production coding group just to execute bridge or large-payload fixtures. On 2026-06-30, Fireworks `accounts/fireworks/models/deepseek-v4-flash` passed the direct and local router 524 KB OpenAI Chat large-payload fixture with 24 tools and about 91K prompt tokens; a deployment rerun requires an operator-owned router URL and reusable safe caller token and should record only scalar helper output plus usage/report buckets. On 2026-07-09, production-derived opencode/AI SDK traffic with Chat `stream_options` produced upstream 400s on several broad Chat targets; keep that shape represented by the sanitized `opencode-ai-sdk-chat-stream-options` fixture and require explicit operator confirmation before changing broad coding-group composition.

### Cursor And OpenAI Chat IDE Clients

- Validate an OpenAI Chat request shape with `stream:true`, multiple messages, many function tools, one image part, and no explicit output cap when the client can send image-bearing repository context.
- The requested model group needs at least one target that supports OpenAI Chat tools and image input on the same target. Do not count an OpenAI Responses image target or an Anthropic Messages tool target as compatible for this OpenAI Chat request shape.
- A passing smoke should show the combined target selected, usage/request-shape telemetry persisted, and safe candidate/filter reasons for the skipped targets. If no combined target exists, expect `502 no-eligible-target`, zero upstream attempts, and no raw prompt, image data, tool schema, bearer token, token hash, or provider key in diagnostics.

### aider

- Use an ignored `.aider.conf.yml` or env vars for the router base URL, router token, and model group.
- Run an edit task in the fixture repo and verify with unit tests or exact file diff.
- Link model selection to `/v1/models`; do not assume a hosted example group exists in every deployment.

## Troubleshooting

| Symptom | Likely cause | Check |
|---|---|---|
| `403` or model access error | Caller token is not allowed to use the requested model group | Call `/v1/models` with the same token |
| `502 no-eligible-target` | Group lacks a target for the requested tools, modality, dialect, structured output, or cap behavior | Inspect target `tool_support`, `input_modalities`, dialect, and `honors_max_tokens` metadata |
| One upstream takes all traffic for one client | The group may have only one effective target for that client's API skin | Inspect Provider catalog status `groupSummary`, `activeEligibilitySkin`, and `inactiveToolSupport`, then smoke the missing skin |
| Claude Code authenticates against Anthropic instead of the router | `ANTHROPIC_API_KEY` is still set or base URL is wrong | Unset `ANTHROPIC_API_KEY`; verify `ANTHROPIC_BASE_URL` |
| Codex uses Chat instead of Responses | Provider config is missing `wire_api="responses"` | Inspect the Codex provider config |
| Codex `/v1/responses` cannot use a Chat-only target | The target lacks validated `responses_to_chat` metadata or the request shape is outside the bridge slice | Inspect request target filter reasons for `responses-to-chat-*` and run the restricted bridge smoke |
| Image task reaches no upstream | URL safety rejected the image or no image-capable target is eligible | Check caller error, usage row, and target `input_modalities` |
| Live client succeeds but verifier fails | Transport works but task quality is insufficient | Treat as model-group quality evidence, not a router compatibility pass |
