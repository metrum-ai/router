# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Check the published synthetic evidence against its derived summary."""
import collections
from decimal import Decimal
import json
from pathlib import Path
import statistics
import unittest

EVIDENCE = Path(__file__).resolve().parent / "evidence/mi300x-2026-10-30"


class PublishedEvidenceTests(unittest.TestCase):
    def test_summary_matches_records(self):
        result = json.loads((EVIDENCE / "result.json").read_text())
        summary = json.loads((EVIDENCE / "summary.json").read_text())
        self.assertEqual(result["backend"], "mixed")
        self.assertEqual(result["hosted_attempts"], 15)
        self.assertEqual(summary["hosted_attempts"], result["hosted_attempts"])
        self.assertEqual(len(result["results"]), 24)
        for path in ("frontier-only", "escalation"):
            rows = [r for r in result["results"] if r["path"] == path]
            self.assertEqual(len(rows), 12)
            self.assertEqual(len({r["case"] for r in rows}), 12)
            self.assertTrue(all(r["status"] == 200 and r["correct"] is True
                                and r["attempts"] == 1 and r["usage_source"] == "reported" for r in rows))
            self.assertEqual(summary[path]["count"], len(rows))
            self.assertEqual(summary[path]["exact"], sum(r["correct"] for r in rows))
            self.assertEqual(summary[path]["tiers"], dict(collections.Counter(r["tier"] for r in rows)))
            self.assertEqual(summary[path]["median_request_ms"], round(statistics.median(r["request_ms"] for r in rows), 3))
            cost = sum(Decimal(str(r["usage_cost_usd"])) for r in rows)
            self.assertEqual(Decimal(summary[path]["configured_rate_estimate_usd"]), cost)
        self.assertEqual(summary["escalation"]["tiers"], {"local": 6, "datacenter": 3, "frontier": 3})
        latency = next(r for r in result["results"] if r["path"] == "escalation" and r["case"] == "latency-01")
        self.assertEqual(summary["latency_01_request_ms"], latency["request_ms"])
        self.assertGreater(latency["request_ms"], 200)  # Fixture label, not an SLA guarantee.


if __name__ == "__main__":
    unittest.main()
