<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance load: synthetic PII and fallback (#431)

Run the isolated local probe with:

```sh
go test -race ./internal/router -run '^TestGovernanceLoadPIIFallback$' -count=3 -v
```

On 2026-10-28 UTC, using router production-code base
`306c38d95c7896baab36c58e94923ecd90a7770c`, Linux x86_64, 16 visible CPUs,
AMD Ryzen 7 7730U and Go 1.27.3, a localhost router handled 128 concurrent
requests (64 streaming and 64 unary) with distinct synthetic email addresses.
A scripted primary returned HTTP 503 before response commitment; a scripted
fallback succeeded. All 128 primary attempts and 128 fallback attempts received
an `[EMAIL_1]` placeholder and no raw synthetic address, and all downstream
responses retained the placeholder without an address. Three race-detector
repetitions passed. The separate `make governance-load` target also passed on
the original stacked reservation-baseline branch; that target is not required
for this standalone probe.

This single `redact_only` scenario does not cover response-generated PII,
arbitrary PII formats, interrupted streams, or failure after commitment. It
does not establish general PII, retry or streaming coverage. Provider and
caller credentials were synthetic; no hosted calls were made and cost was $0.
#431 remains open.
