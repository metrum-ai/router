<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Counting tokens without inference billing

Metrum AI Router accepts Anthropic-compatible count requests at
`/v1/messages/count_tokens` and `/anthropic/v1/messages/count_tokens`.

For a native Anthropic HTTP target, the router forwards the request to the
provider's `/v1/messages/count_tokens` endpoint. It uses the configured provider
credentials, headers, timeout, response-size limit and allowed beta policy.
The public model alias is replaced with the selected upstream model, and router
identifier envelopes are decoded exactly as for inference. All other request
bytes—including cache controls, image blocks, tool schemas and provider
extensions—are preserved. The provider's response body and status are returned
unchanged; `Retry-After` is preserved on upstream errors. A successful native
response has `X-Metrum-Usage-Source: reported`.

For other transports, including Bedrock (whose count API is not the Anthropic
HTTP count API), the router returns a heuristic estimate with
`X-Metrum-Usage-Source: estimated`. This fallback is not tokenizer-exact; callers
should not mistake it for an upstream count. Provider errors on a native count
are returned, not silently replaced with an estimate.

Count requests authenticate and check model access, and are concurrency-bounded.
They do not reserve inference tokens, call token settlement, or consume the
request/token ledger. Inference-only context, translation, and output-quality
eligibility checks do not prevent a compaction count. Disabled/exhausted keys
remain subject to the concurrency admission's existing key-state enforcement.

## Accounting and request visibility

Every new count event is stored with `request_kind=count_tokens`,
`usage_source=none`, zero tokens and zero billed/list cost. `none` describes the
absence of **billable inference usage**; the response header separately tells
the client whether the returned count is reported or estimated.

Count events appear in the admin request list and request detail, including
failed upstream calls. They are excluded from inference overview, cost, savings,
TPS, CLI usage aggregates, rollups and Prometheus inference counters. They also
never train dynamic inference observations or external-policy inference feedback. Admin
request rows expose `requestKind` and the web request table displays it.

Migration 2026102302 adds the non-null `request_kind` column. The historical
empty string remains an ordinary/unknown inference record for compatibility:
old count calls cannot reliably be identified from stored fields, so migration
does not retroactively rewrite or remove historical charges. Rollback requires
restoring the pre-migration database snapshot, as for other usage expansions.

## Local verification

The ordinary `TestCountTokens*` tests start a localhost router and deterministic
Anthropic upstream, compare exact payloads/counts, and verify that both success
and error events leave inference accounting unchanged.

With Claude Code installed, an additional credential-free client check is:

```sh
go test -tags count_tokens_claude_e2e ./internal/router \
  -run '^TestCountTokensClaudeCodeCompactionE2E$' -count=1 -v
```

This uses a synthetic upstream (no provider spend), real Claude Code `Write` tool
execution and tool-result continuation, then `/compact` on the persisted
session. A separate native count request precedes the session; the final ledger
must equal only the mock's recorded inference usage, excluding that count.
The mock reports a large initial input usage to exercise the client's context
handling; this is not a live-provider tokenizer accuracy test. Ports 18447 and
18448 must be available; the ordinary native count smoke uses 18445 and 18446.
