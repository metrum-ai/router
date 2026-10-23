#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Post-run join audit only; never changes responses, scores or denominators."""
from collections import Counter
import hashlib
import json


def known_no_dispatch(result):
    declared = (result.get('vars') or {}).get('declared')
    error = result.get('error')
    return (not result.get('response') and isinstance(declared, list)
        and all(isinstance(x, str) for x in declared)
        and len(declared) != len(set(declared))
        and isinstance(error, str) and error.startswith('Error: schema is invalid:')
        and 'properties/function/properties/name/enum must NOT have duplicate items' in error)


def reconcile(document, journal, lookup):
    """Join actual HTTP IDs; only proven duplicate-enum predispatch needs no ID.

    Missing/failed HTTP IDs are NOT fabricated or priced at zero. All original
    result rows remain in the frozen scorer denominator. `lookup` is the trusted
    operator's read-only router usage DB, not model self-reported token prices.
    """
    results = document['results']['results']
    cases = {(r['provider']['id'], r['testIdx']): r for r in results}
    if len(cases) != len(results):
        raise ValueError('duplicate result case')
    by_case = {}
    ids = set()
    for row in journal:
        key = (row['provider'], row['case_index'])
        if key not in cases or key in by_case or row['router_request_id'] in ids:
            raise ValueError('unknown/duplicate journal case or request ID')
        ids.add(row['router_request_id']); by_case[key] = row['router_request_id']
    joined, no_dispatch, unknown = [], [], []
    for key, result in cases.items():
        if key in by_case:
            record = lookup(by_case[key])
            if record is None:
                unknown.append(key); continue
            joined.append(dict(record))
        elif known_no_dispatch(result):
            no_dispatch.append(key)
        else:
            unknown.append(key)
    proof = hashlib.sha256(json.dumps(joined, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return {'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
        'complete_result_denominator':len(results), 'joined_http_requests':len(joined),
        'known_duplicate_declaration_no_dispatch':len(no_dispatch),
        'unknown_missing_dispatch_or_ids':len(unknown), 'coverage_complete':not unknown,
        'status_counts':dict(Counter(str(r['status']) for r in joined)),
        'target_counts':dict(Counter(r['target_provider'] for r in joined)),
        'joined_records_sha256':proof,
        'cost_scope':'Known no-dispatch has zero routed requests, NOT zero GPU elapsed cost. Missing genuine calls remain unknown. Zero token metadata is NOT free hourly serving. Grader and lifecycle separate.'}
