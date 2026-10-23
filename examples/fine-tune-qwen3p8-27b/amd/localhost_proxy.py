#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""AMD fixed SDK relay18596 -> working-tree router18592; no retry/redirect.

Same transport semantics as the shared localhost_proxy.py; operator ports only.
No request bodies, headers, caller credentials or answers are logged.
"""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LISTEN = ('127.0.0.1', 18596)
TARGET = ('127.0.0.1', 18592)


def response_headers(response):
    """Only fixed names and validated printable ASCII may reach send_header.

    Ignore every other upstream header, including malformed names. Reject folded
    values, CR/LF, tabs, NUL, DEL, C1 controls and non-ASCII before writing status.
    Explicit CR/LF removal also keeps the serialization boundary self-contained.
    """
    headers = {}
    for name in ('Content-Type', 'X-Request-Id'):
        value = response.getheader(name)
        if value is not None:
            if not isinstance(value, str) or any(not 32 <= ord(c) <= 126 for c in value):
                raise ValueError('invalid upstream response header')
            headers[name] = value.replace('\r', '').replace('\n', '')
    return headers


def forward_request(method, path, body, headers):
    if path not in ('/v1/chat/completions', '/v1/models'):
        raise ValueError('unsupported relay path')
    if len(body) > 4194304:
        raise ValueError('bounded request required')
    headers = {k:v for k,v in headers.items() if k.lower() in ('authorization','content-type','accept')}
    connection = http.client.HTTPConnection(*TARGET, timeout=180)
    try:
        connection.request(method, path, body=body, headers=headers)
        response = connection.getresponse()
        if 300 <= response.status < 400:
            raise ValueError('router redirect forbidden')
        data = response.read()
        safe_headers = response_headers(response)
        return response.status, safe_headers, data
    finally:
        connection.close()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass
    def do_GET(self):
        self.forward()
    def do_POST(self):
        self.forward()
    def forward(self):
        if self.path not in ('/v1/chat/completions','/v1/models'):
            self.send_error(404); return
        try:
            n = int(self.headers.get('Content-Length','0'))
            if n < 0 or n > 4194304:
                raise ValueError('bounded request required')
            status, headers, data = forward_request(self.command, self.path, self.rfile.read(n), self.headers)
            self.send_response(status)
            if 'Content-Type' in headers:
                self.send_header('Content-Type', headers['Content-Type'])
            if 'X-Request-Id' in headers:
                self.send_header('X-Request-Id', headers['X-Request-Id'])
            self.send_header('Content-Length', str(len(data)))
            self.end_headers(); self.wfile.write(data)
        except Exception:
            self.send_error(502,'local router transport failed; no retry/redirect')


if __name__ == '__main__':
    ThreadingHTTPServer(LISTEN, Handler).serve_forever()
