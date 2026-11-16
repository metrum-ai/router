# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import call, patch

import release_ai_review as review


class ReleaseAIReviewTest(unittest.TestCase):
    def test_diff_files_are_packed_under_one_megabyte_and_preserved(self) -> None:
        file_diffs = [
            ("a.go", "diff --git a/a.go b/a.go\n" + "a" * 600_000),
            ("b.go", "diff --git a/b.go b/b.go\n" + "b" * 500_000),
            ("c.go", "diff --git a/c.go b/c.go\n" + "c" * 100_000),
        ]
        batches = review.pack_diff_files(file_diffs)
        self.assertEqual(len(batches), 2)
        self.assertEqual([path for _diff, paths in batches for path in paths], ["a.go", "b.go", "c.go"])
        self.assertTrue(all(len(diff.encode("utf-8")) <= review.MAX_DIFF_BYTES for diff, _paths in batches))
        self.assertIn("diff --git a/a.go b/a.go", batches[0][0])
        self.assertIn("diff --git a/b.go b/b.go", batches[1][0])
        self.assertIn("diff --git a/c.go b/c.go", batches[1][0])

    def test_diff_batcher_fails_closed_for_one_oversized_file_or_too_many_batches(self) -> None:
        with self.assertRaisesRegex(ValueError, "per-request"):
            review.pack_diff_files([("large.go", "x" * 11)], max_bytes=10)
        with self.assertRaisesRegex(ValueError, "maximum number"):
            review.pack_diff_files(
                [(f"{index}.go", "x" * 60) for index in range(3)],
                max_bytes=100,
                max_batches=2,
            )

    def test_release_document_diffs_are_repeated_in_every_batch(self) -> None:
        upgrade = "docs-site/docs/release-notes/upgrade-guide.md"
        file_diffs = [
            ("CHANGELOG.md", "changelog context\n"),
            (upgrade, "upgrade context\n"),
            ("first.go", "a" * 60),
            ("second.go", "b" * 60),
        ]
        batches = review.pack_diff_files(file_diffs, max_bytes=100)
        self.assertEqual(len(batches), 2)
        self.assertEqual(batches[0][1], ("CHANGELOG.md", upgrade, "first.go"))
        self.assertEqual(batches[1][1], ("CHANGELOG.md", upgrade, "second.go"))
        for diff, _paths in batches:
            self.assertIn("changelog context", diff)
            self.assertIn("upgrade context", diff)
        self.assertTrue(all(len(diff.encode("utf-8")) <= 100 for diff, _paths in batches))

    def test_git_diff_batches_reads_and_separates_real_file_diffs(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.name", "Release Review Test"], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.email", "release-review-test@example.invalid"], check=True)
            for name in ("first.go", "second.go"):
                (root / name).write_text("initial\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "first.go", "second.go"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "baseline"], check=True)
            base = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            (root / "first.go").write_text("a" * 600_000 + "\n", encoding="utf-8")
            (root / "second.go").write_text("b" * 600_000 + "\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "first.go", "second.go"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "large release diff"], check=True)
            head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            original_cwd = Path.cwd()
            try:
                os.chdir(root)
                batches = review.git_diff_batches(base, head)
            finally:
                os.chdir(original_cwd)
        self.assertEqual([paths for _diff, paths in batches], [("first.go",), ("second.go",)])
        self.assertTrue(all(len(diff.encode("utf-8")) <= review.MAX_DIFF_BYTES for diff, _paths in batches))

    def test_git_diff_batches_uses_literal_paths_and_rejects_empty_file_diff(self) -> None:
        first = type("Result", (), {"stdout": b"literal[1].go\0"})()
        second = type("Result", (), {"stdout": ""})()
        with patch.object(review.subprocess, "run", side_effect=[first, second]) as run:
            with self.assertRaisesRegex(ValueError, "listed release diff file .* empty diff"):
                review.git_diff_batches("base", "head")
        for call_args in run.call_args_list:
            command = call_args.args[0]
            self.assertIn("--literal-pathspecs", command)
            self.assertIn("--no-renames", command)
        self.assertEqual(run.call_args_list[1].args[0][-1], "literal[1].go")

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

    def test_batch_finding_must_reference_a_file_in_that_batch(self) -> None:
        candidate = {
            "summary": "One issue.",
            "findings": [{
                "severity": "blocking", "category": "security", "title": "Unsafe verification",
                "rationale": "The verifier accepts an unexpected signer.",
                "locations": [{"file": "outside.go", "line": 1}],
            }],
        }
        with self.assertRaisesRegex(ValueError, "outside its batch"):
            review.build_artifact(candidate, "v4.8.0", "abc1234", allowed_files={"inside.go"})

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

    def test_model_request_uses_supported_limits_and_final_text_block(self) -> None:
        class Response:
            def __enter__(self):
                return self
            def __exit__(self, *_args):
                return False
            def read(self):
                return json.dumps({"content": [
                    {"type": "text", "text": "not the final answer"},
                    {"type": "thinking", "thinking": "private reasoning"},
                    {"type": "text", "text": '{"summary":"clean","findings":[]}'},
                ]}).encode()

        with patch.object(review.urllib.request, "urlopen", return_value=Response()) as urlopen:
            parsed = review.call_model("test-key", "diff")
        request = urlopen.call_args.args[0]
        payload = json.loads(request.data)
        self.assertEqual(payload["max_tokens"], 16000)
        self.assertNotIn("temperature", payload)
        self.assertEqual(payload["output_config"], {
            "format": {"type": "json_schema", "schema": review.REVIEW_OUTPUT_SCHEMA},
        })
        self.assertEqual(review.REVIEW_OUTPUT_SCHEMA["required"], ["summary", "findings"])
        self.assertFalse(review.REVIEW_OUTPUT_SCHEMA["additionalProperties"])
        self.assertEqual(parsed, {"summary": "clean", "findings": []})

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
                 patch.object(review, "git_diff_batches", return_value=[("changed range diff", ("router.go",))]), \
                 patch.object(review, "call_model", return_value=candidate) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            call_model.assert_called_once_with("test-key", "changed range diff", batch_number=1, batch_count=1)

    def test_uncached_range_reviews_every_bounded_batch(self) -> None:
        clean_batch = {"summary": "No blocking findings in this batch.", "findings": []}
        blocking_batch = {
            "summary": "One release blocker was found in this batch.",
            "findings": [{
                "severity": "blocking", "category": "release-integrity", "title": "Missing provenance check",
                "rationale": "The release workflow does not verify the expected source ref.",
                "locations": [{"file": "docs/release.md", "line": 7}],
            }],
        }
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
            batches = [("diff batch one", ("router.go",)), ("diff batch two", ("docs/release.md",))]
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_batches", return_value=batches), \
                 patch.object(review, "call_model", side_effect=[clean_batch, blocking_batch]) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 1)
            self.assertEqual(call_model.call_args_list, [
                call("test-key", "diff batch one", batch_number=1, batch_count=2),
                call("test-key", "diff batch two", batch_number=2, batch_count=2),
            ])
            artifact = json.loads((root / "review-output" / "review.json").read_text(encoding="utf-8"))
            self.assertEqual(artifact["summary"], "Reviewed 2 changed files across 2 bounded batches.")
            self.assertEqual(len(artifact["findings"]), 1)
            self.assertEqual(artifact["findings"][0]["severity"], "blocking")
            self.assertIn("Decision: **BLOCK**", (root / "review-output" / "AI-release-review.md").read_text(encoding="utf-8"))

    def test_uncached_review_stops_and_fails_when_batch_two_of_three_fails(self) -> None:
        clean_batch = {"summary": "No blocking findings in this batch.", "findings": []}
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
            batches = [
                ("diff batch one", ("one.go",)),
                ("diff batch two", ("two.go",)),
                ("diff batch three", ("three.go",)),
            ]
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_batches", return_value=batches), \
                 patch.object(review, "call_model", side_effect=[clean_batch, RuntimeError("AI release review request failed")]) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 2)
            self.assertEqual(call_model.call_count, 2)
            self.assertFalse((root / "review-output" / "review.json").exists())
            self.assertFalse((root / "review-output" / "AI-release-review.md").exists())


if __name__ == "__main__":
    unittest.main()
