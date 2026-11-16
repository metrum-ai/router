<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Admin audit exports

`GET /admin/v1/audit-events/export` exports audit rows from the control-plane, usage, and credential-event tables. Rows retain their full redacted audit fields, including issuer, ticket, configuration-set identifiers, request hashes, group metadata, and detail markers. Credential-event rows have source `credential` and include the event count and salted client-IP digest. Successful authentication outcomes are grouped by credential, outcome, source digest, and five-minute UTC window, then appended as one immutable count-delta row when that window closes. Its request ID is empty because the row summarizes multiple requests. Other credential outcomes remain individual rows; a previously hashed row is never edited.

Each audit row stores `previous_hash` and `row_hash` in the same transaction as the event. The transaction also updates a separate per-source, per-domain chain anchor with the first retained predecessor hash and current head hash. SQLite appends are serialized by a process mutex keyed to the database handle and by the SQLite write transaction; PostgreSQL uses a transaction-scoped advisory lock. The offline verifier checks those persisted anchors as well as every row. The canonical row is the JSON encoding of all exported event fields in the order defined by the router's audit export schema, excluding `previous_hash` and `row_hash`. Hashes are lowercase hexadecimal SHA-256:

```text
row_hash = hex(SHA256(previous_hash + "\n" + canonical_json_row))
```

The first row for each source and domain starts at its stored anchor. Every later row links to the prior stored hash. The export carries per-source chain starts and heads, so the verifier detects edited fields, missing rows, inserted rows, and reordering. Retention keeps the newest expired row in each source and domain as a checkpoint and advances the stored start anchor to its predecessor; newer retained rows continue to link through that checkpoint. The checkpoint is stored in `router_admin_audit_chain_anchors` in the same database and is not externally signed in v5.0.0. Offline verification therefore trusts the exported checkpoint; deployments needing proof against a database administrator who can rewrite both events and anchors must preserve an external copy of the export/checkpoint.

Save an export and verify it offline:

```sh
metrum-ai-routerctl audit verify audit-export-page-1.json audit-export-page-2.json
```

Verification checks each stored row hash, links adjacent rows in each source across every supplied page, checks page checksums, and checks the export against its chain-start and chain-head anchors. Supply pages in cursor order; verification rejects an incomplete sequence.

Manual usage-audit deletion is limited to the oldest retained event in the caller's domain. It advances that domain's stored start anchor to the deleted row hash and writes a separate deletion marker in the global domain. Markers are excluded from manual deletion and retention pruning, including for callers authorized in the global domain.

The real-upstream Admin API end-to-end suite, including audit export and retention cases, is documented at [`e2e/admin-api/README.md`](../e2e/admin-api/README.md). Run it with `python3 e2e/admin-api/run_e2e.py` after setting the four provider key environment variables listed there.
