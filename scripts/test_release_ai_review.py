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
        with self.assertRaisesRegex(ValueError, "per-request"):
            review.pack_diff_files(
                [("unlisted-large.bin", "x" * (review.MAX_DIFF_BYTES + 1))],
                max_bytes=review.INITIAL_DIFF_BYTES,
                max_single_file_bytes=review.MAX_DIFF_BYTES,
            )
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
        with patch.object(review, "git_binary_paths", return_value=[]), \
             patch.object(review.subprocess, "run", side_effect=[first, second]) as run:
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
        artifact = review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        blocked, unanswered = review.evaluate(artifact, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        self.assertEqual(blocked["decision"], "block")
        self.assertEqual(unanswered, 1)
        finding_id = blocked["findings"][0]["id"]
        artifact_digest = blocked["artifact_digest"]
        reworded = json.loads(json.dumps(candidate))
        reworded["findings"][0]["title"] = "Rollback instructions need evidence"
        reworded["findings"][0]["rationale"] = "A changed model explanation for the same source location."
        rerun, unanswered = review.evaluate(reworded, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        self.assertEqual(rerun["findings"][0]["id"], finding_id)
        self.assertEqual(unanswered, 1)
        passed, unanswered = review.evaluate(artifact, {artifact_digest: "Reviewed this exact release review artifact; rollback step was added and verified."}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        self.assertEqual(passed["decision"], "pass")
        self.assertEqual(unanswered, 0)
        self.assertIn("rollback step was added", review.render(passed, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "v5.0.0"))

    def test_finding_identity_changes_when_location_category_or_range_changes(self) -> None:
        candidate = {
            "summary": "One issue.",
            "findings": [{
                "severity": "blocking", "category": "security", "title": "Unsafe verification",
                "rationale": "The verifier accepts an unexpected signer.",
                "locations": [{"file": "internal/releaseverify/verify.go", "line": 137}],
            }],
        }
        baseline, _ = review.evaluate(candidate, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        finding = candidate["findings"][0]
        variants = [
            {**candidate, "findings": [{**finding, "category": "correctness"}]},
            {**candidate, "findings": [{**finding, "locations": [{"file": "internal/releaseverify/verify.go", "line": 138}]}]},
        ]
        for variant in variants:
            changed, _ = review.evaluate(variant, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
            self.assertNotEqual(baseline["findings"][0]["id"], changed["findings"][0]["id"])
        new_range, _ = review.evaluate(candidate, {}, "v4.8.0", "dddddddddddddddddddddddddddddddddddddddd")
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
            review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", allowed_files={"inside.go"})

    def test_clean_review_and_sensitive_output(self) -> None:
        report, unanswered = review.evaluate({"summary": "No release blockers found.", "findings": []}, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        self.assertEqual(report["decision"], "pass")
        self.assertEqual(unanswered, 0)
        high_entropy_value = "".join(["Gh", "7k", "L2", "pQ", "9w", "R5", "tX", "3z", "Mv", "8c", "Hs", "4n"])
        with self.assertRaises(ValueError):
            review.evaluate({"summary": "No release blockers found.", "findings": [{
                "severity": "informational", "category": "security", "title": f"API key: {high_entropy_value}",
                "rationale": "A finding", "locations": [{"file": "docs/a.md", "line": 1}],
            }]}, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

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

    def test_diff_secret_patterns_are_rejected_before_codex_invocation(self) -> None:
        aws_key = "AKIA" + "A" * 16
        self.assertTrue(review.contains_sensitive_credential(f"AWS_ACCESS_KEY_ID={aws_key}"))
        entropy_value = "Gh7kL2pQ9wR5tX3zMv8cHs4nB6dK1fT"
        self.assertTrue(review.contains_sensitive_credential("AWS_SECRET_ACCESS_KEY=" + entropy_value))
        self.assertFalse(review.contains_sensitive_credential("api_key: token= ordinary review wording"))

    def test_shell_environment_references_are_not_credential_values(self) -> None:
        self.assertFalse(review.contains_sensitive_credential('TOKEN="${ROUTER_LIVE_E2E_CREDENTIAL:?Set the value}"'))
        self.assertFalse(review.contains_sensitive_credential('TOKEN="${ROUTER_LIVE_E2E_CREDENTIAL}"'))
        self.assertTrue(review.contains_sensitive_credential('TOKEN="${ROUTER_TOKEN:-Gh7kL2pQ9wR5tX3zMv8cHs4nB6dK1fT}"'))

    def test_sensitive_values_are_redacted_from_added_and_removed_diff_lines(self) -> None:
        value = "Gh7kL2pQ9wR5tX3zMv8cHs4nB6dK1fT"
        diff = f"+TOKEN=\"{value}\"\n-TOKEN=\"{value}\"\n"
        redacted = review.redact_sensitive_credentials(diff)
        self.assertNotIn(value, redacted)
        self.assertEqual(redacted.count("[REDACTED CREDENTIAL]"), 2)
        self.assertFalse(review.contains_sensitive_credential(redacted))

    def test_redaction_counts_are_attributed_to_their_file(self) -> None:
        value = "Gh7kL2pQ9wR5tX3zMv8cHs4nB6dK1fT"
        diff = (
            "diff --git a/one.go b/one.go\n+TOKEN=\"" + value + "\"\n"
            "diff --git a/two.go b/two.go\n+TOKEN=\"" + value + "\"\n"
        )
        counts: dict[str, int] = {}
        redacted = review.redact_sensitive_credentials(
            diff, paths=("one.go", "two.go"), redaction_counts=counts,
        )
        self.assertEqual(counts, {"one.go": 1, "two.go": 1})
        self.assertEqual(redacted.count("[REDACTED CREDENTIAL]"), 2)
        self.assertNotIn(value, redacted)

    def test_redaction_counts_are_per_file_bound_into_artifact_and_report(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        coverage = [
            {"file": "one.go", "status": "reviewed", "redaction_count": 2},
            {"file": "two.go", "status": "reviewed", "redaction_count": 0},
        ]
        artifact = review.build_artifact(
            candidate, "v4.8.0", "a" * 40, file_coverage=coverage,
        )
        validated = review.validate_artifact(
            artifact, "v4.8.0", "a" * 40, {"one.go", "two.go"},
            expected_redactions={"one.go": 2, "two.go": 0},
        )
        report, _ = review.evaluate(validated, {}, "v4.8.0", "a" * 40)
        rendered = review.render(report, "v4.8.0", "a" * 40, "v5.0.0")
        self.assertEqual(
            {item["file"]: item["redaction_count"] for item in artifact["file_coverage"]},
            {"one.go": 2, "two.go": 0},
        )
        self.assertIn("| `one.go` | 2 |", rendered)
        self.assertIn("| `two.go` | 0 |", rendered)
        self.assertIn("[REDACTED CREDENTIAL]", review.REVIEW_SYSTEM)
        with self.assertRaisesRegex(ValueError, "redaction counts do not match"):
            review.validate_artifact(
                artifact, "v4.8.0", "a" * 40, {"one.go", "two.go"},
                expected_redactions={"one.go": 1, "two.go": 0},
            )

    def test_incremental_plan_reviews_only_changed_hashes_and_reuses_unchanged_findings(self) -> None:
        prior = [
            {"file": "same.go", "status": "reviewed", "diff_sha256": "a" * 64},
            {"file": "changed.go", "status": "reviewed", "diff_sha256": "b" * 64},
            {"file": "pair.go", "status": "reviewed", "diff_sha256": "c" * 64},
        ]
        current = [
            {"file": "same.go", "status": "reviewed", "diff_sha256": "a" * 64},
            {"file": "changed.go", "status": "reviewed", "diff_sha256": "d" * 64},
            {"file": "pair.go", "status": "reviewed", "diff_sha256": "c" * 64},
            {"file": "new.go", "status": "reviewed", "diff_sha256": "e" * 64},
        ]
        findings = [
            {"id": "unchanged", "files": ["same.go"]},
            {"id": "cross-file", "files": ["changed.go", "pair.go"]},
            {"id": "changed", "files": ["changed.go"]},
        ]
        unchanged, changed, reusable = review.incremental_review_plan(prior, current, findings)
        self.assertEqual(unchanged, {"same.go", "pair.go"})
        self.assertEqual(changed, {"changed.go", "new.go", "pair.go"})
        self.assertEqual([finding["id"] for finding in reusable], ["unchanged"])

    def test_incremental_retains_findings_with_non_text_evidence(self) -> None:
        prior = [
            {"file": "source.go", "status": "reviewed", "diff_sha256": "a" * 64},
            {"file": "model.bin", "status": "binary"},
        ]
        current = [
            {"file": "source.go", "status": "reviewed", "diff_sha256": "b" * 64},
            {"file": "model.bin", "status": "binary"},
        ]
        finding = {"id": "retained", "files": ["source.go", "model.bin"]}
        unchanged, changed, retained = review.incremental_review_plan(prior, current, [finding])
        self.assertEqual(unchanged, set())
        self.assertEqual(changed, {"source.go"})
        self.assertEqual(retained, [finding])

    def test_reviewed_file_diff_hash_is_bound_into_artifact_and_verified(self) -> None:
        diff_hash = "a" * 64
        artifact = review.build_artifact(
            {"summary": "No blockers found.", "findings": []}, "v4.8.0", "a" * 40,
            file_coverage=[{
                "file": "one.go", "status": "reviewed", "redaction_count": 0,
                "diff_sha256": diff_hash,
            }],
        )
        self.assertEqual(artifact["file_coverage"][0]["diff_sha256"], diff_hash)
        review.validate_artifact(
            artifact, "v4.8.0", "a" * 40, {"one.go"},
            expected_diff_hashes={"one.go": diff_hash},
        )
        with self.assertRaisesRegex(ValueError, "diff hashes do not match"):
            review.validate_artifact(
                artifact, "v4.8.0", "a" * 40, {"one.go"},
                expected_diff_hashes={"one.go": "b" * 64},
            )

    def test_file_diff_hashes_are_bound_to_each_git_diff_header(self) -> None:
        first = "diff --git a/one.go b/one.go\n+one\n"
        second = "diff --git a/two.go b/two.go\n+two\n"
        hashes = review.file_diff_hashes_from_batches([(first + second, ("one.go", "two.go"))])
        self.assertEqual(hashes, {
            "one.go": __import__("hashlib").sha256(first.encode()).hexdigest(),
            "two.go": __import__("hashlib").sha256(second.encode()).hexdigest(),
        })

    def test_prior_review_rereviews_cross_file_findings_and_reuses_single_file_findings(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.name", "Review Test"], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.email", "review@example.invalid"], check=True)
            for name in ("one.go", "two.go"):
                (root / name).write_text("base\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "one.go", "two.go"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "base"], check=True)
            base = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            subprocess.run(["git", "-C", str(root), "tag", "v4.8.0"], check=True)
            (root / "one.go").write_text("first reviewed change\n", encoding="utf-8")
            (root / "two.go").write_text("second reviewed change\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "one.go", "two.go"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "initial reviewed head"], check=True)
            prior_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            original_cwd = Path.cwd()
            try:
                os.chdir(root)
                prior_coverage = review.git_file_coverage("v4.8.0", prior_head)
                findings = [
                    {
                        "severity": "blocking", "category": "correctness", "title": "Cross-file blocker",
                        "rationale": "A test finding spanning both files.",
                        "locations": [{"file": "one.go", "line": 1}, {"file": "two.go", "line": 1}],
                    },
                    {
                        "severity": "informational", "category": "correctness", "title": "Prior single-file finding",
                        "rationale": "A test finding limited to the unchanged file.",
                        "locations": [{"file": "two.go", "line": 1}],
                    },
                ]
                prior = review.build_artifact(
                    {"summary": "Full review completed.", "findings": findings},
                    "v4.8.0", prior_head, file_coverage=prior_coverage,
                    provider="codex", model="gpt-6.1-sol", codex_cli_version="codex-cli test",
                    base_commit=base,
                )
                prior_path = root / "prior.json"
                prior_path.write_text(json.dumps(prior), encoding="utf-8")
                (root / "one.go").write_text("fixed changed file\n", encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "one.go"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "fix one file"], check=True)
                target_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                loaded = review.load_prior_artifact(prior_path, "v4.8.0", base, target_head)
                current_coverage = review.git_file_coverage("v4.8.0", target_head)
                current_hashes = review.git_file_diff_hashes("v4.8.0", target_head)
                current_coverage = [
                    {**entry, **({"diff_sha256": current_hashes[entry["file"]]} if entry["status"] == "reviewed" else {})}
                    for entry in current_coverage
                ]
                unchanged, changed, reusable = review.incremental_review_plan(
                    loaded["file_coverage"], current_coverage, loaded["findings"],
                )
                responses = root / "responses.json"
                responses.write_text(json.dumps({"schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {}}))
                output = root / "review.md"
                artifact_output = root / "review.json"
                with patch.object(review, "codex_cli_version", return_value="codex-cli test"), \
                     patch.object(review, "call_codex", return_value={"summary": "Changed file reviewed.", "findings": []}) as call_codex, \
                     patch("sys.argv", [
                         "release_ai_review.py", "--provider", "codex", "--model", "gpt-6.1-sol",
                         "--base", "v4.8.0", "--base-commit", base, "--head", target_head,
                         "--tag", "v5.0.0", "--responses", str(responses), "--prior-artifact", str(prior_path),
                         "--artifact-output", str(artifact_output), "--output", str(output),
                     ]):
                    self.assertEqual(review.main(), 0)
                call_codex.assert_called_once()
                self.assertIn("one.go", call_codex.call_args.args[0])
                self.assertIn("two.go", call_codex.call_args.args[0])
                incremental = json.loads(artifact_output.read_text(encoding="utf-8"))
                self.assertEqual({item["file"] for item in incremental["file_coverage"]}, {"one.go", "two.go"})
                self.assertEqual([item["title"] for item in incremental["findings"]], ["Prior single-file finding"])
            finally:
                os.chdir(original_cwd)
            self.assertEqual(unchanged, {"two.go"})
            self.assertEqual(changed, {"one.go", "two.go"})
            self.assertEqual([item["title"] for item in reusable], ["Prior single-file finding"])

    def test_model_timeout_fails_without_logging_request(self) -> None:
        with patch.object(review.urllib.request, "urlopen", side_effect=TimeoutError()):
            with self.assertRaisesRegex(review.RequestTimeoutError, "timed out after"):
                review.call_model("not-a-real-key", "diff contents", timeout=0.001)

    def test_model_http_error_reports_status_without_response_body(self) -> None:
        stderr = io.StringIO()
        failure = review.urllib.error.HTTPError(
            review.API_URL, 400, "Bad request", {}, io.BytesIO(
                b'{"type":"error","error":{"type":"invalid_request_error","message":"request is too large"}}'
            )
        )
        with patch.object(review.urllib.request, "urlopen", side_effect=failure), redirect_stderr(stderr):
            with self.assertRaisesRegex(RuntimeError, "HTTP status 400") as raised:
                review.call_model("test-key", "diff")
        self.assertNotIn("request is too large", str(raised.exception))
        self.assertIn("error.type=invalid_request_error", stderr.getvalue())
        self.assertIn("error.message=request is too large", stderr.getvalue())

    def test_cli_fails_closed_when_credential_is_missing(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "review.md"
            artifact_output = Path(temporary) / "review-artifact.json"
            with patch.dict(os.environ, {}, clear=True), patch("sys.argv", [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--tag", "v5.0.0",
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
        artifact = review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
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
            with patch.dict(os.environ, {}, clear=True), \
                 patch.object(review, "git_diff_names", return_value=[]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "call_model") as call_model, patch("sys.argv", [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(cache),
                "--artifact-output", str(artifact_output), "--output", str(output),
            ]):
                self.assertEqual(review.main(), 0)
            call_model.assert_not_called()
            self.assertIn("Decision: **PASS**", output.read_text(encoding="utf-8"))
            self.assertIn(artifact["artifact_digest"], output.read_text(encoding="utf-8"))
            self.assertEqual(json.loads(artifact_output.read_text(encoding="utf-8")), artifact)

    def test_finding_acceptance_matches_title_and_files_across_changing_ids(self) -> None:
        candidate = {
            "summary": "A detector limitation is accepted for a later release.",
            "findings": [{
                "severity": "blocking", "category": "security",
                "title": "Detector deadline does not bound queued classifier work",
                "rationale": "The classifier can continue after the request deadline.",
                "locations": [{"file": "examples/detectors/prompt-injection/server.py", "line": 115}],
            }],
        }
        first = review.build_artifact(candidate, "v4.8.0", "a" * 40)
        later = review.build_artifact(candidate, "v4.8.0", "b" * 40)
        self.assertNotEqual(first["findings"][0]["id"], later["findings"][0]["id"])
        blob_sha = "1" * 40
        matches = review.parse_finding_acceptances([{
            "category": "SECURITY",
            "title": " detector deadline does not bound queued classifier work ",
            "files": ["examples/detectors/prompt-injection/server.py"],
            "git_blob_shas": {"examples/detectors/prompt-injection/server.py": blob_sha},
            "response": "Accepted for v5.1; bound classifier admission and work before that release.",
        }])
        with patch.object(review, "git_blob_sha", return_value=blob_sha):
            report, unanswered = review.evaluate(later, {}, "v4.8.0", "b" * 40, matches)
        self.assertEqual(unanswered, 0)
        self.assertEqual(report["findings"][0]["maintainer_response"], next(iter(matches.values()))["response"])

    def test_finding_acceptance_does_not_match_changed_blob_or_category(self) -> None:
        candidate = {
            "summary": "A finding remains blocking.",
            "findings": [{
                "severity": "blocking", "category": "security", "title": "Stable finding title",
                "rationale": "The issue remains exploitable.",
                "locations": [{"file": "internal/router/example.go", "line": 10}],
            }],
        }
        artifact = review.build_artifact(candidate, "v4.8.0", "d" * 40)
        path = "internal/router/example.go"
        response = "Accepted only for the reviewed code."
        for accepted_category, accepted_blob in (("correctness", "3" * 40), ("security", "2" * 40)):
            entries = review.parse_finding_acceptances([{
                "category": accepted_category, "title": " stable   finding title ",
                "files": [path], "git_blob_shas": {path: accepted_blob}, "response": response,
            }])
            with patch.object(review, "git_blob_sha", return_value="3" * 40):
                report, unanswered = review.evaluate(artifact, {}, "v4.8.0", "d" * 40, entries)
            self.assertEqual(unanswered, 1)
            self.assertEqual(report["findings"][0]["maintainer_response"], "")
            self.assertEqual(len(report["unmatched_finding_acceptances"]), 1)

    def test_finding_acceptances_reject_duplicate_or_invalid_entries(self) -> None:
        entry = {
            "category": "security", "title": "Accepted finding", "files": ["docs/release.md"],
            "git_blob_shas": {"docs/release.md": "1" * 40}, "response": "Accepted for v5.1.",
        }
        with self.assertRaisesRegex(ValueError, "duplicate"):
            review.parse_finding_acceptances([entry, entry])
        with self.assertRaisesRegex(ValueError, "unique files"):
            review.parse_finding_acceptances([{
                "category": "security", "title": "Unsafe path", "files": ["../outside"],
                "git_blob_shas": {"../outside": "1" * 40}, "response": "Accepted.",
            }])

    def test_detector_v51_acceptance_is_bound_to_title_and_file(self) -> None:
        response_data = json.loads((Path(__file__).parent.parent / "docs/release-review-responses.json").read_text(encoding="utf-8"))
        acceptances = review.parse_finding_acceptances(response_data["finding_acceptances"])
        candidate = {
            "summary": "Detector deadline is deferred.",
            "findings": [{
                "severity": "informational", "category": "security",
                "title": "Detector deadline does not bound queued classifier work",
                "rationale": "Queued work can outlive the deadline.",
                "locations": [
                    {"file": "examples/detectors/prompt-injection/server.py", "line": 115},
                    {"file": "examples/detectors/prompt-injection/server.py", "line": 185},
                ],
            }],
        }
        artifact = review.build_artifact(candidate, "v4.8.0", "c" * 40)
        blob_sha = response_data["finding_acceptances"][0]["git_blob_shas"]["examples/detectors/prompt-injection/server.py"]
        with patch.object(review, "git_blob_sha", return_value=blob_sha):
            report, unanswered = review.evaluate(artifact, {}, "v4.8.0", "c" * 40, acceptances)
        self.assertEqual(unanswered, 0)
        self.assertIn("Accepted for v5.1", report["findings"][0]["maintainer_response"])

    def test_unmatched_finding_acceptance_is_rendered(self) -> None:
        candidate = {"summary": "No findings.", "findings": []}
        artifact = review.build_artifact(candidate, "v4.8.0", "e" * 40)
        acceptances = review.parse_finding_acceptances([{
            "category": "security", "title": "No longer reported", "files": ["docs/old.md"],
            "git_blob_shas": {"docs/old.md": "1" * 40}, "response": "Accepted previously.",
        }])
        report, unanswered = review.evaluate(artifact, {}, "v4.8.0", "e" * 40, acceptances)
        rendered = review.render(report, "v4.8.0", "e" * 40, "v5.0.0")
        self.assertEqual(unanswered, 0)
        self.assertIn("Unmatched maintainer finding acceptances", rendered)
        self.assertIn("No longer reported", rendered)

    def test_git_blob_sha_uses_blob_at_requested_ref(self) -> None:
        path = "examples/detectors/prompt-injection/server.py"
        expected = subprocess.check_output(["git", "rev-parse", f"HEAD:{path}"], text=True).strip()
        self.assertEqual(review.git_blob_sha("HEAD", path), expected)

    def test_streaming_request_accumulates_final_text_block_and_message_delta_stop_reason(self) -> None:
        response = sse_response([
            ("content_block_start", {"index": 0, "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"index": 0, "delta": {"type": "text_delta", "text": "obsolete answer"}}),
            ("content_block_stop", {"index": 0}),
            ("content_block_start", {"index": 1, "content_block": {"type": "text", "text": ""}}),
            ("content_block_delta", {"index": 1, "delta": {"type": "text_delta", "text": '{"summary":"clean",' }}),
            ("content_block_delta", {"index": 1, "delta": {"type": "text_delta", "text": '"findings":[]}' }}),
            ("content_block_stop", {"index": 1}),
            ("message_delta", {"delta": {"stop_reason": "end_turn"}, "usage": {"output_tokens": 4321}}),
            ("message_stop", {"type": "message_stop"}),
        ])
        stderr = io.StringIO()
        with redirect_stderr(stderr), patch.object(review.urllib.request, "urlopen", return_value=response) as urlopen:
            parsed = review.call_model("test-key", "diff")
        request = urlopen.call_args.args[0]
        payload = json.loads(request.data)
        self.assertEqual(payload["max_tokens"], 64000)
        self.assertTrue(payload["stream"])
        self.assertEqual(urlopen.call_args.kwargs["timeout"], 900)
        self.assertNotIn("temperature", payload)
        self.assertNotIn("thinking", payload)
        self.assertEqual(payload["output_config"], {
            "effort": "medium",
            "format": {"type": "json_schema", "schema": review.REVIEW_OUTPUT_SCHEMA},
        })
        self.assertIn("batch 1/1 usage.output_tokens=4321", stderr.getvalue())
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

    def test_model_handles_max_tokens_before_requiring_text_block(self) -> None:
        response = sse_response([
            ("message_delta", {"delta": {"stop_reason": "max_tokens"}}),
            ("message_stop", {"type": "message_stop"}),
        ])
        with patch.object(review.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(review.MaxTokensStopError, "stop_reason=max_tokens"):
                review.call_model("test-key", "diff")

    def test_empty_end_turn_reports_safe_stop_reason(self) -> None:
        response = sse_response([
            ("message_delta", {"delta": {"stop_reason": "end_turn"}}),
            ("message_stop", {"type": "message_stop"}),
        ])
        with patch.object(review.urllib.request, "urlopen", return_value=response):
            with self.assertRaisesRegex(RuntimeError, "no text block \\(stop_reason=end_turn\\)"):
                review.call_model("test-key", "diff")

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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(root / "cache.json"),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            stderr = io.StringIO()
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_names", return_value=["one.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "git_diff_batches", return_value=[("one diff", ("one.go",))]) as batches, \
                 patch.object(review.urllib.request, "urlopen", return_value=response) as urlopen, \
                 patch("sys.argv", args), redirect_stderr(stderr):
                self.assertEqual(review.main(), 2)
            batches.assert_called_once_with(
                "v4.8.0",
                "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "dddddddddddddddddddddddddddddddddddddddd", "--tag", "v5.0.0",
                "--responses", str(root / "responses.json"), "--artifact-cache", str(root / "cache" / "review.json"),
                "--artifact-output", str(root / "review-output" / "review.json"),
                "--output", str(root / "review-output" / "AI-release-review.md"),
            ]
            (root / "responses.json").write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_names", return_value=["router.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "git_diff_batches", return_value=[("changed range diff", ("router.go",))]), \
                 patch.object(review, "call_model", return_value=candidate) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            call_model.assert_called_once_with("test-key", "changed range diff", batch_number=1, batch_count=1)

    def test_codex_provider_generates_range_artifact_without_anthropic_credential(self) -> None:
        candidate = {"summary": "No blocking findings.", "findings": []}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            responses.write_text(json.dumps({"schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {}}), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "dddddddddddddddddddddddddddddddddddddddd", "--tag", "v5.0.0",
                "--responses", str(responses), "--provider", "codex",
                "--artifact-output", str(root / "review.json"), "--output", str(root / "review.md"),
            ]
            actual_base_commit = review.resolve_git_commit("v4.8.0")
            with patch.dict(os.environ, {}, clear=True), \
                 patch.object(review, "git_file_coverage", return_value=[{"file": "one.go", "status": "reviewed"}]), \
                 patch.object(review, "resolve_git_commit", side_effect=lambda ref: actual_base_commit if ref == "v4.8.0" else ref), \
                 patch.object(review, "git_diff_names", return_value=["one.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "git_diff_batches", return_value=[("diff contents", ("one.go",))]), \
                 patch.object(review, "codex_cli_version", return_value="codex-cli 0.169.0"), \
                 patch.object(review, "call_codex", return_value=candidate) as call_codex, \
                 patch.object(review, "call_model") as call_model, patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            call_codex.assert_called_once()
            self.assertEqual(call_codex.call_args.kwargs["model"], "gpt-6.1-sol")
            call_model.assert_not_called()
            artifact = json.loads((root / "review.json").read_text(encoding="utf-8"))
            self.assertEqual((artifact["provider"], artifact["model"]), ("codex", "gpt-6.1-sol"))
            self.assertEqual(artifact["codex_cli_version"], "codex-cli 0.169.0")

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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "dddddddddddddddddddddddddddddddddddddddd", "--tag", "v5.0.0",
                "--responses", str(root / "responses.json"), "--artifact-cache", str(root / "cache" / "review.json"),
                "--artifact-output", str(root / "review-output" / "review.json"),
                "--output", str(root / "review-output" / "AI-release-review.md"),
            ]
            (root / "responses.json").write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            batches = [("diff batch one", ("router.go",)), ("diff batch two", ("docs/release.md",))]
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_names", return_value=["router.go", "docs/release.md"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "git_diff_batches", return_value=batches), \
                 patch.object(review, "call_model", side_effect=[clean_batch, blocking_batch]) as call_model, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 1)
            self.assertEqual(call_model.call_args_list, [
                call("test-key", "diff batch one", batch_number=1, batch_count=2),
                call("test-key", "diff batch two", batch_number=2, batch_count=2),
            ])
            artifact = json.loads((root / "review-output" / "review.json").read_text(encoding="utf-8"))
            self.assertEqual(artifact["summary"],
                             "Reviewed 2 changed text files across 2 bounded batches; reused prior results for 0 unchanged text files; recorded 0 binary file hashes and 0 generated-file checks within a 2-file release range.")
            self.assertEqual(artifact["file_coverage"], [
                {"file": "docs/release.md", "status": "reviewed", "redaction_count": 0},
                {"file": "router.go", "status": "reviewed", "redaction_count": 0},
            ])
            self.assertEqual(len(artifact["findings"]), 1)
            self.assertEqual(artifact["findings"][0]["severity"], "blocking")
            self.assertIn("Decision: **BLOCK**", (root / "review-output" / "AI-release-review.md").read_text(encoding="utf-8"))

    def test_artifact_requires_explicit_verification_for_generated_coverage(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        with self.assertRaisesRegex(ValueError, "verification allowlist"):
            review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
                "file": "docs/generated.json", "status": "generated-and-verified", "verification": "",
            }])
        with patch.dict(review.GENERATED_FILE_VERIFICATION_ALLOWLIST, {
            "docs/generated.json": "admin-openapi-check",
        }):
            with self.assertRaisesRegex(ValueError, "verification allowlist"):
                review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
                    "file": "docs/unlisted.json", "status": "generated-and-verified",
                    "verification": "admin-openapi-check",
                }])
            with self.assertRaisesRegex(ValueError, "verification allowlist"):
                review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
                    "file": "docs/generated.json", "status": "generated-and-verified",
                    "verification": "some other check",
                }])
            artifact = review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
                "file": "docs/generated.json", "status": "generated-and-verified",
                "verification": "admin-openapi-check",
            }])
        self.assertEqual(artifact["file_coverage"], [{
            "file": "docs/generated.json", "status": "generated-and-verified",
            "verification": "admin-openapi-check", "redaction_count": 0,
        }])

    def test_generated_static_directory_requires_named_check_coverage(self) -> None:
        path = "internal/router/admindist/static/admin-abc.js"
        self.assertEqual(review.generated_check(path), "supplemental-checks / generated-release-files")
        artifact = review.build_artifact({"summary": "Generated output was checked.", "findings": []}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
            "file": path, "status": "generated-and-verified",
            "verification": "supplemental-checks / generated-release-files",
        }])
        with self.assertRaisesRegex(ValueError, "coverage status does not match"):
            forged = review.build_artifact({"summary": "No blockers found.", "findings": []}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
                "file": path, "status": "reviewed",
            }])
            review.validate_artifact(forged, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {path})

    def test_generated_named_check_must_pass_on_reviewed_commit(self) -> None:
        artifact = review.build_artifact({"summary": "Generated spec was checked.", "findings": []}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
            "file": "docs/specs/admin-v1.openapi.json", "status": "generated-and-verified",
            "verification": "supplemental-checks / generated-release-files",
        }])
        check_run = {
            "name": "generated-release-files", "app": {"slug": "github-actions"},
            "details_url": "https://github.com/metrum-ai/router/actions/runs/123/job/456",
            "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "status": "completed",
            "conclusion": "success", "started_at": "2026-01-01T00:00:00Z",
        }
        checks = subprocess.CompletedProcess(args=[], returncode=0, stdout=json.dumps({"check_runs": [check_run]}), stderr="")
        runs = subprocess.CompletedProcess(args=[], returncode=0, stdout=json.dumps({"workflow_runs": [{
            "id": 123, "path": ".github/workflows/supplemental-checks.yml",
        }]}), stderr="")
        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "metrum-ai/router"}), patch.object(review.subprocess, "run", side_effect=[checks, runs]):
            review.verify_generated_file_checks(artifact, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        failed_check = {**check_run, "conclusion": "failure"}
        failed = subprocess.CompletedProcess(args=[], returncode=0, stdout=json.dumps({"check_runs": [failed_check]}), stderr="")
        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "metrum-ai/router"}), patch.object(review.subprocess, "run", side_effect=[failed, runs]):
            with self.assertRaisesRegex(ValueError, "generated-file checks have not passed"):
                review.verify_generated_file_checks(artifact, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

    def test_generated_check_api_failure_is_clear_and_fails_closed(self) -> None:
        artifact = review.build_artifact({"summary": "Generated spec was checked.", "findings": []}, "v4.8.0", "a" * 40, file_coverage=[{
            "file": "docs/specs/admin-v1.openapi.json", "status": "generated-and-verified",
            "verification": "supplemental-checks / generated-release-files",
        }])
        with patch.dict(os.environ, {"GITHUB_REPOSITORY": "metrum-ai/router"}), \
             patch.object(review.subprocess, "run", side_effect=subprocess.CalledProcessError(1, ["gh", "api"])):
            with self.assertRaisesRegex(ValueError, "GitHub could not verify required generated-file checks"):
                review.verify_generated_file_checks(artifact, "a" * 40)

    def test_fresh_review_verifies_generated_checks_before_pass(self) -> None:
        generated = {
            "file": "docs/specs/admin-v1.openapi.json", "status": "generated-and-verified",
            "verification": "supplemental-checks / generated-release-files", "redaction_count": 0,
        }
        base_commit = "a" * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--base-commit", base_commit,
                "--head", "d" * 40, "--tag", "v5.0.0", "--responses", str(responses),
                "--provider", "codex", "--artifact-output", str(root / "review.json"),
                "--output", str(root / "review.md"),
            ]
            with patch.dict(os.environ, {}, clear=True), \
                 patch.object(review, "resolve_git_commit", return_value=base_commit), \
                 patch.object(review, "git_file_coverage", return_value=[generated]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "git_diff_batches", return_value=[]), \
                 patch.object(review, "codex_cli_version", return_value="codex-cli test"), \
                 patch.object(review, "verify_generated_file_checks") as verify, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            verify.assert_called_once()
            self.assertIn("Decision: **PASS**", (root / "review.md").read_text(encoding="utf-8"))

    def test_cached_review_verifies_generated_checks_before_pass(self) -> None:
        base_commit = review.resolve_git_commit("v4.8.0")
        head = "d" * 40
        coverage = [{
            "file": "docs/specs/admin-v1.openapi.json", "status": "generated-and-verified",
            "verification": "supplemental-checks / generated-release-files", "redaction_count": 0,
        }]
        artifact = review.build_artifact(
            {"summary": "Generated spec was checked.", "findings": []}, "v4.8.0", head,
            file_coverage=coverage, provider="codex", model="gpt-6.1-sol", base_commit=base_commit,
            codex_cli_version="codex-cli test",
        )
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            cache = root / "cache.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            cache.write_text(json.dumps(artifact), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--base-commit", base_commit,
                "--head", head, "--tag", "v5.0.0", "--responses", str(responses),
                "--provider", "codex", "--artifact-cache", str(cache),
                "--artifact-output", str(root / "review.json"), "--output", str(root / "review.md"),
            ]
            with patch.dict(os.environ, {}, clear=True), \
                 patch.object(review, "resolve_git_commit", return_value=base_commit), \
                 patch.object(review, "git_file_coverage", return_value=coverage), \
                 patch.object(review, "git_redaction_counts", return_value={}), \
                 patch.object(review, "git_file_diff_hashes", return_value={}), \
                 patch.object(review, "verify_generated_file_checks") as verify, \
                 patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            verify.assert_called_once()
            self.assertIn("Decision: **PASS**", (root / "review.md").read_text(encoding="utf-8"))

    def test_artifact_coverage_must_exactly_match_changed_files(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        artifact = review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
            "file": "one.go", "status": "reviewed",
        }])
        with self.assertRaisesRegex(ValueError, "coverage does not match the diff"):
            review.validate_artifact(artifact, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"one.go", "two.go"})
        with self.assertRaisesRegex(ValueError, "coverage does not match the diff"):
            review.validate_artifact(artifact, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", set())
        with self.assertRaisesRegex(ValueError, "does not match the requested range"):
            review.validate_artifact(artifact, "v4.8.0", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", {"one.go"})
        with self.assertRaisesRegex(ValueError, "requested base commit does not match"):
            review.validate_artifact(
                artifact, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"one.go"},
                base_commit="b" * 40,
            )

    def test_binary_changes_are_covered_by_before_and_after_sha256(self) -> None:
        before = "a" * 64
        after = "b" * 64
        candidate = {"summary": "Binary assets have recorded content hashes.", "findings": []}
        artifact = review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
            "file": "assets/logo.png", "status": "binary", "sha256_before": before, "sha256_after": after,
        }])
        self.assertEqual(artifact["file_coverage"], [{
            "file": "assets/logo.png", "status": "binary", "sha256_before": before, "sha256_after": after,
            "redaction_count": 0,
        }])
        review.validate_artifact(artifact, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"assets/logo.png"}, {
            "assets/logo.png": {"before": before, "after": after},
        })
        with self.assertRaisesRegex(ValueError, "binary hashes do not match"):
            review.validate_artifact(artifact, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"assets/logo.png"}, {
                "assets/logo.png": {"before": before, "after": "c" * 64},
            })

    def test_binary_detection_and_hashing_use_numstat_and_git_objects(self) -> None:
        changed = type("Result", (), {"stdout": b"assets/logo.png\0"})()
        before_oid = type("Result", (), {"stdout": "before-oid\n"})()
        before_blob = type("Result", (), {"stdout": b"old\0binary"})()
        with patch.object(review.subprocess, "run", side_effect=[changed, before_oid, before_blob]) as run:
            self.assertEqual(review.git_binary_paths("base", "head"), ["assets/logo.png"])
        self.assertIn("git", run.call_args.args[0])
        self.assertIn("--name-only", run.call_args_list[0].args[0])
        with patch.object(review.subprocess, "run", side_effect=[
            type("Result", (), {"stdout": "before-oid\n"})(),
            type("Result", (), {"stdout": b"old binary"})(),
            type("Result", (), {"stdout": "after-oid\n"})(),
            type("Result", (), {"stdout": b"new binary"})(),
        ]):
            hashes = review.git_binary_hashes("base", "head", {"assets/logo.png"})
        import hashlib
        self.assertEqual(hashes["assets/logo.png"], {
            "before": hashlib.sha256(b"old binary").hexdigest(),
            "after": hashlib.sha256(b"new binary").hexdigest(),
        })

    def test_openapi_spec_uses_named_generated_check_instead_of_text_diff(self) -> None:
        paths = ["docs/specs/admin-v1.openapi.json", "assets/logo.png", "src/change.go"]
        with patch.object(review, "git_diff_names", return_value=paths), \
             patch.object(review, "git_binary_paths", return_value=["assets/logo.png"]), \
             patch.object(review.subprocess, "run", return_value=type("Result", (), {"stdout": "diff --git a/src/change.go b/src/change.go\n+change\n"})()) as run:
            batches = review.git_diff_batches("base", "head")
        self.assertEqual(batches[0][1], ("src/change.go",))
        self.assertTrue(all("admin-v1.openapi.json" not in command.args[0][-1] and "logo.png" not in command.args[0][-1]
                            for command in run.call_args_list))
        self.assertEqual(review.GENERATED_FILE_VERIFICATION_ALLOWLIST["docs/specs/admin-v1.openapi.json"],
                         "supplemental-checks / generated-release-files")

    def test_codex_provider_uses_read_only_cli_schema_and_strips_secret_environment(self) -> None:
        fake = subprocess.CompletedProcess(args=[], returncode=0, stdout='{"summary":"Clean.","findings":[]}', stderr="")
        observed = {}
        def fake_run(command, **kwargs):
            observed["contents"] = [path.name for path in Path(kwargs["cwd"]).iterdir()]
            observed["cwd"] = kwargs["cwd"]
            observed["kwargs"] = kwargs
            observed["home_exists_during_call"] = Path(kwargs["env"]["HOME"]).is_dir()
            observed["command"] = command
            return fake
        repository_root = Path(review.__file__).resolve().parents[1]
        login_dir = str(Path(tempfile.gettempdir()) / "codex-login-fixture")
        with patch.dict(os.environ, {
            "ANTHROPIC_API_KEY": "must-not-pass", "OTHER_SECRET": "must-not-pass",
            "CODEX_HOME": login_dir, "HOME": str(repository_root),
        }, clear=True), \
             patch.object(review.subprocess, "run", side_effect=fake_run) as run:
            result = review.call_codex("diff payload", model="gpt-6.1-sol")
        self.assertEqual(result, {"summary": "Clean.", "findings": []})
        command = observed["command"]
        self.assertEqual(command[:7], ["codex", "exec", "-m", "gpt-6.1-sol", "--sandbox", "read-only", "--ephemeral"])
        self.assertIn("--skip-git-repo-check", command)
        self.assertIn("--output-schema", command)
        self.assertEqual(command[-1], "-")
        self.assertIn("diff payload", observed["kwargs"]["input"])
        self.assertNotIn("ANTHROPIC_API_KEY", observed["kwargs"]["env"])
        self.assertNotIn("OTHER_SECRET", observed["kwargs"]["env"])
        codex_cwd = Path(observed["cwd"]).resolve()
        with self.assertRaises(ValueError):
            codex_cwd.relative_to(repository_root)
        self.assertEqual(codex_cwd.parent, Path(tempfile.gettempdir()).resolve())
        self.assertEqual(observed["kwargs"]["env"]["CODEX_HOME"], login_dir)
        self.assertNotEqual(observed["kwargs"]["env"]["HOME"], str(repository_root))
        self.assertNotEqual(observed["kwargs"]["env"]["HOME"], login_dir)
        self.assertTrue(observed["home_exists_during_call"])
        self.assertEqual(observed["kwargs"]["env"]["TMPDIR"], str(Path(tempfile.gettempdir()).resolve()))
        self.assertEqual(observed["contents"], ["schema.json"])

    def test_codex_provider_rejects_nonzero_invalid_json_and_invalid_schema(self) -> None:
        cases = [
            (subprocess.CompletedProcess(args=[], returncode=1, stdout="", stderr="secret output"), "exited with status 1"),
            (subprocess.CompletedProcess(args=[], returncode=0, stdout="not json", stderr=""), "not valid JSON"),
            (subprocess.CompletedProcess(args=[], returncode=0, stdout='{"summary":"missing findings"}', stderr=""), "required schema"),
        ]
        for result, message in cases:
            with self.subTest(message=message), patch.object(review.subprocess, "run", return_value=result):
                with self.assertRaisesRegex(RuntimeError, message):
                    review.call_codex("diff payload")

    def test_codex_model_is_bound_into_artifact_digest_and_report(self) -> None:
        artifact = review.build_artifact(
            {"summary": "No blocking findings.", "findings": []}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
            provider="codex", model="gpt-6.1-sol", codex_cli_version="codex-cli 0.169.0",
            file_coverage=[{"file": "one.go", "status": "reviewed"}],
        )
        validated = review.validate_artifact(artifact, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", {"one.go"})
        report, _ = review.evaluate(validated, {}, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
        self.assertEqual(validated["model"], "gpt-6.1-sol")
        self.assertIn("- Model: `gpt-6.1-sol`", review.render(report, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "v5.0.0"))
        self.assertEqual(validated["verdict"], "PASS")
        self.assertEqual(validated["codex_cli_version"], "codex-cli 0.169.0")

    def test_committed_artifact_may_follow_reviewed_commit_only_by_artifact_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.name", "Release Review Test"], check=True)
            subprocess.run(["git", "-C", str(root), "config", "user.email", "release-review-test@example.invalid"], check=True)
            (root / "base.txt").write_text("base\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "base.txt"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "base"], check=True)
            base = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            (root / "src.go").write_text("change\n", encoding="utf-8")
            subprocess.run(["git", "-C", str(root), "add", "src.go"], check=True)
            subprocess.run(["git", "-C", str(root), "commit", "-qm", "reviewed source"], check=True)
            reviewed_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
            original_cwd = Path.cwd()
            try:
                os.chdir(root)
                coverage = review.git_file_coverage(base, reviewed_head)
                diff_hashes = review.git_file_diff_hashes(base, reviewed_head)
                coverage = [
                    {**item, **({"diff_sha256": diff_hashes[item["file"]]} if item["status"] == "reviewed" else {})}
                    for item in coverage
                ]
                base_commit = review.resolve_git_commit(base)
                artifact = review.build_artifact(
                    {"summary": "No blocking findings.", "findings": []}, base, reviewed_head,
                    provider="codex", model="gpt-6.1-sol", codex_cli_version="codex-cli 0.169.0",
                    file_coverage=coverage, base_commit=base_commit,
                )
                key = __import__("hashlib").sha256(f"{base}@{base_commit}..{reviewed_head}".encode()).hexdigest()[:24]
                directory = root / "docs" / "release-reviews"
                directory.mkdir(parents=True)
                artifact_path = directory / f"{key}.json"
                artifact_path.write_text(json.dumps(artifact), encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "commit review artifact"], check=True)
                release_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                loaded, loaded_head = review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, release_head)
                self.assertEqual(loaded, artifact)
                self.assertEqual(loaded_head, reviewed_head)

                unrelated_path = directory / "unrelated.json"
                unrelated_path.write_text("{}\n", encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "docs/release-reviews/unrelated.json"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "add unrelated artifact file"], check=True)
                unrelated_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                with self.assertRaisesRegex(ValueError, "exactly one valid committed review artifact"):
                    review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, unrelated_head)

                unrelated_path.unlink()
                subprocess.run(["git", "-C", str(root), "add", "-A", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "remove unrelated artifact file"], check=True)
                artifact_data = json.loads(artifact_path.read_text(encoding="utf-8"))
                incomplete = dict(artifact_data)
                incomplete["file_coverage"] = []
                unsigned = dict(incomplete)
                unsigned.pop("artifact_digest")
                incomplete["artifact_digest"] = __import__("hashlib").sha256(json.dumps(
                    unsigned, ensure_ascii=True, separators=(",", ":"), sort_keys=True,
                ).encode("utf-8")).hexdigest()
                artifact_path.write_text(json.dumps(incomplete), encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "incomplete review artifact"], check=True)
                incomplete_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                with self.assertRaisesRegex(ValueError, "coverage does not match"):
                    review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, incomplete_head)

                artifact_path.write_text(json.dumps(artifact_data), encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "restore complete artifact"], check=True)
                duplicate_path = directory / "duplicate.json"
                duplicate_path.write_text(json.dumps(artifact_data), encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "duplicate artifact"], check=True)
                duplicate_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                with self.assertRaisesRegex(ValueError, "multiple matching"):
                    review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, duplicate_head)

                duplicate_path.unlink()
                subprocess.run(["git", "-C", str(root), "add", "-A", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "remove duplicate artifact"], check=True)
                wrong_name_path = directory / "wrong-key.json"
                artifact_path.rename(wrong_name_path)
                subprocess.run(["git", "-C", str(root), "add", "-A", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "wrong artifact filename"], check=True)
                wrong_name_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                with self.assertRaisesRegex(ValueError, "filename does not match"):
                    review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, wrong_name_head)

                symlink_path = directory / "symlink.json"
                symlink_path.symlink_to(wrong_name_path.name)
                subprocess.run(["git", "-C", str(root), "add", "docs/release-reviews/symlink.json"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "symlink artifact"], check=True)
                symlink_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                with self.assertRaisesRegex(ValueError, "non-regular"):
                    review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, symlink_head)

                symlink_path.unlink()
                wrong_name_path.rename(artifact_path)
                subprocess.run(["git", "-C", str(root), "add", "-A", "docs/release-reviews"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "restore artifact path"], check=True)
                (root / "src.go").write_text("unreviewed change\n", encoding="utf-8")
                subprocess.run(["git", "-C", str(root), "add", "src.go"], check=True)
                subprocess.run(["git", "-C", str(root), "commit", "-qm", "code after artifact"], check=True)
                later_release_head = subprocess.check_output(["git", "-C", str(root), "rev-parse", "HEAD"], text=True).strip()
                with self.assertRaisesRegex(ValueError, "exactly one valid committed review artifact"):
                    review.load_committed_artifact(Path("docs/release-reviews"), base, base_commit, later_release_head)
            finally:
                os.chdir(original_cwd)

    def test_committed_artifact_mode_renders_with_responses_file_without_model_call(self) -> None:
        artifact = review.build_artifact(
            {"summary": "A blocking issue needs maintainer acceptance.", "findings": [{
                "severity": "blocking", "category": "compatibility", "title": "Upgrade evidence required",
                "rationale": "The changed migration behavior needs an explicit acceptance.",
                "locations": [{"file": "one.go", "line": 1}],
            }]}, "v4.8.0", "cccccccccccccccccccccccccccccccccccccccc",
            provider="codex", model="gpt-6.1-sol", codex_cli_version="codex-cli 0.169.0",
            file_coverage=[{"file": "one.go", "status": "reviewed"}],
        )
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "--tag", "v5.0.0",
                "--responses", str(root / "responses.json"),
                "--committed-artifact-dir", str(root / "artifacts"),
                "--artifact-output", str(root / "review.json"), "--output", str(root / "review.md"),
            ]
            (root / "responses.json").write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1",
                "acceptances": {artifact["artifact_digest"]: "Accepted after reviewing the migration note and rollback evidence."},
            }), encoding="utf-8")
            actual_base_commit = review.resolve_git_commit("v4.8.0")
            with patch.dict(os.environ, {}, clear=True), \
                 patch.object(review, "resolve_git_commit", side_effect=lambda ref: actual_base_commit if ref == "v4.8.0" else ref), \
                 patch.object(review, "load_committed_artifact", return_value=(artifact, "cccccccccccccccccccccccccccccccccccccccc")), \
                 patch.object(review, "verify_generated_file_checks"), \
                 patch.object(review, "call_codex") as call_codex, \
                 patch.object(review, "call_model") as call_model, patch("sys.argv", args):
                self.assertEqual(review.main(), 0)
            call_codex.assert_not_called()
            call_model.assert_not_called()
            self.assertIn("Decision: **PASS**", (root / "review.md").read_text(encoding="utf-8"))
            self.assertIn("Model: `gpt-6.1-sol`", (root / "review.md").read_text(encoding="utf-8"))
            self.assertEqual(artifact["verdict"], "BLOCK")
            self.assertIn("Accepted after reviewing", (root / "review.md").read_text(encoding="utf-8"))

    def test_fresh_batches_must_match_git_diff_name_only_paths(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            responses = root / "responses.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(root / "absent-cache.json"),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_names", return_value=["one.go", "two.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "git_diff_batches", return_value=[("one diff", ("one.go",))]), \
                 patch.object(review, "call_model") as call_model, patch("sys.argv", args):
                self.assertEqual(review.main(), 2)
            call_model.assert_not_called()
            self.assertFalse((root / "artifact.json").exists())

    def test_cached_artifact_with_incomplete_file_coverage_fails_closed(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        artifact = review.build_artifact(candidate, "v4.8.0", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", file_coverage=[{
            "file": "one.go", "status": "reviewed",
        }])
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            cache = root / "cached-review.json"
            cache.write_text(json.dumps(artifact), encoding="utf-8")
            responses = root / "responses.json"
            responses.write_text(json.dumps({
                "schema": "metrum-ai-router-release-review-responses/v1", "acceptances": {},
            }), encoding="utf-8")
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(cache),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            with patch.object(review, "git_diff_names", return_value=["one.go", "two.go"]), \
            patch.object(review, "git_binary_paths", return_value=[]), \
                 patch.object(review, "call_model") as call_model, patch("sys.argv", args):
                self.assertEqual(review.main(), 2)
            call_model.assert_not_called()
            self.assertFalse((root / "review.md").exists())

    def test_uncached_review_stops_and_fails_when_batch_two_of_three_fails(self) -> None:
        clean_batch = {"summary": "No blocking findings in this batch.", "findings": []}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            args = [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "dddddddddddddddddddddddddddddddddddddddd", "--tag", "v5.0.0",
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
                 patch.object(review, "git_diff_names", return_value=["one.go", "two.go", "three.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "dddddddddddddddddddddddddddddddddddddddd", "--tag", "v5.0.0",
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
                 patch.object(review, "git_diff_names", return_value=["passed.go", "two-a.go", "two-b.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "dddddddddddddddddddddddddddddddddddddddd", "--tag", "v5.0.0",
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
                 patch.object(review, "git_diff_names", return_value=["passed.go", "two-a.go", "two-b.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(root / "cache.json"),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            def batches(_base, _head, *, max_bytes, only_paths=None, max_single_file_bytes=None):
                return [(f"diff limited to {max_bytes}", ("one.go",))]

            stderr = io.StringIO()
            with patch.dict(os.environ, {"ANTHROPIC_API_KEY": "test-key"}), \
                 patch.object(review, "git_diff_names", return_value=["one.go"]), \
                 patch.object(review, "git_binary_paths", return_value=[]), \
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
