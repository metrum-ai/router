---
title: Reasoning provenance
doc_type: howto
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Reasoning provenance

OpenAI Responses returns provider-owned `encrypted_content` for reasoning items,
and Anthropic returns signed `thinking` blocks and opaque `redacted_thinking`
data. Those values are only meaningful to the provider account and model that
minted them. When a request falls back to a different account or provider, a
foreign value is rejected upstream or, worse, crosses a trust boundary.

Reasoning provenance binds each opaque value to its origin so the router can
unwrap it for the right target and drop it everywhere else. The feature is
**off by default**: with `mode: off` the router changes no bytes and behaves as
it did before.

## Configuration

```yaml
server:
  reasoning_provenance:
    mode: enforce            # off | observe | enforce
    key_env: ROUTER_REASONING_PROVENANCE_KEY
    key_id: k2026-11         # log-safe identifier for this key generation
    ttl: 24h                 # optional, default 24h, maximum 30d
```

Provide the secret through the named environment variable:

```sh
export ROUTER_REASONING_PROVENANCE_KEY="$(openssl rand -base64 48)"
```

- The secret must be at least 32 bytes. It is read once at startup, never written
  to configuration or state, and never appears in logs, traces, usage rows or
  error bodies. A startup failure only says that the key is missing or too short.
- `observe` and `enforce` both require `key_env`. Defining `key_env`, `key_id` or
  `ttl` while the mode is `off` is rejected, so a dormant configuration cannot
  look active.
- `key_id` must match `^[a-z0-9][a-z0-9._-]{0,31}$`. Use it to rotate: values
  bound under an older `key_id` are treated as unknown origin, so rotate during a
  window where losing one continuation is acceptable.

## Modes

| Mode | Wraps new values | Unknown or mismatched history |
|---|---|---|
| `off` | no | forwarded exactly as before |
| `observe` | yes | forwarded, traced as `would-drop` classes |
| `enforce` | yes | dropped, traced |

Start in `observe` to measure how much traffic depends on foreign history, then
move to `enforce`.

## What is bound and where it lives

Each opaque value is replaced by a router envelope in the same field the client
already echoes:

```
rtr_rz1.<key_id>.<payload>.<mac>
```

The payload records the binding scope — dialect, provider name,
`provider.key_id`, model and model group — plus an expiry, and carries the
provider value. The MAC is HMAC-SHA256 over the envelope under a
domain-separated subkey. Envelopes are self-contained, so no table, migration or
shared store is required and replicas agree without coordination.

Supporting values are sized for real provider payloads: one bound value may be up
to 256 KiB, the encoded payload up to 384 KiB and the whole envelope up to
512 KiB.

Clients echo history verbatim and are unaffected. Clients that inspect or
rewrite `encrypted_content`, `signature` or `redacted_thinking.data` themselves
are unsupported when provenance is enabled.

## Behaviour on mismatch

| Situation | `observe` | `enforce` |
|---|---|---|
| Scope matches the selected target | unwrapped and forwarded | unwrapped and forwarded |
| Different provider, account, model or group | forwarded, traced `scope-mismatch` | block or item dropped, traced |
| Expired envelope | forwarded, traced `expired` | dropped, traced `expired` |
| MAC, format or key id invalid | forwarded, traced `invalid-envelope` | dropped, traced |
| Value the router did not mint | forwarded, traced `unknown-origin` | dropped, traced `unknown-origin` |
| Value above the size limit | forwarded, traced `oversized` | dropped, traced `oversized` |

Only the reasoning item or the thinking block is removed. Tool calls, tool
results, text and user content are never modified: rewriting is restricted to
protocol containers, so a tool result that happens to contain the word
`signature` is left alone.

Values the router cannot bind are never emitted unbound in `enforce` mode, in
either direction.

## Diagnostics

Each request records at most one bounded trace per direction
(`reasoning_provenance_inbound`, `reasoning_provenance_outbound`) carrying class
names and counts only:

```
scope-mismatch=1 unwrapped=2
oversized=1 wrapped=3
```

At most eight distinct classes are recorded per request; further classes collapse
into `other`. No opaque value, signature, ciphertext, key material or credential
is logged, and none appears in usage rows, reports or error bodies.

## Interaction with stored responses

Stored Responses ownership and affinity decide *who* may fetch a stored
response. Provenance decides *whether* opaque history may be forwarded to a
given upstream. Both apply: a stored response is released only to its owner, and
its opaque items are forwarded only to the scope that minted them.

## Limits

- Binding proves router-observed origin, not provider-side validity. A matching
  scope can still be rejected upstream, and that stays a caller-visible error.
- Account identity is only as precise as `provider.key_id`. Two providers that
  point at the same account must share it, or values will be dropped as
  `scope-mismatch`.
- Enabling `enforce` drops history minted before wrapping and history the router
  did not mint: expect one lost continuation per conversation at cutover. Use
  `observe` first.
- Anthropic `thinking` text is not bound; the block is dropped as a unit with its
  signature.
- The TTL bounds how long a binding is accepted. It is not a provider validity
  guarantee.
