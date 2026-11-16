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
                "category": "compatibility",
                "title": "Missing rollback note",
                "rationale": "The upgrade change needs a tested rollback instruction.",
                "locations": [{"file": "docs-site/docs/release-notes/upgrade-guide.md", "line": 42}],
            }],
        }
        blocked, unanswered = review.evaluate(candidate, {}, "v4.8.0", "abc1234")
        self.assertEqual(blocked["decision"], "block")
        self.assertEqual(unanswered, 1)
        finding_id = blocked["findings"][0]["id"]
        reworded = json.loads(json.dumps(candidate))
        reworded["findings"][0]["title"] = "Rollback instructions need evidence"
        reworded["findings"][0]["rationale"] = "A changed model explanation for the same source location."
        rerun, unanswered = review.evaluate(reworded, {finding_id: "Rollback step added and verified in the release rehearsal."}, "v4.8.0", "abc1234")
        self.assertEqual(rerun["findings"][0]["id"], finding_id)
        self.assertEqual(unanswered, 0)
        passed, unanswered = review.evaluate(candidate, {finding_id: "Rollback step added and verified in the release rehearsal."}, "v4.8.0", "abc1234")
        self.assertEqual(passed["decision"], "pass")
        self.assertEqual(unanswered, 0)
        self.assertIn("Rollback step added", review.render(passed, "v4.8.0", "abc1234", "v5.0.0"))

    def test_finding_identity_changes_when_location_category_or_range_changes(self) -> None:
        candidate = {
            "summary": "One issue.",
            "findings": [{
                "severity": "blocking", "category": "security", "title": "Unsafe verification",
                "rationale": "The verifier accepts an unexpected signer.",
                "locations": [{"file": "internal/releaseverify/verify.go", "line": 137}],
            }],
        }
        baseline, _ = review.evaluate(candidate, {}, "v4.8.0", "abc1234")
        finding = candidate["findings"][0]
        variants = [
            {**candidate, "findings": [{**finding, "category": "correctness"}]},
            {**candidate, "findings": [{**finding, "locations": [{"file": "internal/releaseverify/verify.go", "line": 138}]}]},
        ]
        for variant in variants:
            changed, _ = review.evaluate(variant, {}, "v4.8.0", "abc1234")
            self.assertNotEqual(baseline["findings"][0]["id"], changed["findings"][0]["id"])
        new_range, _ = review.evaluate(candidate, {}, "v4.8.0", "def5678")
        self.assertNotEqual(baseline["findings"][0]["id"], new_range["findings"][0]["id"])

    def test_clean_review_and_sensitive_output(self) -> None:
        report, unanswered = review.evaluate({"summary": "No release blockers found.", "findings": []}, {}, "v4.8.0", "abc1234")
        self.assertEqual(report["decision"], "pass")
        self.assertEqual(unanswered, 0)
        with self.assertRaises(ValueError):
            review.evaluate({"summary": "No release blockers found.", "findings": [{
                "severity": "informational", "category": "security", "title": "API key: sk-abcdefghijklmnop",
                "rationale": "A finding", "locations": [{"file": "docs/a.md", "line": 1}],
            }]}, {}, "v4.8.0", "abc1234")

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
