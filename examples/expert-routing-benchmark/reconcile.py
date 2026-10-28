# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Join private router policy telemetry; never infer missing decisions."""
import argparse
import json
from pathlib import Path
import sqlite3
import scoring


def reconcile(records, database):
    results = []; times = []; missing = 0
    with sqlite3.connect(f'file:{database.resolve()}?mode=ro', uri=True) as db:
        for row in records:
            request_id = row.get('request_id'); expected = 'unknown' if row['domain'] == 'out-of-domain' else row['domain']
            decision = db.execute('SELECT class_label,duration_ms FROM request_policy_executions WHERE request_id=? ORDER BY seq LIMIT 1', (request_id,)).fetchone() if request_id else None
            target = db.execute('SELECT target_provider FROM request_usage WHERE request_id=?', (request_id,)).fetchone() if request_id else None
            predicted = decision[0].removeprefix('domain:') if decision and decision[0].startswith('domain:') else None
            if decision: times.append(decision[1])
            else: missing += 1
            provider = target[0] if target else None
            desired = 'coding' if expected == 'unknown' else expected
            results.append({'case_id':row['case_id'], 'expected':expected, 'predicted':predicted,
                            'provider':provider, 'correct':predicted == expected, 'route_correct':provider == desired})
    return {'attempted':len(results), 'correct':sum(r['correct'] for r in results),
            'route_correct':sum(r['route_correct'] for r in results), 'missing_decisions':missing,
            'latency_ms':{'p50':scoring.percentile(times,.5),'p99':scoring.percentile(times,.99),'count':len(times)},
            'misroutes':[r for r in results if not r['correct'] or not r['route_correct']]}


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--results',type=Path,required=True);p.add_argument('--usage-db',type=Path,required=True)
    p.add_argument('--out',type=Path,required=True);a=p.parse_args()
    result=reconcile(json.loads(a.results.read_text())['records'],a.usage_db)
    with a.out.open('x') as f:json.dump(result,f,indent=2)
    print(json.dumps({'attempted':result['attempted'],'correct':result['correct'],'missing_decisions':result['missing_decisions']}))


if __name__=='__main__':main()
