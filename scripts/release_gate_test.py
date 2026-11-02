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

FULL = """models:
  gpt:
    targets:
    - provider: openai_chat
      model: gpt-live
      dialect: openai-chat
    - provider: openai_responses
      model: gpt-live
      dialect: openai-responses
  claude:
    targets:
    - provider: claude
      model: claude-live
      dialect: anthropic
    - provider: claude_bridge
      model: claude-live
      dialect: openai-chat
  gpt-bridge:
    targets:
    - provider: openai_responses
      model: gpt-live
      dialect: openai-responses
"""

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


class LaneCoverageTest(unittest.TestCase):
    def check(self, text):
        import tempfile
        with tempfile.TemporaryDirectory() as raw:
            path = Path(raw) / "config.yaml"
            path.write_text(text, encoding="utf-8")
            return release_gate.ensure_lanes_servable(path)

    def test_accepts_a_config_with_both_groups(self):
        self.assertEqual(self.check(FULL), len(release_gate.LANES) + len(release_gate.EXTRA_LANES))

    def test_rejects_a_missing_group(self):
        with self.assertRaises(release_gate.GateError) as caught:
            self.check(FULL.replace("  claude:\n", "  other:\n"))
        self.assertIn("claude", str(caught.exception))


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
