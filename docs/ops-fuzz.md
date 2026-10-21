<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Operational cost accounting and fuzz checks

## Router response cache billing

Metrum AI Router records `request_usage.cost_list_usd` (the estimated price
of the returned usage) and `cost_billed_usd` (the actual estimated charge).
Router response-cache hits bill at zero. Provider prompt-cache discounts are
separate: those requests still bill using the configured cached-input price.
The legacy `total_cost_usd` column remains an alias for billed cost, so existing
reports, exports and cost sorting use billed cost. Token counts on cache hits
remain informational and are not erased. Input/image/output component costs
also retain their estimated breakdown; on a free hit their sum describes list
cost, not billed cost. Use `cost_billed_usd` (or the legacy total alias) for spend.

Migration 2026102101 adds the two non-null columns and backfills existing rows:
list cost is the old total; billed cost is zero for `cache = 'hit'`, otherwise
the old total. Unknown cache states retain their prior charge. Old zero prices
cannot be reconstructed; the migration does not invent prices. Back up the DB
before migration: the historical total column changes and rollback requires a
restore. Historical retained rollups created before this change retain their
original accounting until regenerated from retained request rows.

The admin overview summary exposes `cost_list_usd`, `cost_billed_usd`, and
`cache_savings_usd` (list minus billed). The UI labels these List cost, Billed
cost and Cache savings. Existing baseline savings reports remain baseline minus
billed cost: baseline routing savings and response-cache savings are distinct.

## Fuzz checks

The nightly workflow runs on self-hosted `router` runners and uploads corpus
files on failure. Run locally:

```sh
go test ./internal/stream -run '^$' -fuzz '^FuzzSSEFrame$' -fuzztime=60s -parallel=2
go test ./internal/router -run '^$' -fuzz '^FuzzIRRoundTrip$' -fuzztime=60s -parallel=2
go test ./internal/router -run '^$' -fuzz '^FuzzConfigLoader$' -fuzztime=60s -parallel=2
```

Seeds are under each package's `testdata/fuzz`. Their first line is Go's required
`go test fuzz v1` marker; copyright and SPDX notices are in the seed payload.
The SSE target checks parser progress and accepted-event serialization identity.
The config target invokes the real loader (including defaults and validation)
with bounded arbitrary YAML in an isolated temporary directory.

### IR identity limitation reported (OPS-11)

The translation IR is **not** an identity representation of arbitrary Chat JSON.
For example `{"model":"test","messages":[{"role":"developer","content":"x"}],"stream":true}`
round-trips through `decodeRequest` and `encodeUpstream` to a system message and
`stream:false`. Tool call identifiers and arbitrary extension fields are also
not represented by the text translation encoder. This is reported rather than
silently claiming full identity or modifying the parallel CHAT-10 workstream.
`FuzzIRRoundTrip` currently proves identity for canonical unary user-text Chat
requests, including arbitrary Unicode and malformed UTF-8 normalization at the
JSON boundary. Expanding identity coverage to all Chat fields requires the
same-dialect passthrough workstream; this scope limitation remains open.
