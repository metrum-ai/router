#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Calibrate the pinned rubric judge against PUBLIC MT-Bench human judgments.

Requires pyarrow. Stores scalars only; never stores conversations or responses.
"""
import argparse
import hashlib
import json
import random
import urllib.request
from pathlib import Path
import sys
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from preflight import preflight

REVISION = 'f7d2896d2cc5d80f8b55c2bbc722613555233c25'
JUDGE = 'gpt-4o-2024-11-20'
RUBRIC = 'Evaluate correctness, relevance, instruction following and completeness. Compare the two conversations. Output only model_a, model_b, or tie. Do not favor verbosity or position.'


def bootstrap(values, seed=326, repeats=10000):
    if not values:
        raise ValueError('empty sample')
    rng = random.Random(seed)
    means = sorted(sum(rng.choices(values, k=len(values))) / len(values) for _ in range(repeats))
    return [means[int(repeats * .025)], means[int(repeats * .975)]]


def main():
    import pyarrow.parquet as pq
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--human-parquet', type=Path, required=True)
    p.add_argument('--token-file', type=Path, required=True)
    p.add_argument('--api-base', default='http://127.0.0.1:18525/v1')
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--preflight-targets', nargs='*', default=[], help='static target aliases for custom judge routes')
    a = p.parse_args()
    rows = pq.read_table(a.human_parquet).to_pylist()
    rng = random.Random(326)
    rng.shuffle(rows)
    # One public human vote per distinct question avoids correlated duplicate votes.
    selected = {}
    for row in rows:
        selected.setdefault(row['question_id'], row)
    selected = list(selected.values())[:50]
    if len(selected) != 50:
        raise ValueError('need 50 distinct human-labeled questions')
    token = a.token_file.read_text().strip()
    preflight(a.api_base,token,['frontier-ref',*a.preflight_targets])
    records = []
    for row in selected:
        payload = {'model': 'frontier-ref', 'temperature': 0, 'max_tokens': 16,
            'messages': [{'role': 'system', 'content': RUBRIC},
                         {'role': 'user', 'content': json.dumps({'model_a': row['conversation_a'], 'model_b': row['conversation_b']})}]}
        req = urllib.request.Request(a.api_base + '/chat/completions', data=json.dumps(payload).encode(),
            headers={'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token})
        with urllib.request.urlopen(req, timeout=180) as response:
            result = json.load(response)
        prediction = result['choices'][0]['message']['content'].strip().lower()
        usage = result.get('usage', {})
        records.append({'question_id': row['question_id'], 'agree': int(prediction == row['winner']),
                        'valid': int(prediction in ('model_a', 'model_b', 'tie')),
                        'prompt_tokens': usage.get('prompt_tokens', 0), 'completion_tokens': usage.get('completion_tokens', 0)})
        summary = {'copyright': 'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0', 'dataset': 'lmsys/mt_bench_human_judgments', 'revision': REVISION,
            'license': 'CC-BY-4.0', 'split': 'human', 'judge': JUDGE, 'temperature': 0,
            'rubric_sha256': hashlib.sha256(RUBRIC.encode()).hexdigest(),
            'sample_count': len(records), 'complete': len(records) == 50,
            'agreement': sum(r['agree'] for r in records) / len(records),
            'agreement_95_ci': bootstrap([r['agree'] for r in records]), 'records': records}
        a.out.parent.mkdir(parents=True, exist_ok=True)
        a.out.write_text(json.dumps(summary, indent=2))
        print('completed', len(records), flush=True)

if __name__ == '__main__':
    main()
