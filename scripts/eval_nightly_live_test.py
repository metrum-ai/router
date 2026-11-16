#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline safety tests for the bounded nightly evaluation runner."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timezone
from decimal import Decimal
from pathlib import Path
from unittest.mock import patch

from eval_nightly_live import NightlyError, failure_report, history_reserved, preflight_upper_bound
import eval_nightly_live as nightly


class NightlySafetyTest(unittest.TestCase):
    def test_preflight_reservation_is_bounded_and_nonzero(self):
        amount = preflight_upper_bound(nightly.canary.load_probes())
        self.assertGreater(amount, Decimal("0"))
        self.assertLessEqual(amount, nightly.MAX_NIGHTLY_USD)

    def test_same_day_history_counts_reservations_and_rejects_corruption(self):
        today = datetime.now(timezone.utc).strftime("%Y-%m-%d")
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "previous.json"
            report.write_text(json.dumps({"nightly_cost": {"date": today, "reserved_usd": 0.25,
                                                              "charged_usd": 0.30}}))
            self.assertEqual(history_reserved([report], today), Decimal("0.30"))
            report.write_text("not-json")
            with self.assertRaisesRegex(NightlyError, "unreadable"):
                history_reserved([report], today)

    def test_failure_report_is_schema_valid_and_contains_no_probe_text(self):
        report = failure_report("nightly-runner-failed")
        violations = nightly.canary.validate_report(report)
        self.assertEqual(len(violations), 4)
        self.assertTrue(all("observed error rate" in item for item in violations))
        self.assertEqual(report["canary"]["classification"]["unavailable"], len(nightly.canary.load_probes()))
        serialized = json.dumps(report)
        for probe in nightly.canary.load_probes():
            self.assertNotIn(probe["prompt"], serialized)
        self.assertNotIn("OPENAI_API_KEY", serialized)
        self.assertEqual(report["nightly_status"]["status"], "failed")
        self.assertEqual(report["nightly_cost"]["cost_basis"], {
            "router_usage_rows_usd": "measured request_usage rows",
            "direct_provider_usage_estimate_usd": "list-rate estimate",
        })

    def test_provider_timeout_is_bounded_and_failure_summary_is_sanitized(self):
        with tempfile.TemporaryDirectory() as directory:
            out = Path(directory) / "canary.json"
            with patch.dict(os.environ, {"OPENAI_API_KEY": "test-secret"}), \
                    patch.object(nightly.subprocess, "run", side_effect=subprocess.TimeoutExpired("canary", 600)) as run:
                with self.assertRaises(subprocess.TimeoutExpired):
                    nightly._run_one("model", "pinned", "http://127.0.0.1/v1", "caller-token",
                                     Decimal("0.50"), out)
                self.assertEqual(run.call_args.kwargs["timeout"], 600)
            report = failure_report("nightly-runner-failed", Decimal("0.1"))
            report["gate_violations"] = nightly.canary.validate_report(report)
            rendered = json.dumps(report)
            self.assertNotIn("test-secret", rendered)
            self.assertNotIn("caller-token", rendered)
            self.assertEqual(report["nightly_cost"]["reserved_usd"], 0.1)

    def test_cli_without_credentials_emits_sanitized_failure_report(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "failed.json"
            with patch.dict(os.environ, {"OPENAI_API_KEY": "", "PARALLEL_LIVE_TOKEN": ""}), \
                    patch("sys.argv", ["eval_nightly_live.py", "--out", str(output)]):
                self.assertEqual(nightly.main(), 1)
            report = json.loads(output.read_text(encoding="utf-8"))
            self.assertEqual(report["nightly_status"]["status"], "failed")
            rendered = output.read_text(encoding="utf-8")
            for probe in nightly.canary.load_probes():
                self.assertNotIn(probe["prompt"], rendered)
            self.assertIn("nightly-runner-failed", rendered)


if __name__ == "__main__":
    unittest.main()
