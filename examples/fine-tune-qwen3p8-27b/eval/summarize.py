#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Reduce PRIVATE promptfoo output to scalar bootstrap/paired/gate evidence."""
import argparse
import hashlib
import json
import math
import re
import sqlite3
from collections import Counter, defaultdict
from pathlib import Path
from calibrate import bootstrap
from scorers.tools import score as frozen_tool_score


def summarize(document):
    groups = defaultdict(dict)
    joins = []
    skipped_objective_callbacks = 0
    for result in document['results']['results']:
        metrics = {}
        for component in (result.get('gradingResult') or {}).get('componentResults', []):
            kind = component.get('assertion', {}).get('type')
            if kind in ('is-json','contains-json'):
                metrics['json_validity' if kind == 'is-json' else 'json_schema'] = int(component.get('pass', False))
            try:
                value = json.loads(component.get('reason', ''))
                if isinstance(value, dict) and 'exact' in value:
                    metrics.update(value)
            except (ValueError, TypeError):
                pass
        variables = result.get('vars') or {}
        if variables.get('expected') and 'declared' in variables:
            if not isinstance(variables['expected'],list) or not isinstance(variables['declared'],list):
                raise ValueError('objective scorer variables must remain atomic arrays')
            # Keep original SDK scores: exported reference values can be redacted.
            # Only absent outputs need reconciliation, and the frozen scorer
            # unconditionally assigns those zero, independent of reference values.
            if 'exact' not in metrics:
                if (result.get('response') or {}).get('output') is not None:
                    raise ValueError('nonempty objective response missing frozen SDK callback')
                metrics.update(frozen_tool_score(None,variables['expected'],variables['declared']))
                metrics.update(json_validity=0,json_schema=0)
                skipped_objective_callbacks += 1
        record = {'passed': int(result.get('success', False)),
                  'error': int(bool((result.get('response') or {}).get('error'))),
                  'latency_ms': result.get('latencyMs', 0), 'cost_usd': result.get('cost', 0) or 0,
                  **metrics}
        key = result['testIdx']
        provider = result['provider']['id']
        if key in groups[provider]:
            raise ValueError('duplicate provider/case: split repeat outputs before summarizing')
        groups[provider][key] = record
        metadata = (result.get('response') or {}).get('metadata', result.get('metadata') or {})
        headers = metadata.get('http', {}).get('headers', {})
        request_id = next((v for k,v in headers.items() if k.lower() == 'x-request-id'), None)
        if isinstance(request_id,str) and re.fullmatch(r'req_[A-Za-z0-9_-]{1,100}',request_id):
            joins.append({'provider':provider,'case_index':key,'router_request_id':request_id})
    summary = {'copyright': 'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0', 'providers': {}, 'paired': {}, 'router_joins': joins}
    summary['objective_audit'] = {'method':'Retain original SDK scores (exported references may be redacted); apply unchanged frozen tools.score only to absent outputs whose SDK callback was skipped; zero, no new inference',
        'sdk_skipped_callbacks':skipped_objective_callbacks,
        'scorer_sha256':hashlib.sha256(Path(__file__).with_name('scorers').joinpath('tools.py').read_bytes()).hexdigest()}
    for provider, cases in groups.items():
        result = {'count': len(cases), 'metrics': {}, 'total_cost_usd': sum(r['cost_usd'] for r in cases.values())}
        latency = sorted(r['latency_ms'] for r in cases.values())
        result['p95_latency_ms'] = latency[max(0, math.ceil(len(latency)*.95)-1)]
        keys = set().union(*(r.keys() for r in cases.values())) - {'cost_usd'}
        for metric in sorted(keys):
            values = [r[metric] for r in cases.values() if metric in r]
            result['metrics'][metric] = {'n': len(values), 'mean': sum(values)/len(values), 'ci95': bootstrap(values)}
        summary['providers'][provider] = result
    names = sorted(groups)
    for index, left in enumerate(names):
        for right in names[index+1:]:
            pairs = sorted(set(groups[left]) & set(groups[right]))
            if not pairs:
                continue
            result = {}
            keys = set.union(*(set(groups[left][k]) & set(groups[right][k]) for k in pairs)) - {'cost_usd'}
            for metric in sorted(keys):
                values = [groups[right][k][metric]-groups[left][k][metric] for k in pairs
                          if metric in groups[right][k] and metric in groups[left][k]]
                result[metric] = {'n': len(values), 'right_minus_left': sum(values)/len(values), 'ci95': bootstrap(values)}
            summary['paired'][left + ' -> ' + right] = result
    return summary, groups


def aggregate_joins(joins):
    ids = [j['router_request_id'] for j in joins]
    if len(set(ids)) != len(ids):
        raise ValueError('duplicate joined router request IDs')
    groups = defaultdict(list)
    for j in joins:groups[j['provider']].append(j['telemetry'])
    result = {'complete_responses':len(joins),'unique_request_ids':len(set(ids)),
        'joined_records_sha256':hashlib.sha256(json.dumps(joins,sort_keys=True,separators=(',',':')).encode()).hexdigest(),
        'by_eval_provider':{}}
    for name,rows in groups.items():
        targets = Counter(re.sub(r'accounts/(?!fireworks/)[^/]+/','accounts/OPERATOR/',
            str(r['target_provider'] or 'unselected')+'/'+str(r['target_model'] or 'unselected')) for r in rows)
        result['by_eval_provider'][name] = {'responses':len(rows),'observed_targets':dict(targets),
            'status_counts':dict(Counter(str(r['status']) for r in rows)),
            'input_tokens':sum(r['input_tokens'] or 0 for r in rows),
            'output_tokens':sum(r['output_tokens'] or 0 for r in rows)}
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', required=True, type=Path)
    p.add_argument('--out', required=True, type=Path)
    p.add_argument('--usage-db', type=Path)
    p.add_argument('--journal', type=Path, help='run_eval safe request-ID journal; no headers/secrets')
    p.add_argument('--case-prefix', choices=['ifeval','mmlu-pro'])
    p.add_argument('--aggregate-joins',action='store_true',help='public scalar join proof; keep full per-request rows private')
    a = p.parse_args()
    document = json.loads(a.input.read_text())
    if a.case_prefix:
        document['results']['results'] = [r for r in document['results']['results']
            if r.get('testCase', {}).get('description', '').startswith(a.case_prefix + '-')]
    result, groups = summarize(document)
    if a.journal:
        joins = [json.loads(line) for line in a.journal.read_text().splitlines()]
        seen = set()
        filtered = []
        for join in joins:
            key = (join['provider'],join['case_index'])
            if a.case_prefix and (join['provider'] not in groups or join['case_index'] not in groups[join['provider']]):
                continue
            if key in seen or join['provider'] not in groups or join['case_index'] not in groups[join['provider']]:
                p.error('duplicate or unknown telemetry journal case')
            if not re.fullmatch(r'req_[A-Za-z0-9_-]{1,100}', join['router_request_id']):
                p.error('invalid router request ID')
            seen.add(key)
            filtered.append(join)
        result['router_joins'] = [{k:join[k] for k in ('provider','case_index','router_request_id')} for join in filtered]
    if a.usage_db:
        if len(result['router_joins']) != sum(len(v) for v in groups.values()):
            p.error('missing router request IDs; cannot claim complete telemetry join')
        connection = sqlite3.connect(a.usage_db.resolve().as_uri()+'?mode=ro',uri=True)
        connection.row_factory = sqlite3.Row
        for join in result['router_joins']:
            row = connection.execute('SELECT request_id,target_provider,target_model,status,latency_ms,ttfb_ms,input_tokens,output_tokens,total_cost_usd FROM request_usage WHERE request_id=?',
                (join['router_request_id'],)).fetchone()
            if row is None:
                p.error('router request ID absent from usage DB')
            join['telemetry'] = dict(row)
        connection.close()
        for provider, summary in result['providers'].items():
            summary['total_cost_usd'] = sum(float(j['telemetry']['total_cost_usd'] or 0)
                for j in result['router_joins'] if j['provider']==provider)
    result['cost_scope'] = 'Model-request token estimates only; excludes grader calls and hourly GPU/deployment charges. Zero token price is not free serving.'
    if a.aggregate_joins:
        if not a.usage_db or not a.journal:p.error('aggregate proof requires complete journal and DB join')
        result['router_join_audit'] = aggregate_joins(result.pop('router_joins'))
        result['router_join_audit']['journal_sha256'] = hashlib.sha256(a.journal.read_bytes()).hexdigest()
        result['router_join_audit']['privacy'] = 'Full per-request rows private; provider account IDs redacted. Hashes commit to original joined rows and journal.'
    a.out.write_text(json.dumps(result, indent=2))

if __name__ == '__main__':
    main()
