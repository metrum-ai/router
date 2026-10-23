# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import unittest
from unittest.mock import patch
import collect
import backend
import local
from pathlib import Path


class LoopTests(unittest.TestCase):
    def test_all_local_callers_have_capacity_for_documented_workflows(self):
        with patch.object(local.bcrypt, 'hashpw', return_value=b'synthetic-hash'):
            cfg = local.config(Path('/tmp/synthetic-router-test'), {})
        for caller in cfg['callers']:
            self.assertGreaterEqual(caller['rate']['concurrent'],64)
            self.assertGreaterEqual(caller['rate']['rpm'],20000)
            self.assertGreaterEqual(caller['rate']['tpm'],100000000)
            self.assertGreaterEqual(caller['quota']['day']['requests'],1000000)
            self.assertGreaterEqual(caller['quota']['day']['tokens'],1000000000)

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

    def test_general_completion_labels_are_not_oracle_answers(self):
        row = {'messages':[{'role':'user','content':'Explain a mock ticket.'},{'role':'assistant','content':'GOLD_SENTINEL_NOT_INPUT'}], 'tools':[]}
        def request(base,path,payload,key):
            self.assertNotIn('GOLD_SENTINEL_NOT_INPUT',json.dumps(payload))
            return 200, {'X-Request-Id':'general'}, json.dumps({'choices':[{'message':{'role':'assistant','content':'A complete new response.'}}]}).encode()
        with patch.object(collect,'request',request):
            result=collect.episode('http://localhost','not-real',row,1)
        self.assertTrue(result['success'])
        self.assertEqual(result['turns'],1)
        self.assertEqual(result['expected'],[])

    def test_letter_anchor_uses_exact_final_not_completion_only(self):
        fixture={'version':'router-replay-v1','backend':'acme-support-v1','checker':'expected-tool-calls-v1','seed':0,'tools':[],'expected_calls':[],'expected_final':'B'}
        state=backend.initialize(fixture)
        self.assertTrue(backend.success(state,{'content':'B'}))
        self.assertFalse(backend.success(state,{'content':'A'}))
        self.assertFalse(backend.success(state,{'content':'The answer is B'}))

    def test_replay_backend_state_is_isolated(self):
        fixture = {'version':'router-replay-v1','backend':'acme-support-v1','checker':'expected-tool-calls-v1','seed':2,
                   'tools':[{'function':{'name':'lookup','parameters':{'type':'object'}}}],
                   'expected_calls':[{'name':'lookup','arguments':{}}]}
        a, b = backend.initialize(fixture), backend.initialize(fixture)
        self.assertTrue(backend.execute(a, 'lookup', {})['mock'])
        self.assertTrue(backend.success(a, {'content':'done'}))
        self.assertFalse(backend.success(b, {'content':'done'}))
        with self.assertRaises(ValueError):
            backend.initialize({'backend':'https://untrusted.example/code'})

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
