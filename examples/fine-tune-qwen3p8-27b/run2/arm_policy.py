#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Loopback metadata-only same-group caller+project arm binding.

Trusted operator bindings, never headers/row-provided destinations. Unknown
callers stay teacher. Weighted arm is50/50 per request, no quality-based choice.
"""
import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import secrets


def route(context,bindings):
    caller=context.get('caller') or {}
    item=bindings.get(caller.get('tokenId'))
    providers=['teacher']
    if item and caller.get('project')==item['project']:
        providers=item['providers']
        if providers not in (['teacher'],['base'],['ft_onprem'],['ft_fireworks'],['ft_onprem','ft_fireworks']):
            raise ValueError('unregistered arm target')
    provider=providers[secrets.randbelow(len(providers))]
    matches=[i for i,t in enumerate(context['targets']) if t['provider']==provider]
    if len(matches)!=1:raise ValueError('bound target unavailable/ambiguous; no fallback')
    return {'targetIndex':matches[0],'fallbackIndexes':[],'classLabel':'frozen-arm-'+provider}


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--bindings',type=Path,required=True);p.add_argument('--port',type=int,default=18627)
    a=p.parse_args();bindings=json.loads(a.bindings.read_text())
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def do_POST(self):
            n=int(self.headers.get('Content-Length','0'))
            if n<1 or n>262144:self.send_error(400);return
            try:
                context=json.loads(self.rfile.read(n))
                if context.get('request') is not None:raise ValueError('include_request must remain false')
                result=route(context,bindings);body=json.dumps(result).encode()
                self.send_response(200);self.send_header('Content-Type','application/json')
                self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
            except Exception:self.send_error(500,'frozen arm unavailable')
    ThreadingHTTPServer(('127.0.0.1',a.port),Handler).serve_forever()


if __name__=='__main__':main()
