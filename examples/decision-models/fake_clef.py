#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Deterministic Clef-shaped fake; it is not a model or quality evaluation."""
import json
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def log_message(self,*_):pass
    def do_POST(self):
        if self.path!='/v1/systemone':self.send_error(404);return
        try:
            n=int(self.headers.get('Content-Length','0'))
            if n<1 or n>65536:raise ValueError()
            data=json.loads(self.rfile.read(n));state=data['state'].lower()
            question=data['questions']['tier']
            if data['model']!='clef' or question['type']!='choice' or set(question['criteria'])!={'cheap','strong'}:raise ValueError()
            tier='strong' if 'prove' in state or 'theorem' in state else 'cheap'
            body=json.dumps({'answers':{'tier':{'type':'choice','choice':tier,'confidence':0.9}}}).encode()
        except (ValueError,KeyError,TypeError):self.send_error(400);return
        self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)


def start():
    import threading
    server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
    thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
    return server,thread
