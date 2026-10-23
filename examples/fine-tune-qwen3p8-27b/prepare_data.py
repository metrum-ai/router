#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Prepare pinned Apache-2.0 Glaive data; real data must stay outside Git."""
import argparse
import hashlib
import json
import random
import re
import unicodedata
from pathlib import Path
from functools import lru_cache

REVISION = 'e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac'
MODEL_REVISION = '1d4bf0f2ff6012fd82039f2fa52739d0dd7c60c0'

def normalized(text):
    return ' '.join(unicodedata.normalize('NFKC', text).casefold().split())

def prompt(row):
    return '\n'.join(m['content'] for m in row['messages'] if m['role'] == 'user')

@lru_cache(maxsize=1)
def validator():
    from jsonschema import Draft202012Validator
    return Draft202012Validator(json.loads(Path(__file__).with_name('schema.json').read_text()))


def validate(row):
    from jsonschema import ValidationError
    try:
        validator().validate(row)
    except ValidationError as exc:
        raise ValueError('schema validation failed') from exc
    tools = {t['function']['name'] for t in row.get('tools', [])}
    for m in row['messages']:
        for c in m.get('tool_calls', []):
            if c['function']['name'] not in tools:
                raise ValueError('undeclared tool')
            if not isinstance(json.loads(c['function']['arguments']), dict):
                raise ValueError('arguments must be an object')
    if not prompt(row):
        raise ValueError('missing user prompt')
    return row

def convert(row):
    if 'messages' in row:
        return validate(row)
    system = row['system']
    decoder = json.JSONDecoder()
    tools = []
    pos = system.find('{')
    while pos >= 0:
        fn, end = decoder.raw_decode(system[pos:])
        tools.append({'type': 'function', 'function': fn})
        pos = system.find('{', pos + end)
    # Conservative first-turn extraction: never use later turns as new cases.
    match = re.match(r'\s*USER:\s*(.*?)\s*ASSISTANT:\s*(.*?)(?:<\|endoftext\|>|$)', row['chat'], re.S)
    if not match:
        raise ValueError('unsupported conversation')
    user, answer = match.groups()
    messages = [{'role': 'user', 'content': user.strip()}]
    marker = '<functioncall>'
    if marker in answer:
        call = json.loads(answer.split(marker, 1)[1].strip())
        args = call['arguments']
        if isinstance(args, str):
            args = json.loads(args)
        messages.append({'role': 'assistant', 'content': None, 'tool_calls': [
            {'id': 'call_0', 'type': 'function', 'function': {'name': call['name'], 'arguments': json.dumps(args, sort_keys=True)}}]})
    else:
        messages.append({'role': 'assistant', 'content': answer.strip()})
    return validate({'messages': messages, 'tools': tools})

def assert_no_leakage(train, test):
    exact = {prompt(r) for r in train}
    norm = {normalized(p) for p in exact}
    if any(prompt(r) in exact or normalized(prompt(r)) in norm for r in test):
        raise ValueError('test prompt leakage')

def split(rows, train_size=20000, val_size=500, test_size=300, seed=326):
    unique = {}
    rejected = 0
    for row in rows:
        try:
            row = convert(row)
            unique.setdefault(normalized(prompt(row)), row)
        except (ValueError, KeyError, TypeError):
            rejected += 1
    rows = [unique[k] for k in sorted(unique)]
    random.Random(seed).shuffle(rows)
    if len(rows) < train_size + val_size + test_size:
        raise ValueError(f'insufficient unique valid rows: {len(rows)}')
    test = rows[:test_size]
    val = rows[test_size:test_size + val_size]
    train = rows[test_size + val_size:test_size + val_size + train_size]
    assert_no_leakage(train + val, test)
    return {'train': train, 'val': val, 'test': test}, rejected

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--train-size', type=int, default=20000)
    a = p.parse_args()
    if not 1 <= a.train_size <= 20000:
        p.error('train-size must be 1..20000')
    raw = json.loads(a.input.read_text())
    data, rejected = split(raw, train_size=a.train_size)
    a.out.mkdir(parents=True, exist_ok=True)
    hashes = {}
    for name, rows in data.items():
        content = ''.join(json.dumps(r, sort_keys=True, ensure_ascii=False) + '\n' for r in rows).encode()
        (a.out / (name + '.jsonl')).write_bytes(content)
        hashes[name] = hashlib.sha256(content).hexdigest()
    (a.out / 'manifest.json').write_text(json.dumps({'copyright': 'Copyright 2026 Metrum AI; SPDX-License-Identifier: Apache-2.0', 'dataset_revision': REVISION, 'model_revision': MODEL_REVISION, 'sha256': hashes, 'counts': {k: len(v) for k, v in data.items()}, 'rejected': rejected}, indent=2))

if __name__ == '__main__':
    main()
