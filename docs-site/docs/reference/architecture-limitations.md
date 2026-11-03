---
title: Architecture, Platforms, And Limitations
doc_type: reference
---

# Architecture, Platforms, And Limitations

Metrum AI Router is a self-managed reverse proxy between authenticated AI
clients and operator-configured upstream providers or private model servers.

```mermaid
flowchart LR
  Client[Applications and agent clients] --> Ingress[TLS ingress]
  Ingress --> Router[Metrum AI Router]
  Router --> Policy[Model-group eligibility and routing]
  Policy --> Provider[External provider APIs]
  Policy --> Private[Private OpenAI-compatible upstreams]
  Router --> State[(State and usage database)]
  Router --> Ops[Restricted metrics and admin reports]
```

The router authenticates callers, checks model-group access and request-shape
eligibility, selects a configured target, translates supported API dialects,
and records sanitized operational and usage data. Provider credentials stay on
the server side. Configuration remains deployment-owned.

## Supported Deployment Platforms

Release artifacts support Linux on `amd64` and `arm64`. The documented runtime
shapes are a standalone binary, Docker Compose, and Kubernetes. SQLite is the
single-writer default; PostgreSQL is the explicit choice for validated
multi-replica or externally managed database deployments. Operators provide
TLS ingress, DNS, storage, secret management, network policy, backups, and
provider accounts.

## Runtime And Lifecycle Separation

The serving router handles proxy traffic, caller policy, routing, usage, and
safe diagnostics. Customer runtime images include the router and the
customer-local `metrum-ai-routerctl`. Release packages ship the same
customer-facing runtime CLIs for binary and Docker installs.

Validate the exact artifact before deployment. See [Deployment
Artifacts](../installation/deployment-artifacts) and [Package
Validation](../installation/package-validation).

## Security Boundaries

Caller tokens and model allow lists protect proxy traffic. Administrative
reports require configured admin authentication and authorization. `/metrics`
is restricted to callers with metrics-admin permission; ordinary application
callers receive `403 metrics-forbidden`. Provider keys, caller secrets, token
hashes, prompts, raw images, tool payloads, and full configuration must not be
placed in public logs or issue reports.

The router does not replace host, cluster, database, ingress, identity-provider,
secret-manager, provider-account, or client security. Operators must validate
those controls and the exact upstream model/API combinations they enable.

## Server Connection Deadlines

The four HTTP server deadlines are configurable; zero selects the default and
negative values and values above 24 hours are rejected at load.

| Key | Default | Bound |
| --- | --- | --- |
| `server.read_timeout_ms` | 60000 | reading the request body |
| `server.write_timeout_ms` | `server.upstream.timeout_ms` + 60000 (660000 by default) | absolute deadline for one response |
| `server.idle_timeout_ms` | 120000 | keep-alive idle connection |
| `server.stream_idle_timeout_ms` | 120000 | progress deadline for an event stream |

Go's `http.Server.WriteTimeout` is absolute for the whole response, so a long
answer used to be cut off while it was still being written. The write default now
follows the worst-case attempt chain — the largest attempt budget
(`targets[].timeout_ms`, `models.*.attempt_timeout_ms`, or
`server.upstream.default_attempt_timeout_ms`, at least `server.upstream.timeout_ms`)
times the number of targets a group can try, plus 60 s — so a slow attempt and a
fallback chain are given up by the attempt/upstream timeouts, which record a clear
error class and a usage row, not by the HTTP server. Setting
`server.write_timeout_ms` explicitly is allowed; the loader warns when it is
below that chain budget.

Event streams (`Content-Type: text/event-stream`) additionally get a *progress*
deadline: every successful stream write or flush extends the connection write
deadline by `server.stream_idle_timeout_ms`. A stream that keeps delivering data
therefore outlives the absolute write deadline. The upstream idle window starts
at the **first data byte**, not at the response headers, so an upstream that
reasons or prefills for minutes before its first token is bounded by the
attempt/upstream timeout rather than by this window. A mid-stream silent gap
longer than `server.stream_idle_timeout_ms`, or a downstream that stops reading,
ends the stream and is recorded as `upstream_timeout` with a stream-idle cause.
Unary responses are never extended, so their absolute bound is unchanged.

Edge and hierarchy reference deployments set these explicitly; see
`deploy/reference/hierarchy/config/edge.yaml`, `.../dc.yaml`,
`deploy/reference/amd-strix-halo-edge/config.yaml` and
`deploy/reference/nvidia-gb10-edge/config.yaml`.

## Explicit Limitations And Non-Goals

- Model-group names and provider availability are deployment-defined; the
  project does not guarantee access to any provider or model.
- Catalog metadata is not capability proof. Tools, images, API bridges, and
  large request shapes require direct upstream and router-level validation.
- The in-process response cache is per process and is cleared by restart.
- Dynamic-score conversation affinity is caller-isolated but process-local; it
  is cleared by restart and is not automatically shared between replicas.
- Native incremental upstream streaming applies to same-dialect OpenAI Chat,
  OpenAI Responses, and Anthropic Messages. Responses uses an event translator
  that rewrites protocol IDs and drops and counts unknown events without
  retaining response content. Eligible Chat ↔ Responses and Anthropic ↔ Chat/Responses
  bridges translate text and tool SSE incrementally. Existing request-shape and
  tool admission gates still apply; Anthropic reasoning stays on native Anthropic
  routes. `server.streaming.translator: synthesized` explicitly selects unary
  upstream and `writeIRStream` for streaming requests. Other upstream dialects
  without an incremental translator retain unary upstream plus synthesized SSE
  where streaming is eligible; Gemini generateContent rejects streaming. The
  default is `incremental` on the implemented paths.
- By default (`server.responses.model_identity: upstream`), caller-facing
  responses report the serving upstream model, which can change between
  requests to a weighted group or after fallback. `requested_group` reports the
  requested group on every response and stream and redacts upstream names from
  caller errors. Only the protocol `model` field changes; model names inside
  assistant text, tool arguments or reasoning are not rewritten. Usage rows,
  attempt rows, logs and admin reports keep the upstream in both modes.
- After the first flushed caller SSE frame, the HTTP response is committed. A later
  failure cannot change the caller's `200`, append a reliable error envelope,
  or fall back to another target; clients must detect a missing terminal event.
- TypeScript policies run synchronously in the serving process. Per-group
  concurrency limits bound admitted VMs but do not impose a pure-JavaScript
  execution timeout or preempt an unbounded loop.
- The Gemini `generateContent` adapter is an outbound unary text codec, not a
  caller-facing Gemini endpoint. Active targets require exact-model direct and
  router evidence; tools, images, reasoning, structured output, and streaming
  are not enabled for that adapter.
- `targets[].region` is operator-declared diagnostics metadata. It does not by
  itself enforce residency, retention, transfer, jurisdiction, or provider
  training behavior and is not included in TypeScript or external-policy
  inputs.
- The `intelligent` strategy is a baseline-only configuration
  foundation in the current release. It does not invoke its decision model or
  alter serving-target selection.
- SQLite is not a shared multi-writer database and must not back horizontally
  scaled router replicas.
- Kubernetes examples are starting points, not a cluster installer or a
  substitute for deployment-specific security review.
- Hierarchical routing can be assembled through supported API boundaries, but
  there is no universal topology controller.
- The project does not provide provider uptime, model-quality, legal,
  compliance, or support-service guarantees.

See [Router Configuration](../configuration/router-config), [Security And
Governance](../security-governance/overview), and the [Upgrade
Guide](../release-notes/upgrade-guide).


## Stored Responses

`POST /v1/responses` is the only Responses surface served without opt-in. The
stored-response operations (`GET`, `DELETE`, input items, cancel and
`previous_response_id` pinning) require explicit enablement per model group with
`server.responses_affinity.enabled_groups`, a durable usage database and a
retention policy, and they support native Responses targets only. With no group
enabled the stored routes keep returning `501 unsupported_endpoint` and
continuation keeps the earlier best-effort behaviour.

Local affinity metadata expires after its configured TTL (30 days by default)
and is removed by the retention job; expiry and purge remove router metadata
only and do not assert that the provider deleted its copy. Ownership is the exact
caller token, so a rotated or deleted token loses access to its earlier records;
there is no tenant-admin transfer path. Background Responses are not supported in
this release, so a `stream=true` retrieval is forwarded but refused by providers
that only stream background responses.

## Identifier transformation

`server.identifiers.mode` defaults to `rewrite`. Configure a 64-byte random
AES-256-SIV key in `transform.current.key` using hex or standard base64 (for
example `${ROUTER_ID_TRANSFORM_KEY}`). `key_id` is a printable, loggable label
(for example `idk-2026-09`); the single base62 wire epoch character is derived
from it. Caller IDs have the form `mr_` + epoch + unpadded base64url ciphertext.
Encryption is deterministic: equal IDs under the same key remain equal. Keep the
key stable across replicas and restarts.

Rotation supports only `current` and optional `previous`, with different
`key_id` values whose derived epochs must not collide.
The previous key needs an RFC3339 `valid_until` in the future, no more than 30 days
away at validation. Encoding always uses current; decoding accepts previous only
until its deadline. Remove the previous configuration when its grace period ends.
The router validates keys and performs an encode/decode self-test at startup.
The relational configuration loader accepts deployment-owned identifier settings
separately; identifier secrets are not stored in its database projection.

Native Anthropic, Responses, and Chat SSE streams rewrite only protocol ID string
fields, preserving content deltas. Responses frames are reserialized; comments,
SSE retry/ID fields, and unknown event types are not forwarded. Responses function-call
`call_id` is transformed along with item IDs so tool results can round-trip.
Ingress decodes prefixed `tool_call_id`, `tool_use_id`, `call_id`, and
`previous_response_id`. Unprefixed IDs remain accepted. This change does not add
ID rewriting to unary responses or translated streams, or enable upstream
stateful Responses routing across providers.

Explicit `passthrough` skips transformation and emits a startup warning.
`pii_filter` restoration (`redact_and_restore` or `restore_response: true`) is
rejected with `F-011: pii-filter-redact-and-restore-unsupported`; use `redact_only`
or `fail_on_match`. Native streams cannot safely restore arbitrary content deltas.
