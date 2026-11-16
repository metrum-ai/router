<!-- Copyright 2026 Metrum AI -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

# Admin audit exports

`GET /admin/v1/audit-events/export` exports audit rows from the control-plane, usage, and credential-event tables. Rows retain their full redacted audit fields, including issuer, ticket, configuration-set identifiers, request hashes, and detail markers. Credential-event rows have source `credential` and include their collapsed count and salted client-IP digest.

Each audit row stores `previous_hash` and `row_hash` in the same transaction as the event. The transaction also updates a separate per-source, per-domain chain anchor with the first retained predecessor hash and current head hash. The offline verifier checks those persisted anchors as well as every row. The canonical row is the JSON encoding of the exported fields in the order defined by the router's audit export schema. Hashes are lowercase hexadecimal SHA-256:

```text
row_hash = hex(SHA256(previous_hash + "\n" + canonical_json_row))
```

The first row for each source and domain starts at its stored anchor. Every later row links to the prior stored hash. The export carries per-source chain starts and heads, so the verifier detects edited fields, missing rows, inserted rows, and reordering. Retention keeps the newest expired row in each source and domain as a checkpoint and advances the stored start anchor to its predecessor; newer retained rows continue to link through that checkpoint.

Save an export and verify it offline:

```sh
metrum-ai-routerctl audit verify audit-export.json
```

Verification checks each stored row hash, links adjacent rows in each source, and checks the export against its chain-start and chain-head anchors. A paginated export reports whether more rows remain; verify the final page to check its chain heads.

Manual usage-audit deletion is limited to the oldest retained event in the caller's domain. It advances that domain's stored start anchor to the deleted row hash and writes a separate deletion marker in the global domain. This preserves verification for the retained suffix and prevents the deleting domain's privilege from removing its own deletion record.
