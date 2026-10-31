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
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer

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
        result=run();self.assertEqual(len(result['results']),8);self.assertEqual(result['decisions'],'fake')
        excluded=subprocess.run([sys.executable,str(HERE/'run.py'),'--router',binary,'--policies','openjev'],capture_output=True,text=True,env=env,timeout=30)
        self.assertNotEqual(excluded.returncode,0);self.assertIn('invalid policies',excluded.stderr)
        benchmark=run('--benchmark-dataset',str(HERE/'benchmark_dataset.json'),'--policies','d3')
        self.assertEqual(len(benchmark['results']),24)
        self.assertEqual({(r['decision'],r['mode']) for r in benchmark['results']},{('d3','shadow'),('d3','enforce')})
        for policy in ('clef',):
            single=run('--benchmark-dataset',str(HERE/'benchmark_dataset.json'),'--policies',policy)
            self.assertEqual(len(single['results']),24)
            self.assertEqual({r['decision'] for r in single['results']},{policy})
        for row in benchmark['benchmark']['summary']:
            self.assertEqual(row['count'],12);self.assertEqual(row['count'],row['successes']+row['errors'])
            self.assertIsNone(row['policy_p99_ms'])
        class Broken(BaseHTTPRequestHandler):
            def log_message(self,*_):pass
            def do_POST(self):self.send_response(503);self.send_header('Content-Length','0');self.end_headers()
        with ExitStack() as broken_stack,tempfile.TemporaryDirectory(dir='/dev/shm') as temp:
            server=ThreadingHTTPServer(('127.0.0.1',0),Broken)
            threading.Thread(target=server.serve_forever,daemon=True).start();broken_stack.callback(server.server_close);broken_stack.callback(server.shutdown)
            failed=run('--benchmark-dataset',str(HERE/'benchmark_dataset.json'),'--policies','d3','--decisions','external',
                '--d3-url',f'http://127.0.0.1:{server.server_port}','--state-dir',str(Path(temp)/'state'))
            self.assertTrue(all(r['errors']==12 and r['successes']==0 for r in failed['benchmark']['summary']))
            self.assertEqual({r['policy_outcome'] for r in failed['results']},{'error','shadow_error'})
        d=load('d3_e2e',ROOT/'scripts/run_d3_policy_e2e.py');fake=d.load_fake();clef=load('clef_fake',HERE/'fake_clef.py')
        with ExitStack() as stack,tempfile.TemporaryDirectory(dir='/dev/shm') as temp:
            upstream=d.upstream_server(stack);d3=fake.start_in_thread(api_key='');stack.callback(d3.server_close);stack.callback(d3.shutdown)
            c,thread=clef.start();stack.callback(thread.join);stack.callback(c.server_close);stack.callback(c.shutdown)
            result=run('--backend','local','--local-url',f'http://127.0.0.1:{upstream.server_port}/v1','--local-model','fake-model',
                '--decisions','external','--d3-url',f'http://127.0.0.1:{d3.server_port}',
                '--clef-url',f'http://127.0.0.1:{c.server_port}/v1/systemone','--state-dir',str(Path(temp)/'state'))
            self.assertEqual(result['decisions'],'external');self.assertEqual(result['backend'],'local');self.assertEqual(len(result['results']),8)
            self.assertEqual(len(upstream.calls),8)

if __name__=='__main__':unittest.main()
