---
title: Tool Call Firewall
doc_type: howto
---

# Tool Call Firewall

`server.tool_firewall` inspects completed tool calls before the router returns
them to an agent. It is off by default. `shadow` records a safe policy reason
without changing the response and never adds a serving hold; `enforce` suppresses denied calls and returns
an immediate pending result when a sequence rule uses `action: hold`. Enforced
streamed tool calls are held automatically for inspection, using 30 seconds and
1 MiB per stream when the model group has no explicit hold bounds. This automatic
hold does not enable schema validation; the group must opt into
`streaming.hold_until_validated` for that check.

```yaml
server:
  tool_firewall:
    mode: enforce
    url_allow_hosts: ["api.example.com"]
    built_in_rules:
      remove_root: false # optional; omitted rules are enabled
    approval_timeout_ms: 30000
    max_pending_approvals: 100
    sequences:
      - first_tool: read_customer
        first_args_regex: 'sensitive'
        then_tool: http_request
        within_turns: 3
        action: hold
    deny:
      - tool: bash
        args_regex: '(?i)curl[^|]*\|\s*(ba)?sh'
```

Built-in checks deny dangerous shell pipelines, root deletion, paths under
`~/.aws`, `~/.ssh`, and `.env` (including `$HOME`, `/home/<user>`, `${HOME}`,
and `/Users/<user>` paths), cloud metadata addresses, and URLs not accepted by the
shared `validateEgressURL` policy. Omitted `built_in_rules` entries are
enabled; set an entry to `false` to disable only that built-in. Built-in URL
and shell checks skip file content fields (`write_file.content`,
`apply_patch.input`/`patch`, and `edit.new_string`); sensitive-path,
root-delete, metadata-address, and configured deny checks still inspect complete
arguments. Hosted Responses tools, including `mcp_call`, run at the provider and
are allowed by default unless a configured rule names them. An `mcp_call` rule
matches the MCP tool's name, not the `mcp_call` item type. Responses
`custom_tool_call` input is freeform text and is matched as a string. An empty
`url_allow_hosts` denies every outbound URL, and non-local HTTP is always
denied. Configured `deny` rules match a tool name, RE2 tool or argument
expression, or an argument substring. URL hosts are exact hostnames.

Sequence state stores only a caller and first-user-turn fingerprint, sequence
rule index, turn number, and expiry in a bounded process-local cache. Client
conversation/session IDs and model aliases do not control this security key.
Requests without a first user turn fail closed when sequence rules are
configured. A full cache evicts the least-recently-used entry for that caller
and never blocks a response without tool calls. A firewall configuration
change clears sequence history and denies approval holds created under the
previous policy. State is process-local; each replica observes only its own
turns.

For a `hold`, the router immediately returns a pending-approval tool result
with an opaque approval ID. After the approver submits
`POST /admin/v1/tool-approvals/{approval_id}` with `{"decision":"release"}` or
`{"decision":"deny"}`, the agent retries the same request. The operation
requires the explicit `admin:tool_approvals:resolve` permission. Approval IDs
are process-local, so send the admin request to the replica that emitted the
pending event. Timeout, full pending capacity, and denial all fail closed. A
release allows the matching retry to return the original tool call to the
agent; the router never executes tools itself. Enforce mode automatically
inspects streamed tool calls before releasing tool frames, with the bounded
defaults described above. Shadow mode does not delay streamed responses; it
observes calls only when another enabled feature already holds them.

Tool arguments and prompt text are never copied into firewall events or usage
metadata. Events include only the rule code, action, and, for approval events,
the opaque approval ID.
