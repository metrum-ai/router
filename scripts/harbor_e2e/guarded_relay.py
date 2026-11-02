#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Bounded Harbor provider relay; credentials read only from root-owned tmpfs.

Not an independently sufficient lease/run controller. A runner must assert the
budget reservation, lifecycle, caller isolation, and watcher before use.
"""
import http.server,json,os,pathlib,threading,urllib.error,urllib.parse,urllib.request

class Budget:
 def __init__(self,max_calls=160,max_reserve=4.0):
  self.lock=threading.Lock();self.calls=0;self.reserved=0.;self.max_calls=max_calls;self.max_reserve=max_reserve
 def take(self,n):
  # One token per byte at max relevant Haiku input/cache-write price + output cap.
  amount=n*1.25/1_000_000+512*5/1_000_000
  with self.lock:
   if self.calls>=self.max_calls or self.reserved+amount>self.max_reserve:return False
   self.calls+=1;self.reserved+=amount;return True

class Relay(http.server.BaseHTTPRequestHandler):
 protocol_version='HTTP/1.0'
 def log_message(self,*args):pass
 def do_POST(self):
  parsed=urllib.parse.urlsplit(self.path)
  provider='openai' if parsed.path in ('/openai/v1/responses','/openai/v1/chat/completions') and not parsed.query else 'anthropic' if parsed.path=='/anthropic/v1/messages' and parsed.query in ('','beta=true') else None
  try:
   length=int(self.headers.get('Content-Length','0'))
   if provider is None or not 0<length<=24000:raise ValueError('path/size')
   raw=self.rfile.read(length);body=json.loads(raw)
   cap=body.get('max_output_tokens') if parsed.path.endswith('/responses') else body.get('max_completion_tokens',body.get('max_tokens'))
   if body.get('model')!=('gpt-4.1-mini' if provider=='openai' else 'claude-haiku-4-5-20251001') or type(cap)!=int or not 0<cap<=512 or body.get('stream') not in (True,False):raise ValueError('model/cap/stream')
   if not self.server.budget.take(length):self.send_error(429);return
  except (ValueError,TypeError,json.JSONDecodeError):self.send_error(400);return
  secrets=json.loads(pathlib.Path(self.server.secrets_path).read_text())
  override=os.environ.get('RELAY_UPSTREAM_BASE','').rstrip('/')
  if override:
   url=override+('/v1/responses' if provider=='openai' and parsed.path.endswith('/responses') else '/v1/chat/completions' if provider=='openai' else '/v1/messages')
  else:
   url=('https://api.openai.com/v1/'+('responses' if parsed.path.endswith('/responses') else 'chat/completions') if provider=='openai' else 'https://api.anthropic.com/v1/messages'+('?' + parsed.query if parsed.query else ''))
  headers={'Content-Type':'application/json','Accept':'text/event-stream'}
  if provider=='openai':headers['Authorization']='Bearer '+secrets['openai']
  else:
   headers['x-api-key']=secrets['anthropic'];headers['anthropic-version']='2023-06-01'
   if self.headers.get('anthropic-beta'):headers['anthropic-beta']=self.headers['anthropic-beta']
  req=urllib.request.Request(url,data=raw,headers=headers,method='POST');status=None;usage={}
  try:
   with urllib.request.urlopen(req,timeout=90) as response:
    status=response.status;self.send_response(status);self.send_header('Content-Type',response.headers.get('Content-Type','text/event-stream'));self.end_headers()
    if not body['stream']:
     data=response.read(2_000_001)
     if len(data)>2_000_000:raise ValueError('oversized response')
     usage=json.loads(data).get('usage',{});self.wfile.write(data);self.wfile.flush()
    else:
     for line in response:
      if len(line)>1_000_000:raise ValueError('oversized stream line')
      if line.startswith(b'data: '):
       try:
        event=json.loads(line[6:]);u=event.get('usage') or event.get('message',{}).get('usage') or event.get('response',{}).get('usage')
        if isinstance(u,dict):usage.update({k:v for k,v in u.items() if type(v) is int})
       except (ValueError,AttributeError):pass
      self.wfile.write(line);self.wfile.flush()
  except urllib.error.HTTPError as e:
   status=e.code
   try:self.send_error(e.code,'upstream error')
   except OSError:pass
  except (OSError,TimeoutError,ValueError):status=status or 599
  finally:
   row={'provider':provider,'status':status,'usage':usage}
   with self.server.budget.lock:
    with open(self.server.records_path,'a') as f:f.write(json.dumps(row)+'\n');f.flush();os.fsync(f.fileno())
