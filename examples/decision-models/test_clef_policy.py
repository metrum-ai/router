# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
import threading
import unittest
from contextlib import ExitStack
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from urllib.request import Request,urlopen
import clef_policy


class TestClefPolicy(unittest.TestCase):
    def setUp(self):self.payload={'targets':[{'tier':'cheap','keyConfigured':True},{'tier':'strong','keyConfigured':True}]}
    def test_tiers_and_low_confidence_escalation(self):
        def answer(choice,confidence):return {'answers':{'tier':{'choice':choice,'confidence':confidence}}}
        self.assertEqual(clef_policy.pick(self.payload,answer('cheap',.9))['targetIndex'],0)
        self.assertEqual(clef_policy.pick(self.payload,answer('strong',.9))['targetIndex'],1)
        self.assertEqual(clef_policy.pick(self.payload,answer('cheap',.2))['targetIndex'],1)
    def test_custom_model_reaches_decision_endpoint(self):
        seen=[]
        class Decision(BaseHTTPRequestHandler):
            def log_message(self,*_):pass
            def do_POST(self):
                seen.append(json.loads(self.rfile.read(int(self.headers['Content-Length']))))
                data=json.dumps({'answers':{'tier':{'choice':'cheap','confidence':0.9}}}).encode()
                self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
        with ExitStack() as stack:
            def start(handler):
                server=ThreadingHTTPServer(('127.0.0.1',0),handler)
                threading.Thread(target=server.serve_forever,daemon=True).start()
                stack.callback(server.server_close);stack.callback(server.shutdown)
                return server
            upstream=start(Decision)
            adapter=start(clef_policy.make_handler(f'http://127.0.0.1:{upstream.server_port}/v1/systemone',model='Cloudflare/clef-flash'))
            payload={**self.payload,'request':{'messages':[{'role':'user','content':'hello'}]}}
            req=Request(f'http://127.0.0.1:{adapter.server_port}/route',json.dumps(payload).encode(),{'Content-Type':'application/json'})
            with urlopen(req,timeout=3) as response:self.assertEqual(response.status,200)
            self.assertEqual(seen[0]['model'],'Cloudflare/clef-flash')

    def test_reject_malformed_and_ineligible(self):
        for value in (-.1,1.1,float('nan'),True,'0.9'):
            with self.assertRaises(ValueError):clef_policy.pick(self.payload,{'answers':{'tier':{'choice':'cheap','confidence':value}}})
        self.payload['targets'][1]['keyConfigured']=False
        with self.assertRaises(ValueError):clef_policy.pick(self.payload,{'answers':{'tier':{'choice':'strong','confidence':.9}}})

if __name__=='__main__':unittest.main()
