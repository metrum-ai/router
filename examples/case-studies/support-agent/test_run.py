#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Local mock-router end-to-end exercise. No paid model calls."""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
import uuid

from backend import ORDERS, credit


class MockRouter(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        oid = data['messages'][1]['content'].split('order ')[1].split()[0]
        tool_messages = [m for m in data['messages'] if m['role'] == 'tool']
        turn = len(tool_messages)
        if turn < 4:
            name = ['lookup_order', 'calculate_credit', 'apply_credit', 'create_ticket'][turn]
            args = {'order_id': oid}
            if name == 'apply_credit': args['credit_usd'] = credit(ORDERS[oid])
            message = {'role': 'assistant', 'content': None, 'tool_calls': [
                {'id': f'call-{turn}', 'type': 'function', 'function': {'name': name, 'arguments': json.dumps(args)}}]}
        else:
            message = {'role': 'assistant', 'content': json.dumps({'order_id': oid, 'credit_usd': credit(ORDERS[oid]), 'ticket_id': 'CASE-' + oid})}
        body = json.dumps({'choices': [{'message': message}]}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.send_header('X-Request-Id', uuid.uuid4().hex)
        self.end_headers()
        self.wfile.write(body)


class RunnerTests(unittest.TestCase):
    def test_six_paired_trials_using_mock(self):
        server = ThreadingHTTPServer(('127.0.0.1', 0), MockRouter)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            with tempfile.TemporaryDirectory() as d:
                token = Path(d)/'token'
                token.write_text('test-only-token')
                out = Path(d)/'results.jsonl'
                script = Path(__file__).with_name('run.py')
                cmd = [sys.executable, str(script), '--url', f'http://127.0.0.1:{server.server_port}', '--token-file', str(token), '--output', str(out)]
                p = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
                self.assertEqual(p.returncode, 0, p.stderr)
                rows = [json.loads(line) for line in out.read_text().splitlines()]
                self.assertEqual(len(rows), 6)
                self.assertTrue(all(r['passed'] and len(r['requests']) == 5 for r in rows))
                self.assertNotIn('test-only-token', out.read_text() + p.stdout + p.stderr)
                self.assertNotEqual(subprocess.run(cmd, capture_output=True, timeout=30).returncode, 0)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == '__main__':
    unittest.main()
