#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Build fresh run-2 cohorts outside Git, before any new held-out inference."""
import argparse
from collections import Counter
import gzip
import hashlib
import importlib.util
import json
from pathlib import Path
import random
import re

import yaml

ROOT = Path(__file__).resolve().parents[1]
SEED = 355
OASST_REVISION = 'fdf72ae0827c1cda404aff25b6603abec9e3399b'
OASST_SHA = '286a6e9a5a413b3272ae9c0b5a20d327983dea1c24342ae28cb244a6da65185c'
GLAIVE_SHA = 'e9b5d671812b5ca2fbd7b625a37d5c99a19576c37252cdc806defe256aea6dad'
MTBENCH_SHA = '4877bc46a40929f4082c3c79593700fb897b1d6c7f4c473032694a01322f5769'
CONTROL = re.compile(r'<\|[^<>]*\|>|</?(?:think|tool_call)>')
COPYRIGHT = 'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0'


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


legacy = module('run2_legacy_data', ROOT / 'prepare_data.py')
builder = module('run2_frozen_builder', ROOT / 'eval/build_tests.py')


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def key(row):
    return legacy.normalized(legacy.prompt(row))


def digest(text):
    return hashlib.sha256(text.encode()).hexdigest()


def write_jsonl(path, rows):
    path.write_text(''.join(json.dumps(r, sort_keys=True, ensure_ascii=False) + '\n' for r in rows))
    path.chmod(0o600)
    return sha(path)


def question(row):
    return row['question'] + '\n' + '\n'.join(
        f'{chr(65+i)}. {text}' for i, text in enumerate(row['options'])
    ) + '\nReturn only the letter of the correct option.'


def fresh_unique(rows, text, excluded, count, seed):
    unique = {}
    for row in rows:
        k = legacy.normalized(text(row))
        if k not in excluded:
            unique.setdefault(k, row)
    pool = [unique[k] for k in sorted(unique)]
    random.Random(seed).shuffle(pool)
    if len(pool) < count:
        raise ValueError('insufficient fresh unique source cases')
    return pool[:count]


def axolotl_view(row):
    """Opaque JSON tools avoid Arrow struct null-fill/key-order normalization.

    Axolotl _get_tools decodes string tools after generic null removal. Keep
    canonical sources schema-valid arrays; do not fork the base chat template.
    """
    return {**row, 'tools': json.dumps(row.get('tools', []), sort_keys=True, ensure_ascii=False)}


def json_rehearsal(row):
    """Same public task contract as eval, but exclusively training/val sources."""
    if not row['messages'][-1].get('tool_calls'):
        raise ValueError('JSON rehearsal needs a tool target')
    case = builder.build([row])[0]
    calls = [{'function': {'name': c['function']['name'],
                          'arguments': json.loads(c['function']['arguments'])}}
             for c in row['messages'][-1]['tool_calls']]
    messages = json.loads(case['vars']['messages'])
    return legacy.validate({'messages': messages + [{'role': 'assistant',
        'content': json.dumps({'tool_calls': calls}, sort_keys=True, ensure_ascii=False)}], 'tools': []})


def format_rehearsal(count=1000, seed=355000):
    """Metrum-authored arithmetic labels, not copied benchmark questions."""
    rng = random.Random(seed)
    result, seen = [], set()
    for index in range(count):
        while True:
            a, b = rng.randrange(1, 100), rng.randrange(1, 100)
            answer = a + b
            options = rng.sample([v for v in range(answer-30, answer+31) if v != answer], 9)
            position = index % 10
            options.insert(position, answer)
            text = f'What is {a} + {b}?\n' + '\n'.join(
                f'{chr(65+i)}. {value}' for i, value in enumerate(options))
            text += '\nReturn only the letter of the correct option.'
            if text not in seen:
                seen.add(text)
                break
        result.append({'messages': [{'role': 'user', 'content': text},
            {'role': 'assistant', 'content': chr(65+position)}], 'tools': []})
    return result


def annotation(row, name):
    labels = row.get('labels') or {}
    if isinstance(labels, dict):
        return (labels.get(name) or {}).get('value', 0)
    return next((label.get('value', 0) for label in labels if label.get('name') == name), 0)


def general_pairs(rows, excluded):
    """Reviewed English nonsynthetic root pairs; drop metadata and flagged PII."""
    byid = {r['message_id']: r for r in rows}
    unique = {}
    for r in rows:
        p = byid.get(r.get('parent_id'))
        if (r['role'] != 'assistant' or r.get('rank') != 0 or not p
                or p['role'] != 'prompter' or p.get('parent_id')):
            continue
        pair = (p, r)
        if any(m.get('lang') != 'en' or m.get('deleted')
               or m.get('review_result') is not True or m.get('synthetic') is not False for m in pair):
            continue
        if any(annotation(m, flag) > .1 for m in pair for flag in ('pii', 'spam')):
            continue
        if sum(len(m['text']) for m in pair) > 12000 or any(CONTROL.search(m['text']) for m in pair):
            continue
        row = {'messages': [{'role': 'user', 'content': p['text']},
                            {'role': 'assistant', 'content': r['text']}], 'tools': []}
        k = key(row)
        if k not in excluded:
            unique.setdefault(k, row)
    return [unique[k] for k in sorted(unique)]


def assert_disjoint(groups):
    sets = {name: {key(r) for r in rows} for name, rows in groups.items()}
    for left in sets:
        for right in sets:
            if left < right and sets[left] & sets[right]:
                raise ValueError('cross-split exact/normalized prompt leakage')


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for flag in ('glaive', 'oasst', 'mtbench', 'old-canonical', 'regressions', 'out'):
        p.add_argument('--' + flag, type=Path, required=True)
    p.add_argument('--historical-preparation-only',action='store_true',
        help='reproduce superseded direct-download preparation, NEVER authorized run2 training data')
    a = p.parse_args()
    if not a.historical_preparation_only:
        p.error('direct-download run2 superseded by #358; consume router_export.py instead')
    if a.out.resolve().is_relative_to(ROOT.parents[1]):
        p.error('raw output must be outside the worktree')
    for path, expected in ((a.oasst, OASST_SHA), (a.glaive, GLAIVE_SHA), (a.mtbench, MTBENCH_SHA)):
        if sha(path) != expected:
            p.error('source differs from pinned license-checked archive')
    previous_manifest = json.loads((ROOT.parents[1] / 'docs/evidence/finetune/2026-10-23/dataset-manifest.json').read_text())
    previous = []
    for name in ('train', 'val', 'test'):
        path = a.old_canonical / (name + '.jsonl')
        if sha(path) != previous_manifest['sha256'][name]:
            p.error('run-1 exclusion input differs from published hash')
        previous.extend(json.loads(line) for line in path.read_text().splitlines())
    old_keys = {key(row) for row in previous}
    source_manifest = json.loads((ROOT.parents[1] / 'docs/evidence/finetune/2026-10-23/regression-manifest.json').read_text())
    if sha(a.regressions/'tests.yaml') != source_manifest['test_yaml_sha256']:
        p.error('run-1 regression exclusion input differs from published hash')
    old_tests = yaml.safe_load((a.regressions / 'tests.yaml').read_text())
    if Counter(t['description'].split('-')[0] for t in old_tests) != {'ifeval': 200, 'mmlu': 200}:
        p.error('expected all 400 old regression cases for exclusion')
    old_regression = {legacy.normalized(json.loads(t['vars']['messages'])[-1]['content']) for t in old_tests}
    # Read pinned original sources, never a model's scores or responses.
    for name in ('ifeval.jsonl', 'mmlu.parquet'):
        if sha(a.regressions/name) != source_manifest['source_sha256'][name]:
            p.error('regression source hash mismatch')
    ifeval = [json.loads(line) for line in (a.regressions/'ifeval.jsonl').read_text().splitlines()]
    import pyarrow.parquet as pq
    mmlu = pq.read_table(a.regressions/'mmlu.parquet').to_pylist()
    development = module('run2_precheck_cases', Path(__file__).with_name('precheck.py')).cases()
    development_keys = {legacy.normalized(c['messages'][0]['content']) for c in development}
    development_keys |= {legacy.normalized(c['messages'][0]['content'].split('\n')[0]) for c in development}
    fresh_i = fresh_unique(ifeval, lambda r: r['prompt'], old_regression | development_keys, 200, SEED)
    old_stems = {legacy.normalized(r['question']) for r in mmlu if legacy.normalized(question(r)) in old_regression}
    fresh_m = fresh_unique(mmlu, lambda r: r['question'], old_stems | old_regression | development_keys, 200, SEED+1)
    calibration_prompts = {legacy.normalized(m['content']) for r in pq.read_table(a.mtbench).to_pylist()
        for side in ('conversation_a', 'conversation_b') for m in r[side] if m['role'] == 'user'}
    protected = old_keys | old_regression | calibration_prompts | development_keys | {legacy.normalized(r['prompt']) for r in ifeval}
    protected |= {legacy.normalized(r['question']) for r in mmlu}
    protected |= {legacy.normalized(question(r)) for r in mmlu}
    pool, rejected = [], 0
    for raw in json.loads(a.glaive.read_text()):
        try:
            row = legacy.convert(raw)
        except (ValueError, KeyError, TypeError):
            rejected += 1
            continue
        serialized = json.dumps(row, ensure_ascii=False)
        if key(row) not in protected and len(serialized) <= 16000 and not CONTROL.search(serialized):
            pool.append(row)
    data, extra_rejected = legacy.split(pool, train_size=1000, val_size=200, test_size=300, seed=SEED)
    del pool
    protected |= {key(row) for rows in data.values() for row in rows}
    with gzip.open(a.oasst, 'rt') as f:
        general = general_pairs([json.loads(line) for line in f], protected)
    random.Random(SEED+2).shuffle(general)
    if len(general) < 3200:
        p.error('insufficient reviewed general instruction root pairs')
    native_train, native_val = data['train'], data['val']
    json_train = [json_rehearsal(row) for row in native_train if row['messages'][-1].get('tool_calls')]
    json_val = [json_rehearsal(row) for row in native_val if row['messages'][-1].get('tool_calls')][:100]
    arithmetic = format_rehearsal()
    if any(key(row) in protected for row in arithmetic):
        p.error('synthetic training prompt overlaps protected source')
    data['train'] = native_train + json_train + general[:3000] + arithmetic
    data['val'] = native_val + json_val + general[3000:3200]
    assert_disjoint(data)
    if any(key(row) in old_keys | old_regression for rows in data.values() for row in rows):
        p.error('run-1 evaluated/source item reused')
    for name in ('train', 'val'):
        random.Random(SEED + len(name)).shuffle(data[name])
    a.out.mkdir(parents=True, exist_ok=True, mode=0o700)
    hashes = {name: write_jsonl(a.out/(name+'.jsonl'), rows) for name, rows in data.items()}
    view_hashes = {name: write_jsonl(a.out/(name+'-axolotl.jsonl'), [axolotl_view(r) for r in data[name]])
                   for name in ('train', 'val')}
    cases = []
    for index, row in enumerate(fresh_i):
        cases.append({'description': f'ifeval-{index:03d}', 'vars': {'messages': json.dumps([{'role':'user','content':row['prompt']}]),
            'instruction_ids': row['instruction_id_list'], 'instruction_kwargs': row['kwargs'], 'instruction_prompt': row['prompt']},
            'assert': [{'type':'python','value':'file://scorers/ifeval.py'}]})
    for index, row in enumerate(fresh_m):
        cases.append({'description':f'mmlu-pro-{index:03d}', 'vars':{'messages':json.dumps([{'role':'user','content':question(row)}])},
            'assert':[{'type':'regex','value':r'^\s*'+row['answer']+r'[.)]?\s*$'}]})
    (a.out/'regressions.yaml').write_text(yaml.safe_dump(cases, sort_keys=False))
    (a.out/'regressions.yaml').chmod(0o600)
    (a.out/'held-out.yaml').write_text(yaml.safe_dump(builder.build(data['test']), sort_keys=False))
    (a.out/'held-out.yaml').chmod(0o600)
    manifest = {'copyright': COPYRIGHT, 'protocol':'metrum-qwen3p8-text-lora-20261023-run2', 'seed':SEED,
        'dataset_revision':legacy.REVISION, 'model_revision':legacy.MODEL_REVISION, 'general_revision':OASST_REVISION,
        'sha256':hashes, 'axolotl_view_sha256':view_hashes,
        'axolotl_view_format':'same messages; tools encoded as opaque JSON string, decoded by Axolotl before base-template rendering',
        'counts':{k:len(v) for k,v in data.items()}, 'source_rejections':rejected+extra_rejected,
        'components':{'train':{'native_glaive':1000,'json_text_glaive':len(json_train),'oasst':3000,'synthetic_letter':1000},
                      'val':{'native_glaive':200,'json_text_glaive':len(json_val),'oasst':200}},
        'heldout_tool_targets':sum(bool(r['messages'][-1].get('tool_calls')) for r in data['test']),
        'old_canonical_excluded':len(old_keys), 'old_regressions_excluded':len(old_regression),
        'public_calibration_user_prompts_excluded':len(calibration_prompts),
        'synthetic_precheck_prompts_excluded':len(development),
        'cross_split_normalized_prompt_overlap':0, 'old_evaluated_prompt_overlap':0,
        'heldout_yaml_sha256':sha(a.out/'held-out.yaml'), 'regressions_yaml_sha256':sha(a.out/'regressions.yaml'),
        'regression_counts':{'ifeval':200,'mmlu_pro':200}, 'oasst_eligible_root_pairs':len(general),
        'source_sha256':{'glaive':sha(a.glaive),'oasst':sha(a.oasst),'mtbench':sha(a.mtbench),
                         'regressions_manifest':sha(a.regressions/'manifest.json'),
                         'old_regression_tests':sha(a.regressions/'tests.yaml')},
        'code_sha256':{'data.py':sha(Path(__file__)), 'prepare_data.py':sha(ROOT/'prepare_data.py'),
                       'build_tests.py':sha(ROOT/'eval/build_tests.py'),'tools.py':sha(ROOT/'eval/scorers/tools.py'),
                       'precheck.py':sha(Path(__file__).with_name('precheck.py'))},
        'sampling':'seed355; exclude old/protected prompts before source-group split; fresh tool/text2:1; test then val then native train; augment train/val only; OASST root groups disjoint'}
    (a.out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
    print(json.dumps({'counts':manifest['counts'],'sha256':hashes,'heldout_tools':manifest['heldout_tool_targets']}))


if __name__ == '__main__':
    main()
