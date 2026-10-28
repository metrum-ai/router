#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Mock Docker checks; these never execute generated code."""
import contextlib
import io
import json
import os
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import judge


class JudgeTests(unittest.TestCase):
    def test_sandbox_constraints_and_host_grading(self):
        seen = []
        def fake_run(command, **kwargs):
            seen.append(command)
            if command[1] == 'run':
                for _, expected in judge.CASES:
                    row = {'result': expected} if expected is not None else {'error': 'invalid expression'}
                    kwargs['stdout'].write((json.dumps(row)+'\n').encode())
            return SimpleNamespace(returncode=0)
        inp = SimpleNamespace(buffer=io.BytesIO(json.dumps({'source': '# fake'}).encode()))
        out = io.StringIO()
        image = 'python:3.13-slim@sha256:' + 'a'*64
        with patch.object(judge.sys, 'stdin', inp), patch.dict(os.environ, {'CASE_STUDY_PYTHON_IMAGE': image}), patch.object(judge.subprocess, 'run', fake_run), contextlib.redirect_stdout(out):
            judge.main()
        self.assertEqual(json.loads(out.getvalue())['passed'], 20)
        command = seen[0]
        for flag in ('--interactive', '--network=none', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges', '--user=65534:65534', '--memory=256m', '--pids-limit=32', '--pull=never'):
            self.assertIn(flag, command)
        self.assertEqual(seen[1][:3], ['docker', 'rm', '-f'])

    def test_unpinned_image_rejected(self):
        inp = SimpleNamespace(buffer=io.BytesIO(b'{"source":"x"}'))
        with patch.object(judge.sys, 'stdin', inp), patch.dict(os.environ, {'CASE_STUDY_PYTHON_IMAGE': 'python:3.13-slim'}):
            with self.assertRaises(SystemExit): judge.main()


if __name__ == '__main__': unittest.main()
