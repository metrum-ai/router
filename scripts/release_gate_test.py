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


class HarborPinTest(unittest.TestCase):
    def test_shim_pins_the_validated_cli(self):
        import tempfile
        with tempfile.TemporaryDirectory() as raw:
            shim, version = release_gate.harbor_shim(Path(raw))
            text = shim.read_text(encoding="utf-8")
            self.assertEqual(version, "0.24.0")
            self.assertIn("uvx --from harbor==0.24.0 harbor", text)
            self.assertTrue(shim.stat().st_mode & 0o111)

    def test_rejects_an_unpinned_version(self):
        import os
        import tempfile
        with tempfile.TemporaryDirectory() as raw:
            os.environ["RELEASE_GATE_HARBOR_VERSION"] = "latest"
            try:
                with self.assertRaises(release_gate.GateError):
                    release_gate.harbor_shim(Path(raw))
            finally:
                del os.environ["RELEASE_GATE_HARBOR_VERSION"]


if __name__ == "__main__":
    unittest.main()
