# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Both fixed relays must validate before serializing upstream response headers."""
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import MagicMock, Mock, patch

ROOT = Path(__file__).parent


def load(arm):
    spec = importlib.util.spec_from_file_location('header_test_' + arm, ROOT / arm / 'localhost_proxy.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class RelayHeaderTests(unittest.TestCase):
    def handler(self, module):
        handler = object.__new__(module.Handler)
        handler.path = '/v1/chat/completions'
        handler.command = 'POST'
        handler.headers = {'Content-Length': '2'}
        handler.rfile = io.BytesIO(b'{}')
        handler.wfile = io.BytesIO()
        for name in ('send_response', 'send_header', 'end_headers', 'send_error'):
            setattr(handler, name, Mock())
        return handler

    def forward(self, arm, headers):
        module = load(arm)
        handler = self.handler(module)
        connection = MagicMock()
        response = connection.getresponse.return_value
        response.status = 200
        response.read.return_value = b'unchanged-body'
        response.getheader.side_effect = headers.get
        with patch.object(module.http.client, 'HTTPConnection', return_value=connection):
            handler.forward()
        connection.close.assert_called_once()
        return handler

    def test_allowlist_and_locally_computed_content_length(self):
        for arm in ('run2', 'amd'):
            with self.subTest(arm=arm):
                handler = self.forward(arm, {'Content-Type': 'application/json',
                    'X-Request-Id': 'synthetic-id-123', 'Content-Length': '999',
                    'Set-Cookie': 'untrusted', 'Location': 'https://invalid.example',
                    'X-Evil\r\nInjected': 'yes', 'X-Request-Id\x00': 'no'})
                handler.send_error.assert_not_called()
                self.assertEqual(handler.send_header.call_args_list, [
                    unittest.mock.call('Content-Type', 'application/json'),
                    unittest.mock.call('X-Request-Id', 'synthetic-id-123'),
                    unittest.mock.call('Content-Length', '14')])
                self.assertEqual(handler.wfile.getvalue(), b'unchanged-body')

    def test_crlf_injection_and_all_controls_rejected_before_status(self):
        attacks = ['good\r\nX-Injected: yes', 'good\rmalicious', 'good\nmalicious',
                   *[f'good{chr(c)}bad' for c in [*range(32), *range(127, 160)]],
                   'good\u2028bad']
        for arm in ('run2', 'amd'):
            for name in ('Content-Type', 'X-Request-Id'):
                for attack in attacks:
                    with self.subTest(arm=arm, header=name, attack=repr(attack)):
                        handler = self.forward(arm, {name: attack})
                        handler.send_error.assert_called_once_with(502,
                            'local router transport failed; no retry/redirect')
                        handler.send_response.assert_not_called()
                        handler.send_header.assert_not_called()
                        handler.end_headers.assert_not_called()
                        self.assertEqual(handler.wfile.getvalue(), b'')

    def test_folded_header_rejected(self):
        from http.client import parse_headers
        headers = parse_headers(io.BytesIO(b'X-Request-Id: safe\r\n injected\r\n\r\n'))
        for arm in ('run2', 'amd'):
            with self.subTest(arm=arm):
                response = Mock()
                response.getheader.side_effect = headers.get
                with self.assertRaises(ValueError):
                    load(arm).response_headers(response)
