# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import unittest
from unittest.mock import patch
import collect


class LoopTests(unittest.TestCase):
    def test_real_continuation_and_no_oracle_input(self):
        tools = [{'type': 'function', 'function': {'name': 'lookup', 'parameters': {
            'type': 'object', 'properties': {'ticket': {'type': 'integer'}}, 'required': ['ticket']}}}]
        call = {'id': 'call_1', 'type': 'function', 'function': {'name': 'lookup', 'arguments': '{"ticket":1}'}}
        row = {'messages': [{'role': 'user', 'content': 'Look up ticket 1'},
                            {'role': 'assistant', 'tool_calls': [call]}], 'tools': tools}
        sent = []
        def request(base, path, payload, key):
            sent.append(json.loads(json.dumps(payload)))
            message = {'role': 'assistant', 'tool_calls': [call], 'content': None} if len(sent) == 1 else {'role': 'assistant', 'content': 'Completed'}
            return 200, {'X-Request-Id': 'request' + str(len(sent))}, json.dumps({'choices': [{'message': message}]}).encode()
        with patch.object(collect, 'request', request):
            result = collect.episode('http://localhost', 'not-a-real-key', row, 0)
        self.assertTrue(result['success'])
        self.assertEqual([m['role'] for m in sent[0]['messages']], ['user'])
        self.assertEqual([m['role'] for m in sent[1]['messages']], ['user', 'assistant', 'tool'])
        self.assertEqual(sent[1]['messages'][-1]['tool_call_id'], 'call_1')

    def test_backend_does_not_execute_commands_and_validates_schema(self):
        tools = [{'function': {'name': 'refund', 'parameters': {'type': 'object', 'properties': {'amount': {'type': 'integer'}}, 'required': ['amount']}}}]
        with self.assertRaises(Exception):
            collect.execute({'function': {'name': 'refund', 'arguments': '{"amount":"shell code"}'}}, tools, 0)
        with self.assertRaises(ValueError):
            collect.execute({'function': {'name': 'lookup', 'arguments': '{}'}},
                            [{'function': {'name': 'lookup', 'parameters': {'$ref': 'https://invalid.example/schema'}}}], 0)
        with self.assertRaises(StopIteration):
            collect.execute({'function': {'name': 'execute_shell', 'arguments': '{}'}}, tools, 0)

if __name__ == '__main__':
    unittest.main()
