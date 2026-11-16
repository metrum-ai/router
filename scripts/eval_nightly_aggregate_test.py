#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline tests for rolling nightly report aggregation."""

from __future__ import annotations

import copy
import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from eval_nightly_aggregate import AggregateError, aggregate_reports, main, wilson_interval


MATRIX = {
    "cells": [
        {"id": "fixed", "environment": "nightly-live", "sample_size": 2},
        {"id": "stable", "environment": "nightly-live", "sample_size": 2},
    ],
    "task_gates": {},
}


def task(task_id: str, outcome: str) -> dict:
    return {"task_id": task_id, "outcome": outcome, "time_to_first_event_ms": 1,
            "completion_latency_ms": 2, "tool_turns": 1, "cost_usd": 0.001}


def report(run_id: str, fixed_outcome: str = "pass", stable_outcome: str = "pass") -> dict:
    return {
        "schema_version": 1,
        "run_id": run_id,
        "started_utc": "2026-11-16T00:00:00Z",
        "cells": [
            {"cell_id": "fixed", "mode": "fixed-router", "attempts": 1, "passes": int(fixed_outcome == "pass"),
             "paired_baseline": {"wins": int(fixed_outcome == "pass"), "losses": int(fixed_outcome != "pass")},
             "provenance": {"client_versions": {"eval": "1"}, "seed": run_id, "baseline_kind": "pinned"},
             "task_results": [task("t1", fixed_outcome)]},
            {"cell_id": "stable", "mode": "fixed-router", "attempts": 1, "passes": int(stable_outcome == "pass"),
             "provenance": {"client_versions": {"eval": "1"}, "seed": run_id, "baseline_kind": "current-stable", "baseline_of": "fixed"},
             "task_results": [task("t1", stable_outcome)]},
        ],
    }


class WilsonTest(unittest.TestCase):
    def test_known_endpoints(self):
        self.assertEqual(wilson_interval(0, 1), [0.0, 0.793451])
        self.assertEqual(wilson_interval(1, 1), [0.206549, 1.0])


class AggregateTest(unittest.TestCase):
    def test_rolls_attempts_passes_intervals_and_paired_outcomes(self):
        result = aggregate_reports([report("run-a"), report("run-b", fixed_outcome="fail")], MATRIX)
        cells = {cell["cell_id"]: cell for cell in result["cells"]}
        self.assertEqual(cells["fixed"]["attempts"], 2)
        self.assertEqual(cells["fixed"]["passes"], 1)
        self.assertEqual(cells["fixed"]["pass_rate_ci95"], wilson_interval(1, 2))
        self.assertEqual(cells["fixed"]["paired_baseline"], {"wins": 1, "losses": 1})
        self.assertEqual(cells["fixed"]["task_results"][0]["task_id"], "run-a/t1")
        self.assertEqual(result["aggregate"]["certification"], "sample_complete_not_certified")
        self.assertTrue(result["aggregate"]["sample_complete"])
        self.assertEqual(result["aggregate"]["gate_violations"], [])
        self.assertEqual(result["aggregate"]["cost_basis"], {
            "router_usage_rows_usd": "measured request_usage rows",
            "direct_provider_usage_estimate_usd": "list-rate estimate",
        })

    def test_missing_cells_and_insufficient_samples_never_become_ready(self):
        partial_matrix = {"cells": [MATRIX["cells"][0]], "task_gates": {}}
        incomplete = report("run-a")
        incomplete["cells"] = [incomplete["cells"][0]]
        result = aggregate_reports([incomplete], partial_matrix)
        self.assertFalse(result["aggregate"]["sample_complete"])
        self.assertEqual(result["aggregate"]["certification"], "not_available")
        self.assertEqual(result["aggregate"]["under_sampled_cells"], ["fixed"])
        self.assertEqual(result["aggregate"]["missing_cells"], [])
        self.assertEqual(result["aggregate"]["gate_violations"], [])

    def test_expected_missing_cells_are_reported_without_failing_gate(self):
        matrix = {"cells": [*MATRIX["cells"], {
            "id": "humaneval", "environment": "nightly-live", "sample_size": 10,
            "nightly_status": "expected-missing",
        }], "task_gates": {}}
        result = aggregate_reports([report("run-a"), report("run-b")], matrix)
        self.assertEqual(result["aggregate"]["expected_missing_cells"], ["humaneval"])
        self.assertEqual(result["aggregate"]["missing_cells"], [])
        self.assertFalse(result["aggregate"]["sample_complete"])
        self.assertEqual(result["aggregate"]["gate_violations"], [])

    def test_old_report_gate_violations_are_historical_not_current_failures(self):
        old = report("old-run")
        old["started_utc"] = "2020-01-01T00:00:00Z"
        old["gate_violations"] = ["provider regression from old run"]
        matrix = {"cells": MATRIX["cells"], "task_gates": {
            "max_time_to_first_event_ms": 100,
            "max_completion_latency_ms": 1,
            "max_tool_turns": 10,
            "max_cost_per_verified_task_usd": 1,
        }}
        result = aggregate_reports([old], matrix)
        self.assertEqual(result["aggregate"]["gate_violations"], [])
        self.assertTrue(any("provider regression from old run" in error
                            for error in result["aggregate"]["historical_gate_violations"]))
        self.assertTrue(any("completion latency" in error
                            for error in result["aggregate"]["historical_gate_violations"]))
        self.assertEqual(result["aggregate"]["historical_gate_violation_max_age_days"], 30)

    def test_recent_gate_violations_still_fail(self):
        current = report("current-run")
        current["gate_violations"] = ["current provider regression"]
        result = aggregate_reports([current], MATRIX)
        self.assertIn("current-run: current provider regression", result["aggregate"]["gate_violations"])

    def test_unexpected_missing_cells_remain_gate_violations(self):
        partial_matrix = {"cells": [*MATRIX["cells"], {
            "id": "unexpected", "environment": "nightly-live", "sample_size": 1,
        }], "task_gates": {}}
        result = aggregate_reports([report("run-a"), report("run-b")], partial_matrix)
        self.assertEqual(result["aggregate"]["missing_cells"], ["unexpected"])
        self.assertEqual(result["aggregate"]["gate_violations"], ["unexpected: no nightly reports"])

    def test_reports_from_different_nights_accumulate_toward_sample(self):
        first = report("night-one")
        second = report("night-two")
        first["started_utc"] = "2026-11-16T05:17:00Z"
        second["started_utc"] = "2026-11-17T05:17:00Z"
        result = aggregate_reports([first, second], MATRIX)
        cells = {cell["cell_id"]: cell for cell in result["cells"]}
        self.assertEqual(cells["fixed"]["attempts"], 2)
        self.assertTrue(result["aggregate"]["sample_complete"])

    def test_duplicate_run_ids_fail_closed(self):
        first = report("same")
        second = copy.deepcopy(first)
        with self.assertRaisesRegex(AggregateError, "duplicate run_id"):
            aggregate_reports([first, second], MATRIX)

    def test_bad_report_schema_fails_closed(self):
        broken = report("broken")
        broken["cells"][0]["attempts"] = 0
        with self.assertRaises(Exception):
            aggregate_reports([broken], MATRIX)

    def test_cli_writes_aggregate_and_refuses_certification_below_sample_size(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first, second = root / "one.json", root / "two.json"
            first.write_text(json.dumps(report("run-a")), encoding="utf-8")
            second.write_text(json.dumps(report("run-b")), encoding="utf-8")
            output = root / "aggregate.json"
            yaml_stub = SimpleNamespace(safe_load=lambda _: MATRIX)
            with patch.dict(sys.modules, {"yaml": yaml_stub}), patch("sys.argv", ["eval_nightly_aggregate.py",
                       "--out", str(output), str(first), str(second)]):
                self.assertEqual(main(), 0)
            result = json.loads(output.read_text(encoding="utf-8"))
            self.assertTrue(result["aggregate"]["sample_complete"])
            self.assertEqual(result["aggregate"]["certification"], "sample_complete_not_certified")

            output = root / "incomplete.json"
            with patch.dict(sys.modules, {"yaml": yaml_stub}), patch("sys.argv", ["eval_nightly_aggregate.py",
                       "--out", str(output), str(first)]):
                self.assertEqual(main(), 0)
            result = json.loads(output.read_text(encoding="utf-8"))
            self.assertFalse(result["aggregate"]["sample_complete"])


if __name__ == "__main__":
    unittest.main()
