#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Build schema-v1 report and preregistered paired comparisons from observed rows."""
import json, math, sys
from collections import defaultdict
from pathlib import Path

rows = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines() if line]
expected = {'humaneval-direct':'direct','humaneval-fixed':'fixed-router','humaneval-dynamic':'dynamic-group'}
buckets = defaultdict(list)
for row in rows: buckets[row['cell_id']].append(row)
if set(buckets) != set(expected) or any(len(buckets[k]) != 10 or {r['task_id'] for r in buckets[k]} != {f'HumanEval/{i}' for i in range(10)} for k in expected):
    raise SystemExit('incomplete or mismatched paired samples')

def wilson(passes, n):
    z = 1.959963984540054
    p = passes / n
    denominator = 1 + z*z/n
    center = (p + z*z/(2*n))/denominator
    half = z*math.sqrt(p*(1-p)/n+z*z/(4*n*n))/denominator
    return [round(center-half, 8), round(center+half, 8)]

baseline = {r['task_id']: r['outcome'] == 'pass' for r in buckets['humaneval-direct']}
base_rate = sum(baseline.values()) / 10
cells = []
for cell_id, mode in expected.items():
    tasks = buckets[cell_id]
    passes = sum(r['outcome'] == 'pass' for r in tasks)
    if any(r['outcome'] not in ('pass','fail') for r in tasks):
        raise SystemExit('incomplete task result')
    paired = {'wins': sum(r['outcome'] == 'pass' and not baseline[r['task_id']] for r in tasks), 'losses': sum(r['outcome'] != 'pass' and baseline[r['task_id']] for r in tasks)}
    cells.append({'cell_id': cell_id, 'mode': mode, 'attempts': len(tasks), 'passes': passes,
                  'pass_rate_ci95': wilson(passes, len(tasks)), 'paired_baseline': paired,
                  'allowed_score_delta': 0.05, 'observed_score_delta': round(passes/10-base_rate, 8),
                  'allowed_error_rate': 0.10, 'observed_error_rate': 0,
                  'score_regression_gate_pass': passes/10 >= base_rate - 0.05,
                  'task_results': tasks,
                  'provenance': {'seed':330, 'client_versions':{'inspect-ai':'0.3.242','inspect-evals':'0.15.0','openai-python':'2.54.0','python':'3.12.3'},
                     'task_image_digests':{'inspect-tool-support':'sha256:bbaecf7a093cfe24703c97d562bb059899e272cd5d57f16d9916fe05e6614419'},
                     'router_commit':'5cb00518fc6c6adcd94826999f4c67bce97a3629',
                     'router_local_image_id':'sha256:da57421f7aa6ca096239bf5baacd4199b43730b213d1a83f1f0e8c9b199a050e',
                     'dataset_revision':'7dce6050a7d6d172f3cc5c32aa97f52fa1a2e544'}})
report={'schema_version':1,'run_id':'issue-330-20261027','started_utc':sys.argv[3], 'cells':cells}
Path(sys.argv[2]).write_text(json.dumps(report,indent=2,sort_keys=True)+'\n')
print('Report constructed; independent schema and gate validation required.')
