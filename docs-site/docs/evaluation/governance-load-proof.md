---
title: Governance under load proof
doc_type: explanation
---

<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Governance under load: bounded proof

These local synthetic results address part of [#431](https://github.com/metrum-ai/router/issues/431); they are not a production-scale guarantee. The source reports describe hardware, Go and router versions and exact fixtures.

| Controlled scenario | Observed result | Evidence |
|---|---|---|
| HTTP token reservation, separate daily/monthly/lifetime/TPM limits | At 32 workers: 8 admitted, 24 rejected, 160 settled against 104 threshold; at 128: 32 admitted, 96 rejected, 640 settled against 416 threshold. The respective 56 and 224 token excesses result from a 13-token reservation and 20-token reported usage. | [HTTP report](https://github.com/metrum-ai/router/blob/main/docs/GOVERNANCE_HTTP_LOAD.md); `router / v45-proof / 431`, snapshot `0d0c6758` |
| HTTP RPM and concurrency, separately | At 8/32/128 workers: 2/8/32 admitted and 6/24/96 rejected; concurrency slots were released. | [HTTP report](https://github.com/metrum-ai/router/blob/main/docs/GOVERNANCE_HTTP_LOAD.md); snapshot `551d468e` |
| Synthetic email redaction and pre-commit failover | 128 primary and 128 fallback attempts contained the placeholder, not the raw synthetic address. | [PII report](https://github.com/metrum-ai/router/blob/main/docs/GOVERNANCE_PII_LOAD.md); snapshot `e9f5304a` |
| Two-key SQLite and PostgreSQL audit/cut-stream fixture | Each key had 32 unique request and security-export rows; 24 completed rows matched mock usage and configured price; eight cuts per key were observed as status 499 with estimated usage. | [Audit report](https://github.com/metrum-ai/router/blob/main/docs/GOVERNANCE_AUDIT_LOAD.md); snapshot `11c32699` |

All snapshots are tagged `router / v45-proof / 431`. These tests used no hosted calls or paid services; actual hosted cost was $0. The configured mock price in the audit fixture is synthetic arithmetic, not a provider charge.

## Rerun

With Go and a C compiler installed, from a checkout run:

```sh
make governance-load
```

This runs the local race-enabled suite without provider credentials or paid services. Go module downloads may require network access. PostgreSQL is opt-in and requires a disposable local database; see the [audit report](https://github.com/metrum-ai/router/blob/main/docs/GOVERNANCE_AUDIT_LOAD.md) for its separate command. Without its DSN it is skipped.

## Limits

The excess above the token threshold is a measured outcome of a controlled reservation mismatch, **not a universal overspend bound**. These fixtures are single-process, finite and synthetic; they do not establish distributed persistence, multi-process crash/restart, sustained-rate accuracy, dollar-budget behavior, all authentication configurations, or general PII protection on every retry and stream. The cut-stream test does not reconcile token or cost amounts. PostgreSQL is not covered by the credential-free default run. Release-wide zero-high-severity security status is tracked separately in [#510](https://github.com/metrum-ai/router/issues/510); remaining load acceptance is tracked in [#573](https://github.com/metrum-ai/router/issues/573).
