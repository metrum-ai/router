# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import unittest
from contextlib import ExitStack
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import urlopen

import reconcile
import run
import setup


class FakeProvider(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_POST(self):
        self.rfile.read(int(self.headers['Content-Length']))
        events = [
            {'id':'fake','object':'chat.completion.chunk','model':'fake','choices':[{'index':0,'delta':{'role':'assistant','content':'B'},'finish_reason':None}]},
            {'id':'fake','object':'chat.completion.chunk','model':'fake','choices':[{'index':0,'delta':{},'finish_reason':'stop'}]},
        ]
        body = ''.join('data: '+json.dumps(x)+'\n\n' for x in events).encode()+b'data: [DONE]\n\n'
        self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Content-Length',str(len(body)))
        self.end_headers();self.wfile.write(body)


class Integration(unittest.TestCase):
    def test_real_router_matched_arms_and_telemetry(self):
        binary=os.environ.get('ROUTER_TEST_BINARY')
        if not binary:self.skipTest('set ROUTER_TEST_BINARY')
        with ExitStack() as stack:
            root=Path(stack.enter_context(tempfile.TemporaryDirectory()));tmp=Path(stack.enter_context(tempfile.TemporaryDirectory(dir='/dev/shm')))
            state=root/'state';token_dir=tmp/'tokens';setup.build(state,token_dir)
            servers={}
            for name in (*setup.PERSONAS,'baseline'):
                server=ThreadingHTTPServer(('127.0.0.1',0),FakeProvider);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
                stack.callback(thread.join,5);stack.callback(server.server_close);stack.callback(server.shutdown);servers[name]=server
            cases=[{'id':'bio','domain':'biomedical','prompt':'Find the gene and protein','tools':[],'expected':'B'},
                   {'id':'fin','domain':'finance','prompt':'financial revenue','tools':[],'expected':'B'},
                   {'id':'math','domain':'math','prompt':'calculate the sum','tools':[],'expected':'B'},
                   {'id':'coding','domain':'coding','prompt':'implement a Python function','tools':[],'expected':'B'},
                   {'id':'ood','domain':'out-of-domain','prompt':'repeat the word violet','tools':[],'expected':'B'}]
            keys={name:(token_dir/name).read_text().strip() for name in setup.PERSONAS}
            env={k:v for k,v in os.environ.items() if k not in ('OPENAI_API_KEY','FIREWORKS_API_KEY','OPENROUTER_API_KEY')}
            for arm in ('experts','baseline'):
                path=state/(arm+'.json');cfg=json.loads(path.read_text())
                for name,server in servers.items():cfg['providers'][name]['base_url']=f'http://127.0.0.1:{server.server_port}/v1'
                path.write_text(json.dumps(cfg));path.chmod(0o600)
                with (state/(arm+'.log')).open('w') as log:
                    process=subprocess.Popen([binary,'-config',str(path)],env=env,stdout=log,stderr=log)
                    try:
                        for _ in range(120):
                            if process.poll() is not None:self.fail('router failed to start')
                            try:urlopen('http://127.0.0.1:18426/healthz',timeout=1).close();break
                            except OSError:time.sleep(.25)
                        else:self.fail('router startup timed out')
                        records,_=run.run('http://127.0.0.1:18426','expert-study',keys,cases)
                        self.assertEqual(len(records),5)
                        self.assertTrue(all(r['status']==200 and r['error'] is None for r in records),records)
                        self.assertTrue(all(r['ttft_ms'] is not None for r in records))
                        if arm=='experts':
                            for _ in range(50):
                                result=reconcile.reconcile(records,state/'experts-usage.sqlite')
                                if not result['missing_decisions']:break
                                time.sleep(.1)
                            self.assertEqual(result['correct'],5,result)
                    finally:
                        process.terminate()
                        try:process.wait(timeout=15)
                        except subprocess.TimeoutExpired:process.kill();process.wait()


if __name__=='__main__':unittest.main()
