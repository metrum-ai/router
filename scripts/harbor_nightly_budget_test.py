# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from harbor_nightly_budget import remaining_budget


class HarborNightlyBudgetTest(unittest.TestCase):
    def test_sums_same_day_reservations(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "prior.json"
            report.write_text(json.dumps({"run_id": "run-one", "reserved_upper_usd": 0.2}))
            self.assertAlmostEqual(remaining_budget([report]), 0.3)

    def test_rejects_corrupt_or_duplicate_history(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "prior.json"
            report.write_text("not-json")
            with self.assertRaisesRegex(ValueError, "unreadable"):
                remaining_budget([report])
            report.write_text(json.dumps({"run_id": "run-one", "reserved_upper_usd": 0.2}))
            with self.assertRaisesRegex(ValueError, "duplicate"):
                remaining_budget([report, report])

    def test_refuses_history_over_ceiling(self):
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "prior.json"
            report.write_text(json.dumps({"run_id": "run-one", "reserved_upper_usd": 0.51}))
            with self.assertRaisesRegex(ValueError, "exceeds"):
                remaining_budget([report])


if __name__ == "__main__":
    unittest.main()
