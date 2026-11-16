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
        artifact = review.build_artifact(candidate, "v4.8.0", "abc1234")
        blocked, unanswered = review.evaluate(artifact, {}, "v4.8.0", "abc1234")
        self.assertEqual(blocked["decision"], "block")
        self.assertEqual(unanswered, 1)
        finding_id = blocked["findings"][0]["id"]
        artifact_digest = blocked["artifact_digest"]
        reworded = json.loads(json.dumps(candidate))
        reworded["findings"][0]["title"] = "Rollback instructions need evidence"
        reworded["findings"][0]["rationale"] = "A changed model explanation for the same source location."
        rerun, unanswered = review.evaluate(reworded, {}, "v4.8.0", "abc1234")
        self.assertEqual(rerun["findings"][0]["id"], finding_id)
        self.assertEqual(unanswered, 1)
        passed, unanswered = review.evaluate(artifact, {artifact_digest: "Reviewed this exact release review artifact; rollback step was added and verified."}, "v4.8.0", "abc1234")
        self.assertEqual(passed["decision"], "pass")
        self.assertEqual(unanswered, 0)
        self.assertIn("rollback step was added", review.render(passed, "v4.8.0", "abc1234", "v5.0.0"))

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
            artifact_output = Path(temporary) / "review-artifact.json"
            with patch.dict(os.environ, {}, clear=True), patch("sys.argv", [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0",
                "--artifact-output", str(artifact_output), "--output", str(output)
            ]):
                self.assertEqual(review.main(), 2)
            self.assertFalse(output.exists())

    def test_same_range_rerun_reuses_artifact_after_digest_acceptance(self) -> None:
        candidate = {
            "summary": "One release blocker was reviewed.",
            "findings": [{
                "severity": "blocking", "category": "correctness", "title": "Missing rollback detail",
                "rationale": "The documented rollback path omits one verification step.",
                "locations": [{"file": "docs/upgrade-guide.md", "line": 42}],
            }],
        }
        artifact = review.build_artifact(candidate, "v4.8.0", "abc1234")
        self.assertTrue(artifact["artifact_digest"])
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / "cached-review.json"
            output = root / "AI-release-review.md"
            artifact_output = root / "review-artifact.json"
            responses = root / "responses.json"
            cache.write_text(json.dumps(artifact), encoding="utf-8")
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1",
                "acceptances": {artifact["artifact_digest"]: "Accepted for this exact artifact after verifying rollback evidence."},
            }), encoding="utf-8")
            with patch.dict(os.environ, {}, clear=True), patch.object(review, "call_model") as call_model, patch("sys.argv", [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(cache),
                "--artifact-output", str(artifact_output), "--output", str(output),
            ]):
                self.assertEqual(review.main(), 0)
            call_model.assert_not_called()
            self.assertIn("Decision: **PASS**", output.read_text(encoding="utf-8"))
            self.assertIn(artifact["artifact_digest"], output.read_text(encoding="utf-8"))
            self.assertEqual(json.loads(artifact_output.read_text(encoding="utf-8")), artifact)

    def test_model_request_uses_zero_temperature(self) -> None:
        class Response:
            def __enter__(self):
                return self
            def __exit__(self, *_args):
                return False
            def read(self):
                return json.dumps({"content": [{"type": "text", "text": '{"summary":"clean","findings":[]}'}]}).encode()

        with patch.object(review.urllib.request, "urlopen", return_value=Response()) as urlopen:
            review.call_model("test-key", "diff")
        request = urlopen.call_args.args[0]
        self.assertEqual(json.loads(request.data)["temperature"], 0)

    def test_uncached_changed_range_calls_model_once(self) -> None:
        candidate = {"summary": "No blocking findings.", "findings": []}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "def5678", "--tag", "v5.0.0",
                "--responses", str(root / "responses.json"), "--artifact-cache", str(root / "cache" / "review.json"),
                "--artifact-output", str(root / "review-output" / "review.json"),
                "--output", str(root / "review-output" / "AI-release-review.md"),
            ]
            (root / "responses.json").write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff", return_value="changed range diff"), \
                 patch.object(review, "call_model", return_value=candidate) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            call_model.assert_called_once_with("test-key", "changed range diff")


if __name__ == "__main__":
    unittest.main()
