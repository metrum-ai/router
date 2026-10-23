#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Apply existing scalar gate PLUS preregistered +10pt/2pt regression rules."""
import argparse
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]


def require_pairs(document, base, candidate, metric, expected):
    pairs = document.get('paired', {})
    pair = pairs.get(base + ' -> ' + candidate) or pairs.get(candidate + ' -> ' + base)
    if not pair or pair.get(metric, {}).get('n') != expected:
        raise ValueError('missing complete paired comparison for ' + metric)


def additional_failures(quality, ifeval, mmlu, base, candidate):
    failures = []
    require_pairs(quality, base, candidate, 'passed', 300)
    arms = quality['providers']
    if arms[base]['count'] != 300 or arms[candidate]['count'] != 300:
        raise ValueError('quality arms must contain 300 paired cases')
    exact_base = arms[base]['metrics']['exact']
    exact_candidate = arms[candidate]['metrics']['exact']
    if exact_base['n'] != 200 or exact_candidate['n'] != 200:
        raise ValueError('frozen cohort requires exactly 200 tool cases in each arm')
    require_pairs(quality, base, candidate, 'exact', exact_base['n'])
    if exact_candidate['mean'] - exact_base['mean'] < .10 - 1e-12:
        failures.append('tool_exact_improvement_below_10pt')
    for label, document in [('ifeval', ifeval), ('mmlu_pro', mmlu)]:
        require_pairs(document, base, candidate, 'passed', 200)
        arms = document['providers']
        if arms[base]['count'] != 200 or arms[candidate]['count'] != 200:
            raise ValueError('standard regression must contain 200 cases per arm')
        if arms[base]['metrics']['passed']['mean'] - arms[candidate]['metrics']['passed']['mean'] > .02 + 1e-12:
            failures.append(label + '_regression_above_2pt')
    return failures


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--quality', type=Path, required=True)
    p.add_argument('--ifeval', type=Path, required=True)
    p.add_argument('--mmlu', type=Path, required=True)
    p.add_argument('--base', required=True)
    p.add_argument('--candidate', required=True)
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--preregistration', type=Path, default=Path(__file__).with_name('preregistration.json'),
                   help='scalar registration; run2 changes protocol identity, not numerical thresholds')
    a = p.parse_args()
    try:
        quality, ifeval, mmlu = [json.loads(path.read_text()) for path in [a.quality,a.ifeval,a.mmlu]]
        failures = additional_failures(quality, ifeval, mmlu, a.base, a.candidate)
        prereg = json.loads(a.preregistration.read_text())
        arms = []
        for role, provider, model in [('fixed_model',a.base,prereg['fixed_model_id']),('candidate',a.candidate,prereg['candidate_id'])]:
            summary = quality['providers'][provider]
            arms.append({'role':role,'model_id':model,'total_cases':summary['count'],
                'passed_cases':round(summary['metrics']['passed']['mean']*summary['count']),
                'error_count':round(summary['metrics']['error']['mean']*summary['count']),
                'p95_latency_ms':summary['p95_latency_ms'],'total_cost_usd':summary['total_cost_usd']})
        result_path = a.out.with_suffix('.scalar-input.json')
        scalar_output = a.out.with_suffix('.scalar-gate.json')
        result_path.write_text(json.dumps({'schema_version':prereg['schema_version'],'preregistration_id':prereg['preregistration_id'],'arms':arms},indent=2))
        process = subprocess.run([sys.executable,str(ROOT/'scripts/fixed_model_outcome_gate.py'),
            '--preregistration',str(a.preregistration),
            '--results',str(result_path),'--out',str(scalar_output)],check=False)
        if process.returncode == 2:
            return 2
        scalar = json.loads(scalar_output.read_text())
        failures += scalar['failure_metrics']
        a.out.write_text(json.dumps({'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0',
            'status':'fail' if failures else 'pass','failure_metrics':failures,
            'preregistration_id':prereg['preregistration_id'],
            'evaluated_provider_groups':{'fixed_model':a.base,'candidate':a.candidate},'scalar_gate':scalar},indent=2))
        return 1 if failures else 0
    except (OSError,ValueError,KeyError,TypeError) as exc:
        print('outcome gate invalid input:',str(exc),file=sys.stderr)
        return 2

if __name__ == '__main__':
    raise SystemExit(main())
