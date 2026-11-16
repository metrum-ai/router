# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import release_ai_review as review


class ReleaseAIReviewTest(unittest.TestCase):
    def test_unanswered_blocker_blocks_and_answered_blocker_is_recorded(self) -> None:
        candidate = {
            "summary": "Release has one issue to resolve.",
            "findings": [{
                "severity": "blocking",
                "title": "Missing rollback note",
                "rationale": "The upgrade change needs a tested rollback instruction.",
                "files": ["docs-site/docs/release-notes/upgrade-guide.md"],
            }],
        }
        blocked, unanswered = review.evaluate(candidate, {})
        self.assertEqual(blocked["decision"], "block")
        self.assertEqual(unanswered, 1)
        finding_id = blocked["findings"][0]["id"]
        passed, unanswered = review.evaluate(candidate, {finding_id: "Rollback step added and verified in the release rehearsal."})
        self.assertEqual(passed["decision"], "pass")
        self.assertEqual(unanswered, 0)
        self.assertIn("Rollback step added", review.render(passed, "v4.8.0", "abc1234", "v5.0.0"))

    def test_clean_review_and_sensitive_output(self) -> None:
        report, unanswered = review.evaluate({"summary": "No release blockers found.", "findings": []}, {})
        self.assertEqual(report["decision"], "pass")
        self.assertEqual(unanswered, 0)
        with self.assertRaises(ValueError):
            review.evaluate({"summary": "No release blockers found.", "findings": [{
                "severity": "informational", "title": "API key: sk-abcdefghijklmnop",
                "rationale": "A finding", "files": [],
            }]}, {})

    def test_model_timeout_fails_without_logging_request(self) -> None:
        with patch.object(review.urllib.request, "urlopen", side_effect=TimeoutError()):
            with self.assertRaisesRegex(RuntimeError, "failed or timed out"):
                review.call_model("not-a-real-key", "diff contents", timeout=0.001)

    def test_cli_fails_closed_when_credential_is_missing(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "review.md"
            with patch.dict(os.environ, {}, clear=True), patch("sys.argv", [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0", "--output", str(output)
            ]):
                self.assertEqual(review.main(), 2)
            self.assertFalse(output.exists())


if __name__ == "__main__":
    unittest.main()
