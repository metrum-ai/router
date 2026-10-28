#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Loopback-only example policy: prompt length is a crude complexity proxy."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def choose(payload, cheap_max=200, datacenter_max=1000):
    context = payload.get('context') or {}
    chars = context.get('textChars')
    if not isinstance(chars, int) or chars < 0: return None
    tier = 'local' if chars <= cheap_max else 'datacenter' if chars <= datacenter_max else 'frontier'
    matches = [i for i,t in enumerate(payload.get('targets') or []) if t.get('tier') == tier and t.get('keyConfigured') and int(t.get('weight') or 0) > 0]
    if len(matches) != 1: return None
    return {'targetIndex':matches[0], 'fallbackIndexes':[], 'classLabel':'length:'+tier}


class Handler(BaseHTTPRequestHandler):
    cheap_max = 200
    datacenter_max = 1000
    def log_message(self, *_): pass
    def do_POST(self):
        if self.path != '/route': self.send_error(404); return
        try:
            size=int(self.headers.get('Content-Length','0'))
            if size < 1 or size > 65536: raise ValueError('size')
            result=choose(json.loads(self.rfile.read(size)), self.cheap_max, self.datacenter_max)
        except (TypeError,ValueError): result=None
        data=json.dumps(result if result is not None else {'error':'no eligible tier'}).encode()
        self.send_response(200 if result is not None else 503)
        self.send_header('Content-Type','application/json'); self.send_header('Content-Length',str(len(data)))
        self.end_headers(); self.wfile.write(data)


if __name__ == '__main__':
    ThreadingHTTPServer(('127.0.0.1',18092),Handler).serve_forever()
