# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import ExitStack
from http.server import ThreadingHTTPServer

ROOT=Path(__file__).resolve().parents[2];HERE=Path(__file__).resolve().parent

def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m

class Integration(unittest.TestCase):
    def test_mock_and_external_endpoints_with_local_fake(self):
        binary=os.environ.get('ROUTER_TEST_BINARY')
        if not binary:self.skipTest('set ROUTER_TEST_BINARY')
        env={k:v for k,v in os.environ.items() if k not in ('OPENAI_API_KEY','FIREWORKS_API_KEY','OPENROUTER_API_KEY','AWS_BEARER_TOKEN_BEDROCK')}
        def run(*args):
            p=subprocess.run([sys.executable,str(HERE/'run.py'),'--router',binary,*args],capture_output=True,text=True,env=env,timeout=120)
            self.assertEqual(p.returncode,0,p.stderr);return json.loads(p.stdout)
        result=run();self.assertEqual(len(result['results']),12);self.assertEqual(result['decisions'],'fake')
        d=load('d3_e2e',ROOT/'scripts/run_d3_policy_e2e.py');fake=d.load_fake();jev=load('jev',ROOT/'examples/external-routing-policy/fake_openjev.py');clef=load('clef_fake',HERE/'fake_clef.py')
        with ExitStack() as stack,tempfile.TemporaryDirectory() as temp:
            upstream=d.upstream_server(stack);d3=fake.start_in_thread(api_key='');stack.callback(d3.server_close);stack.callback(d3.shutdown)
            j=ThreadingHTTPServer(('127.0.0.1',0),jev.Handler);threading.Thread(target=j.serve_forever,daemon=True).start();stack.callback(j.server_close);stack.callback(j.shutdown)
            c,thread=clef.start();stack.callback(thread.join);stack.callback(c.server_close);stack.callback(c.shutdown)
            result=run('--backend','local','--local-url',f'http://127.0.0.1:{upstream.server_port}/v1','--local-model','fake-model',
                '--decisions','external','--d3-url',f'http://127.0.0.1:{d3.server_port}','--openjev-url',f'http://127.0.0.1:{j.server_port}',
                '--clef-url',f'http://127.0.0.1:{c.server_port}/v1/systemone','--state-dir',str(Path(temp)/'state'))
            self.assertEqual(result['decisions'],'external');self.assertEqual(result['backend'],'local');self.assertEqual(len(result['results']),12)
            self.assertEqual(len(upstream.calls),12)

if __name__=='__main__':unittest.main()
