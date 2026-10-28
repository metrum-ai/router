#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
import uuid

from run import retrieve

TASKS = json.loads(Path(__file__).with_name('tasks.json').read_text())


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        text = body['messages'][1]['content']
        task = next(t for t in TASKS if t['question'] in text)
        response = json.dumps({'choices': [{'message': {'content': task['answer']}}]}).encode()
        self.send_response(200); self.send_header('X-Request-Id', uuid.uuid4().hex)
        self.send_header('Content-Length', str(len(response))); self.end_headers(); self.wfile.write(response)


class RunnerTests(unittest.TestCase):
    def test_retrieval_selects_correct_document(self):
        for task in TASKS:
            self.assertEqual(retrieve(task['question'], TASKS)[0], task['id'])

    def test_paired_mock_run(self):
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            with tempfile.TemporaryDirectory() as d:
                token, out = Path(d)/'token', Path(d)/'results.jsonl'
                token.write_text('test-only-token')
                cmd = [sys.executable, str(Path(__file__).with_name('run.py')), '--url', f'http://127.0.0.1:{server.server_port}', '--token-file', str(token), '--output', str(out)]
                p = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
                self.assertEqual(p.returncode, 0, p.stderr)
                rows = [json.loads(line) for line in out.read_text().splitlines()]
                self.assertEqual(len(rows), 10)
                self.assertTrue(all(row['passed'] for row in rows))
                self.assertNotIn('test-only-token', out.read_text() + p.stdout + p.stderr)
        finally:
            server.shutdown(); server.server_close(); thread.join()


if __name__ == '__main__': unittest.main()
