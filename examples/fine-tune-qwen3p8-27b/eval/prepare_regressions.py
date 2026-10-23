#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Download pinned licensed standard regressions outside Git (no model calls)."""
import argparse
import hashlib
import json
import random
import urllib.request
from pathlib import Path
import yaml

IFEVAL = '966cd89545d6b6acfd7638bc708b98261ca58e84'
MMLU = 'b189ec765aa7ed75c8acfea42df31fdae71f97be'
VERIFIERS = 'd6de81643928d653435c431bae19945d41d32520'


def fetch(url, path):
    with urllib.request.urlopen(url, timeout=120) as response:
        path.write_bytes(response.read())
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    import pyarrow.parquet as pq
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--out', required=True, type=Path)
    a = p.parse_args()
    a.out.mkdir(parents=True, exist_ok=True)
    files = {}
    urls = {
        'ifeval.jsonl': f'https://huggingface.co/datasets/google/IFEval/resolve/{IFEVAL}/ifeval_input_data.jsonl',
        'ifeval-README.md': f'https://huggingface.co/datasets/google/IFEval/resolve/{IFEVAL}/README.md',
        'mmlu.parquet': f'https://huggingface.co/datasets/TIGER-Lab/MMLU-Pro/resolve/{MMLU}/data/test-00000-of-00001.parquet',
        'mmlu-README.md': f'https://huggingface.co/datasets/TIGER-Lab/MMLU-Pro/resolve/{MMLU}/README.md'}
    for name, url in urls.items():
        files[name] = fetch(url, a.out / name)
    # Keep upstream source/license headers, do not vendor benchmark content into Git.
    for part in ['lm_eval', 'lm_eval/tasks', 'lm_eval/tasks/ifeval']:
        directory = a.out / part
        directory.mkdir(parents=True, exist_ok=True)
        (directory/'__init__.py').write_text('')
    for name in ['instructions.py', 'instructions_util.py', 'instructions_registry.py']:
        files[name] = fetch(f'https://raw.githubusercontent.com/EleutherAI/lm-evaluation-harness/{VERIFIERS}/lm_eval/tasks/ifeval/{name}', a.out/'lm_eval/tasks/ifeval'/name)
    rng = random.Random(326)
    cases = []
    rows = [json.loads(line) for line in (a.out/'ifeval.jsonl').read_text().splitlines()]
    rng.shuffle(rows)
    for index, row in enumerate(rows[:200]):
        cases.append({'description': f'ifeval-{index:03d}', 'vars': {'messages': json.dumps([{'role':'user','content':row['prompt']}]),
            'instruction_ids':row['instruction_id_list'], 'instruction_kwargs':row['kwargs'], 'instruction_prompt':row['prompt']},
            'assert':[{'type':'python','value':'file://scorers/ifeval.py'}]})
    rows = pq.read_table(a.out/'mmlu.parquet').to_pylist()
    rng.shuffle(rows)
    for index, row in enumerate(rows[:200]):
        question = row['question'] + '\n' + '\n'.join(f'{chr(65+i)}. {text}' for i, text in enumerate(row['options'])) + '\nReturn only the letter of the correct option.'
        cases.append({'description': f'mmlu-pro-{index:03d}', 'vars':{'messages':json.dumps([{'role':'user','content':question}])},
            'assert':[{'type':'regex','value':r'^\s*'+row['answer']+r'[.)]?\s*$'}]})
    (a.out/'tests.yaml').write_text(yaml.safe_dump(cases, sort_keys=False))
    (a.out/'manifest.json').write_text(json.dumps({'copyright':'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0','ifeval_revision':IFEVAL,'ifeval_license':'Apache-2.0',
        'mmlu_revision':MMLU,'mmlu_license':'MIT','verifier_revision':VERIFIERS,'seed':326,'count_per_benchmark':200,
        'source_sha256':files,'test_yaml_sha256':hashlib.sha256((a.out/'tests.yaml').read_bytes()).hexdigest()},indent=2))

if __name__ == '__main__':
    main()
