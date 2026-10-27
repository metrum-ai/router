#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Bounded, serial #330 runner. Raw Inspect logs remain private on the droplet."""
import datetime as dt
import glob
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import time

os.umask(0o077)
root = Path('/root/trials')
result_path = root / 'results.jsonl'
if result_path.exists():
    raise SystemExit('refusing to overwrite results')
secret = dict(line.split('=', 1) for line in Path('/run/eval-secrets/env').read_text().splitlines() if '=' in line)
caller = Path('/run/eval-secrets/caller').read_text().strip()
base = ['/root/eval-venv/bin/inspect', 'eval', 'inspect_evals/humaneval']
common = ['-M', 'responses_api=false', '--sandbox', 'docker:/root/sandbox-compose.yaml', '--seed', '330', '--max-connections', '1', '--max-samples', '1', '--max-retries', '0', '--max-tokens', '4096', '--token-limit', '8192', '--timeout', '300', '--log-format', 'json', '--display', 'none', '--no-log-model-api']
rows = []
max_attempt_cost = (8192 * 0.30 + 4096 * 1.20) / 1_000_000

def stop(reason):
    (root / 'stop-reason.txt').write_text(reason + '\n')
    print('STOP:', reason, flush=True)
    raise SystemExit(2)

def parse_sample(folder, expected):
    files = glob.glob(str(folder / '*.json'))
    if len(files) != 1:
        stop(f'{expected}: no unique inspect log; provider cost requires reconciliation')
    data = json.loads(Path(files[0]).read_text())
    samples = data.get('samples') or []
    if data.get('status') != 'success' or len(samples) != 1 or samples[0].get('id') != expected:
        stop(f'{expected}: inspect error or missing sample; provider cost requires reconciliation')
    sample = samples[0]
    score = (sample.get('scores') or {}).get('verify', {}).get('value')
    usage = sample.get('output', {}).get('usage')
    events = [e for e in sample.get('events', []) if e.get('event') == 'model']
    if score not in ('C', 'I') or len(events) != 1 or not isinstance(usage, dict):
        stop(f'{expected}: missing verifier, model event, or provider usage; reconciliation required')
    if not all(isinstance(usage.get(k), int) and usage[k] >= 0 for k in ('input_tokens', 'output_tokens')):
        stop(f'{expected}: incomplete token usage; reconciliation required')
    def timestamp(v): return dt.datetime.fromisoformat(v.replace('Z', '+00:00'))
    model = events[0]
    # OpenAI chat-completions adapter uses a unary call; first response equals completion.
    first_ms = (timestamp(model['completed']) - timestamp(model['timestamp'])).total_seconds() * 1000
    completion_ms = (timestamp(sample['completed_at']) - timestamp(sample['started_at'])).total_seconds() * 1000
    if min(first_ms, completion_ms) < 0:
        stop(f'{expected}: invalid timing')
    return {'task_id': expected, 'outcome': 'pass' if score == 'C' else 'fail', 'time_to_first_event_ms': round(first_ms, 3), 'completion_latency_ms': round(completion_ms, 3), 'tool_turns': 0, 'input_tokens': usage['input_tokens'], 'output_tokens': usage['output_tokens'], 'cached_input_tokens': usage.get('input_tokens_cache_read', 0)}

def request_rows():
    with sqlite3.connect('/root/eval-state/usage.sqlite') as c:
        c.row_factory = sqlite3.Row
        return [dict(r) for r in c.execute('select requested_model,target_model,status,attempts,fallback_used,input_tokens,output_tokens,total_cost_usd,usage_source from request_usage order by ts,request_id')]

def save(row):
    rows.append(row)
    with result_path.open('a') as f:
        f.write(json.dumps(row, sort_keys=True) + '\n')
        f.flush()
        os.fsync(f.fileno())

def direct_cost(sample):
    cached = sample['cached_input_tokens']
    if not isinstance(cached, int) or cached < 0 or cached > sample['input_tokens']:
        stop(f"{sample['task_id']}: invalid cache usage")
    return ((sample['input_tokens'] - cached) * 0.30 + cached * 0.006 + sample['output_tokens'] * 1.20) / 1_000_000

first = parse_sample(root / 'direct-0', 'HumanEval/0')
first.update(cell_id='humaneval-direct', cost_usd=direct_cost(first), cost_basis='provider_usage_published_token_rates')
save(first)
for cell, model, endpoint in [
    ('humaneval-direct', 'accounts/fireworks/models/deepseek-v4p1-flash', 'https://api.fireworks.ai/inference/v1'),
    ('humaneval-fixed', 'fixed', 'http://127.0.0.1:18081/v1'),
    ('humaneval-dynamic', 'dynamic', 'http://127.0.0.1:18081/v1'),
]:
    for i in range(1 if cell == 'humaneval-direct' else 0, 10):
        spent = sum(r['cost_usd'] for r in rows)
        remaining = 30 - len(rows)
        # Upper bound includes two upstream attempts per router request and six hours of VM time.
        projection = spent + 0.42858 + remaining * 2 * max_attempt_cost
        if projection > 10:
            stop(f'budget: spent ${spent:.6f}; projection ${projection:.6f} exceeds $10')
        folder = root / f'{cell}-{i}'
        folder.mkdir(exist_ok=False)
        expected = f'HumanEval/{i}'
        cmd = base + ['--model', 'openai/' + model, '--model-base-url', endpoint, '--sample-id', expected, '--log-dir', str(folder)] + common
        env = os.environ.copy()
        env['OPENAI_API_KEY'] = secret['FIREWORKS_API_KEY'] if cell == 'humaneval-direct' else caller
        before = len(request_rows())
        with (folder / 'run.log').open('w') as log:
            try:
                p = subprocess.run(cmd, cwd='/root/inspect-empty', env=env, stdout=log, stderr=subprocess.STDOUT, timeout=420)
            except subprocess.TimeoutExpired:
                stop(f'{cell}/{expected}: timeout; provider cost requires reconciliation')
        if p.returncode:
            stop(f'{cell}/{expected}: inspect exited {p.returncode}; provider cost requires reconciliation')
        sample = parse_sample(folder, expected)
        if cell == 'humaneval-direct':
            sample.update(cell_id=cell, cost_usd=direct_cost(sample), cost_basis='provider_usage_published_token_rates')
        else:
            for _ in range(10):
                recorded = request_rows()
                if len(recorded) > before: break
                time.sleep(1)
            if len(recorded) != before + 1:
                stop(f'{cell}/{expected}: router usage row missing or ambiguous')
            usage = recorded[-1]
            if usage['requested_model'] != model or usage['status'] != 200 or usage['attempts'] != 1 or usage['fallback_used']:
                stop(f'{cell}/{expected}: router request failed or had retries/fallback; reconcile provider usage')
            if usage['input_tokens'] != sample['input_tokens'] or usage['output_tokens'] != sample['output_tokens']:
                stop(f'{cell}/{expected}: router/provider usage mismatch; reconcile')
            sample.update(cell_id=cell, cost_usd=usage['total_cost_usd'], cost_basis='router_request_time_usage', target_model=usage['target_model'])
        save(sample)
        print(f'{cell} {i+1}/10 recorded; aggregate token-derived cost ${sum(r["cost_usd"] for r in rows):.6f}', flush=True)
print('All 30 rows recorded; outcomes require report validation.', flush=True)
