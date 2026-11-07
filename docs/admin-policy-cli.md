<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->
# Policy commands (integration work)

`metrum-ai-routerctl policies` uses the existing admin HTTP client against the dedicated versioned API. It never opens the policy/config database or falls back to a legacy route. Production policy handlers and global registration must be installed before these commands can succeed; client tests alone do not establish that integration.

Use `--router-url` or `ROUTER_URL` with HTTPS. Plain HTTP is allowed only for loopback tests. URL credentials, query strings and fragments are rejected, and redirects are never followed. Policy calls use fresh HTTP/1 connections: unlike the default Go transport, they cannot transparently replay a mutation after a lost response on a reused connection. This intentionally trades connection reuse/HTTP2 for explicit mutation-replay control; the legacy admin client's transport defaults are unchanged. Authentication uses `--admin-key-file`, then `ROUTER_ADMIN_KEY`, then `ROUTER_ADMIN_KEY_FILE`. Credential files must be regular mode-0600 files, not symlinks. There is no plaintext credential flag or key output. Other machine credential transports must use shared client support, not a separate policy TLS/token implementation.

```sh
metrum-ai-routerctl policies list --router-url https://admin.example.test --admin-key-file ./admin.key --limit 25
metrum-ai-routerctl policies get --router-url https://admin.example.test --admin-key-file ./admin.key --set-id policy-1
metrum-ai-routerctl policies draft --router-url https://admin.example.test --admin-key-file ./admin.key --file draft.json --if-match '"rev-0"' --idempotency-key create-request-1
metrum-ai-routerctl policies validate --router-url https://admin.example.test --admin-key-file ./admin.key --set-id policy-1
metrum-ai-routerctl policies diff --router-url https://admin.example.test --admin-key-file ./admin.key --set-id policy-2 --against policy-1
metrum-ai-routerctl policies activate --router-url https://admin.example.test --admin-key-file ./admin.key --set-id policy-2 --if-match '"rev-7"' --idempotency-key activate-request-1
metrum-ai-routerctl policies rollback --router-url https://admin.example.test --admin-key-file ./admin.key --set-id historical-policy --if-match '"rev-8"' --idempotency-key rollback-request-1
```

Draft/replace request JSON is passed with `--file` (or `--file -` for stdin) and is bounded to 1 MiB. Every mutation, including draft creation, requires explicit `--if-match`. Fetch a current list/get ETag first (the initialized empty store starts at `"rev-0"`). The client does not guess a revision or retry a mutation automatically. If a stable idempotency key is omitted, the shared client generates one for that request; supply the same explicit key and body when deliberately replaying a request.

Output is a JSON envelope containing `data`, `etag` and `link` where present. Preserve the quoted ETag for a later mutation. To continue a list, extract the opaque cursor from the server's signed Link and pass `--cursor`; the CLI does not follow arbitrary server URLs. The query parameter is the shared API's `limit`, not a private pagination protocol. Problem errors expose only fixed status/code information, never server detail or raw response diagnostics.

Replica-status command integration, nested-record command ergonomics, shared non-key credential transports and actual CLI-to-production-router acceptance remain work in progress. No replica-status endpoint is invented by this client.
