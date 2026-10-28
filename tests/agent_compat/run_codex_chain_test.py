#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import signal
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

import run_codex_chain as runner


class CodexChainTest(unittest.TestCase):
    def test_cell_requires_exit_and_exact_marker(self):
        process = MagicMock(returncode=0)
        process.poll.return_value = 0
        with patch.object(runner.subprocess, "Popen", return_value=process) as popen:
            process.communicate.return_value = ("PASS: codex / OpenAI Responses / same dialect / depth 3: ok", "")
            self.assertEqual(runner.run_cell(3)["status"], "pass")
            self.assertIn("3", popen.call_args.args[0])
            process.communicate.return_value = ("PASS: codex / OpenAI Responses / same dialect / depth 1: ok", "")
            self.assertEqual(runner.run_cell(3)["status"], "fail")
            process.returncode = 1
            process.communicate.return_value = ("PASS: codex / OpenAI Responses / same dialect / depth 3: ok", "sandbox denied")
            cell = runner.run_cell(3)
            self.assertEqual(cell["status"], "fail")
            self.assertIn("sandbox denied", cell["reason"])

    def test_timeout_stops_driver(self):
        process = MagicMock(pid=123, returncode=None)
        process.poll.return_value = None
        process.communicate.side_effect = [subprocess.TimeoutExpired("codex", 180), ("", "")]
        with patch.object(runner.subprocess, "Popen", return_value=process), patch.object(runner.os, "kill") as kill:
            self.assertEqual(runner.run_cell(1)["reason"], "timeout")
            kill.assert_called_once_with(123, signal.SIGTERM)

    def test_matrix_records_each_cell_and_nonzero(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "nested" / "report.json"
            def fake(depth):
                return {"depth": depth, "status": "fail" if depth == 2 else "pass"}
            with patch.object(runner, "run_cell", side_effect=fake), patch.object(runner.sys, "argv", ["runner", "--output", str(output)]):
                self.assertEqual(runner.main(), 1)
            data = json.loads(output.read_text())
            self.assertEqual([c["depth"] for c in data["results"]], list(runner.DEPTHS))
            self.assertEqual(data["hosted_model_calls"], 0)
            self.assertEqual(data["results"][2]["status"], "fail")


if __name__ == "__main__":
    unittest.main()
