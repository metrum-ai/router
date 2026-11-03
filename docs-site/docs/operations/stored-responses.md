---
title: Stored Responses affinity
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Stored Responses affinity

The router serves `POST /v1/responses`. With the defaults it does not keep any
state about a response, so a client that uses the stored-response surface cannot
`GET`, `DELETE`, list input items or `cancel` a response through the router, and
a `previous_response_id` follow-up may land on an account that does not hold the
first response. Stored Responses affinity fixes both: the router records which
caller owns which client-visible response id and on which upstream account it
lives, then pins the follow-up and the stored-response operations to that
account.

The feature is **off by default**, enabled explicitly per model group, and only
for native Responses targets. With no group enabled the router serves
`POST /v1/responses` exactly as before and the stored-response routes keep
returning `501 unsupported_endpoint`.

## Configuration

```yaml
server:
  usage_db:
    enabled: true
    driver: sqlite          # or postgres
  retention:
    enabled: true
  responses_affinity:
    enabled_groups: [support-chat]
    key_env: ROUTER_RESPONSES_AFFINITY_KEY
    key_id: affinity-2026-11
    retention_days: 30      # optional, default 30, maximum 365
models:
  support-chat:
    strategy: weighted
    targets:
      - provider: openai-responses-account-a
        model: gpt-6-luna
      - provider: openai-responses-account-b
        model: gpt-6-luna
```

Provide 32 bytes of key material through the named environment variable, as hex
or standard base64:

```sh
export ROUTER_RESPONSES_AFFINITY_KEY="$(openssl rand -hex 32)"
```

- `enabled_groups` must name existing model groups. Each listed group must have
  at least one target whose dialect is `openai-responses`; a group without one
  fails startup rather than silently never storing anything.
- The key is read once at startup and never written to configuration, logs,
  traces, usage rows or error bodies. Per-purpose keys are derived from it, so
  the lookup hash, the owner fingerprint and upstream-id encryption never reuse
  key bytes.
- `key_id` is a loggable label (`^[a-z0-9][a-z0-9._-]{0,31}$`). Rotate by setting
  `key_id` plus `previous_key_env`/`previous_key_id` for the old epoch; ownership
  lookup, response lookup and upstream-id decryption all accept either epoch
  while it is configured, so rows written before the rotation stay reachable.
  Removing the previous epoch makes those rows unknown again (the same `404` as a
  missing id). The pair must be set together.
- Stored-response metadata must follow a retention policy, so startup fails when
  affinity is enabled without `server.retention` on. A `responses_affinity`
  retention class is added automatically with `retention_days` (30 by default)
  unless you configure that class explicitly.

## What is stored

No prompt, response content or bearer token is stored. One row per stored
native Responses request (`response_affinity`) holds:

- a keyed fingerprint of the owning caller token and the fingerprint key id;
- tenant (owning user account), project and environment from the caller, plus
  the model group;
- a keyed lookup hash of the response id and its key id;
- the upstream response id under authenticated encryption (ciphertext, nonce and
  key id);
- the exact provider, `provider.key_id` account reference, model, provider
  dialect and endpoint;
- local lifecycle state (`in_progress`, `completed`, `cancelled`, `failed`,
  `incomplete`, `deleted`, `expired`), background and settlement markers, the
  upstream-delete state, a bounded error class, and creation, update and expiry
  timestamps.

Lookup uniqueness is scoped to the owner token, tenant, project, environment and
lookup hash, so an externally supplied id is never treated as globally unique.

## Behaviour

A request is stored only when the served target is a native Responses target,
the group is enabled, the client did not send `store: false`, and the target does
not set `force_store_false`. Both no-store paths are covered by tests and create
no row. A `store: false` follow-up never creates a record and
still forwards `store: false` after the ownership check.

| Operation | Behaviour |
|---|---|
| `POST /v1/responses` (stored) | The record is written before the response id is exposed. A streaming request holds the id-bearing `response.created` event until the write succeeds. If the write fails, the router fails closed: unary returns `503`; a stream terminates before the first byte. |
| `POST /v1/responses` with `previous_response_id` | Owner lookup, then the request is pinned to the recorded account. Authentication, authorization, admission, quota and capability checks still apply. A pinned account that is over its limits returns its own rate-limit response instead of moving the conversation. |
| `GET /v1/responses/{id}` | Forwards to the pinned account, updates the local lifecycle state from the provider status, and returns the response. `stream=true` is forwarded and its request row is recorded; if the stream fails before the first byte the caller gets a JSON error instead of an empty 200. Providers currently allow `stream=true` retrieval only for responses created with `background: true`, which this release does not create. |
| `GET /v1/responses/{id}/input_items` | Forwards to the pinned account. `after`/`before` pagination cursors are decoded from router ids to provider ids before forwarding. |
| `DELETE /v1/responses/{id}` | Forwards the deletion, records the local tombstone and the upstream delete state (`confirmed`, or `not_found` when the provider already lost it), and never claims deletion when the provider call failed. |
| `POST /v1/responses/{id}/cancel` | Forwards the cancellation and records `cancelled` when the provider accepts it. A provider rejection keeps its client-error class and changes no state. |

GET, input-items, delete and cancel are recorded as request-usage rows with
their bounded endpoint label (`responses.get`, `responses.input_items`,
`responses.delete`, `responses.cancel`) and as request trace events
(`stored_response_get`, `stored_response_input_items`, `stored_response_delete`,
`stored_response_cancel`); no tool or model cost is recorded for them. With
affinity disabled the routes return the pre-v4.7 `501 unsupported_endpoint` and
keep their `unsupported` usage label.

Missing, expired, deleted and other-caller ids all return the same
`404 response_not_found` before any upstream call, so an id cannot be used to
probe another caller's stored responses. A target or model group that disappeared
or was re-keyed returns `409 response_target_unavailable`; there is no silent
re-route, because the state lives at the provider.

Ownership is the exact caller token. Another token for the same user or project
does not inherit access, and a deleted, disabled or rotated token loses access to
its old records. Authorization is re-checked on every stored-response operation:
a token that is no longer allowed to use the recorded model group gets the same
404 as an unknown id, without an upstream call. There is no tenant-admin transfer
path in this release.

When affinity is configured for any model group, a `previous_response_id` aimed
at a group that does not enable it is refused with
`400 stored_response_unsupported` before dispatch, rather than forwarding the
provider id on that group's account. Stored creates (requests that would store)
and every `previous_response_id` continuation are never shadow replayed, because
a shadow target is a different account; a `store:false` request keeps its
previous shadow behavior.

## Retention, expiry and legal holds

The local TTL (`retention_days`, 30 by default) sets `expires_at` when the record
is written. A row stops being usable as soon as its expiry passes; lookups then
return the same `404` as an unknown id.

Physical removal is a retention job, not the expiry check. With retention on, the
`responses_affinity` class purges rows past its cutoff in bounded batches and
supports dry-run, and legal holds on that class block deletion by their time
range. Affinity rows are owned by a caller token rather than a request id, so
request-scoped hold matching does not apply to this table. Releasing a hold makes
the row eligible on the next run.

Neither the TTL nor the purge claims that the upstream provider deleted its copy.
DELETE is the only operation that asks the provider to delete, and its result is
recorded separately. The default 30 days is a router policy choice, not a
provider guarantee.

## Identifiers

With `identifiers.mode: passthrough` the client sees provider ids. With
`identifiers.mode: rewrite` the stored-response surfaces return router ids
(`mr_...`) and translate them back to the provider id before forwarding, for
response ids, output item ids, call ids and input-item pagination ids. For a
group with affinity enabled, the unary Responses payload also encodes those ids,
matching the streamed surface, so a stored id can be passed back unchanged. A
stock group with affinity off keeps its existing caller-visible ids and bytes.
The lookup hash is computed over the provider id, so the same record resolves
whether a client presents the raw id or the router-encoded form.

## Deployment modes and router chains

- A durable usage database (SQLite or PostgreSQL) is required. Without one,
  startup fails when a group is enabled; with no group enabled nothing changes.
- Router chains (`provider.upstream_kind: router`) work hop by hop: a parent
  records its child router as the target that holds the response, and the child
  applies its own affinity policy.
- Stored Responses affinity is native-Responses only. Chat and Anthropic
  bridges are not advertised as stored-response support, and the chat-to-
  Responses session store remains a separate mechanism.

## Verifying a deployment

The compatibility suites for the stored-response surface are covered by focused
tests:

```sh
go test ./internal/router -run 'TestResponsesAffinity|TestStoredResponse'
```

For a bounded live check, run the one-command smoke. It builds the router, uses
a disposable SQLite database and a locally generated caller token and affinity
key, and exercises create, get, input items, a `previous_response_id`
continuation, a streamed create, a streamed get, cancel, delete and the
after-delete 404 with `max_output_tokens: 256`:

```sh
OPENAI_API_KEY=... MODEL=gpt-6-luna bash scripts/smoke_stored_responses_affinity.sh
```

It prints statuses and the redacted local `response_affinity` states, writes the
raw responses and rows under a temporary working directory, and stops the router
on exit. `GET ?stream=true` will return the provider's 400 because this release
does not create background responses; that is a stated limit, not a routing
failure.

## Limits

- Background responses are rejected in this release, so `GET ...?stream=true`
  reaches the provider only to be refused for non-background responses, and
  background usage settlement is not exercised.
- Token rotation does not transfer ownership; plan for one lost continuation per
  conversation after a rotation.
- Tenant-admin access and token-transfer tooling are not implemented.
- Expiry and purge remove router metadata only; provider-side retention is
  independent.
