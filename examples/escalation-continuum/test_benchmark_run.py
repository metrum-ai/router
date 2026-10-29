# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch
from http.server import ThreadingHTTPServer
import benchmark_policy
import benchmark_relay
import benchmark_run

HERE=Path(__file__).resolve().parent

class Integration(unittest.TestCase):
    def test_mixed_path_against_all_loopback_fakes_without_network(self):
        binary=os.environ.get('ROUTER_TEST_BINARY')
        if not binary:self.skipTest('set ROUTER_TEST_BINARY')
        cases=benchmark_policy.load_cases();answers={benchmark_run.prompt(c):c['answer'] for c in cases}
        fake=ThreadingHTTPServer(('127.0.0.1',0),benchmark_run.make_fake(cases))
        threading.Thread(target=fake.serve_forever,daemon=True).start();self.addCleanup(fake.server_close);self.addCleanup(fake.shutdown)
        calls=[]
        class Response:
            status=200
            def __init__(self,body):self.body=body
            def getheader(self,_,default=None):return 'application/json'
            def read1(self,_):out=self.body;self.body=b'';return out
        class Connection:
            debuglevel=0
            def __init__(self,host,timeout):self.host=host
            def request(self,method,path,body,headers):
                req=json.loads(body);item=req['input'][0]['content'];text=item if isinstance(item,str) else item[0]['text']
                calls.append((self.host,method,path,headers['Authorization']))
                self.payload=json.dumps({'id':'resp_fake','object':'response','created_at':1,'status':'completed','model':'gpt-6.1-sol',
                    'output':[{'id':'msg_fake','type':'message','status':'completed','role':'assistant','content':[{'type':'output_text','text':answers[text],'annotations':[]}]}],
                    'usage':{'input_tokens':16,'output_tokens':1,'total_tokens':17}}).encode()
            def getresponse(self):return Response(self.payload)
            def close(self):pass
        original_relay=benchmark_relay.Relay;relays=[]
        class InspectRelay(original_relay):
            def __init__(self,*args,**kwargs):super().__init__(*args,**kwargs);relays.append(self)
        with tempfile.TemporaryDirectory(dir='/dev/shm') as d:
            url=f'http://127.0.0.1:{fake.server_port}/v1';args=['benchmark_run.py','--router',binary,'--backend','mixed','--allow-paid','--local-url',url,'--local-model','fake-local','--datacenter-url',url,'--datacenter-model','fake-datacenter','--state-dir',str(Path(d)/'state')]
            captured=io.StringIO()
            with patch.dict(os.environ,{'OPENAI_API_KEY':'synthetic-test-key'},clear=False),patch.object(benchmark_relay,'https_connection',Connection), \
                 patch.object(benchmark_relay,'Relay',InspectRelay),patch('sys.argv',args),contextlib.redirect_stdout(captured):
                try:benchmark_run.main()
                except RuntimeError as exc:
                    partial=Path(d)/'state/partial.jsonl'
                    detail=partial.read_text() if partial.exists() else 'no partial rows'
                    raise AssertionError(f'{exc}; partial={detail}; rejection={relays[0].last_rejection if relays else None}; keys={relays[0].last_keys if relays else None}; forwarded={len(calls)}') from exc
            data=json.loads(captured.getvalue())
            self.assertEqual(len(calls),15)
            self.assertTrue(all(c[:3]==('api.openai.com','POST','/v1/responses') for c in calls))
            # A failed frontier request must stop immediately and leave a
            # reconciliation marker rather than silently retrying.
            args[-1]=str(Path(d)/'failed-state');calls.clear()
            class FailedResponse(Response):status=500
            with patch.dict(os.environ,{'OPENAI_API_KEY':'synthetic-test-key'},clear=False),patch.object(benchmark_relay,'https_connection',Connection), \
                 patch.object(Connection,'getresponse',lambda self:FailedResponse(b'{"error":"synthetic"}')), \
                 patch.object(benchmark_relay,'Relay',InspectRelay),patch('sys.argv',args),contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(RuntimeError):benchmark_run.main()
            partial=[json.loads(line) for line in (Path(d)/'failed-state/partial.jsonl').read_text().splitlines()]
            self.assertEqual(partial[0],{'phase':'started','case':'local-01','path':'frontier-only'})
            self.assertEqual(partial[1]['phase'],'received');self.assertNotEqual(partial[1]['status'],200)
            self.assertEqual(len(calls),1);self.assertEqual(relays[-1].attempts,1)
        self.assertEqual(data['hosted_attempts'],15)
        self.assertEqual(len(data['results']),24)
        self.assertTrue(all(c[:3]==('api.openai.com','POST','/v1/responses') for c in calls))
        self.assertTrue(all(r['correct'] for r in data['results']))
        self.assertEqual({r['reason'] for r in data['results'] if r['path']=='escalation'},
            {'synthetic-simple-local','synthetic-medium-datacenter','synthetic-complex-frontier','synthetic-sensitive-local','synthetic-latency-local'})

if __name__=='__main__':unittest.main()
