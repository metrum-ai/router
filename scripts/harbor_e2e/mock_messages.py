#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Mock upstream for a Docker/Harbor adapter replay; never contacts providers."""
import http.server,json,threading,urllib.parse
class Mock(http.server.BaseHTTPRequestHandler):
 calls=0
 def log_message(self,*a):pass
 def do_POST(self):
  if urllib.parse.urlsplit(self.path).path!='/anthropic/v1/messages':self.send_error(404);return
  body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))));Mock.calls+=1
  if not body.get('stream'):self.send_error(400);return
  blocks=[b for m in body.get('messages',[]) if isinstance(m.get('content'),list) for b in m['content'] if isinstance(b,dict)]
  if not any(b.get('type')=='tool_result' for b in blocks):
   arg=json.dumps({'path':'/app/hello.txt','content':'Hello, Harbor!\n'})
   events=[('content_block_start',{'type':'content_block_start','index':0,'content_block':{'type':'tool_use','id':'toolu_replay','name':'write','input':{}}}),('content_block_delta',{'type':'content_block_delta','index':0,'delta':{'type':'input_json_delta','partial_json':arg}}),('content_block_stop',{'type':'content_block_stop','index':0})];stop='tool_use'
  else:
   events=[('content_block_start',{'type':'content_block_start','index':0,'content_block':{'type':'text','text':''}}),('content_block_delta',{'type':'content_block_delta','index':0,'delta':{'type':'text_delta','text':'Done.'}}),('content_block_stop',{'type':'content_block_stop','index':0})];stop='end_turn'
  events.insert(0,('message_start',{'type':'message_start','message':{'id':'msg_replay','type':'message','role':'assistant','model':'claude','content':[],'stop_reason':None,'stop_sequence':None,'usage':{'input_tokens':10,'output_tokens':0}}}))
  events.extend([('message_delta',{'type':'message_delta','delta':{'stop_reason':stop,'stop_sequence':None},'usage':{'output_tokens':8}}),('message_stop',{'type':'message_stop'})])
  self.send_response(200);self.send_header('Content-Type','text/event-stream');self.end_headers()
  for name,event in events:self.wfile.write(('event: '+name+'\ndata: '+json.dumps(event)+'\n\n').encode());self.wfile.flush()
def start(host,port):
 s=http.server.ThreadingHTTPServer((host,port),Mock);threading.Thread(target=s.serve_forever,daemon=True).start();return s
