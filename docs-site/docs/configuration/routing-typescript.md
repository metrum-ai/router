---
title: TypeScript Routing Migration
doc_type: reference
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# TypeScript Routing Migration

The in-process TypeScript routing strategy was removed in v5.0.0. Router
configuration using `strategy: script`, `script`, `script_max_concurrent`, or
`script_http` now fails during configuration loading with a migration message.

Move custom routing logic to a standalone policy service and configure the
model group with `strategy: external` and `external_policy`. The router sends
safe derived request context by default. Set `external_policy.include_request:
true` only when a trusted policy service is authorized to receive request
content. The policy service runs outside the router process.

See [External Routing Policy Service](./external-routing-policy) for the
request and response contract and the
`examples/typescript-request-shape/` and `examples/typescript-pii-policy/`
contain the ported runnable examples.
For redaction before upstream calls, configure [PII filtering](./pii-filtering).
