# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib
import json
import io
import os
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
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


class FakeMessagesServer:
    def __init__(self, scripted: list[list[tuple[str, dict[str, object]]]]) -> None:
        self.scripted = scripted
        self.requests: list[dict[str, object]] = []
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                size = int(self.headers.get("content-length", "0"))
                owner.requests.append(json.loads(self.rfile.read(size)))
                events = owner.scripted.pop(0)
                response = b"".join(
                    f"event: {name}\ndata: {json.dumps(payload)}\n\n".encode()
                    for name, payload in events
                )
                self.send_response(200)
                self.send_header("content-type", "text/event-stream")
                self.send_header("content-length", str(len(response)))
                self.end_headers()
                self.wfile.write(response)

            def log_message(self, *_args):
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.url = f"http://127.0.0.1:{self.server.server_port}/v1/messages"

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.server.shutdown()
        self.thread.join()
        self.server.server_close()


def tool_turn(name: str, arguments: dict[str, object], *, stop_reason: str = "tool_use"):
    return [
        ("message_start", {"type": "message_start", "message": {"usage": {"input_tokens": 13}}}),
        ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "thinking", "thinking": ""}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "brief reasoning"}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "signature_delta", "signature": "signed-block"}}),
        ("content_block_start", {"type": "content_block_start", "index": 1, "content_block": {"type": "tool_use", "id": "call-1", "name": name, "input": arguments}}),
        ("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop_reason}, "usage": {"output_tokens": 7}}),
        ("message_stop", {"type": "message_stop"}),
    ]


def final_turn(text: str, *, stop_reason: str = "end_turn"):
    return [
        ("message_start", {"type": "message_start", "message": {"usage": {
            "input_tokens": 17, "cache_read_input_tokens": 2, "cache_creation_input_tokens": 3,
        }}}),
        ("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
        ("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": text}}),
        ("message_delta", {"type": "message_delta", "delta": {"stop_reason": stop_reason}, "usage": {"output_tokens": 9}}),
        ("message_stop", {"type": "message_stop"}),
    ]


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

    def test_finding_must_reference_a_file_covered_by_the_review(self) -> None:
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
                review._send_streaming_turn("not-a-real-key", [], timeout=0.001)

    def test_model_http_error_reports_status_without_response_body(self) -> None:
        stderr = io.StringIO()
        failure = review.urllib.error.HTTPError(
            review.API_URL, 400, "Bad request", {}, io.BytesIO(
                b'{"type":"error","error":{"type":"invalid_request_error","message":"request is too large"}}'
            )
        )
        with patch.object(review.urllib.request, "urlopen", side_effect=failure), redirect_stderr(stderr):
            with self.assertRaisesRegex(RuntimeError, "HTTP status 400") as raised:
                review._send_streaming_turn("test-key", [], timeout=review.REQUEST_TIMEOUT_SECONDS)
        self.assertNotIn("request is too large", str(raised.exception))
        self.assertIn("error.type=invalid_request_error", stderr.getvalue())
        self.assertIn("error.message=request is too large", stderr.getvalue())

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
            with patch.dict(os.environ, {}, clear=True), \
                 patch.object(review, "git_diff_names", return_value=[]), \
                 patch.object(review, "changed_file_metadata", return_value=[]), \
                 patch.object(review, "run_agentic_review") as run_review, patch("sys.argv", [
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(cache),
                "--artifact-output", str(artifact_output), "--output", str(output),
            ]):
                self.assertEqual(review.main(), 0)
            run_review.assert_not_called()
            self.assertIn("Decision: **PASS**", output.read_text(encoding="utf-8"))
            self.assertIn(artifact["artifact_digest"], output.read_text(encoding="utf-8"))
            self.assertEqual(json.loads(artifact_output.read_text(encoding="utf-8")), artifact)

    def test_artifact_requires_explicit_verification_for_generated_coverage(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        with self.assertRaisesRegex(ValueError, "verification allowlist"):
            review.build_artifact(candidate, "v4.8.0", "abc1234", file_coverage=[{
                "file": "docs/generated.json", "status": "generated-and-verified", "verification": "",
            }])
        with patch.dict(review.GENERATED_FILE_VERIFICATION_ALLOWLIST, {
            "docs/generated.json": "admin-openapi-check",
        }):
            with self.assertRaisesRegex(ValueError, "verification allowlist"):
                review.build_artifact(candidate, "v4.8.0", "abc1234", file_coverage=[{
                    "file": "docs/unlisted.json", "status": "generated-and-verified",
                    "verification": "admin-openapi-check",
                }])
            with self.assertRaisesRegex(ValueError, "verification allowlist"):
                review.build_artifact(candidate, "v4.8.0", "abc1234", file_coverage=[{
                    "file": "docs/generated.json", "status": "generated-and-verified",
                    "verification": "some other check",
                }])
            artifact = review.build_artifact(candidate, "v4.8.0", "abc1234", file_coverage=[{
                "file": "docs/generated.json", "status": "generated-and-verified",
                "verification": "admin-openapi-check",
            }])
        self.assertEqual(artifact["file_coverage"], [{
            "file": "docs/generated.json", "status": "generated-and-verified",
            "verification": "admin-openapi-check",
        }])

    def test_artifact_coverage_must_exactly_match_changed_files(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        artifact = review.build_artifact(candidate, "v4.8.0", "abc1234", file_coverage=[{
            "file": "one.go", "status": "reviewed",
        }])
        with self.assertRaisesRegex(ValueError, "coverage does not match the diff"):
            review.validate_artifact(artifact, "v4.8.0", "abc1234", {"one.go", "two.go"})
        with self.assertRaisesRegex(ValueError, "coverage does not match the diff"):
            review.validate_artifact(artifact, "v4.8.0", "abc1234", set())

    def test_binary_artifact_coverage_binds_before_and_after_hashes(self) -> None:
        before, after = "a" * 64, "b" * 64
        artifact = review.build_artifact({"summary": "Binary files are hash-covered.", "findings": []},
                                         "v4.8.0", "abc1234", file_coverage=[{
            "file": "assets/image.png", "status": "binary",
            "sha256_before": before, "sha256_after": after,
        }])
        review.validate_artifact(artifact, "v4.8.0", "abc1234", {"assets/image.png"}, {
            "assets/image.png": {"before": before, "after": after},
        })
        with self.assertRaisesRegex(ValueError, "binary hashes do not match"):
            review.validate_artifact(artifact, "v4.8.0", "abc1234", {"assets/image.png"}, {
                "assets/image.png": {"before": before, "after": "c" * 64},
            })

    def test_cached_artifact_with_incomplete_file_coverage_fails_closed(self) -> None:
        candidate = {"summary": "No blockers found.", "findings": []}
        artifact = review.build_artifact(candidate, "v4.8.0", "abc1234", file_coverage=[{
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
                "release_ai_review.py", "--base", "v4.8.0", "--head", "abc1234", "--tag", "v5.0.0",
                "--responses", str(responses), "--artifact-cache", str(cache),
                "--artifact-output", str(root / "artifact.json"), "--output", str(root / "review.md"),
            ]
            with patch.object(review, "git_diff_names", return_value=["one.go", "two.go"]), \
                 patch.object(review, "changed_file_metadata", return_value=[
                     {"path": "one.go", "size_bytes": 1, "added_lines": 1, "deleted_lines": 0},
                     {"path": "two.go", "size_bytes": 1, "added_lines": 1, "deleted_lines": 0},
                 ]), \
                 patch.object(review, "run_agentic_review") as run_review, patch("sys.argv", args):
                self.assertEqual(review.main(), 2)
            run_review.assert_not_called()
            self.assertFalse((root / "review.md").exists())

    def test_agentic_fake_server_reads_diff_then_returns_verdict_and_caches_stable_prefix(self) -> None:
        verdict = '{"summary":"No blocking issues found.","findings":[]}'
        changed = [
            {"path": "src/a.py", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0},
            {"path": "assets/image.png", "size_bytes": 48, "added_lines": None, "deleted_lines": None,
             "binary": True, "coverage": "binary", "sha256_before": "a" * 64, "sha256_after": "b" * 64},
        ]
        with FakeMessagesServer([
            tool_turn("read_diff", {"path": "src/a.py", "start": 0, "max_bytes": 256}),
            final_turn(verdict),
        ]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed), \
             patch.object(review, "_git_bytes", return_value=b"diff --git a/src/a.py b/src/a.py\n+new\n"):
            result, covered, input_tokens, output_tokens = review.run_agentic_review("test-key", "base", "head")
        self.assertEqual(result["summary"], "No blocking issues found.")
        self.assertEqual(covered, {"src/a.py"})
        self.assertEqual((input_tokens, output_tokens), (35, 16))
        first = server.requests[0]
        self.assertNotIn("diff --git", first["messages"][0]["content"])
        self.assertIn("src/a.py", first["messages"][0]["content"])
        self.assertIn("assets/image.png", first["messages"][0]["content"])
        self.assertEqual(first["system"][0]["cache_control"], {"type": "ephemeral"})
        self.assertEqual(first["tools"][-1]["cache_control"], {"type": "ephemeral"})
        self.assertEqual(first["output_config"]["effort"], "medium")
        self.assertEqual(first["max_tokens"], 64000)
        self.assertTrue(first["stream"])
        self.assertNotIn("temperature", first)
        self.assertNotIn("thinking", first)
        self.assertEqual(len(server.requests), 2)
        assistant_blocks = server.requests[1]["messages"][1]["content"]
        self.assertEqual(assistant_blocks[0]["thinking"], "brief reasoning")
        self.assertEqual(assistant_blocks[0]["signature"], "signed-block")

    def test_agentic_fake_server_fails_closed_when_changed_file_diff_is_not_read(self) -> None:
        changed = [{"path": "src/a.py", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([final_turn('{"summary":"Looks clear.","findings":[]}')]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed):
            with self.assertRaisesRegex(ValueError, "coverage is incomplete"):
                review.run_agentic_review("test-key", "base", "head")

    def test_agentic_fake_server_fails_closed_at_turn_cap(self) -> None:
        changed = [{"path": "src/a.py", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([tool_turn("list_changed_files", {})]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed), \
             patch.object(review, "MAX_REVIEW_TURNS", 1):
            with self.assertRaisesRegex(ValueError, "maximum turn limit"):
                review.run_agentic_review("test-key", "base", "head")

    def test_agentic_fake_server_refusal_fails_closed(self) -> None:
        changed = [{"path": "src/a.py", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([final_turn("private refusal", stop_reason="refusal")]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed):
            with self.assertRaisesRegex(RuntimeError, "refused") as raised:
                review.run_agentic_review("test-key", "base", "head")
        self.assertNotIn("private refusal", str(raised.exception))

    def test_agentic_tool_paths_reject_traversal_and_docs_outside_allowlist(self) -> None:
        changed = {"src/a.py": {"path": "src/a.py"}}
        reads: dict[str, list[tuple[int, int, int]]] = {}
        with self.assertRaisesRegex(ValueError, "invalid"):
            review.execute_client_tool("read_diff", {"path": "../secret", "start": 0, "max_bytes": 10},
                                       base="base", head="head", changed=changed, diff_reads=reads)
        with self.assertRaisesRegex(ValueError, "eligible changed file"):
            review.execute_client_tool("read_diff", {"path": "assets/image.png", "start": 0, "max_bytes": 10},
                                       base="base", head="head", changed={
                                           "assets/image.png": {"path": "assets/image.png", "binary": True},
                                       }, diff_reads=reads)
        with self.assertRaisesRegex(ValueError, "allowed documentation"):
            review.execute_client_tool("read_doc", {"path": "secrets.txt", "start": 0, "max_bytes": 10},
                                       base="base", head="head", changed=changed, diff_reads=reads)
        with self.assertRaisesRegex(ValueError, "generated and covered"):
            review.execute_client_tool("read_doc", {"path": "docs/specs/admin-v1.openapi.json", "start": 0, "max_bytes": 10},
                                       base="base", head="head", changed=changed, diff_reads=reads)
        with self.assertRaisesRegex(ValueError, "invalid"):
            review.execute_client_tool("read_doc", {"path": "docs/../secrets.txt", "start": 0, "max_bytes": 10},
                                       base="base", head="head", changed=changed, diff_reads=reads)
        fake_changed = [{"path": "src/a.py", "size_bytes": 8, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([
            tool_turn("read_diff", {"path": "../secret", "start": 0, "max_bytes": 10}),
            final_turn('{"summary":"No issues.","findings":[]}'),
        ]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=fake_changed), \
             patch.object(review, "_git_bytes") as git_bytes:
            with self.assertRaisesRegex(ValueError, "coverage is incomplete"):
                review.run_agentic_review("test-key", "base", "head")
        git_bytes.assert_not_called()
        self.assertIn("tool path is invalid", server.requests[1]["messages"][2]["content"][0]["content"])

    def test_changed_file_list_includes_numstat_and_byte_size(self) -> None:
        with patch.object(review, "_git_bytes", side_effect=[
            b"4\t2\tsrc/a.py\0-\t-\tassets/image.png\0",
            b"old binary",
            b"new binary",
            b"23",
            b"11",
        ]):
            metadata = review.changed_file_metadata("base", "head")
        self.assertEqual(metadata, [
            {"path": "src/a.py", "size_bytes": 23, "added_lines": 4, "deleted_lines": 2},
            {"path": "assets/image.png", "size_bytes": 11, "added_lines": None, "deleted_lines": None,
             "binary": True, "coverage": "binary",
             "sha256_before": hashlib.sha256(b"old binary").hexdigest(),
             "sha256_after": hashlib.sha256(b"new binary").hexdigest()},
        ])

    def test_agentic_input_token_cap_fails_closed(self) -> None:
        changed = [{"path": "src/a.py", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([final_turn('{"summary":"Looks clear.","findings":[]}')]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed), \
             patch.object(review, "MAX_REVIEW_INPUT_TOKENS", 0):
            with self.assertRaisesRegex(ValueError, "input-token budget"):
                review.run_agentic_review("test-key", "base", "head")

    def test_agentic_max_tokens_retries_same_turn_once(self) -> None:
        changed = [{"path": "docs/specs/admin-v1.openapi.json", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([
            final_turn("", stop_reason="max_tokens"),
            final_turn('{"summary":"No issues.","findings":[]}'),
        ]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed):
            result, covered, _, _ = review.run_agentic_review("test-key", "base", "head")
        self.assertEqual(result["summary"], "No issues.")
        self.assertEqual(covered, set())
        self.assertEqual(len(server.requests), 2)
        self.assertEqual(server.requests[0]["messages"], server.requests[1]["messages"])

    def test_agentic_repeated_max_tokens_fails_closed_after_one_retry(self) -> None:
        changed = [{"path": "docs/specs/admin-v1.openapi.json", "size_bytes": 14, "added_lines": 1, "deleted_lines": 0}]
        with FakeMessagesServer([
            final_turn("", stop_reason="max_tokens"),
            final_turn("", stop_reason="max_tokens"),
        ]) as server, \
             patch.object(review, "API_URL", server.url), \
             patch.object(review, "changed_file_metadata", return_value=changed):
            with self.assertRaisesRegex(RuntimeError, "repeatedly reached max_tokens"):
                review.run_agentic_review("test-key", "base", "head")
        self.assertEqual(len(server.requests), 2)
        self.assertEqual(review.MAX_REVIEW_INPUT_TOKENS, 2_000_000)


if __name__ == "__main__":
    unittest.main()
