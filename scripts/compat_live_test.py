#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Focused, offline tests for the live harness."""
import os
import sqlite3
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import compat_live


class CompatLiveTest(unittest.TestCase):
    def test_suite_scope(self):
        self.assertEqual(len(compat_live.suites('openai')), 27)
        self.assertEqual(len(compat_live.suites('anthropic')), 27)
        self.assertNotIn('messages_code_execution', compat_live.suites('anthropic'))
        for suite in ('responses_get', 'responses_delete', 'responses_input_items', 'responses_multi_turn'):
            self.assertNotIn(suite, compat_live.suites('openai'))
        self.assertIn('chat_completions_stream_usage', compat_live.suites('openai'))

    def test_meter_sums_only_persisted_rows(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'usage.sqlite'
            with sqlite3.connect(path) as db:
                db.execute('create table request_usage (total_cost_usd real)')
                db.executemany('insert into request_usage values (?)', [(0.01,), (None,), (0.02,)])
            self.assertAlmostEqual(compat_live.meter(path), 0.03)

    def test_failed_suite_causes_nonzero_exit(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            config = root / 'config.yaml'
            config.write_text('test')
            tester = root / 'tester'
            tester.write_text('binary')
            source = root / 'router-source'
            source.mkdir()
            env = {'COMPAT_LIVE_CONFIG': str(config), 'COMPAT_LIVE_OUT': str(root / 'results'),
                   'COMPAT_LIVE_ROUTER_SOURCE': str(source), 'OPENAI_API_KEY': 'synthetic',
                   'ANTHROPIC_API_KEY': 'synthetic', 'ROUTER_CALLER_TOKEN': 'synthetic'}
            build_dirs = []

            def run(args, **kwargs):
                if args[:2] == ['go', 'run']:
                    return SimpleNamespace(stdout='{"usage_db":"unused"}', returncode=0)
                if str(args[0]) == str(tester):
                    return SimpleNamespace(returncode=1)
                if args[:2] == ['go', 'build']:
                    build_dirs.append(kwargs['cwd'])
                return SimpleNamespace(returncode=0)

            def output(args, **kwargs):
                return 'commit' if args[1:3] == ['rev-parse', 'HEAD'] else ''

            with mock.patch.dict(os.environ, env), mock.patch.object(compat_live.subprocess, 'run', side_effect=run), \
                 mock.patch.object(compat_live, 'check_config', return_value='unused') as guard, \
                 mock.patch.object(compat_live.subprocess, 'check_output', side_effect=output), \
                 mock.patch.object(compat_live.subprocess, 'Popen', return_value=object()), \
                 mock.patch.object(compat_live, 'wait_ready'), mock.patch.object(compat_live, 'stop'), \
                 mock.patch.object(compat_live, 'build_tester', return_value=(tester, tester)), \
                 mock.patch.object(compat_live, 'meter', return_value=0.0):
                self.assertEqual(compat_live.main(), 1)
                guard.assert_called_once_with(config.resolve(), source.resolve())
                self.assertEqual(build_dirs, [source])

    def test_hash_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'input'
            path.write_text('first')
            first = compat_live.sha256(path)
            path.write_text('second')
            self.assertNotEqual(first, compat_live.sha256(path))


if __name__ == '__main__':
    unittest.main()
