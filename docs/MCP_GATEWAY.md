<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# MCP gateway

The MCP gateway exposes configured upstream MCP servers through the router's
single `/mcp` endpoint. It is disabled by default. Clients authenticate with a
router caller key; upstream credentials are read from server environment
variables and are never returned to clients.

Configure each upstream with a unique name, an HTTPS URL, and an explicit
`allow_hosts` list. Set `credential_env` to the name of an environment variable
with the `ROUTER_MCP_` prefix. Store the value in the deployment environment;
do not put credentials in configuration files or URLs. Cleartext HTTP is
accepted only for loopback hosts, for local development and sidecars.
`allow_http` does not permit a non-loopback MCP URL to use cleartext.

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
    - caller: support-agent
      deny_by_default: true
      tools:
        - knowledge/search
  limits:
    - caller: support-agent
      tool: knowledge/search
      rpm: 30
      cost_per_call_usd: 0.01
      hourly_usd: 2
      daily_usd: 10
```

The egress policy blocks unlisted hosts, private, loopback, link-local, and
metadata addresses. Redirects are rejected and requests go directly to the
allowlisted host without using an environment proxy. The gateway negotiates
protocol versions `2025-11-25` and `2026-07-28`; the default is
`2025-11-25`. `timeout_ms` defaults to 30 seconds and is capped at five minutes,
independently of provider timeouts. Requests are limited to 2 MiB and upstream
responses to 8 MiB. JSON-RPC batches are rejected. The `rpm` value sets the
per-caller gateway request ceiling per minute.

Policies can scope tool allow-lists to a caller, project, or model group.
Applicable policies compose restrictively: a tool must be allowed by every
matching policy. A nonempty `tools` list is an allow-list and denies omitted
tools; `deny_by_default: true` also denies omitted tools when the list is empty.
A model-group header is honored only when the authenticated caller is allowed
to route to that group. Tool names use `server/tool` form.

The `limits` block accepts caller-wide limits or a `server/tool` limit. `rpm`
sets a request-rate ceiling. `cost_per_call_usd` is an operator-provided fixed
estimate charged for each call against `hourly_usd` and `daily_usd`; it is not a
provider-reported charge. Caller-wide and per-tool limits both apply when
configured. Rate and spend counters persist with quota state.

When a caller selects a model group with `pii_filter.enabled: true`, MCP call
arguments use the group's `apply_to.messages` setting and returned tool results
use `apply_to.tool_results`. Set `secrets_enabled: true` to include the built-in
secret matcher. `redact_only` masks matching values before they reach the
upstream server and before results reach the caller; `fail_on_match` denies a
matching call or withholds a matching result. Invalid filters and bounded
inspection-limit failures fail closed. No raw arguments or results are saved.

MCP calls also pass through `server.tool_firewall` when it is enabled. Firewall
rules use the qualified `server/tool` name. A held call returns a portable text
result with an opaque approval ID and asks the client to retry after approval;
the gateway forwards the retry only after the approval is released. Configure
`admin:tool_approvals:resolve` only for principals allowed to release holds.
Firewall approvals and sequence state are process-local. MCP sequence state is
scoped to the authenticated caller, and approval retries must reach the same
replica. Expired or denied approvals fail closed.

Each tool call has a durable audit row with the caller, qualified tool name, a
keyed HMAC-SHA-256 digest of canonicalized arguments, result status, and
latency. Raw arguments, tool results, and credentials are not stored. The audit
table follows the security-access retention class. A start record is written
before forwarding; an unavailable audit store prevents the call from reaching
the upstream server. Tool sessions are closed after use, and `tools/list`
queries servers in parallel with per-server error isolation.

Completed invocation outcomes are also written to the hash-chained admin audit
store and included in `/admin/v1/audit-events/export`. Export rows contain the
caller, tool name, result status, and keyed argument digest, but no raw payload.
