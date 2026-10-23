# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Collect licensed public task seeds as real routed mock-tool episodes.
Run with uv --with jsonschema. All credentials come from environment, never argv.
Outputs contain governed data: use an operator-owned directory outside Git.
"""
import argparse
import base64
import hashlib
import io
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.request
import zipfile
from datetime import datetime, timezone
from jsonschema import Draft202012Validator

TERMS_SHA = '78c0dfb857eb860d15cfc2dc7296f3b19950d3145b0e5a40bc46e28cfbd19d30'
DECISION = {
    'training_outputs_allowed': 'true',
    'decision': 'Maintainer authorized synthetic technical loop test, illustrative Acme scenario and deterministic mock tools; not real customer outputs.',
    'reference': 'https://github.com/metrum-ai/router/issues/358#issuecomment-6366186842',
    'terms_url': 'https://fireworks.ai/terms-of-service',
    'terms_sha256': TERMS_SHA,
    'teacher': 'accounts/fireworks/models/gpt-oss-120b',
    'model_license': 'Apache-2.0',
    'model_license_sha256': '58d1e17ffe5109a7ae296caafcadfdbe6a7d176f0bc4ab01e12a689b0499d8bd',
    'scope': 'This example only; users must independently verify license and provider output-training permission.',
}
SOURCE = {'dataset': 'glaiveai/glaive-function-calling-v2', 'revision': 'e7f4b6456019f5d8bcb991ef0dd67d8ff23221ac', 'license': 'Apache-2.0'}

def now():
    return datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')

def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()

def request(base, path, payload, key=None):
    headers = {'Content-Type': 'application/json', 'User-Agent': 'acme-router-loop/1'}
    if key:
        headers['Authorization'] = 'Bearer ' + key
    else:
        auth = os.environ['TRACE_ADMIN_USER'] + ':' + os.environ['TRACE_ADMIN_PASSWORD']
        headers['Authorization'] = 'Basic ' + base64.b64encode(auth.encode()).decode()
    req = urllib.request.Request(base + path, json.dumps(payload).encode(), headers)
    with urllib.request.urlopen(req, timeout=180) as response:
        body = response.read()
        return response.status, response.headers, body


def execute(call, tools, seed):
    """Allowlisted local mock backend; no eval, subprocesses, URLs or real writes."""
    name = call['function']['name']
    schema = next(t['function']['parameters'] for t in tools if t['function']['name'] == name)
    def local_refs(value):
        if isinstance(value, dict):
            for key in ('$ref', '$dynamicRef', '$recursiveRef'):
                if key in value and not value[key].startswith('#'):
                    raise ValueError('remote schema references forbidden')
            for child in value.values():
                local_refs(child)
        elif isinstance(value, list):
            for child in value:
                local_refs(child)
    local_refs(schema)
    args = json.loads(call['function']['arguments'])
    if not isinstance(args, dict):
        raise ValueError('function arguments must be an object')
    Draft202012Validator(schema).validate(args)
    return {'ok': True, 'mock': True, 'status': 'completed', 'seed': seed}, {'name': name, 'arguments': args}


def episode(base, key, row, seed):
    # No recorded assistant answer is ever sent as an initial model input.
    initial = [m for m in row['messages'] if m['role'] in ('system', 'user', 'developer')]
    tools = row.get('tools', [])
    expected = [{'name': c['function']['name'], 'arguments': json.loads(c['function']['arguments'])}
                for m in row['messages'] for c in m.get('tool_calls', [])]
    gold = row['messages'][-1].get('content')
    expected_final = gold.strip() if isinstance(gold, str) and gold.strip() in ('A','B','C','D') and not expected else ''
    messages = json.loads(json.dumps(initial))
    actual, schema_valid, tokens = [], True, 0
    request_ids = []
    for turn in range(6):
        _, headers, raw = request(base, '/v1/chat/completions', {
            'model': 'support-agent', 'messages': messages, 'tools': tools,
            'max_tokens': 512, 'temperature': 0, 'stream': False}, key)
        response = json.loads(raw)
        tokens += response.get('usage', {}).get('total_tokens', 0)
        request_ids.append(headers['X-Request-Id'])
        assistant = response['choices'][0]['message']
        messages.append(assistant)
        calls = assistant.get('tool_calls', [])
        if not calls:
            content = assistant.get('content') or ''
            complete = response['choices'][0].get('finish_reason', 'stop') == 'stop'
            success = actual == expected and bool(content) and complete and schema_valid and (not expected_final or content.strip() == expected_final)
            return {'initial': initial, 'tools': tools, 'expected': expected, 'expected_final': expected_final, 'success': success,
                    'schema_valid': schema_valid, 'request_ids': request_ids, 'tokens': tokens,
                    'task_hash': digest({'initial': initial, 'tools': tools}), 'turns': turn + 1}
        for call in calls:
            try:
                result, checked = execute(call, tools, seed)
                actual.append(checked)
            except Exception as exc:
                # Tool schema/task errors are feedback, not harness execution.
                schema_valid = False
                result = {'ok': False, 'error': type(exc).__name__}
            messages.append({'role': 'tool', 'tool_call_id': call['id'], 'content': json.dumps(result)})
    raise RuntimeError('tool episode exceeded six turns')


def validate_archive(raw):
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        manifest = json.loads(archive.read('manifest.json'))
        assert manifest['source']['kind'] == 'router_capture'
        assert manifest['audit_event_id'] > 0
        assert manifest['teacher_terms_decision']['terms_sha256'] == TERMS_SHA
        tasks = set()
        for name, info in manifest['files'].items():
            b = archive.read(name)
            assert hashlib.sha256(b).hexdigest() == info['sha256']
            rows = [json.loads(line) for line in b.splitlines()]
            assert len(rows) == info['count']
            for row in rows:
                assert row['provenance']['task_hash'] not in tasks
                tasks.add(row['provenance']['task_hash'])
                if name != 'test.jsonl':
                    assert row['outcome']['task_success']
                assert row['replay']['initial_messages']
        return manifest


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--base', default='http://127.0.0.1:18410')
    p.add_argument('--tasks', type=Path, required=True, help='licensed canonical task-seed JSONL; not training data')
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--per-split', type=int, default=2)
    p.add_argument('--include-text', action='store_true', help='also collect general/text seed tasks; completion validity is not semantic answer quality')
    p.add_argument('--task-source-json', type=Path, help='reviewed map of string attribution/license/revision fields for mixed public seed sources')
    args = p.parse_args()
    if not 1 <= args.per_split <= 10:
        p.error('small-sample spend guard: per-split must be 1..10')
    args.out.mkdir(parents=True, exist_ok=True)
    source = json.loads(args.task_source_json.read_text()) if args.task_source_json else dict(SOURCE)
    if not isinstance(source, dict) or not source.get('license') or not all(isinstance(v, str) for v in source.values()):
        raise ValueError('source attribution must be a reviewed string map with license')
    source['seed_file_sha256'] = hashlib.sha256(args.tasks.read_bytes()).hexdigest()
    rows = [json.loads(line) for line in args.tasks.read_text().splitlines()]
    rows = [r for r in rows if args.include_text or any(m.get('tool_calls') for m in r['messages'])]
    if args.include_text and not args.task_source_json:
        p.error('--include-text requires reviewed --task-source-json attribution')
    selected, seen = [], set()
    for row in rows:
        task = digest([m for m in row['messages'] if m['role'] == 'user'])
        if task not in seen:
            selected.append(row); seen.add(task)
        if len(selected) == 3 * args.per_split:
            break
    if len(selected) < 3 * args.per_split:
        raise ValueError('insufficient unique tool tasks')
    boundaries, evidence = [now()], []
    for split in range(3):
        for i in range(args.per_split):
            seed = split * args.per_split + i
            row = selected[seed]
            for key_name in ('TRACE_SUPPORT_KEY', 'TRACE_IT_KEY'):
                result = episode(args.base, os.environ[key_name], row, seed)
                label = {'version': 'router-outcome-v1', 'task_hash': result['task_hash'],
                         'task_success': result['success'], 'schema_valid': result['schema_valid'],
                         'quality': float(result['success']), 'backend': 'acme-support-v1', 'seed': seed,
                         'expected_calls': result['expected'], 'expected_final': result['expected_final']}
                # Asynchronous capture must settle before authorized labeling.
                time.sleep(.25)
                for attempt in range(20):
                    try:
                        request(args.base, '/admin/training/outcomes/' + result['request_ids'][-1], label)
                        captured = True
                        break
                    except urllib.error.HTTPError as error:
                        if error.code != 404:
                            raise
                        captured = False
                        if key_name == 'TRACE_IT_KEY':
                            break
                        time.sleep(.25)
                assert captured == (key_name == 'TRACE_SUPPORT_KEY')
                evidence.append({'caller': key_name, **result, 'captured': captured})
        time.sleep(.05)
        boundaries.append(now())
    filters = {'project': 'acme-support', 'group': 'support-agent', 'from': boundaries[0],
               'validation_from': boundaries[1], 'test_from': boundaries[2], 'to': boundaries[3],
               'success_only': True, 'min_quality': 1, 'teacher_terms_decision': DECISION, 'task_source': source}
    _, _, raw = request(args.base, '/admin/training/export', filters)
    (args.out / 'dataset.zip').write_bytes(raw)
    manifest = validate_archive(raw)
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        for name in ('manifest.json', 'train.jsonl', 'val.jsonl', 'test.jsonl'):
            (args.out / name).write_bytes(archive.read(name))
    (args.out / 'collection-evidence.json').write_text(json.dumps({'filters': filters, 'episodes': evidence}, indent=2))
    print(json.dumps({'files': manifest['files'], 'dataset_hash': manifest['dataset_hash'],
                      'episodes': len(evidence), 'all_uncaptured_it_absent': all(not r['captured'] for r in evidence if r['caller'] == 'TRACE_IT_KEY')}))

if __name__ == '__main__':
    main()
