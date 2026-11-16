---
title: Tool Call Firewall
doc_type: howto
---

# Tool Call Firewall

`server.tool_firewall` inspects completed tool calls before the router returns
them to an agent. It is off by default. `shadow` records a safe policy reason
without changing the response; `enforce` suppresses denied calls and waits for
an administrator when a sequence rule uses `action: hold`.

```yaml
server:
  tool_firewall:
    mode: enforce
    url_allow_hosts: ["api.example.com"]
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
`~/.aws`, `~/.ssh`, and `.env`, cloud metadata addresses, and URLs not accepted
by the shared `validateEgressURL` policy with the configured host allow list.
Configured `deny` rules match a tool name, RE2 tool or argument expression, or
an argument substring. URL hosts are exact hostnames.

Sequence state stores only a conversation fingerprint, sequence rule index,
turn number, and expiry in a bounded process-local cache. Requests that omit a
usable conversation identity fail closed when sequence rules are configured,
and a full cache also denies calls rather than forgetting earlier sequence
steps. Use stable conversation identity in requests and route a conversation
consistently to one router process; with multiple replicas each process observes
only its own turns.

For a `hold`, the inference request remains open while the approver submits
`POST /admin/v1/tool-approvals/{approval_id}` with `{"decision":"release"}` or
`{"decision":"deny"}`. The operation requires the explicit
`admin:tool_approvals:resolve` permission. Approval IDs are process-local, so
send the admin request to the replica that emitted the pending event. Timeout,
request cancellation, full pending capacity, and denial all fail closed. A
release returns the original tool call to the agent; the router never executes
tools itself. Streamed requests that offer tools fail closed until #929's
bounded stream gate is available.

Tool arguments and prompt text are never copied into firewall events or usage
metadata. Events include only the rule code, action, and, for approval events,
the opaque approval ID.
