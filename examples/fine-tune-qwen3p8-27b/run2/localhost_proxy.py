#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""SDK loopback-only fixed router relay: no redirects/proxies, no content logs.

http.client never follows redirects. Reject all3xx before the SDK can follow
one. Forward unchanged request bytes and request-ID response headers. No retry.
"""
import http.client
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_GET(self):self.forward()
    def do_POST(self):self.forward()
    def forward(self):
        if self.path not in ('/v1/chat/completions','/v1/models'):
            self.send_error(404);return
        try:
            n=int(self.headers.get('Content-Length','0'))
            if n<0 or n>4194304:raise ValueError('bounded request required')
            body=self.rfile.read(n)
            headers={k:v for k,v in self.headers.items() if k.lower() in ('authorization','content-type','accept')}
            connection=http.client.HTTPConnection('127.0.0.1',18625,timeout=180)
            try:
                connection.request(self.command,self.path,body=body,headers=headers)
                response=connection.getresponse()
                if 300<=response.status<400:raise ValueError('router redirect forbidden')
                data=response.read();self.send_response(response.status)
                for name in ('Content-Type','X-Request-Id'):
                    value=response.getheader(name)
                    if value:self.send_header(name,value)
                self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
            finally:connection.close()
        except Exception:self.send_error(502,'local router transport failed; no retry/redirect')


if __name__=='__main__':ThreadingHTTPServer(('127.0.0.1',18629),Handler).serve_forever()
