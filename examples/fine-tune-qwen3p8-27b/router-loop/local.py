# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Launch a worktree router and run a bounded real-teacher collection.
uv run --with bcrypt --with jsonschema python local.py --binary /path/router --tasks ... --out ...
All generated state, credentials and traces stay outside Git. Stops its router.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time
import urllib.request
import urllib.error
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import sqlite3
import bcrypt
import collect


def config(out, env):
    callers, users, projects, memberships = [], [], [], []
    for team, variable in [('support', 'TRACE_SUPPORT_KEY'), ('it', 'TRACE_IT_KEY')]:
        project = 'acme-' + team
        env[variable] = secrets.token_urlsafe(32)
        users.append({'id': project, 'type': 'service_account', 'status': 'active'})
        projects.append({'id': project, 'status': 'active'})
        memberships.append({'user_id': project, 'project': project, 'role': 'developer', 'status': 'active'})
        caller = {'id': project + '-prod', 'owner_user': project, 'project': project, 'environment': 'test',
                  'token_id': 'rtr_' + project, 'token_sha256': hashlib.sha256(env[variable].encode()).hexdigest(),
                  'allow': ['support-agent'], 'rate': {'rpm': 120, 'tpm': 100000, 'concurrent': 2},
                  'quota': {'day': {'requests': 200, 'tokens': 200000}, 'month': {'requests': 1000, 'tokens': 1000000}}}
        if team == 'support':
            caller['content_capture'] = {'enabled': True, 'retention_days': 7, 'capture_request': True,
                                         'capture_response': True, 'capture_tool_calls': True,
                                         'redact_before_storage': True, 'max_capture_bytes': 1048576,
                                         'encryption': {'enabled': True, 'local_key_id': 'trace-test-key'}}
        callers.append(caller)
    env['CONTENT_CAPTURE_LOCAL_KEY'] = secrets.token_hex(32)
    env['TRACE_ADMIN_USER'] = 'trace-admin'
    env['TRACE_ADMIN_PASSWORD'] = secrets.token_urlsafe(32)
    env['TRACE_ADMIN_HASH'] = bcrypt.hashpw(env['TRACE_ADMIN_PASSWORD'].encode(), bcrypt.gensalt()).decode()
    return {'server': {'listen': '127.0.0.1:18410', 'default_model_group': 'support-agent',
        'identifiers': {'mode': 'passthrough'}, 'cache': {'enabled': False},
        'logging': {'path': str(out / 'requests.jsonl')},
        'usage_db': {'enabled': True, 'driver': 'sqlite', 'path': str(out / 'usage.sqlite'), 'migration_policy': 'auto-safe'},
        'admin_reports': {'enabled': True, 'security': {'enabled': True, 'retention_days': 7}},
        'admin_auth': {'basic': {'enabled': True, 'allow_insecure_http': True,
             'users': [{'username': 'trace-admin', 'password_hash_env': 'TRACE_ADMIN_HASH', 'subject': 'basic:trace-admin', 'domain': '*'}]},
             'authorization': {'enabled': True, 'policy': ['p, basic:trace-admin, *, content:training, export|label']}}},
        'state_path': str(out / 'state.json'),
        'providers': {'teacher': {'dialect': 'openai-chat', 'base_url': 'https://api.fireworks.ai/inference/v1',
                                 'api_key': '${FIREWORKS_API_KEY}', 'api_key_env': 'FIREWORKS_API_KEY'}},
        'models': {'support-agent': {'strategy': 'static', 'targets': [{'provider': 'teacher',
            'model': collect.DECISION['teacher'], 'context_tokens': 131072,
            'tool_support': {'openai_chat': ['tools', 'tool_choice']}}]}},
        'users': users, 'projects': projects, 'project_memberships': memberships, 'callers': callers}


def demonstrate_canary(binary, out, cfg, env):
    """Routing-control proof with a synthetic stand-in, NOT a trained model."""
    class Mock(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass
        def do_POST(self):
            self.rfile.read(int(self.headers.get('Content-Length', 0)))
            body = json.dumps({'id': 'canary-probe', 'object': 'chat.completion', 'model': 'synthetic-ft-standin',
                'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'CANARY_STANDIN'}, 'finish_reason': 'stop'}],
                'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}}).encode()
            self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers(); self.wfile.write(body)
    mock = ThreadingHTTPServer(('127.0.0.1', 18411), Mock)
    thread = threading.Thread(target=mock.serve_forever, daemon=True); thread.start()
    env['TRACE_MOCK_KEY'] = secrets.token_urlsafe(24)
    cfg['providers']['ft-canary'] = {'dialect': 'openai-chat', 'base_url': 'http://127.0.0.1:18411/v1', 'api_key_env': 'TRACE_MOCK_KEY', 'api_key': '${TRACE_MOCK_KEY}'}
    cfg['state_path'] = str(out / 'canary-state.json')
    cfg['callers'][0]['quota']['day']['requests'] = 1
    group = cfg['models']['support-agent']
    group['strategy'] = 'script'; group['script'] = str(Path(__file__).with_name('canary.ts').resolve())
    group['targets'].append({'provider': 'ft-canary', 'model': 'synthetic-ft-standin', 'context_tokens': 131072})
    path = out / 'canary-config.json'; path.write_text(json.dumps(cfg)); path.chmod(0o600)
    evidence = []
    router = None
    try:
        with (out / 'canary-router.log').open('w') as log:
            router = subprocess.Popen([str(binary.resolve()), '-config', str(path)], env=env, stdout=log, stderr=log)
            for _ in range(120):
                if router.poll() is not None:
                    raise RuntimeError('canary router startup failed')
                try:
                    urllib.request.urlopen('http://127.0.0.1:18410/healthz', timeout=1).close(); break
                except Exception:
                    time.sleep(.25)
            for variable in ('TRACE_SUPPORT_KEY', 'TRACE_IT_KEY'):
                _, headers, raw = collect.request('http://127.0.0.1:18410', '/v1/chat/completions', {
                    'model': 'support-agent', 'messages': [{'role': 'user', 'content': 'Reply only ROUTE_PROBE.'}], 'max_tokens': 64}, env[variable])
                answer = json.loads(raw)['choices'][0]['message'].get('content')
                assert (answer == 'CANARY_STANDIN') == (variable == 'TRACE_SUPPORT_KEY')
                evidence.append({'caller': variable, 'request_id': headers['X-Request-Id']})
            try:
                collect.request('http://127.0.0.1:18410', '/v1/chat/completions', {
                    'model': 'support-agent', 'messages': [{'role': 'user', 'content': 'Budget must reject this second request.'}]}, env['TRACE_SUPPORT_KEY'])
                raise AssertionError('canary bypassed caller request budget')
            except urllib.error.HTTPError as error:
                assert error.code == 429
                budget_status = error.code
    finally:
        if router is not None:
            router.terminate()
            try: router.wait(timeout=15)
            except subprocess.TimeoutExpired: router.kill(); router.wait()
        mock.shutdown(); mock.server_close(); thread.join()
    with sqlite3.connect(out / 'usage.sqlite') as db:
        for result in evidence:
            provider, model = db.execute('SELECT target_provider,target_model FROM request_usage WHERE request_id=?', (result['request_id'],)).fetchone()
            assert provider == ('ft-canary' if result['caller'] == 'TRACE_SUPPORT_KEY' else 'teacher')
            result.update(provider=provider, model=model)
    (out / 'canary-evidence.json').write_text(json.dumps({'synthetic_standin_not_trained': True, 'same_group': 'support-agent', 'caller_budget_rejection': budget_status, 'results': evidence}, indent=2))


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--binary', type=Path, required=True)
    p.add_argument('--tasks', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--per-split', type=int, default=2)
    p.add_argument('--include-text', action='store_true')
    p.add_argument('--task-source-json', type=Path)
    a = p.parse_args()
    if not os.environ.get('FIREWORKS_API_KEY'):
        p.error('FIREWORKS_API_KEY required in environment')
    a.out = a.out.resolve(); a.out.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    cfg = config(a.out, env)
    path = a.out / 'config.json'
    path.write_text(json.dumps(cfg, indent=2)); path.chmod(0o600)
    with (a.out / 'router.log').open('w') as log:
        router = subprocess.Popen([str(a.binary.resolve()), '-config', str(path)], env=env, stdout=log, stderr=log)
        try:
            for _ in range(120):
                if router.poll() is not None:
                    raise RuntimeError('router startup failed; inspect operator-owned router.log')
                try:
                    urllib.request.urlopen('http://127.0.0.1:18410/healthz', timeout=1).close()
                    break
                except Exception:
                    time.sleep(.25)
            command = [sys.executable, str(Path(__file__).with_name('collect.py')), '--tasks', str(a.tasks.resolve()),
                       '--out', str(a.out / 'dataset'), '--per-split', str(a.per_split)]
            if a.include_text:
                command.append('--include-text')
            if a.task_source_json:
                command.extend(['--task-source-json', str(a.task_source_json.resolve())])
            subprocess.run(command, env=env, check=True)
        finally:
            router.terminate()
            try:
                router.wait(timeout=15)
            except subprocess.TimeoutExpired:
                router.kill(); router.wait()
    demonstrate_canary(a.binary, a.out, cfg, env)

if __name__ == '__main__':
    main()
