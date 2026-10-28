# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import base64
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

import collector


def decode(value): return base64.urlsafe_b64decode(value + '=' * (-len(value) % 4))


class CollectorTests(unittest.TestCase):
    def test_scoped_keys_and_noexec_tmpfs(self):
        with tempfile.TemporaryDirectory() as out, tempfile.TemporaryDirectory(dir='/dev/shm') as scratch:
            env = {}; cfg = collector.configure(Path(out), env, Path(scratch))
            self.assertEqual(len(cfg['callers']), 6)
            self.assertEqual(len({c['token_id'] for c in cfg['callers']}), 6)
            self.assertEqual(len(list(Path(scratch).iterdir())), 0)
            keys = [env['TRACE_AGENT_'+name.upper()] for name in collector.AGENTS] + [env['TRACE_IT_KEY']]
            for caller, token in zip(cfg['callers'], keys):
                self.assertNotIn('token_sha256', caller); self.assertNotIn(token, json.dumps(cfg))
                scheme, iterations, salt, derived = caller['token_hash'].split(':')
                self.assertEqual(scheme, 'pbkdf2-sha3-512')
                self.assertEqual(hashlib.pbkdf2_hmac('sha3_512', token.encode(), decode(salt), int(iterations[2:])), decode(derived))
                self.assertTrue(token.startswith(caller['token_id'] + '_'))
            self.assertTrue(all(c['content_capture']['enabled'] for c in cfg['callers'][:-1]))
            self.assertNotIn('content_capture', cfg['callers'][-1])
            self.assertEqual({c['project'] for c in cfg['callers'][:-1]}, {'acme-support'})
            self.assertEqual(cfg['callers'][-1]['project'], 'acme-it')

    def test_agent_mapping_and_unknown_fail_closed(self):
        for name in collector.AGENTS:
            task = {'initial_messages': [{'content': 'You are a '+name+' agent handling synthetic work.'}], 'class': 'reviewed_general'}
            self.assertEqual(collector.agent(task), name)
        self.assertIn(collector.agent({'initial_messages': [{'content': 'Return only a letter.'}], 'class':'computed_letter', 'seed':3}), collector.AGENTS)
        with self.assertRaises(ValueError): collector.agent({'initial_messages': [{'content': 'unrecognized'}], 'class':'native_tool'})

    def test_budget_unknown_usage_is_not_free(self):
        ledger = collector.Ledger(); amount = ledger.reserve({'messages': []})
        ledger.settle(amount, {'usage': {'total_tokens': 10}})
        self.assertEqual(ledger.unknown_usage_calls, 1); self.assertEqual(ledger.known_usage_usd, 0)
        self.assertEqual(ledger.spent, amount)
        amount = ledger.reserve({'messages': []}); ledger.settle(amount, {'usage': {'prompt_tokens': 100, 'completion_tokens': 10}})
        self.assertAlmostEqual(ledger.known_usage_usd, 100*.15/1e6+10*.6/1e6)


if __name__ == '__main__': unittest.main()
