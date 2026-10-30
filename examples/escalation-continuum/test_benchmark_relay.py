# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import http.client
import json
import threading
import unittest
from unittest.mock import patch
import benchmark_relay as relay


class RelayTests(unittest.TestCase):
    def test_only_exact_synthetic_payloads_and_attempt_cap(self):
        prompt='SYNTHETIC427:local-01\nWhat is 7 + 8?\nReturn only the number.'
        server=relay.Relay('private-upstream-key','synthetic-local-token',[prompt],limit=2)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start();self.addCleanup(thread.join);self.addCleanup(server.server_close);self.addCleanup(server.shutdown)
        calls=[]
        class Response:
            status=200
            def getheader(self,_,default=None):return 'application/json'
            def read1(self,_):return b''
        class Connection:
            def __init__(self,host,timeout):self.host=host
            def request(self,method,path,body,headers):calls.append((self.host,method,path,headers['Authorization']))
            def getresponse(self):return Response()
            def close(self):pass
        def post(body,token='synthetic-local-token'):
            c=http.client.HTTPConnection('127.0.0.1',server.server_port,timeout=3)
            c.request('POST','/v1/responses',json.dumps(body),{'Authorization':'Bearer '+token,'Content-Type':'application/json'})
            response=c.getresponse();response.read();status=response.status;c.close();return status
        body={'model':'gpt-6.1-sol','input':[{'role':'user','content':[{'type':'input_text','text':prompt}]}],'max_output_tokens':32,'store':False}
        with patch.object(relay,'https_connection',Connection):
            self.assertEqual(post(body,'bad'),401)
            self.assertEqual(post({**body,'instructions':'private data'}),400)
            self.assertEqual(post({**body,'store':True}),400)
            self.assertEqual(post({k:v for k,v in body.items() if k!='store'}),400)
            self.assertEqual(post({**body,'input':[{'role':'user','content':prompt+' extra'}]}),400)
            self.assertEqual(post(body),200);self.assertEqual(post(body),200);self.assertEqual(post(body),429)
        self.assertEqual(server.attempts,2);self.assertEqual(len(calls),2)
        self.assertTrue(all(c[:3]==('api.openai.com','POST','/v1/responses') for c in calls))
        self.assertTrue(all(c[3]=='Bearer private-upstream-key' for c in calls))

if __name__=='__main__':unittest.main()
