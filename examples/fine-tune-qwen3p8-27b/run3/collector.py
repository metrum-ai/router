# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Private, budget-bounded run-3 multi-caller collection; no implicit execution."""
import argparse
import concurrent.futures
import copy
from datetime import date, datetime, timezone
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import secrets
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
import zipfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HERE.parents[0] / 'router-loop'))
import full
import local
import freeze

spec = importlib.util.spec_from_file_location('catalog_hash', ROOT/'examples/catalog/token_hash.py')
secure = importlib.util.module_from_spec(spec); spec.loader.exec_module(secure)
AGENTS = ('support', 'operations', 'inventory', 'documentation', 'analysis')


class Ledger(full.Ledger):
    def __init__(self):
        super().__init__(); self.spent = 0; self.known_usage_usd = 0; self.unknown_usage_calls = 0

    def settle(self, amount, response):
        usage = response.get('usage') or {}
        known = all(isinstance(usage.get(k), int) and not isinstance(usage.get(k), bool) and usage[k] >= 0
                    for k in ('prompt_tokens', 'completion_tokens'))
        super().settle(amount, response if known else {})
        with self.lock:
            if known:
                self.known_usage_usd += usage['prompt_tokens']*.15/1e6 + usage['completion_tokens']*.6/1e6
            else:
                self.unknown_usage_calls += 1


def configure(out, env, hash_state):
    """Generate five distinct capture keys plus an uncaptured negative control."""
    cfg = local.config(out, env)
    template = copy.deepcopy(cfg['callers'][0])
    cfg['callers'] = []
    ctl = secure.prepare(ROOT)
    for name in AGENTS:
        owner = 'run3-' + name
        variable = 'TRACE_AGENT_' + name.upper()
        token = f'rtr_metrum_{owner}_acme-support_test_run3_{secrets.token_urlsafe(32)}'
        env[variable] = token
        caller = copy.deepcopy(template)
        caller.update(id=owner+'-test', owner_user=owner, project='acme-support', environment='test')
        secure.apply(caller, ctl, token, hash_state)
        cfg['users'].append({'id': owner, 'type': 'service_account', 'status': 'active'})
        cfg['project_memberships'].append({'user_id': owner, 'project': 'acme-support', 'role': 'developer', 'status': 'active'})
        cfg['callers'].append(caller)
    control = copy.deepcopy(template)
    control.update(id='acme-it-prod', owner_user='acme-it', project='acme-it', environment='test')
    control.pop('content_capture', None)
    env['TRACE_IT_KEY'] = 'rtr_metrum_acme-it_acme-it_test_run3_' + secrets.token_urlsafe(32)
    secure.apply(control, ctl, env['TRACE_IT_KEY'], hash_state)
    cfg['callers'].append(control)
    env.pop('TRACE_SUPPORT_KEY', None)
    return cfg


def agent(task):
    text = task['initial_messages'][0].get('content', '')
    for name in AGENTS:
        if text.startswith('You are the '+name+' agent.') or text.startswith('You are a '+name+' agent '): return name
    if task['class'] == 'computed_letter': return AGENTS[task['seed'] % len(AGENTS)]
    raise ValueError('unrecognized preregistered agent persona')


def collect(args, env, selected):
    os.environ.update({k: v for k, v in env.items() if k.startswith('TRACE_')})
    ledger = Ledger()
    base = 'http://127.0.0.1:18410'
    full.private_json(args.out/'progress.json', {'status': 'incomplete', 'reason': 'collection not finished; reconcile provider charges before retry'})
    admission = []
    for name in (*AGENTS, 'control'):
        key = env['TRACE_IT_KEY'] if name == 'control' else env['TRACE_AGENT_'+name.upper()]
        admission.extend(full.preflight(base+'/v1', key, ['support-agent'], caller=name,
            request=lambda payload, key=key: full.routed(base, key, payload, ledger)))
    full.private_json(args.out/'preflight.json', admission)
    boundaries = [full.collect.now()]
    counts = {name: 0 for name in AGENTS}; controls = []; completed = 0
    with (args.out/'episode-ledger.jsonl').open('w') as log:
        for split, tasks in selected.items():
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
                futures = {executor.submit(full.episode, base, env['TRACE_AGENT_'+agent(task).upper()], task, ledger): task for task in tasks}
                try:
                    for future in concurrent.futures.as_completed(futures):
                        task = futures[future]; result = future.result(); name = agent(task)
                        if not result['captured']: raise RuntimeError('opted-in capture missing')
                        result.update(split=split, agent=name); counts[name] += 1; completed += 1
                        log.write(json.dumps(result)+'\n'); log.flush()
                        if completed % 50 == 0:
                            full.private_json(args.out/'progress.json', {'status': 'incomplete', 'completed': completed, 'calls': ledger.calls,
                                'conservative_usage_estimate_usd': ledger.spent, 'agent_groups': counts,
                                'reason': 'partial estimate; reconcile provider charges before retry'})
                except Exception:
                    ledger.abort.set()
                    for future in futures: future.cancel()
                    raise
            for kind in full.MATRIX[split]:
                task = next(t for t in tasks if t['class'] == kind)
                result = full.episode(base, env['TRACE_IT_KEY'], task, ledger)
                if result['captured']: raise RuntimeError('negative-control capture was enabled')
                result.update(split=split, agent='uncaptured-control'); controls.append(result)
                log.write(json.dumps(result)+'\n'); log.flush()
            time.sleep(.5); boundaries.append(full.collect.now())
    source = {'license': 'Apache-2.0', 'generator': 'run3/build_pool.py', 'seed_file_sha256': freeze.SEED_SHA,
              'selected_plan_sha256': freeze.PLAN_SHA, 'heldout_gold_sha256': freeze.GOLD_SHA,
              'purpose': 'synthetic source references are checker-only; training answers are new router captures'}
    filters = {'project': 'acme-support', 'group': 'support-agent', 'from': boundaries[0],
               'validation_from': boundaries[1], 'test_from': boundaries[2], 'to': boundaries[3],
               'success_only': True, 'min_quality': 1, 'teacher_terms_decision': full.collect.DECISION, 'task_source': source}
    full.private_json(args.out/'export-request.json', filters)
    _, _, raw = full.collect.request(base, '/admin/training/export', filters)
    manifest = full.collect.validate_archive(raw); dataset = args.out/'dataset'; dataset.mkdir(mode=0o700)
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        for name in ('manifest.json', 'train.jsonl', 'val.jsonl', 'test.jsonl'):
            (dataset/name).write_bytes(archive.read(name)); (dataset/name).chmod(0o600)
    rows = {split: [json.loads(line) for line in (dataset/(split+'.jsonl')).read_text().splitlines()] for split in full.MATRIX}
    if len(rows['test']) != len(selected['test']) or {r['provenance']['task_hash'] for r in rows['test']} != {t['task_hash'] for t in selected['test']}:
        raise RuntimeError('heldout coverage mismatch')
    control_ids = {hashlib.sha256(i.encode()).hexdigest() for result in controls for i in result['request_ids']}
    if any(r['provenance']['trace_hash'] in control_ids for group in rows.values() for r in group): raise RuntimeError('control trace exported')
    with sqlite3.connect(args.out/'usage.sqlite') as db:
        if db.execute("SELECT COUNT(*) FROM request_content_captures WHERE caller_id='acme-it-prod'").fetchone()[0]:
            raise RuntimeError('control trace captured')
    full.private_json(args.out/'delivery.json', {'dataset_hash': manifest['dataset_hash'],
        'manifest_sha256': full.sha(dataset/'manifest.json'), 'counts': {s: len(v) for s, v in rows.items()},
        'test_teacher_failures': sum(not r['outcome']['task_success'] for r in rows['test']), 'agent_groups': counts,
        'control_capture_count': 0, 'usage_derived_known_cost_usd': ledger.known_usage_usd,
        'unknown_usage_calls': ledger.unknown_usage_calls, 'reservation_estimate_usd': ledger.spent,
        'heldout_gold_sha256': freeze.GOLD_SHA})
    full.private_json(args.out/'progress.json', {'status': 'complete', 'completed': completed, 'calls': ledger.calls})
    print(json.dumps({'status': 'complete', 'counts': {s: len(v) for s, v in rows.items()},
                      'agent_groups': counts, 'conservative_usage_estimate_usd': ledger.spent}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--pool', required=True, type=Path); parser.add_argument('--out', required=True, type=Path)
    parser.add_argument('--binary', required=True, type=Path); parser.add_argument('--execute', action='store_true')
    args = parser.parse_args()
    if not args.execute: parser.error('explicit --execute required; this performs paid provider calls')
    if not os.environ.get('FIREWORKS_API_KEY'): parser.error('provider credential unavailable')
    price_age = (datetime.now(timezone.utc).date() - date(2026, 10, 28)).days
    if not 0 <= price_age <= 31: parser.error('dated provider price requires review before collection')
    def stop(_signal, _frame): raise KeyboardInterrupt('collection interrupted')
    signal.signal(signal.SIGTERM, stop)
    os.umask(0o077); args.out = args.out.resolve(); args.out.mkdir(mode=0o700, parents=False, exist_ok=False)
    selected = freeze.freeze(args.pool, args.out/'plan')
    env = secure.minimal_env(); env['FIREWORKS_API_KEY'] = os.environ['FIREWORKS_API_KEY']
    with tempfile.TemporaryDirectory(prefix='run3-caller-', dir='/dev/shm') as temp:
        cfg = configure(args.out, env, Path(temp))
    full.private_json(args.out/'config.json', cfg)
    # The encryption key is required for export; it remains only in process
    # environment, so interrupted runs cannot be resumed or re-exported.
    router = None
    with (args.out/'router.log').open('w') as log:
        try:
            router = subprocess.Popen([str(args.binary.resolve()), '-config', str(args.out/'config.json')], env=env, stdout=log, stderr=log)
            for _ in range(120):
                if router.poll() is not None: raise RuntimeError('router startup failed')
                try: urllib.request.urlopen('http://127.0.0.1:18410/healthz', timeout=1).close(); break
                except OSError: time.sleep(.25)
            else: raise RuntimeError('router readiness timeout')
            collect(args, env, selected)
        finally:
            if router:
                router.terminate()
                try: router.wait(timeout=30)
                except subprocess.TimeoutExpired: router.kill(); router.wait()


if __name__ == '__main__': main()
