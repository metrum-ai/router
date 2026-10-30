# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Paid-run guard: cap attempts and forward only synthetic requests to fixed OpenAI URL."""
import hmac
import http.client
import json
import threading
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer


def https_connection(host,timeout):
    return http.client.HTTPSConnection(host,timeout=timeout)


class Relay(ThreadingHTTPServer):
    daemon_threads=True
    def __init__(self,upstream_key,local_token,allowed_prompts,limit=15):
        super().__init__(('127.0.0.1',0),Handler)
        self.upstream_key=upstream_key;self.local_token=local_token;self.allowed_prompts=frozenset(allowed_prompts)
        self.limit=limit;self.attempts=0;self.lock=threading.Lock();self.last_rejection=None;self.last_keys=[]


class Handler(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*_):pass
    def do_POST(self):
        self.close_connection=True
        if self.path!='/v1/responses':return self.send_error(404)
        if not hmac.compare_digest(self.headers.get('Authorization',''),'Bearer '+self.server.local_token):return self.send_error(401)
        try:
            size=int(self.headers.get('Content-Length','0'))
            if not 0<size<=16384:raise ValueError('size')
            raw=self.rfile.read(size);body=json.loads(raw)
            self.server.last_keys=sorted(body) if isinstance(body,dict) and all(isinstance(k,str) for k in body) else []
            if not isinstance(body,dict) or body.get('model')!='gpt-6.1-sol':raise ValueError('model')
            if type(body.get('max_output_tokens')) is not int or not 1<=body['max_output_tokens']<=32:raise ValueError('output bound')
            if set(body)-{'model','input','max_output_tokens','stream','store'} or body.get('stream',False) is not False or body.get('store') is not False:raise ValueError('unsupported shape')
            items=body.get('input')
            if not isinstance(items,list) or len(items)!=1 or not isinstance(items[0],dict):raise ValueError('input')
            item=items[0]
            if set(item)-{'role','content','type'} or item.get('role')!='user' or item.get('type','message')!='message':raise ValueError('message')
            content=item.get('content')
            if isinstance(content,list):
                if len(content)!=1 or not isinstance(content[0],dict) or set(content[0])!={'type','text'} or content[0]['type']!='input_text':raise ValueError('content')
                content=content[0]['text']
            if not isinstance(content,str) or content not in self.server.allowed_prompts:raise ValueError('unknown prompt')
            with self.server.lock:
                if self.server.attempts>=self.server.limit:return self.send_error(429)
                self.server.attempts+=1  # Count before forwarding, even on errors.
        except (ValueError,TypeError,json.JSONDecodeError) as exc:
            self.server.last_rejection=str(exc) if type(exc) is ValueError and str(exc) in {'size','model','output bound','unsupported shape','input','message','content','unknown prompt'} else 'malformed'
            return self.send_error(400)
        conn=https_connection('api.openai.com',timeout=60)
        try:
            conn.request('POST','/v1/responses',body=raw,headers={'Authorization':'Bearer '+self.server.upstream_key,'Content-Type':'application/json'})
            response=conn.getresponse();self.send_response(response.status)
            self.send_header('Content-Type',response.getheader('Content-Type','application/json'));self.send_header('Connection','close');self.end_headers()
            while chunk:=response.read1(8192):self.wfile.write(chunk)
        except (OSError,http.client.HTTPException):pass
        finally:conn.close()
