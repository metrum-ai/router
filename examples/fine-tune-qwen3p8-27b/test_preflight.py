# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
import preflight


class PreflightTests(unittest.TestCase):
    def test_every_example_target_and_route_once(self):
        calls=[]
        def request(payload):
            calls.append(payload['model'])
            self.assertEqual(payload['messages'][0]['content'],'PREFLIGHT_ONLY: reply OK.')
            return 200,{'X-Request-Id':'req_synthetic'},b'PRIVATE_MODEL_RESPONSE'
        receipt=preflight.preflight('http://localhost/v1','fixture-key',
            ['qwen3p8-27b-ft','qwen3p8-27b-ft-shadow','qwen3p8-27b-ft'],request=request)
        self.assertEqual(len(calls),5)
        self.assertEqual(len(set(calls)),5)
        self.assertNotIn('fixture-key',json.dumps(receipt))
        self.assertNotIn('PRIVATE_MODEL_RESPONSE',json.dumps(receipt))
        self.assertTrue(all(r['status']==200 for r in receipt))

    def test_capability_and_rate_limiting_fail_before_next_route(self):
        for status in [400,401,403,429]:
            calls=[]
            def request(payload):calls.append(payload);return status,{},b'PRIVATE_ERROR'
            with self.assertRaisesRegex(preflight.PreflightError,'abort whole run'):
                preflight.preflight('http://localhost/v1','fixture-key',['first','second'],request=request)
            self.assertEqual(len(calls),1)
        with self.assertRaises(preflight.PreflightError):preflight.preflight('http://localhost/v1','',['first'])

    def test_grader_preflight_failure_prevents_promptfoo_batch(self):
        path=Path(__file__).parent/'eval/run_eval.py'
        spec=importlib.util.spec_from_file_location('eval_preflight_test',path)
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);token=root/'token';token.write_text('fixture-key')
            tests=root/'tests.yaml';tests.write_text('- assert:\n    - type: llm-rubric\n      value: synthetic\n')
            def admission(base,key,models):
                if models==['frontier-ref']:raise preflight.PreflightError('preflight HTTP400: unsupported grader')
                return []
            argv=['run_eval.py','--tests',str(tests),'--token-file',str(token),'--out',str(root/'results.json'),'--groups','qwen3p8-27b-base']
            with patch('sys.argv',argv),patch.object(module,'preflight',side_effect=admission) as probe,patch.object(module.subprocess,'run') as child:
                with self.assertRaisesRegex(preflight.PreflightError,'HTTP400'):module.main()
                self.assertEqual(probe.call_count,2)
                child.assert_not_called()
                self.assertFalse((root/'results.json').exists())

    def test_load_launcher_authenticated_400_stops_before_batch(self):
        received=[]
        class Reject(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_POST(self):
                received.append((self.headers.get('Authorization'),json.loads(self.rfile.read(int(self.headers['Content-Length'])))))
                self.send_response(400);self.end_headers();self.wfile.write(b'{"error":"unsupported_modality"}')
        server=ThreadingHTTPServer(('127.0.0.1',18460),Reject)
        thread=threading.Thread(target=server.serve_forever);thread.start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                root=Path(directory);token=root/'token';token.write_text('fixture-key')
                path=Path(__file__).parent/'eval/load.py'
                spec=importlib.util.spec_from_file_location('load_preflight_test',path)
                module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
                argv=['load.py','--token-file',str(token),'--api-base','http://127.0.0.1:18460/v1','--model','synthetic-route','--hourly-usd','1','--requests','32','--out',str(root/'results.json')]
                with patch('sys.argv',argv),self.assertRaisesRegex(preflight.PreflightError,'HTTP400'):
                    module.main()
                self.assertFalse((root/'results.json').exists())
            self.assertEqual(len(received),1)
            self.assertEqual(received[0][0],'Bearer fixture-key')
            self.assertFalse(received[0][1]['stream'])
        finally:server.shutdown();server.server_close();thread.join()

if __name__=='__main__':unittest.main()
