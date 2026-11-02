#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline contract tests for the release gate's config guards."""

from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))

import release_gate  # noqa: E402

PINNED = """models:
  gpt:
    strategy: static
    targets:
    - provider: openai_responses
      model: gpt-live
      dialect: openai-responses
  claude:
    strategy: static
    targets:
    - provider: claude
      model: claude-live
      dialect: anthropic
"""


class GptPinTest(unittest.TestCase):
    def check(self, text):
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "config.yaml"
            path.write_text(text, encoding="utf-8")
            return release_gate.ensure_gpt_responses_pinned(path)

    def test_accepts_responses_only_group(self):
        self.assertEqual(self.check(PINNED), 1)

    def test_rejects_chat_target_alongside_responses(self):
        text = PINNED.replace(
            "    targets:\n    - provider: openai_responses",
            "    targets:\n    - provider: openai_chat\n      model: gpt-live\n      dialect: openai-chat\n    - provider: openai_responses",
        )
        with self.assertRaises(release_gate.GateError) as caught:
            self.check(text)
        self.assertIn("openai_chat:openai-chat", str(caught.exception))

    def test_rejects_chat_only_group(self):
        text = PINNED.replace("dialect: openai-responses", "dialect: openai-chat")
        with self.assertRaises(release_gate.GateError) as caught:
            self.check(text)
        self.assertIn("must target only", str(caught.exception))

    def test_rejects_missing_group(self):
        text = PINNED.replace("  gpt:\n", "  other:\n")
        with self.assertRaises(release_gate.GateError):
            self.check(text)

    def test_nested_lists_are_not_targets(self):
        text = PINNED.replace(
            "      dialect: openai-responses\n",
            "      dialect: openai-responses\n      tags:\n      - live\n      tool_support:\n        openai_responses:\n        - function\n",
        )
        self.assertEqual(self.check(text), 1)


class HarborStageTest(unittest.TestCase):
    def test_matrix_bundle_is_present_and_pinned(self):
        names = set(release_gate.HARBOR_FILES)
        self.assertIn("metrum_pi_tmpfs.py", names)
        self.assertIn("run_matrix.py", names)
        for name in names:
            self.assertTrue((release_gate.HARBOR_BUNDLE / name).is_file(), name)
        self.assertEqual(release_gate.HARBOR_VERSION_DEFAULT, "0.25.0")

    def test_matrix_generates_five_tasks_and_four_callers(self):
        import hashlib
        import pathlib
        import sys
        import tempfile
        sys.path.insert(0, str(release_gate.HARBOR_BUNDLE))
        try:
            import generate_tasks
            import harbor_config

            with tempfile.TemporaryDirectory() as raw:
                generate_tasks.generate(pathlib.Path(raw) / "tasks")
                tasks = sorted(p.name for p in (pathlib.Path(raw) / "tasks").iterdir())
            self.assertEqual(tasks, ["edit", "hello", "json", "sum", "uppercase"])
            tokens = harbor_config.caller_tokens()
            self.assertEqual(len(tokens), 4)
            config = harbor_config.make_config(pathlib.Path("/tmp/matrix"), 18080, 18081)
            self.assertEqual(config.count("token_sha256"), 4)
            self.assertIn("listen: \"0.0.0.0:18080\"", config)
            for digest in (hashlib.sha256(t.encode()).hexdigest() for t in tokens.values()):
                self.assertIn(digest, config)
        finally:
            sys.path.remove(str(release_gate.HARBOR_BUNDLE))

    def test_stage_b_reports_skipped_without_a_host(self):
        import pathlib
        import tempfile

        with tempfile.TemporaryDirectory() as raw:
            rows = release_gate.stage_harbor(pathlib.Path(raw), None, "0" * 40)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["result"], "skipped")
        self.assertIn("RELEASE_GATE_HARBOR_HOST", rows[0]["note"])

    def test_remote_script_never_puts_the_token_on_a_command_line(self):
        script = release_gate.harbor_remote_script("a" * 40)
        self.assertIn("/run/harbor-e2e/router-token", script)
        self.assertIn("rm -f /run/harbor-e2e/provider-secrets.json", script)


if __name__ == "__main__":
    unittest.main()
