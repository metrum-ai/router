# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import json
import io
import os
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest.mock import call, patch

import release_ai_review as review


def sse_response(events: list[tuple[str, dict[str, object]]]):
    class Response:
        def __init__(self):
            self.lines = [
                line.encode("utf-8")
                for name, payload in events
                for line in f"event: {name}\ndata: {json.dumps(payload)}\n\n".splitlines(keepends=True)
            ]

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

        def __iter__(self):
            return iter(self.lines)

    return Response()


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

    def test_retry_batcher_keeps_a_large_file_atomic_under_the_hard_limit(self) -> None:
        batches = review.pack_diff_files(
            [("CHANGELOG.md", "context"), ("generated.js", "x" * 120)],
            max_bytes=100,
            max_single_file_bytes=150,
        )
        self.assertEqual(batches, [("context" + "x" * 120, ("CHANGELOG.md", "generated.js"))])
        with self.assertRaisesRegex(ValueError, "per-request"):
            review.pack_diff_files(
                [("CHANGELOG.md", "context"), ("generated.js", "x" * 145)],
                max_bytes=100,
                max_single_file_bytes=150,
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
        high_entropy_value = "".join(["Gh", "7k", "L2", "pQ", "9w", "R5", "tX", "3z", "Mv", "8c", "Hs", "4n"])
        with self.assertRaises(ValueError):
            review.evaluate({"summary": "No release blockers found.", "findings": [{
                "severity": "informational", "category": "security", "title": f"API key: {high_entropy_value}",
                "rationale": "A finding", "locations": [{"file": "docs/a.md", "line": 1}],
            }]}, {}, "v4.8.0", "abc1234")

    def test_ordinary_credential_field_wording_is_allowed_but_high_entropy_secret_is_rejected(self) -> None:
        ordinary = "This review uses api_key: to name a field and token= to describe an assignment."
        self.assertEqual(review.safe_text(ordinary, "rationale"), ordinary)
        high_entropy_value = "".join(["Gh", "7k", "L2", "pQ", "9w", "R5", "tX", "3z", "Mv", "8c", "Hs", "4n"])
        with self.assertRaisesRegex(ValueError, "sensitive-looking"):
            review.safe_text(f"api_key={high_entropy_value}", "rationale")

    def test_anthropic_key_prefix_with_high_entropy_value_is_rejected(self) -> None:
        high_entropy_value = "".join(["Gh", "7k", "L2", "pQ", "9w", "R5", "tX", "3z", "Mv", "8c", "Hs", "4n"])
        with self.assertRaisesRegex(ValueError, "sensitive-looking"):
            review.safe_text(f"credential sk-ant-api03-{high_entropy_value}", "rationale")

    def test_model_timeout_fails_without_logging_request(self) -> None:
        with patch.object(review.urllib.request, "urlopen", side_effect=TimeoutError()):
            with self.assertRaisesRegex(review.RequestTimeoutError, "timed out after"):
                review.call_model("not-a-real-key", "diff contents", timeout=0.001)

    def test_model_http_error_reports_status_without_response_body(self) -> None:
        failure = review.urllib.error.HTTPError(
            review.API_URL, 400, "Bad request", {}, io.BytesIO(b"private provider body")
        )
        with patch.object(review.urllib.request, "urlopen", side_effect=failure):
            with self.assertRaisesRegex(RuntimeError, "HTTP status 400") as raised:
                review.call_model("test-key", "diff")
        self.assertNotIn("private provider body", str(raised.exception))

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

    def test_streaming_request_accumulates_final_text_block_and_message_delta_stop_reason(self) -> None:
        response = sse_response([
            ("content_block_start", {"index": 0, "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"index": 0, "delta": {"type": "text_delta", "text": "obsolete answer"}}),
            ("content_block_stop", {"index": 0}),
            ("content_block_start", {"index": 1, "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"index": 1, "delta": {"type": "text_delta", "text": '{"summary":"clean",' }}),
            ("content_block_delta", {"index": 1, "delta": {"type": "text_delta", "text": '"findings":[]}' }}),
            ("content_block_stop", {"index": 1}),
            ("message_delta", {"delta": {"stop_reason": "end_turn"}}),
            ("message_stop", {"type": "message_stop"}),
        ])
        with patch.object(review.urllib.request, "urlopen", return_value=response) as urlopen:
            parsed = review.call_model("test-key", "diff")
        request = urlopen.call_args.args[0]
        payload = json.loads(request.data)
        self.assertEqual(payload["max_tokens"], 32000)
        self.assertTrue(payload["stream"])
        self.assertEqual(urlopen.call_args.kwargs["timeout"], 900)
        self.assertNotIn("temperature", payload)
        self.assertEqual(payload["output_config"], {
            "format": {"type": "json_schema", "schema": review.REVIEW_OUTPUT_SCHEMA},
        })
        self.assertEqual(review.REVIEW_OUTPUT_SCHEMA["required"], ["summary", "findings"])
        self.assertFalse(review.REVIEW_OUTPUT_SCHEMA["additionalProperties"])
        self.assertIn("at most 600", review.REVIEW_OUTPUT_SCHEMA["properties"]["summary"]["description"])
        finding_schema = review.REVIEW_OUTPUT_SCHEMA["properties"]["findings"]["items"]
        self.assertIn("Non-empty", finding_schema["properties"]["title"]["description"])
        line_schema = finding_schema["properties"]["locations"]["items"]["properties"]["line"]
        self.assertIn("Positive", line_schema["description"])
        self.assertNotIn("minLength", json.dumps(review.REVIEW_OUTPUT_SCHEMA))
        self.assertNotIn("maxLength", json.dumps(review.REVIEW_OUTPUT_SCHEMA))
        self.assertNotIn("minimum", json.dumps(review.REVIEW_OUTPUT_SCHEMA))
        self.assertEqual(parsed, {"summary": "clean", "findings": []})

    def test_model_reads_max_tokens_stop_reason_from_message_delta(self) -> None:
        response = sse_response([
            ("content_block_start", {"index": 0, "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"index": 0, "delta": {"type": "text_delta", "text": "private partial report"}}),
            ("message_delta", {"delta": {"stop_reason": "max_tokens"}}),
            ("message_stop", {"type": "message_stop"}),
        ])
        with patch.object(review.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "stop_reason=max_tokens") as raised:
                review.call_model("test-key", "diff")
        self.assertNotIn("private partial report", str(raised.exception))

    def test_refusal_fails_closed_without_splitting_or_retrying(self) -> None:
        response = sse_response([
            ("content_block_start", {"index": 0, "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"index": 0, "delta": {"type": "text_delta", "text": "private refusal text"}}),
            ("message_delta", {"delta": {"stop_reason": "refusal"}}),
            ("message_stop", {"type": "message_stop"}),
        ])

        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(root / "cache.json"),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            stderr = io.StringIO()
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_batches", return_value=[("one diff", ("one.go",))]) as batches, \
                 patch.object(review.urllib.request, "urlopen", return_value=response) as urlopen, \
                 patch("sys.argv", args), redirect_stderr(stderr):
                self.assertEqual(review.main(), 2)
            batches.assert_called_once_with(
                "v4.8.0",
                "abc1234",
                max_bytes=review.INITIAL_DIFF_BYTES,
                max_single_file_bytes=review.MAX_DIFF_BYTES,
            )
            urlopen.assert_called_once()
            self.assertIn("stop_reason=refusal", stderr.getvalue())
            self.assertNotIn("splitting", stderr.getvalue())
            self.assertNotIn("private refusal text", stderr.getvalue())
            self.assertFalse((root / "artifact.json").exists())

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

    def test_max_tokens_splits_only_the_failed_batch(self) -> None:
        clean_batch = {"summary": "No blockers found.", "findings": []}
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
            def batches(_base, _head, *, max_bytes, only_paths=None, max_single_file_bytes=None):
                if only_paths is None:
                    self.assertEqual(max_bytes, review.INITIAL_DIFF_BYTES)
                    return [("passed diff", ("passed.go",)), ("large combined diff", ("two-a.go", "two-b.go"))]
                self.assertEqual(only_paths, {"two-a.go", "two-b.go"})
                self.assertEqual(max_bytes, review.INITIAL_DIFF_BYTES // 2)
                self.assertEqual(max_single_file_bytes, review.MAX_DIFF_BYTES)
                return [("smaller diff two-a", ("two-a.go",)), ("smaller diff two-b", ("two-b.go",))]
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_batches", side_effect=batches) as git_diff_batches, \
                 patch.object(review, "call_model", side_effect=[
                     clean_batch,
                     review.MaxTokensStopError("AI release review response ended with stop_reason=max_tokens"),
                     clean_batch,
                     clean_batch,
                 ]) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            self.assertEqual(git_diff_batches.call_count, 2)
            self.assertEqual(git_diff_batches.call_args_list[1].kwargs["max_bytes"], review.INITIAL_DIFF_BYTES // 2)
            self.assertEqual(call_model.call_args_list, [
                call("test-key", "passed diff", batch_number=1, batch_count=2),
                call("test-key", "large combined diff", batch_number=2, batch_count=2),
                call("test-key", "smaller diff two-a", batch_number=2, batch_count=3),
                call("test-key", "smaller diff two-b", batch_number=3, batch_count=3),
            ])
            self.assertIn("Decision: **PASS**", (root / "review-output" / "AI-release-review.md").read_text(encoding="utf-8"))

    def test_request_timeout_splits_only_the_failed_batch(self) -> None:
        clean_batch = {"summary": "No blockers found.", "findings": []}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "def5678", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(root / "cache" / "review.json"),
                "--artifact-output", str(root / "review-output" / "review.json"),
                "--output", str(root / "review-output" / "AI-release-review.md"),
            ]

            def batches(_base, _head, *, max_bytes, only_paths=None, max_single_file_bytes=None):
                if only_paths is None:
                    self.assertEqual(max_bytes, review.INITIAL_DIFF_BYTES)
                    return [("passed diff", ("passed.go",)), ("slow combined diff", ("two-a.go", "two-b.go"))]
                self.assertEqual(only_paths, {"two-a.go", "two-b.go"})
                self.assertEqual(max_bytes, review.INITIAL_DIFF_BYTES // 2)
                self.assertEqual(max_single_file_bytes, review.MAX_DIFF_BYTES)
                return [("smaller diff two-a", ("two-a.go",)), ("smaller diff two-b", ("two-b.go",))]

            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_batches", side_effect=batches) as build_batches, \
                 patch.object(review, "call_model", side_effect=[
                     clean_batch,
                     review.RequestTimeoutError("AI release review request timed out"),
                     clean_batch,
                     clean_batch,
                 ]) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            self.assertEqual(build_batches.call_count, 2)
            self.assertEqual(call_model.call_args_list, [
                call("test-key", "passed diff", batch_number=1, batch_count=2),
                call("test-key", "slow combined diff", batch_number=2, batch_count=2),
                call("test-key", "smaller diff two-a", batch_number=2, batch_count=3),
                call("test-key", "smaller diff two-b", batch_number=3, batch_count=3),
            ])
            self.assertIn("Decision: **PASS**", (root / "review-output" / "AI-release-review.md").read_text(encoding="utf-8"))

    def test_repeated_max_tokens_at_floor_fails_without_retry_loop(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(root / "cache.json"),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            def batches(_base, _head, *, max_bytes, only_paths=None, max_single_file_bytes=None):
                return [(f"diff limited to {max_bytes}", ("one.go",))]

            stderr = io.StringIO()
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_batches", side_effect=batches) as batch_builder, \
                 patch.object(review, "call_model", side_effect=review.MaxTokensStopError(
                     "AI release review response ended with stop_reason=max_tokens"
                 )) as call_model, \
                 patch("sys.argv", args), redirect_stderr(stderr):
                self.assertEqual(review.main(), 2)
            self.assertEqual(call_model.call_count, 5)
            self.assertEqual(batch_builder.call_count, 5)
            self.assertEqual(
                [entry.kwargs["max_bytes"] for entry in batch_builder.call_args_list],
                [300_000, 150_000, 75_000, 37_500, 32_768],
            )
            self.assertIn("32768-byte minimum", stderr.getvalue())
            self.assertEqual(stderr.getvalue().count("split into"), 4)
            self.assertFalse((root / "artifact.json").exists())


if __name__ == "__main__":
    unittest.main()
