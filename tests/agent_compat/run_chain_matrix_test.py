#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Contract tests for the chain matrix report and failure handling."""
import io
import json
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest.mock import MagicMock, patch

import run_chain_matrix as matrix


class ChainMatrixTest(unittest.TestCase):
    @patch("run_chain_matrix.subprocess.Popen")
    def test_case_failure_is_reported_with_diagnostic(self, popen):
        process = MagicMock()
        process.communicate.return_value = ("", "fixture failure")
        process.returncode = 1
        process.poll.return_value = 1
        popen.return_value = process
        result = matrix.run_case("pi", 2)
        self.assertEqual(result["status"], "fail")
        self.assertIn("fixture failure", result["diagnostic"])
        self.assertEqual(result["depth"], 2)
        self.assertTrue(popen.call_args.kwargs["start_new_session"])

    @patch("run_chain_matrix.subprocess.Popen", side_effect=OSError("missing executable"))
    def test_launch_failure_is_a_failed_cell(self, _popen):
        result = matrix.run_case("claude-code", 0)
        self.assertEqual(result["status"], "fail")
        self.assertIn("missing executable", result["diagnostic"])

    def test_report_contains_all_cases_even_when_one_fails(self):
        def fake_case(client, depth, path="same-dialect"):
            return {"client": client, "depth": depth, "status": "fail" if (client, depth) == ("pi", 2) else "pass", "surface": "mock", "path": path}

        with tempfile.TemporaryDirectory() as tmp, patch.object(matrix, "run_case", side_effect=fake_case), patch.object(sys, "argv", ["matrix", "--output", str(Path(tmp) / "matrix.json")]), redirect_stdout(io.StringIO()):
            self.assertEqual(matrix.main(), 1)
            report = json.loads((Path(tmp) / "matrix.json").read_text())
        self.assertEqual(report["hosted_model_calls"], 0)
        self.assertEqual(len(report["results"]), 24)
        self.assertEqual([cell["depth"] for cell in report["results"] if cell["path"] == "anthropic-to-openai-chat"], [1, 2, 3, 5])
        self.assertEqual(sum(cell["status"] == "fail" for cell in report["results"]), 1)

    @patch("run_chain_matrix.subprocess.Popen")
    def test_cross_dialect_cell_sets_explicit_fixture_mode(self, popen):
        process = MagicMock()
        process.communicate.return_value = ("", "")
        process.returncode = 0
        process.poll.return_value = 0
        popen.return_value = process
        result = matrix.run_case("claude-code", 2, "anthropic-to-openai-chat")
        self.assertEqual(result["path"], "anthropic-to-openai-chat")
        self.assertEqual(popen.call_args.kwargs["env"]["AGENT_COMPAT_CROSS_CHAT"], "1")
        with self.assertRaises(ValueError):
            matrix.run_case("claude-code", 0, "anthropic-to-openai-chat")


if __name__ == "__main__":
    unittest.main()
