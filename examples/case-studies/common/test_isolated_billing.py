# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential isolation uses synthetic keys and local mocks only."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import URLError
from urllib.request import Request, urlopen
import yaml

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
NAMES = ('OPENAI_FRONTIER_API_KEY', 'OPENAI_ROUTED_API_KEY', 'FIREWORKS_ROUTED_API_KEY')


def env_with_keys(*keys):
    names = ('PATH', 'HOME', 'TMPDIR', 'SYSTEMROOT', 'LD_LIBRARY_PATH', 'GOCACHE', 'GOMODCACHE', 'GOROOT', 'GOPATH', 'GOPROXY', 'GOTOOLCHAIN')
    env = {k: os.environ[k] for k in names if k in os.environ}
    env.update(zip(NAMES, keys))
    return env


class IsolatedBillingTests(unittest.TestCase):
    def run_setup(self, state, env):
        return subprocess.run([sys.executable, str(HERE/'setup.py'), '--config', str(HERE/'config.example.json'),
            '--state-dir', str(state), '--isolate-billing-arms'], cwd=ROOT, env=env,
            capture_output=True, text=True, timeout=300)

    def test_missing_or_shared_keys_rejected_before_state_creation(self):
        with tempfile.TemporaryDirectory() as temp:
            for i, keys in enumerate((('', 'routed', 'fireworks'), ('same', 'same', 'fireworks'), ('frontier ', 'routed', 'fireworks'))):
                state = Path(temp)/str(i)
                result = self.run_setup(state, env_with_keys(*keys))
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(state.exists())
                for key in keys:
                    if key: self.assertNotIn(key, result.stderr)

    def test_provider_references_are_separated_without_persisting_values(self):
        keys = ('synthetic-frontier-key', 'synthetic-routed-key', 'synthetic-fireworks-key')
        env = env_with_keys(*keys)
        with tempfile.TemporaryDirectory() as temp:
            state = Path(temp)/'state'
            result = self.run_setup(state, env)
            self.assertEqual(result.returncode, 0, result.stderr)
            raw = (state/'config.yaml').read_text()
            cfg = yaml.safe_load(raw)
            for key in keys:
                self.assertNotIn(key, raw)
                self.assertNotIn(key, result.stdout + result.stderr)
            self.assertEqual(cfg['models']['frontier']['targets'][0]['provider'], 'openai-frontier')
            self.assertEqual([x['provider'] for x in cfg['models']['routed']['targets']], ['fireworks', 'openai-routed'])
            for name, var in (('openai-frontier', NAMES[0]), ('openai-routed', NAMES[1]), ('fireworks', NAMES[2])):
                self.assertEqual(cfg['providers'][name]['api_key_env'], var)
                self.assertEqual(cfg['providers'][name]['api_key'], '${'+var+'}')
            validated = subprocess.run(['go', 'run', './cmd/metrum-ai-routerctl', 'config', 'validate', '--config', str(state/'config.yaml')],
                cwd=ROOT, env=env, capture_output=True, text=True, timeout=300)
            self.assertEqual(validated.returncode, 0, validated.stderr)
            for key in keys: self.assertNotIn(key, validated.stdout + validated.stderr)

    def test_loopback_router_sends_correct_key_for_each_arm(self):
        keys = ('synthetic-frontier-key', 'synthetic-routed-key', 'synthetic-fireworks-key')
        env = env_with_keys(*keys)
        seen = []
        class Upstream(BaseHTTPRequestHandler):
            def log_message(self, *_): pass
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                seen.append(self.headers.get('Authorization'))
                response = {'id':'mock','object':'chat.completion','model':body['model'],
                    'choices':[{'index':0,'message':{'role':'assistant','content':'ok'},'finish_reason':'stop'}],
                    'usage':{'prompt_tokens':1,'completion_tokens':1,'total_tokens':2}}
                raw = json.dumps(response).encode()
                self.send_response(200); self.send_header('Content-Type','application/json')
                self.send_header('Content-Length', str(len(raw))); self.end_headers(); self.wfile.write(raw)
        upstream = ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
        thread = threading.Thread(target=upstream.serve_forever, daemon=True); thread.start()
        process = None
        try:
            with tempfile.TemporaryDirectory() as temp:
                state = Path(temp)/'state'; self.assertEqual(self.run_setup(state, env).returncode, 0)
                cfg_path = state/'config.yaml'; cfg = yaml.safe_load(cfg_path.read_text())
                with socket.socket() as sock:
                    sock.bind(('127.0.0.1', 0)); router_port = sock.getsockname()[1]
                cfg['server']['listen'] = f'127.0.0.1:{router_port}'
                for provider in cfg['providers'].values():
                    provider.update(base_url=f'http://127.0.0.1:{upstream.server_port}/v1', dialect='openai-chat')
                targets = cfg['models']['routed']['targets']
                cfg['models']['routed'] = {'strategy':'static', 'targets':[targets[1]]}
                cfg['models']['cheap-test'] = {'strategy':'static', 'targets':[targets[0]]}
                cfg['callers'][0]['allow'].append('cheap-test')
                cfg_path.write_text(yaml.safe_dump(cfg, sort_keys=False)); cfg_path.chmod(0o600)
                binary = Path(temp)/'router'
                subprocess.run(['go','build','-o',str(binary),'./cmd/metrum-ai-router'], cwd=ROOT, env=env,
                    check=True, timeout=300, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                process = subprocess.Popen([str(binary),'--config',str(cfg_path)], cwd=ROOT, env=env,
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                base = f'http://127.0.0.1:{router_port}'
                # CI runs multiple Go builds and test shards concurrently; use a
                # wall-clock deadline rather than a fixed number of attempts.
                deadline = time.monotonic() + 90
                while time.monotonic() < deadline:
                    if process.poll() is not None: self.fail('router exited before readiness')
                    try:
                        with urlopen(base+'/readyz', timeout=1) as response:
                            if response.status == 200: break
                    except (URLError, TimeoutError): pass
                    time.sleep(.1)
                else: self.fail('router did not become ready within 90 seconds')
                token = (state/'caller').read_text().strip()
                for group in ('frontier','routed','cheap-test'):
                    data = json.dumps({'model':group,'messages':[{'role':'user','content':'synthetic'}],'max_tokens':2}).encode()
                    req = Request(base+'/v1/chat/completions', data=data, headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
                    with urlopen(req, timeout=30) as response: self.assertEqual(response.status, 200)
                self.assertEqual(seen, ['Bearer '+key for key in keys])
        finally:
            if process is not None and process.poll() is None:
                process.terminate()
                try: process.wait(timeout=15)
                except subprocess.TimeoutExpired: process.kill(); process.wait()
            upstream.shutdown(); upstream.server_close(); thread.join()

if __name__ == '__main__': unittest.main()
