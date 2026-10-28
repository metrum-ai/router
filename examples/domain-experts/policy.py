#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Explicit domain-tag routing example, not a domain classifier."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import re

DOMAINS=('medical','legal','finance','code')
PATTERN=re.compile(r'\[domain:(medical|legal|finance|code)\]',re.IGNORECASE)


def choose(payload):
    text=payload.get('text')
    if not isinstance(text,str): return None
    matches={x.lower() for x in PATTERN.findall(text)}
    domain=next(iter(matches)) if len(matches)==1 else 'general'
    candidates=[i for i,t in enumerate(payload.get('targets') or []) if t.get('tier')==domain and t.get('keyConfigured') and int(t.get('weight') or 0)>0]
    if len(candidates)!=1: return None
    return {'targetIndex':candidates[0], 'fallbackIndexes':[], 'classLabel':'domain:'+domain}


class Handler(BaseHTTPRequestHandler):
    def log_message(self,*_): pass
    def do_POST(self):
        if self.path!='/route': self.send_error(404); return
        try:
            n=int(self.headers.get('Content-Length','0'))
            if n<1 or n>65536: raise ValueError('invalid length')
            result=choose(json.loads(self.rfile.read(n)))
        except (TypeError,ValueError): result=None
        data=json.dumps(result if result is not None else {'error':'target unavailable'}).encode()
        self.send_response(200 if result is not None else 503)
        self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(data)))
        self.end_headers(); self.wfile.write(data)


if __name__=='__main__': ThreadingHTTPServer(('127.0.0.1',18093),Handler).serve_forever()
