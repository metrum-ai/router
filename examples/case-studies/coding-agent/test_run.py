#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Mock router and mock SSH smoke: no generated code is executed."""
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
from pathlib import Path
import tempfile
import sys
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import uuid

import run


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        content = None if request['model'] == 'routed' and len(request['messages']) == 1 else '# mock code'
        answer = json.dumps({'choices': [{'message': {'content': content}}]}).encode()
        self.send_response(200); self.send_header('X-Request-Id', uuid.uuid4().hex)
        self.send_header('Content-Length', str(len(answer))); self.end_headers(); self.wfile.write(answer)


class RunnerTests(unittest.TestCase):
    def test_paired_runner_with_mock_judge(self):
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            with tempfile.TemporaryDirectory() as d:
                token, output = Path(d)/'caller', Path(d)/'results'
                token.write_text('test-only-token')
                argv = ['run.py', '--url', f'http://127.0.0.1:{server.server_port}', '--token-file', str(token), '--output', str(output), '--ssh-host', '127.0.0.1', '--ssh-key', 'unused', '--known-hosts', 'unused', '--image', 'python:3.13-slim@sha256:'+'a'*64]
                def fake_ssh(cmd, **kwargs):
                    source = json.loads(kwargs['input'])['source']
                    self.assertIn(source, ('', '# mock code'))
                    passed = 20 if source else 0
                    return SimpleNamespace(returncode=0, stdout=json.dumps({'status':'ok','passed':passed,'total':20,'outcomes':[bool(passed)]*20}).encode())
                out = io.StringIO()
                with patch.object(sys, 'argv', argv), patch.object(run.subprocess, 'run', fake_ssh), contextlib.redirect_stdout(out):
                    run.main()
                rows = [json.loads(line) for line in output.read_text().splitlines()]
                self.assertEqual(len(rows), 6)
                self.assertTrue(all(row['passed'] for row in rows))
                self.assertTrue(all(len(row['requests']) == 2 for row in rows if row['arm'] == 'routed'))
                self.assertNotIn('test-only-token', output.read_text()+out.getvalue())
        finally:
            server.shutdown(); server.server_close(); thread.join()


if __name__ == '__main__': unittest.main()
