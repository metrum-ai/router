#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

import json
import tempfile
import unittest
from decimal import Decimal
from pathlib import Path

import tool_compat_live_matrix as live


def gate_report(**overrides):
    cells = [
        {"cell": "openai-chat-direct", "problems": []},
        {"cell": "openai-chat-routed", "problems": []},
        {"cell": "anthropic-messages-direct", "problems": []},
        {"cell": "anthropic-messages-routed", "problems": []},
        {"cell": "chat-caller-to-messages-target", "problems": []},
        {"cell": "messages-caller-to-chat-target", "problems": []},
    ]
    report = {
        "cells": cells,
        "router_usage_rows": [
            {"provider": "openai", "model": "gpt-4o-mini", "total_cost_usd": 0.00021},
            {"provider": "anthropic", "model": "claude-haiku-4-5", "total_cost_usd": 0.00042},
        ],
        "reserved_usd": 0.06,
        "list_rate_usage_usd": 0.00063,
        "unknown_usage_turns": 0,
        "halted": False,
        "cap_usd": 1.0,
    }
    report.update(overrides)
    return report


class ReportTest(unittest.TestCase):
    def test_every_gate_cell_maps_to_a_matrix_cell(self):
        report = live.build_report(gate_report(), "2026-11-03", "abcdef123")
        self.assertEqual(report["schema_version"], 1)
        self.assertEqual(report["runner"], "agent-tool-compat-live")
        ids = {cell["id"] for cell in report["cells"]}
        self.assertEqual(len(ids), 6)
        self.assertIn("parallel-gate-chat-through-router", ids)
        self.assertIn("parallel-gate-messages-to-chat", ids)

    def test_cost_comes_from_router_rows_and_direct_cells_are_provider_invoiced(self):
        report = live.build_report(gate_report(), "2026-11-03", "abcdef123")
        self.assertAlmostEqual(report["cost"]["usd"], 0.00063, places=8)
        self.assertEqual(report["cost"]["source"], "usage_rows")
        by_id = {cell["id"]: cell for cell in report["cells"]}
        self.assertEqual(by_id["parallel-gate-chat-through-router"]["cost"]["source"], "usage_rows")
        self.assertIsNone(by_id["parallel-gate-chat-direct"]["cost"]["usd"])
        self.assertEqual(by_id["parallel-gate-chat-direct"]["cost"]["source"], "provider_invoiced")

    def test_problems_becomes_a_failing_cell_with_detail(self):
        raw = gate_report()
        raw["cells"][0]["problems"] = ["only 2 tool calls", "missing marker"]
        report = live.build_report(raw, "2026-11-03", "abcdef123")
        by_id = {cell["id"]: cell for cell in report["cells"]}
        self.assertEqual(by_id["parallel-gate-chat-direct"]["status"], "fail")
        self.assertIn("only 2 tool calls", by_id["parallel-gate-chat-direct"]["detail"])
        self.assertEqual(by_id["parallel-gate-chat-through-router"]["status"], "pass")

    def test_unknown_or_missing_cells_fail_closed(self):
        raw = gate_report()
        raw["cells"].append({"cell": "invented-cell", "problems": []})
        with self.assertRaises(live.ReportError):
            live.build_report(raw, "2026-11-03", "abcdef123")
        with self.assertRaises(live.ReportError):
            live.build_report({"cells": []}, "2026-11-03", "abcdef123")

    def test_duplicate_cell_fails_closed(self):
        raw = gate_report()
        raw["cells"].append({"cell": "openai-chat-direct", "problems": []})
        with self.assertRaises(live.ReportError):
            live.build_report(raw, "2026-11-03", "abcdef123")

    def test_unparseable_cost_fails_closed(self):
        raw = gate_report(router_usage_rows=[{"total_cost_usd": "not-a-number"}])
        with self.assertRaises(live.ReportError):
            live.build_report(raw, "2026-11-03", "abcdef123")

    def test_cli_writes_the_report(self):
        with tempfile.TemporaryDirectory() as temp:
            raw_path = Path(temp) / "raw.json"
            out_path = Path(temp) / "report.json"
            raw_path.write_text(json.dumps(gate_report()), encoding="utf-8")
            code = live.main.__globals__["argparse"].ArgumentParser  # module is importable
            self.assertIsNotNone(code)
            import sys

            argv = sys.argv
            sys.argv = ["tool_compat_live_matrix.py", "report", "--gate-report", str(raw_path),
                        "--out", str(out_path), "--date", "2026-11-03", "--commit", "abcdef123"]
            try:
                self.assertEqual(live.main(), 0)
            finally:
                sys.argv = argv
            self.assertEqual(json.loads(out_path.read_text(encoding="utf-8"))["date"], "2026-11-03")


class ChargedCostTest(unittest.TestCase):
    def test_charges_the_larger_of_router_rows_and_list_rate(self):
        # Direct cells are invoiced outside the router, so the gate's list-rate
        # figure is the only place they appear.
        raw = gate_report(router_usage_rows=[{"total_cost_usd": 0.0001}], list_rate_usage_usd=0.0031)
        charged, source, note = live.charged_cost_usd(raw)
        self.assertEqual(charged, Decimal("0.0031"))
        self.assertEqual(source, "estimate")
        self.assertIn(live.DIRECT_CELL_CHARGE_LABEL, note)
        self.assertIn("invoice pending", note)
        raw = gate_report(router_usage_rows=[{"total_cost_usd": 0.0041}], list_rate_usage_usd=0.0031)
        charged, source, note = live.charged_cost_usd(raw)
        self.assertEqual(charged, Decimal("0.0041"))
        self.assertEqual(source, "usage_rows")
        self.assertEqual(note, "router usage rows")

    def test_unknown_usage_or_halted_charges_the_reservation(self):
        raw = gate_report(router_usage_rows=[{"total_cost_usd": 0.0001}],
                          list_rate_usage_usd=0.0002, reserved_usd=0.5, unknown_usage_turns=1)
        charged, source, note = live.charged_cost_usd(raw)
        self.assertEqual(charged, Decimal("0.5"))
        self.assertEqual(source, "estimate")
        self.assertIn("reserved", note)
        self.assertIn("invoice pending", note)
        raw = gate_report(router_usage_rows=[], list_rate_usage_usd=0, reserved_usd=0.25, halted=True)
        charged, source, note = live.charged_cost_usd(raw)
        self.assertEqual(charged, Decimal("0.25"))
        self.assertEqual(source, "estimate")
        self.assertIn("reserved", note)

    def test_report_cost_source_follows_the_charge(self):
        rows_only = live.build_report(gate_report(), "2026-11-03", "abcdef123")
        self.assertEqual(rows_only["cost"]["source"], "usage_rows")
        direct = live.build_report(
            gate_report(router_usage_rows=[{"total_cost_usd": 0.0001}], list_rate_usage_usd=0.0031),
            "2026-11-03", "abcdef123",
        )
        self.assertEqual(direct["cost"]["source"], "estimate")
        self.assertIn("invoice pending", direct["cost"]["note"])
        reserved = live.build_report(
            gate_report(router_usage_rows=[], reserved_usd=0.25, halted=True), "2026-11-03", "abcdef123",
        )
        self.assertEqual(reserved["cost"]["source"], "estimate")
        self.assertAlmostEqual(reserved["cost"]["usd"], 0.25, places=8)

    def test_every_estimate_is_valid_against_the_matrix_schema(self):
        import tool_compat_matrix as matrix

        for report in (
            live.build_report(gate_report(router_usage_rows=[{"total_cost_usd": 0.0001}],
                                          list_rate_usage_usd=0.0031), "2026-11-03", "abcdef123"),
            live.build_report(gate_report(reserved_usd=0.25, halted=True), "2026-11-03", "abcdef123"),
        ):
            self.assertEqual(matrix.validate_cost("cell", True, report["cost"]), [])

    def test_invalid_usage_fields_fail_closed(self):
        for raw in (
            gate_report(unknown_usage_turns=-1),
            gate_report(list_rate_usage_usd="not-a-number"),
            gate_report(reserved_usd="not-a-number", halted=True),
        ):
            with self.assertRaises(live.ReportError):
                live.charged_cost_usd(raw)


class RecordedRunChargeTest(unittest.TestCase):
    def test_fresh_report_wins_when_present(self):
        with tempfile.TemporaryDirectory() as temp:
            report = Path(temp) / "report.json"
            gate = Path(temp) / "gate.json"
            report.write_text(json.dumps({"cost": {"usd": 0.002, "source": "usage_rows", "note": "router usage rows"}}), encoding="utf-8")
            gate.write_text(json.dumps(gate_report()), encoding="utf-8")
            charged, source, reason = live.charge_for_recorded_run(report, gate, Decimal("1"))
            self.assertEqual(charged, Decimal("0.002"))
            self.assertEqual(source, "usage_rows")
            self.assertEqual(reason, "router usage rows")

    def test_fresh_gate_report_is_used_when_the_matrix_report_is_missing(self):
        with tempfile.TemporaryDirectory() as temp:
            gate = Path(temp) / "gate.json"
            gate.write_text(json.dumps(gate_report(router_usage_rows=[{"total_cost_usd": 0.0004}],
                                                   list_rate_usage_usd=0.0009)), encoding="utf-8")
            charged, source, reason = live.charge_for_recorded_run(
                Path(temp) / "matrix-live.json", gate, Decimal("1")
            )
            self.assertEqual(charged, Decimal("0.0009"))
            self.assertEqual(source, "estimate")
            self.assertIn("gate report only", reason)

    def test_stale_restored_reports_are_never_charged(self):
        # A cache entry restores a whole path, so the previous run's report can be
        # present while this run's gate died. That file must not be billed.
        with tempfile.TemporaryDirectory() as temp:
            report = Path(temp) / "report.json"
            gate = Path(temp) / "gate.json"
            report.write_text(json.dumps({"cost": {"usd": 0.002, "source": "usage_rows", "note": "old"}}), encoding="utf-8")
            gate.write_text(json.dumps(gate_report()), encoding="utf-8")
            old = 1_600_000_000
            for path in (report, gate):
                path.touch()
                import os
                os.utime(path, (old, old))
            charged, source, reason = live.charge_for_recorded_run(
                report, gate, Decimal("1"), fresh_after=old + 60
            )
            self.assertEqual(charged, Decimal("1"))
            self.assertEqual(source, "estimate")
            self.assertIn("cached report from a previous run was ignored", reason)
            self.assertIn("invoice pending", reason)

    def test_stale_report_with_a_fresh_gate_report_charges_the_gate(self):
        with tempfile.TemporaryDirectory() as temp:
            report = Path(temp) / "report.json"
            gate = Path(temp) / "gate.json"
            report.write_text(json.dumps({"cost": {"usd": 0.002, "source": "usage_rows"}}), encoding="utf-8")
            gate.write_text(json.dumps(gate_report(router_usage_rows=[{"total_cost_usd": 0.0003}],
                                                   list_rate_usage_usd=0.0004)), encoding="utf-8")
            import os
            old = 1_600_000_000
            os.utime(report, (old, old))
            charged, source, reason = live.charge_for_recorded_run(
                report, gate, Decimal("1"), fresh_after=old + 60
            )
            self.assertEqual(charged, Decimal("0.0004"))
            self.assertIn("gate report only", reason)

    def test_freshness_boundary(self):
        with tempfile.TemporaryDirectory() as temp:
            report = Path(temp) / "report.json"
            report.write_text("{}", encoding="utf-8")
            import os
            stamp = int(report.stat().st_mtime)
            self.assertTrue(live._fresh(report, stamp))
            self.assertFalse(live._fresh(report, stamp + 1))
            self.assertFalse(live._fresh(Path(temp) / "missing.json", None))
            self.assertTrue(live._fresh(report, None))

    def test_neither_report_charges_the_run_cap(self):
        with tempfile.TemporaryDirectory() as temp:
            charged, source, reason = live.charge_for_recorded_run(
                Path(temp) / "matrix-live.json", Path(temp) / "gate.json", Decimal("1")
            )
            self.assertEqual(charged, Decimal("1"))
            self.assertEqual(source, "estimate")
            self.assertIn("no report", reason)
            self.assertIn("invoice pending", reason)

    def test_unreadable_reports_charge_the_run_cap(self):
        with tempfile.TemporaryDirectory() as temp:
            report = Path(temp) / "matrix-live.json"
            report.write_text("{broken", encoding="utf-8")
            charged, source, reason = live.charge_for_recorded_run(report, Path(temp) / "gate.json", Decimal("1"))
            self.assertEqual(charged, Decimal("1"))
            self.assertIn("run cap", reason)
            gate = Path(temp) / "gate.json"
            report.unlink()
            gate.write_text("{broken", encoding="utf-8")
            charged, source, reason = live.charge_for_recorded_run(report, gate, Decimal("1"))
            self.assertEqual(charged, Decimal("1"))
            self.assertIn("run cap", reason)

    def test_record_run_command_writes_the_ledger_with_its_source(self):
        import sys

        with tempfile.TemporaryDirectory() as temp:
            ledger = Path(temp) / "spend.json"
            report = Path(temp) / "matrix-live.json"
            report.write_text(json.dumps({"cost": {"usd": 0.0012, "source": "usage_rows", "note": "router usage rows"}}), encoding="utf-8")
            argv = sys.argv
            sys.argv = ["tool_compat_live_matrix.py", "record-run", "--ledger", str(ledger),
                        "--month", "2026-11", "--report", str(report),
                        "--gate", str(Path(temp) / "gate.json"), "--cap", "1", "--run-id", "run-1",
                        "--fresh-after", str(int(report.stat().st_mtime))]
            try:
                self.assertEqual(live.main(), 0)
            finally:
                sys.argv = argv
            data = json.loads(ledger.read_text(encoding="utf-8"))
            self.assertAlmostEqual(data["monthly"]["2026-11"], 0.0012, places=8)
            self.assertEqual(data["runs"][-1]["source"], "usage_rows")
            self.assertEqual(data["runs"][-1]["run"], "run-1")


class WorkflowTest(unittest.TestCase):
    """Guard the workflow invariants the review asked for, without a YAML dep."""

    def setUp(self):
        self.text = (Path(__file__).resolve().parents[1] / ".github/workflows/agent-tool-compat-live.yml").read_text()
        self.steps = [segment.split("\n")[0] for segment in self.text.split("      - name: ") if segment]

    def test_the_cache_holds_only_the_ledger(self):
        self.assertEqual(self.text.count("path: tmp/compat-matrix/spend.json"), 2)
        self.assertNotIn("path: tmp/compat-matrix\n", self.text)

    def test_the_gate_step_has_an_id_and_recording_depends_on_it(self):
        self.assertIn("id: gate", self.text)
        self.assertIn("if: always() && steps.gate.outcome != 'skipped'", self.text)

    def test_recording_happens_before_the_summary(self):
        record = self.text.index('--ledger "$LEDGER"')
        summary = self.text.index("Publish the run summary")
        self.assertLess(record, summary)

    def test_summary_uses_the_current_month(self):
        self.assertIn("monthly.get(month", self.text)
        self.assertNotIn("max(monthly.values())", self.text)

    def test_stale_reports_are_cleared_after_the_restore(self):
        restore = self.text.index("Restore the monthly spend ledger")
        clear = self.text.index("Clear non-ledger leftovers")
        self.assertLess(restore, clear)
        self.assertIn("gate.json", self.text[clear:clear + 600])


class LedgerTest(unittest.TestCase):
    def test_ledger_miss_warns_but_a_present_ledger_does_not(self):
        with tempfile.TemporaryDirectory() as temp:
            missing = Path(temp) / "spend.json"
            self.assertIn("cache miss", live.ledger_miss_warning(missing, "2026-11"))
            missing.write_text(json.dumps({"monthly": {"2026-11": 1.0}, "runs": []}), encoding="utf-8")
            self.assertEqual(live.ledger_miss_warning(missing, "2026-11"), "")

    def test_guard_allows_then_refuses(self):
        ledger = {"monthly": {"2026-11": 8.5}, "runs": []}
        allowed, spent, _ = live.guard(ledger, "2026-11", Decimal("10"), Decimal("1"))
        self.assertTrue(allowed)
        self.assertEqual(spent, Decimal("8.5"))
        allowed, _, message = live.guard(ledger, "2026-11", Decimal("10"), Decimal("2"))
        self.assertFalse(allowed)
        self.assertIn("exhausted", message)

    def test_new_month_starts_at_zero(self):
        ledger = {"monthly": {"2026-10": 9.9}, "runs": []}
        allowed, spent, _ = live.guard(ledger, "2026-11", Decimal("10"), Decimal("1"))
        self.assertTrue(allowed)
        self.assertEqual(spent, Decimal("0"))

    def test_record_accumulates_and_bounds_history(self):
        ledger = {"monthly": {}, "runs": []}
        ledger = live.record(ledger, "2026-11", Decimal("0.0012"), "run-1")
        ledger = live.record(ledger, "2026-11", Decimal("0.0018"), "run-2")
        self.assertAlmostEqual(ledger["monthly"]["2026-11"], 0.003, places=8)
        self.assertEqual(len(ledger["runs"]), 2)
        for index in range(300):
            ledger = live.record(ledger, "2026-11", Decimal("0"), f"run-{index}")
        self.assertEqual(len(ledger["runs"]), 200)

    def test_ledger_shape_is_validated(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "ledger.json"
            path.write_text('{"monthly": [], "runs": []}', encoding="utf-8")
            with self.assertRaises(live.ReportError):
                live.load_ledger(path)
            path.write_text("{not json", encoding="utf-8")
            with self.assertRaises(live.ReportError):
                live.load_ledger(path)


if __name__ == "__main__":
    unittest.main(verbosity=1)
