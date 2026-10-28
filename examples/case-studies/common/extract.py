#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Join completed sanitized trial outcomes to request-time router SQLite usage."""
import argparse
from decimal import Decimal
import json
from pathlib import Path
import sqlite3

from analyze import summarize


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--workload', required=True, choices=('support-agent', 'document-qa', 'coding-agent'))
    p.add_argument('--results', required=True, type=Path)
    p.add_argument('--database', required=True, type=Path)
    p.add_argument('--out-dir', required=True, type=Path)
    a = p.parse_args()
    rows = [json.loads(line) for line in a.results.read_text().splitlines()]
    expected = {'support-agent': {'ORD-101', 'ORD-102', 'ORD-103'},
                'document-qa': {'leave', 'refund', 'security', 'shipping', 'invoice'},
                'coding-agent': {'calculator-1', 'calculator-2', 'calculator-3'}}[a.workload]
    if len(rows) != 2 * len(expected) or any(r.get('error') for r in rows):
        raise SystemExit('incomplete or errored run; reconcile manually, do not publish as complete')
    db = sqlite3.connect('file:' + str(a.database.resolve()) + '?mode=ro', uri=True)
    db.row_factory = sqlite3.Row
    arms = {}; raw_results = []; counter = 0; seen_ids = set()
    for arm in ('frontier', 'routed'):
        chosen = [r for r in rows if r.get('arm') == arm]
        if {r['trial_id'] for r in chosen} != expected or len(chosen) != len(expected): raise SystemExit('trial plan mismatch')
        tasks, usage = [], []
        for trial in chosen:
            tasks.append({'trial_id': trial['trial_id'], 'passed': trial['passed']})
            safe = {k: v for k, v in trial.items() if k not in ('requests', 'request_id')}
            reqs = trial.get('requests') or [{'request_id': trial['request_id'], 'status': trial['status'], 'latency_ms': trial['latency_ms']}]
            safe['request_ids'] = []
            for req in reqs:
                if not req.get('request_id') or req['request_id'] in seen_ids:
                    raise SystemExit('missing or duplicate request ID')
                seen_ids.add(req['request_id'])
                found = db.execute('select * from request_usage where request_id = ?', (req['request_id'],)).fetchall()
                if len(found) != 1: raise SystemExit('missing or ambiguous usage row')
                r = dict(found[0])
                if r['requested_model'] != arm or r['status'] != 200 or req['status'] != 200 or r['attempts'] != 1 or r['fallback_used']:
                    raise SystemExit('failed request, retry or fallback: manual cost reconciliation required')
                if r['usage_source'] != 'reported':
                    raise SystemExit('usage must be reported by the provider, not estimated')
                if r['input_tokens'] < 0 or r['output_tokens'] < 0 or r['cached_input_tokens'] < 0 or r['cached_input_tokens'] > r['input_tokens']:
                    raise SystemExit('invalid token counts')
                counter += 1; pseudonym = f'request-{counter:04d}'; safe['request_ids'].append(pseudonym)
                item = {'trial_id': trial['trial_id'], 'request_id': pseudonym, 'model': r['target_model'],
                        'status': r['status'], 'latency_ms': req['latency_ms'],
                        'router_cost_usd': str(r['total_cost_usd']), 'price_source': r['pricing_source'], 'price_date': r['pricing_updated_at'],
                        'input_tokens': r['input_tokens'], 'cached_input_tokens': r['cached_input_tokens'], 'output_tokens': r['output_tokens'],
                        'input_price_per_million_usd': str(r['input_price_per_million_usd']),
                        'cached_input_price_per_million_usd': str(r['cached_input_price_per_million_usd']),
                        'output_price_per_million_usd': str(r['output_price_per_million_usd']),
                        'cost_list_usd': str(r['cost_list_usd']), 'cost_billed_usd': str(r['cost_billed_usd']), 'usage_source': r['usage_source']}
                values = [Decimal(item[k]) for k in ('router_cost_usd', 'input_price_per_million_usd', 'cached_input_price_per_million_usd', 'output_price_per_million_usd')]
                if any(not x.is_finite() or x < 0 for x in values): raise SystemExit('invalid cost')
                expected_cost = ((r['input_tokens']-r['cached_input_tokens'])*values[1] + r['cached_input_tokens']*values[2] + r['output_tokens']*values[3])/Decimal(1000000)
                if abs(values[0]-expected_cost) > Decimal('0.00000001'): raise SystemExit('usage cost does not match priced tokens')
                usage.append(item)
            raw_results.append(safe)
        arms[arm] = {'tasks': tasks, 'usage': usage, 'billing': None}
    document = {'workload': a.workload, 'verifier': a.workload + '/independent-verifier', 'planned_trials': sorted(expected),
                'frontier_model': 'gpt-6.1-sol', 'arms': arms}
    summary = summarize(document)
    a.out_dir.mkdir(parents=True, exist_ok=False)
    for name, data in [('results.json', raw_results), ('usage.json', document), ('summary.json', summary)]:
        (a.out_dir / name).write_text(json.dumps(data, indent=2, sort_keys=True)+'\n')
    print('Published sanitized usage-derived summary; provider invoices are not per-run.')


if __name__ == '__main__': main()
