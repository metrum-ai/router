# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
import os
from pathlib import Path
import re
from types import SimpleNamespace
import subprocess
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import urlopen

import collector


class FakeProvider(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        if not data.get('tools') or any(m['role'] == 'tool' for m in data['messages']):
            message = {'role': 'assistant', 'content': 'Completed.'}; finish = 'stop'
        else:
            tool = data['tools'][0]['function']
            param = next(iter(tool['parameters']['properties']))
            value = re.search(r'case-\d{4}', data['messages'][-1]['content']).group()
            message = {'role': 'assistant', 'content': None, 'tool_calls': [{'id': 'fake-call', 'type': 'function',
                'function': {'name': tool['name'], 'arguments': json.dumps({param: value})}}]}; finish = 'tool_calls'
        body = json.dumps({'id': 'fake', 'object': 'chat.completion', 'model': 'fake',
            'choices': [{'index': 0, 'message': message, 'finish_reason': finish}],
            'usage': {'prompt_tokens': 20, 'completion_tokens': 10, 'total_tokens': 30}}).encode()
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.send_header('Content-Length', str(len(body)))
        self.end_headers(); self.wfile.write(body)


class LocalIntegration(unittest.TestCase):
    def test_real_router_multi_key_capture_and_negative_control(self):
        binary = os.environ.get('ROUTER_TEST_BINARY')
        if not binary: self.skipTest('set ROUTER_TEST_BINARY')
        server = ThreadingHTTPServer(('127.0.0.1', 0), FakeProvider)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory(dir='/dev/shm') as scratch:
                root = Path(directory); env = {k:v for k,v in os.environ.items() if k not in ('FIREWORKS_API_KEY','OPENAI_API_KEY','OPENROUTER_API_KEY')}
                cfg = collector.configure(root, env, Path(scratch))
                cfg['providers']['teacher'] = {'dialect': 'openai-chat', 'base_url': f'http://127.0.0.1:{server.server_port}/v1', 'api_key': 'synthetic-only'}
                cfg['models']['support-agent']['targets'][0]['model'] = 'fake'
                path = root/'config.json'; path.write_text(json.dumps(cfg)); path.chmod(0o600)
                with (root/'router.log').open('w') as log:
                    process = subprocess.Popen([binary, '-config', str(path)], env=env, stdout=log, stderr=log)
                    try:
                        for _ in range(120):
                            if process.poll() is not None: self.fail('router exited during startup')
                            try: urlopen('http://127.0.0.1:18410/healthz', timeout=1).close(); break
                            except OSError: time.sleep(.25)
                        else: self.fail('router startup timed out')
                        tool = {'type':'function','function': {'name':'lookup','parameters': {'type':'object',
                            'properties': {'ticket': {'type':'string'}},'required':['ticket']}}}
                        with patch.dict(os.environ, {k:v for k,v in env.items() if k.startswith('TRACE_')}):
                            for i, name in enumerate((*collector.AGENTS, 'control')):
                                task = {'initial_messages':[{'role':'user','content':'Look up case-1234.'}], 'tools':[tool],
                                    'expected_calls':[{'name':'lookup','arguments':{'ticket':'case-1234'}}], 'expected_final':'',
                                    'task_hash':format(i+1,'064x'), 'class':'native_tool', 'seed':i}
                                key = env['TRACE_IT_KEY'] if name == 'control' else env['TRACE_AGENT_'+name.upper()]
                                result = collector.full.episode('http://127.0.0.1:18410', key, task, collector.Ledger())
                                self.assertTrue(result['success']); self.assertEqual(result['captured'], name != 'control')
                            selected = {}
                            for i, split in enumerate(('train','val','test')):
                                value = f'case-{i+2000:04d}'
                                initial = [{'role':'system','content':'You are the support agent. Use the available mock tool when needed.'},
                                           {'role':'user','content':f'Look up {value}.'}]
                                task = {'initial_messages':initial, 'tools':[tool], 'expected_calls':[{'name':'lookup','arguments':{'ticket':value}}],
                                        'expected_final':'', 'task_hash':collector.full.collect.digest({'initial':initial,'tools':[tool]}),
                                        'class':'native_tool', 'seed':i}
                                selected[split] = [task]
                            with patch.object(collector.full, 'MATRIX', {s:{'native_tool':1} for s in selected}):
                                collector.collect(SimpleNamespace(out=root), env, selected)
                            delivery = json.loads((root/'delivery.json').read_text())
                            self.assertEqual(delivery['counts'], {'train':1,'val':1,'test':1})
                            self.assertEqual(delivery['control_capture_count'], 0)
                    finally:
                        process.terminate()
                        try: process.wait(timeout=15)
                        except subprocess.TimeoutExpired: process.kill(); process.wait()
        finally:
            server.shutdown(); server.server_close(); thread.join(timeout=5)


if __name__ == '__main__': unittest.main()
