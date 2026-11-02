---
title: Coding-Agent Client Matrix
doc_type: reference
---

# Coding-Agent Client Matrix

Metrum AI Router can serve coding-agent clients through deployment-defined model groups. Use `/v1/models` with your router token to see the groups your token may request; hosted examples may use names such as `big-coder`, but your deployment can choose different names.

## Compatibility Matrix

| Client | Router API shape | Preferred base URL | Model value |
|---|---|---|---|
| Codex CLI | OpenAI Responses | `https://<router-host>/v1` | A group returned by `/v1/models` |
| Claude Code CLI | Anthropic Messages | `https://<router-host>/anthropic` | A group returned by `/v1/models` |
| opencode | OpenAI-compatible Chat in the recommended setup | `https://<router-host>/v1` | A group returned by `/v1/models` |
| aider | OpenAI-compatible Chat in common router setups | `https://<router-host>/v1` | A group returned by `/v1/models` |
| IDE/agent clients | Client-specific OpenAI or Anthropic adapter | `/v1` for OpenAI-compatible, `/anthropic` for Anthropic-compatible | A group returned by `/v1/models` |

| Client | What to validate |
|---|---|
| Codex CLI | Text, file edits, function tools, image attachments when supported, tiny output caps, and allowed model-group selection |
| Claude Code CLI | Text, client tools, subagent model selection, default thinking behavior where configured, image-bearing Messages payloads, and Anthropic auth environment |
| opencode | Text, workspace edit tasks, tools, and the configured OpenAI-compatible provider shape |
| aider | Workspace edit task, unit-test verifier, and explicit model-group selection |
| IDE/agent clients | Minimal manual smoke when headless validation is unavailable |

Successful smokes should prove task outcome, not just HTTP status. For workspace edit tasks, run a unit test or check an exact file diff. For image tasks, verify the answer against an expected result. For disallowed model groups, expect a safe router error and no upstream provider call.

Different clients can see different effective target pools inside the same model group because they use different API surfaces. Codex uses OpenAI Responses, Claude Code uses Anthropic Messages, and many IDE clients use OpenAI Chat. A target validated for Chat tools is not automatically eligible for Responses function tools or Anthropic client tools. If one client receives a compatibility error or appears to route to fewer upstreams than another client, ask the deployment admin to inspect effective provider-skin eligibility for that group.

Admins can validate production-safe coding-agent request shapes with sanitized smoke fixtures instead of captured customer prompts. A complete fixture set should cover Codex Responses reasoning/tools, Cursor Chat tools and bridge shapes, Claude Code thinking/tools, opencode/aider Chat flows, large tool schemas, provider-skin mismatch, no-eligible diagnostics, and upstream error classification. Run the fixtures against a dedicated smoke model group, not an active production group, and grant the test caller explicit access to that group in deployment config.

The smoke prints only safe scalar evidence such as request ID, status, selected provider/model/dialect, API surface, and request-shape buckets. It must not include raw prompts, tool schemas, images, router tokens, provider keys, token hashes, or full configs.

Reasoning controls follow the same rule. Codex normally sends OpenAI Responses-shaped traffic, so explicit reasoning appears as a `reasoning` object and needs a Responses-native or explicitly bridged target. Claude Code uses Anthropic Messages and may send `thinking` or rely on deployment-configured default thinking behavior. Cursor, opencode, aider, LiteLLM adapters, and custom OpenAI-compatible clients can send OpenAI Chat, Anthropic-compatible, or mixed legacy request shapes depending on version and configuration; some custom-provider flows may not expose reasoning settings directly to the user. Call `/v1/models` with the same router token, use one of the returned deployment-defined groups, and treat the request ID plus usage diagnostics as the source of truth for the actual inbound dialect and selected provider skin.

For rollout decisions, keep client setup distinct from outcome evaluation. A setup smoke proves that a client can reach a compatible router path; a workload verifier such as Harbor, unit tests, browser-control checks, OCR goldens, or product acceptance tests proves whether the model group completes the job. See [Prove Router Quality](../evaluation/prove-router-quality) and the [Harbor Case Study](../evaluation/harbor-case-study) for evaluation patterns.

## Codex CLI

Codex CLI uses the OpenAI Responses-compatible router path. Set a router token in `METRUM_ROUTER_KEY`, fetch `/v1/codex/models.json` with that same token into a local protected file, and configure `wire_api="responses"` plus `model_catalog_json`. `/v1/models` remains the source of the allowed group IDs; the Codex endpoint is the caller-filtered metadata projection for those IDs. See the complete [Codex CLI](./codex-cli) fetch-then-run workflow.

```bash
export METRUM_ROUTER_KEY="rtr_metrum_<user>_<project>_<env>_<key>_<secret>"

codex exec --ignore-user-config --ephemeral \
  --skip-git-repo-check \
  -c 'model="<allowed-model-group>"' \
  -c 'model_provider="metrum-ai-router"' \
  -c 'model_catalog_json="~/.codex/metrum-models.json"' \
  -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
  -c 'model_providers.metrum-ai-router.base_url="https://<router-host>/v1"' \
  -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
  -c 'model_providers.metrum-ai-router.wire_api="responses"' \
  "Reply with exactly: router codex ok" </dev/null
```

For image-bearing validation, attach an image with `--image` and use the same allowed coding group if that group includes validated multimodal Responses targets.

Codex requests use the Responses API shape. If a model group contains many OpenAI Chat targets but only a smaller set of Responses-compatible targets, Codex will route within that Responses subset. For example, a deployment may validate MiniMax `MiniMax-M3` as separate Chat, Responses, and Anthropic Messages provider skins; Codex can use only the Responses skin in that group.

## Containerized tool smokes

Tool-capable smokes should run in a disposable container. Pass only the router
base URL and a scoped router token. Bind-mount only a scratch work directory.

```bash
unset ANTHROPIC_API_KEY
mkdir -p "$WORK/claude-tool-work"
docker run --rm --network host --cap-drop ALL --security-opt no-new-privileges \
  --cpus 1 --memory 1g --pids-limit 256 --read-only \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount type=bind,source="$WORK/claude-tool-work",target=/workspace \
  -e "ANTHROPIC_BASE_URL=http://127.0.0.1:18081/anthropic" \
  -e "ANTHROPIC_AUTH_TOKEN=$ROUTER_TOKEN" \
  -w /workspace "$TOOL_SMOKE_IMAGE" \
  claude --bare --print --model claude-tools-smoke \
    --permission-mode bypassPermissions \
    --allowedTools "Write,Bash" \
    "Create claude_tool_smoke.txt containing exactly claude-tool-ok, run cat claude_tool_smoke.txt, then finish with claude-tool-ok."

test "$(cat "$WORK/claude-tool-work/claude_tool_smoke.txt")" = "claude-tool-ok"
```

```bash
mkdir -p "$WORK/codex-tool-work"
curl -fsS "http://127.0.0.1:18081/v1/codex/models.json" \
  -H "Authorization: Bearer $ROUTER_TOKEN" \
  -o "$WORK/codex-tool-work/metrum-models.json"
docker run --rm --network host --cap-drop ALL --security-opt no-new-privileges \
  --cpus 1 --memory 1g --pids-limit 256 --read-only \
  --tmpfs /tmp:rw,nosuid,nodev,size=256m \
  --mount type=bind,source="$WORK/codex-tool-work",target=/workspace \
  -e "METRUM_ROUTER_KEY=$ROUTER_TOKEN" \
  -e "ROUTER_BASE_URL=http://127.0.0.1:18081" \
  -w /workspace "$TOOL_SMOKE_IMAGE" \
  codex exec --ignore-user-config --ephemeral \
    --ignore-rules \
    --skip-git-repo-check \
    --dangerously-bypass-approvals-and-sandbox \
    -C /workspace \
    -c 'model="agent-tools-smoke"' \
    -c 'model_provider="metrum-ai-router"' \
    -c 'model_catalog_json="/workspace/metrum-models.json"' \
    -c 'model_providers.metrum-ai-router.name="Metrum AI Router"' \
    -c 'model_providers.metrum-ai-router.base_url="http://127.0.0.1:18081/v1"' \
    -c 'model_providers.metrum-ai-router.env_key="METRUM_ROUTER_KEY"' \
    -c 'model_providers.metrum-ai-router.wire_api="responses"' \
    "Create codex_tool_smoke.txt containing exactly codex-tool-ok, run cat codex_tool_smoke.txt, then finish with codex-tool-ok." </dev/null

test "$(cat "$WORK/codex-tool-work/codex_tool_smoke.txt")" = "codex-tool-ok"
```

## Claude Code CLI

Claude Code uses the Anthropic Messages-compatible router path. Use `ANTHROPIC_AUTH_TOKEN` for the router token and unset `ANTHROPIC_API_KEY` for router traffic.

Router tokens can only request model groups returned by `/v1/models` for that same token. Pin both the main Claude Code session and subagents to allowed router groups.

```bash
unset ANTHROPIC_API_KEY
export ANTHROPIC_BASE_URL="https://<router-host>/anthropic"
export ANTHROPIC_AUTH_TOKEN="rtr_metrum_<user>_<project>_<env>_<key>_<secret>"
export ANTHROPIC_MODEL="<allowed-model-group>"
export CLAUDE_CODE_SUBAGENT_MODEL="<allowed-model-group>"

claude --bare --print --model "<allowed-model-group>" \
  "Reply with exactly: router claude ok"
```

Local Claude Code settings can pin the same values without exposing private deployment values in shared docs:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://<router-host>/anthropic",
    "ANTHROPIC_AUTH_TOKEN": "rtr_metrum_<user>_<project>_<env>_<key>_<secret>",
    "ANTHROPIC_MODEL": "<allowed-model-group>",
    "CLAUDE_CODE_SUBAGENT_MODEL": "<allowed-model-group>"
  }
}
```

Save that shape as `.claude/settings.local.json` for local use. For a specific subagent to use a different router group, first leave `CLAUDE_CODE_SUBAGENT_MODEL` unset or set it to inherit behavior for that run. Then set the router group in the subagent frontmatter and confirm the same router token returns that group from `/v1/models`:

```md
---
name: router-subagent
description: Work on the assigned task using the selected router model group.
model: <allowed-model-group>
---

Complete the assigned task and report the result.
```

A pinned `CLAUDE_CODE_SUBAGENT_MODEL` takes precedence over per-subagent frontmatter, so unset or inherit it before testing a different frontmatter model group.

When a Messages request includes image content, the router filters the requested group to targets that advertise `image` in `input_modalities`.

## opencode

Configure opencode to use the router through the OpenAI-compatible endpoint at `https://<router-host>/v1`. The configured model is a router model group returned by `/v1/models`, not a raw upstream provider model name. Hosted examples may use groups such as `big-coder`, but each deployment chooses its own group names and token allow lists.

opencode uses the AI SDK OpenAI-compatible provider, which can send streaming Chat requests with `stream_options`. For same-dialect Chat targets, the router filters to targets that have passed that request shape, forwards compatible `stream_options`, and proxies native upstream SSE. If a deployment sees repeated upstream 400s for opencode traffic, validate a sanitized fixture for the exact request-shape buckets before changing the production model-group composition.

Create a local token file with owner-only permissions. Use the real router token in place of the placeholder, and keep this file in local secret storage rather than packages, tickets, shared support bundles, or shared project files.

```bash
mkdir -p ~/.config/opencode
umask 077
printf %s "rtr_metrum_<user>_<project>_<env>_<key>_<secret>" > ~/.config/opencode/metrum-ai-router.key
chmod 600 ~/.config/opencode/metrum-ai-router.key
```

Edit `~/.config/opencode/opencode.json` and add or merge a provider that uses `@ai-sdk/openai-compatible`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "metrum/<allowed-model-group>",
  "provider": {
    "metrum": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Metrum AI Router",
      "options": {
        "baseURL": "https://<router-host>/v1",
        "apiKey": "{file:~/.config/opencode/metrum-ai-router.key}"
      },
      "models": {
        "<allowed-model-group>": {
          "name": "<allowed-model-group>"
        }
      }
    }
  }
}
```

If the file already has other settings, keep them and merge in the top-level `model` value and the `provider.metrum` block. The provider prefix in `metrum/<allowed-model-group>` must match the provider key in the JSON object.

Verify model access with the same token file before running opencode:

```bash
curl -fsS "https://<router-host>/v1/models" \
  -H "Authorization: Bearer $(cat ~/.config/opencode/metrum-ai-router.key)"
```

The response should include the allowed router model group in `data[].id`:

```json
{
  "object": "list",
  "data": [
    {
      "id": "<allowed-model-group>"
    }
  ]
}
```

If `/v1/models` does not show the desired group, the token is not allowed to request that group. Use one of the listed group IDs or ask the deployment admin to update the token allow list.

Run a text smoke:

```bash
opencode run --pure --model metrum/<allowed-model-group> \
  "Reply with exactly: opencode router ok"
```

Expected output:

```text
opencode router ok
```

For workspace-edit validation, run opencode in a disposable fixture project and require an exact file diff or passing test, not just a successful HTTP response. A passing smoke should be visible in usage reports with the selected upstream provider/model. If your opencode setup uses an Anthropic-compatible provider instead, use the router origin and the Anthropic auth variables from the Claude Code section.

## aider

Use an ignored local `.aider.conf.yml` or environment variables to point aider at the router. The exact model string can vary by aider and LiteLLM version; prefer an OpenAI-compatible configuration that sends requests to the router `/v1` base URL and uses a model group returned by `/v1/models`.

Example local config pattern:

```yaml
model: openai/<allowed-model-group>
openai-api-base: https://<router-host>/v1
openai-api-key: rtr_metrum_<user>_<project>_<env>_<key>_<secret>
auto-commits: false
```

Run aider in a disposable fixture project and verify the edit with a unit test or exact diff:

```bash
aider --config .aider.conf.yml app.py tests.py \
  --message "Change route_label() to return after, then run the tests."
```

## IDE And Agent Clients

For Cursor, Continue.dev, Cline, Roo Code, SDK-based agents, LiteLLM adapters, and similar clients, prefer headless validation when the client supports it. When it does not, run a minimal manual smoke:

- confirm the client uses the router base URL and a router token;
- request a model group returned by `/v1/models`;
- perform a text request and one workspace edit or tool task;
- when the client can attach images, run a mixed OpenAI Chat tools-plus-image smoke against the intended model group and require a target that supports tools and image input on the same OpenAI Chat skin;
- record client version, request time, request ID if shown, model group, and expected output;
- verify usage reports show the selected upstream provider/model/dialect, token totals, status, and no unexpected fallback.

## Common Errors

| Error | Meaning | Next step |
|---|---|---|
| `403` or model access failure | The token is not allowed to use that model group | Call `/v1/models` with the same token |
| 400 compatibility or modality error | Configured targets cannot satisfy the request's dialect, tools, modality, structured output, or cap requirement | Use a compatible group or ask the deployment admin to validate and enable a target; see [Error Reference](../reference/errors) |
| Reasoning request rejected | The group has no active target or bridge that can preserve the requested `reasoning_effort`, `reasoning`, or `thinking` control | Check `/v1/models`, request diagnostics, bridge metadata, and selected provider skin |
| One client routes to fewer upstreams than another | The clients use different API surfaces and the group has different active targets per skin | Ask the admin to check provider catalog status for effective eligibility by skin |
| Claude Code calls Anthropic directly | Environment still has direct Anthropic settings | Unset `ANTHROPIC_API_KEY` and check `ANTHROPIC_BASE_URL` |
| Image request rejected before upstream | URL safety rejected the image URL, or no image-capable target is eligible | Use a public HTTPS image URL or an inline image, and check group modality support |
| Client output is wrong but transport succeeded | The selected model group did not meet the workload quality bar | Treat this as model-group evaluation evidence, not a client setup pass |
