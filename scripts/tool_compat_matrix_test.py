#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

import tool_compat_matrix as matrix


def base():
    return copy.deepcopy(matrix.load(matrix.MATRIX_PATH))


class MatrixDataTest(unittest.TestCase):
    def test_committed_matrix_is_valid(self):
        self.assertEqual(matrix.validate(matrix.load(matrix.MATRIX_PATH)), [])

    def test_sdk_suites_are_read_from_the_pin(self):
        suites = matrix.compat_mock_suites()
        self.assertIn("chat_completions_tools", suites)
        self.assertIn("messages_tools", suites)


class CellValidationTest(unittest.TestCase):
    def cell(self, data, cell_id):
        for cell in data["cells"]:
            if cell["id"] == cell_id:
                return cell
        raise AssertionError(f"missing cell {cell_id}")

    def test_unknown_enum_is_rejected(self):
        data = base()
        self.cell(data, "pi-chat-same-dialect-stream")["features"] = ["telepathy"]
        self.assertTrue(any("unknown or empty features" in error for error in matrix.validate(data)))

    def test_failing_cell_needs_an_issue(self):
        data = base()
        self.cell(data, "pi-chat-to-messages-stream").pop("issue")
        errors = matrix.validate(data)
        self.assertTrue(any("failing cell needs a triage issue" in error for error in errors))

    def test_untriaged_live_failure_is_allowed_only_on_request(self):
        data = base()
        cell = self.cell(data, "pi-chat-to-messages-stream")
        cell.pop("issue")
        cell["live"]["triage_required"] = True
        self.assertTrue(any("triage" in error for error in matrix.validate(data)))
        self.assertEqual(
            [error for error in matrix.validate(data, allow_untriaged=True) if "triage" in error],
            [],
        )

    def test_unsupported_cell_must_cite_a_doc_that_exists(self):
        data = base()
        cell = self.cell(data, "codex-responses-provider-hosted-tools")
        cell["doc"] = "docs-site/docs/reference/api-compatibility.md#not-a-heading"
        self.assertTrue(any("anchor not found" in error for error in matrix.validate(data)))
        cell.pop("doc")
        self.assertTrue(any("must cite the doc" in error for error in matrix.validate(data)))

    def test_not_covered_cell_needs_a_reason(self):
        data = base()
        self.cell(data, "langgraph-chat-to-messages-stream").pop("reason")
        self.assertTrue(any("not-covered cell needs a reason" in error for error in matrix.validate(data)))

    def test_live_cell_needs_a_cost_and_a_commit(self):
        data = base()
        cell = self.cell(data, "parallel-gate-chat-through-router")
        cell["live"].pop("cost")
        self.assertTrue(any("cost" in error for error in matrix.validate(data)))
        cell = self.cell(data, "parallel-gate-chat-through-router")
        cell["live"]["commit"] = "main"
        self.assertTrue(any("live.commit" in error for error in matrix.validate(data)))

    def test_estimated_cost_must_be_labelled_invoice_pending(self):
        data = base()
        cell = self.cell(data, "parallel-gate-chat-through-router")
        cell["live"]["cost"] = {"usd": 0.001, "source": "estimate", "scope": "cell"}
        self.assertTrue(any("invoice pending" in error for error in matrix.validate(data)))

    def test_no_unlisted_cost_source(self):
        data = base()
        cell = self.cell(data, "parallel-gate-chat-through-router")
        cell["live"]["cost"] = {"usd": None, "source": "vibes", "scope": "cell"}
        self.assertTrue(any("cost.source" in error for error in matrix.validate(data)))

    def test_suite_claim_must_exist_in_the_pinned_tester(self):
        data = base()
        self.cell(data, "openai-sdk-chat-same-dialect-tools")["suite"] = "chat_completions_telepathy"
        self.assertTrue(any("is not in scripts/compat_mock.py TESTERS" in error for error in matrix.validate(data)))
        self.cell(data, "pi-chat-same-dialect-stream")["suite"] = "chat_completions_tools"
        self.assertTrue(any("only valid for the pinned SDK testers" in error for error in matrix.validate(data)))

    def test_path_must_match_surface_and_upstream(self):
        data = base()
        cell = self.cell(data, "pi-chat-to-responses-stream")
        cell["upstream"] = "anthropic-messages"
        self.assertTrue(any("path chat_to_responses expects" in error for error in matrix.validate(data)))


class CoverageTest(unittest.TestCase):
    def test_removing_a_bridge_cell_is_rejected(self):
        data = base()
        data["cells"] = [
            cell for cell in data["cells"] if cell["id"] != "codex-responses-to-chat-stream"
        ]
        errors = matrix.validate(data)
        self.assertTrue(
            any("surface openai-responses has no cell for path responses_to_chat" in error for error in errors),
            errors,
        )

    def test_removing_a_client_cells_is_rejected(self):
        data = base()
        data["cells"] = [cell for cell in data["cells"] if cell["client"] != "langgraph"]
        errors = matrix.validate(data)
        self.assertTrue(
            any("client langgraph surface openai-chat has no cell" in error for error in errors),
            errors,
        )


class RenderTest(unittest.TestCase):
    def test_render_is_deterministic_and_published(self):
        data = matrix.load(matrix.MATRIX_PATH)
        first = matrix.render(data)
        self.assertEqual(first, matrix.render(copy.deepcopy(data)))
        self.assertEqual(matrix.check_page(data), [])
        self.assertIn("Every cell states what is proven", first)

    def test_render_emits_mdx_safe_html(self):
        # A bare <br> is an unclosed JSX tag in MDX and breaks the docs-site
        # build; every rendered HTML tag must be self-closing or closed.
        rendered = matrix.render(matrix.load(matrix.MATRIX_PATH))
        self.assertNotIn("<br>", rendered)
        for tag in ("<br/>",):
            if tag in rendered:
                self.assertGreater(rendered.count(tag), 0)
        self.assertNotRegex(rendered, r"<br\s*>")

    def test_page_drift_is_reported(self):
        data = matrix.load(matrix.MATRIX_PATH)
        with tempfile.TemporaryDirectory() as temp:
            page = Path(temp) / "page.md"
            page.write_text(matrix.render(data) + "\nstale\n", encoding="utf-8")
            original = matrix.PAGE_PATH
            matrix.PAGE_PATH = page
            try:
                self.assertTrue(any("out of date" in error for error in matrix.check_page(data)))
            finally:
                matrix.PAGE_PATH = original


class IngestAndCompareTest(unittest.TestCase):
    def report(self, status="pass", issue=None):
        return {
            "schema_version": 1,
            "date": "2026-11-03",
            "commit": "abcdef1234567890",
            "runner": "agent-tool-compat-live",
            "cost": {"usd": 0.00042, "source": "usage_rows", "scope": "run"},
            "cells": [{"id": "pi-chat-same-dialect-stream", "status": status, "issue": issue}],
        }

    def test_ingest_pass_upgrades_to_live(self):
        data = base()
        updated, errors = matrix.ingest(data, self.report())
        self.assertEqual(errors, [])
        cell = next(c for c in updated["cells"] if c["id"] == "pi-chat-same-dialect-stream")
        self.assertEqual((cell["tier"], cell["status"]), ("live", "pass"))
        self.assertEqual(cell["live"]["date"], "2026-11-03")
        self.assertEqual(updated["updated"], "2026-11-03")

    def test_ingest_failure_without_issue_is_recorded_but_untriaged(self):
        data = base()
        updated, errors = matrix.ingest(data, self.report("fail"))
        self.assertEqual(errors, [])
        cell = next(c for c in updated["cells"] if c["id"] == "pi-chat-same-dialect-stream")
        self.assertTrue(cell["live"]["triage_required"])
        self.assertTrue(any("triage" in error for error in matrix.validate(updated)))

    def test_ingest_failure_with_issue_is_triaged(self):
        data = base()
        updated, errors = matrix.ingest(data, self.report("fail", issue=123))
        self.assertEqual(errors, [])
        cell = next(c for c in updated["cells"] if c["id"] == "pi-chat-same-dialect-stream")
        self.assertEqual(cell["issue"], 123)
        self.assertEqual(matrix.validate(updated), [])

    def test_ingest_unknown_cell_fails_closed(self):
        data = base()
        report = self.report()
        report["cells"][0]["id"] = "pi-chat-invented-cell"
        _, errors = matrix.ingest(data, report)
        self.assertTrue(any("unknown cell" in error for error in errors))

    def test_compare_detects_regression_and_disappearance(self):
        before = base()
        after = copy.deepcopy(before)
        cell = next(c for c in after["cells"] if c["id"] == "pi-chat-same-dialect-stream")
        cell["status"] = "not-covered"
        cell["tier"] = "not-covered"
        cell["reason"] = "regressed in the nightly"
        after["cells"] = [c for c in after["cells"] if c["id"] != "parallel-gate-messages-to-chat"]
        errors = matrix.compare(before, after)
        self.assertTrue(any("regressed pass -> not-covered" in error for error in errors))
        self.assertTrue(any("cell disappeared" in error for error in errors))

    def test_compare_reports_new_cells_without_failing(self):
        before = base()
        after = copy.deepcopy(before)
        after["cells"][0]["id"] = after["cells"][0]["id"] + "-new"
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(out):
            errors = matrix.compare(before, after)
        self.assertTrue(errors)
        self.assertIn("new cell", out.getvalue())


class CommandLineTest(unittest.TestCase):
    def run_cli(self, argv):
        original = sys.argv
        sys.argv = ["tool_compat_matrix.py"] + argv
        try:
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                return matrix.main()
        finally:
            sys.argv = original

    def test_check_command_passes_on_the_committed_matrix(self):
        self.assertEqual(self.run_cli(["--check"]), 0)

    def test_unreadable_matrix_fails_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            broken = Path(temp) / "matrix.json"
            broken.write_text("{not json", encoding="utf-8")
            self.assertEqual(self.run_cli(["--check", "--matrix", str(broken)]), 2)

    def test_ingested_report_without_write_goes_to_stdout(self):
        with tempfile.TemporaryDirectory() as temp:
            report = Path(temp) / "report.json"
            report.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "date": "2026-11-03",
                        "commit": "abcdef1234567890",
                        "runner": "manual",
                        "cost": {"usd": None, "source": "not_recorded", "scope": "run"},
                        "cells": [{"id": "pi-chat-same-dialect-stream", "status": "pass"}],
                    }
                ),
                encoding="utf-8",
            )
            out = io.StringIO()
            original = sys.argv
            sys.argv = ["tool_compat_matrix.py", "--ingest", str(report)]
            try:
                with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
                    code = matrix.main()
            finally:
                sys.argv = original
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(out.getvalue())["updated"], "2026-11-03")


if __name__ == "__main__":
    unittest.main(verbosity=1)
