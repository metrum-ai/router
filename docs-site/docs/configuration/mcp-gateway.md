---
title: MCP Gateway
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# MCP gateway

The router exposes configured upstream MCP servers through one `/mcp`
endpoint. The gateway is disabled by default. Clients authenticate with a
router caller key; upstream credentials are read from server environment
variables and are never returned to clients.

```yaml
mcp:
  enabled: true
  timeout_ms: 30000
  rpm: 60
  servers:
    - name: knowledge
      url: https://mcp.example.invalid/rpc
      allow_hosts: [mcp.example.invalid]
      credential_env: ROUTER_MCP_KNOWLEDGE_TOKEN
      protocol_version: 2025-11-25
  policies:
    - project: support
      deny_by_default: true
      tools:
        - knowledge/search
  limits:
    - caller: support-bot
      tool: knowledge/search
      rpm: 30
      cost_per_call_usd: 0.01
      hourly_usd: 2
      daily_usd: 10
```

Every server must name its allowed destination hosts in `allow_hosts`. The
gateway blocks private, loopback, link-local, and metadata addresses for
upstream HTTPS requests; loopback HTTP is permitted for local servers. The
`allow_http` setting does not permit cleartext HTTP to non-loopback MCP hosts.
Redirects are rejected. Set `credential_env` to an environment variable with the
`ROUTER_MCP_` prefix containing the upstream bearer credential; credentials
must not be placed in URLs or configuration files. The gateway sends requests
directly to the allowlisted host without using an environment proxy.
`protocol_version` accepts `2025-11-25` or `2026-07-28` and defaults to
`2025-11-25`. `timeout_ms` defaults to 30 seconds and is capped at five
minutes, independently of provider timeouts. Requests are limited to 2 MiB
and upstream responses to 8 MiB. JSON-RPC batches are rejected. `rpm` sets
the per-caller gateway request ceiling (default 60 per minute).

Policies can scope allow-lists to a caller, project, or model group. Applicable
policies compose restrictively: a tool must be allowed by every matching
policy. A nonempty `tools` list is an allow-list and denies omitted tools;
`deny_by_default: true` also denies omitted tools when the list is empty. A model
group supplied by the client is honored only when the authenticated caller is
also allowed to route to that group. Tool names use `server/tool` form.

`limits` can set a caller-wide limit or a `server/tool` limit. `rpm` limits
requests per minute. `cost_per_call_usd` is an operator-provided fixed cost
estimate charged for each call against `hourly_usd` and `daily_usd` ceilings;
it is not a provider-reported charge. Caller-wide and tool-specific limits
both apply when configured. Limit counters are persisted with quota state.

Each tool call has a durable audit row with caller, qualified tool name,
keyed HMAC-SHA-256 digest of canonicalized arguments, result status, and latency. Raw
arguments, tool results, and credentials are not stored. The audit table follows
the security-access retention class. The router writes a start record before
forwarding a tool call; an unavailable audit store prevents forwarding.

When the selected model group's PII filter is enabled, the gateway applies it
to tool arguments and results. With `server.tool_firewall.mode: enforce`, a
held call returns a pending-approval tool result; an authorized operator
releases or denies it through the admin API, and the client retries the same
call. Completed invocation outcomes are included in the hash-chained
`/admin/v1/audit-events/export` with caller and tool identifiers, status, and a
keyed argument digest. Raw arguments, results, and credentials remain excluded.
See [tool-call firewall](./tool-firewall), [PII filtering](./pii-filtering),
and [admin audit exports](../security-governance/admin-audit-export).
