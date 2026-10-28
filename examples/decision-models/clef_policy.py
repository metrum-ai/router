#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Loopback-only Clef-shaped decision adapter for the external policy API.

A compatible decision server must accept {model,state,questions} and return
{answers:{tier:{choice,confidence}}}; a shim is needed for other serving APIs.
"""
import argparse
import importlib.util
import json
import math
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse
from urllib.request import Request,build_opener,ProxyHandler

ROOT=Path(__file__).resolve().parents[2]
spec=importlib.util.spec_from_file_location('openjev_policy',ROOT/'examples/external-routing-policy/openjev_policy.py')
openjev=importlib.util.module_from_spec(spec);spec.loader.exec_module(openjev)
MAX_BYTES=65536
QUESTIONS={'tier':{'type':'choice','instructions':'Select the model tier appropriate for the request.',
                   'criteria':{'cheap':'Simple, bounded requests','strong':'Complex, high-stakes, or multi-step requests'}}}


def pick(payload,answer,floor=0.7):
    block=answer['answers']['tier'];choice=block['choice'];confidence=block['confidence']
    if choice not in ('cheap','strong') or isinstance(confidence,bool) or not isinstance(confidence,(int,float)) or not math.isfinite(confidence) or not 0<=confidence<=1:raise ValueError('invalid response')
    if confidence<floor:choice='strong'
    targets=payload['targets'];indices=[i for i,t in enumerate(targets) if t.get('tier')==choice and t.get('keyConfigured',True)]
    if not indices:raise ValueError('selected tier unavailable')
    return {'targetIndex':indices[0],'classLabel':'clef:'+choice}


def make_handler(endpoint):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*_):pass
        def do_GET(self):
            if self.path!='/health':self.send_error(404);return
            self.send_response(200);self.send_header('Content-Length','0');self.end_headers()
        def do_POST(self):
            if self.path!='/route':self.send_error(404);return
            try:
                length=int(self.headers.get('Content-Length','0'))
                if length<1 or length>MAX_BYTES:raise ValueError('bad request size')
                payload=json.loads(self.rfile.read(length));state=openjev.extract_text(payload)
                raw=json.dumps({'model':'clef','state':state,'questions':QUESTIONS}).encode()
                req=Request(endpoint,data=raw,headers={'Content-Type':'application/json'},method='POST')
                with build_opener(ProxyHandler({})).open(req,timeout=2) as r:reply=r.read(MAX_BYTES+1)
                if len(reply)>MAX_BYTES:raise ValueError('oversized answer')
                result=pick(payload,json.loads(reply));code=200
            except Exception:
                # Do not reflect server responses, request text or exception details.
                result={'error':'decision unavailable'};code=502
            body=json.dumps(result).encode();self.send_response(code);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
    return Handler


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--port',required=True,type=int);p.add_argument('--clef-url',required=True);a=p.parse_args()
    url=urlparse(a.clef_url)
    if url.scheme!='http' or url.hostname!='127.0.0.1' or url.username or url.password or url.query or url.fragment:p.error('Clef shim must be loopback HTTP with no credentials')
    ThreadingHTTPServer(('127.0.0.1',a.port),make_handler(a.clef_url)).serve_forever()

if __name__=='__main__':main()
