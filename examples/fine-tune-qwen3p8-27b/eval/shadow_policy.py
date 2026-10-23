#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Safe loopback recommendation-only policy; never duplicates model inference."""
import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def recommend(payload):
    targets = payload.get('targets', [])
    if len(targets) < 2:
        raise ValueError('shadow example requires baseline and FT targets')
    return {'targetIndex': 1, 'classLabel': 'ft-onprem-shadow'}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_POST(self):
        try:
            length = int(self.headers.get('Content-Length','0'))
            if length > 65536 or self.path != '/route':
                raise ValueError('invalid request')
            response = recommend(json.loads(self.rfile.read(length)))
            status = 200
        except (ValueError, TypeError):
            response = {'error':'invalid policy request'};status = 400
        raw = json.dumps(response).encode()
        self.send_response(status);self.send_header('Content-Type','application/json')
        self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--port',type=int,default=18534)
    a = p.parse_args()
    ThreadingHTTPServer(('127.0.0.1',a.port),Handler).serve_forever()
