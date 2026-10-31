# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Verify committed partial measurements against raw synthetic samples."""
import collections
import gzip
import json
import math
from pathlib import Path
import unittest

import load

EVIDENCE = Path(__file__).resolve().parent / "evidence/a100-2026-10-30"


class A100EvidenceTests(unittest.TestCase):
    def test_plain_summaries_match_raw_samples(self):
        source = EVIDENCE / "plain-latency"
        manifest = json.loads((source / "manifest.json").read_text())
        summary = json.loads((source / "summary.json").read_text())
        rows = collections.defaultdict(list)
        with gzip.open(source / "samples.jsonl.gz", "rt", encoding="utf-8") as stream:
            for line in stream:
                row = json.loads(line)
                self.assertEqual(set(row), {"latency_ms", "ttft_content_ms", "content_event_gaps_ms", "prompt_tokens",
                                            "completion_tokens", "worker", "request", "route", "concurrency", "streaming", "repetition"})
                self.assertIn(row["route"], ("direct", "router"))
                self.assertIs(type(row["streaming"]), bool)
                for name in ("latency_ms", "worker", "request", "concurrency", "repetition"):
                    self.assertTrue(type(row[name]) in (int, float) and math.isfinite(row[name]))
                self.assertTrue(all(type(v) in (int, float) and math.isfinite(v) for v in row["content_event_gaps_ms"]))
                self.assertTrue(row["ttft_content_ms"] is None or (type(row["ttft_content_ms"]) in (int, float) and math.isfinite(row["ttft_content_ms"])))
                self.assertEqual(row["prompt_tokens"], 43)
                self.assertEqual(row["completion_tokens"], 32)
                key = (row["concurrency"], row["streaming"], row["repetition"], row["route"])
                rows[key].append(row)
        self.assertEqual(sum(map(len, rows.values())), 16224)
        self.assertEqual(manifest["repetitions"], 3)
        self.assertEqual(manifest["concurrency"], [1, 8, 32, 128])
        self.assertEqual(len(summary["pairs"]), 24)
        for pair in summary["pairs"]:
            for route in ("direct", "router"):
                key = (pair["concurrency"], pair["streaming"], pair["repetition"], route)
                sample = rows[key]
                self.assertEqual(len(sample), pair["concurrency"] * manifest["requests_per_worker"])
                metric = "ttft_content_ms" if pair["streaming"] else "latency_ms"
                values = [row[metric] for row in sample]
                for percentile in (50, 99):
                    self.assertAlmostEqual(load.percentile(values, percentile / 100), pair[route][f"p{percentile}_ms"])
            for percentile in (50, 99):
                expected = pair["router"][f"p{percentile}_ms"] - pair["direct"][f"p{percentile}_ms"]
                self.assertAlmostEqual(expected, pair[f"p{percentile}_delta_ms"])
        for row in summary["delta_variance"]:
            matches = [p for p in summary["pairs"] if p["concurrency"] == row["concurrency"] and p["streaming"] == row["streaming"]]
            self.assertEqual(len(matches), 3)
            for p in (50, 99):
                values = [v[f"p{p}_delta_ms"] for v in matches]
                self.assertAlmostEqual(sum(values) / 3, row[f"p{p}_delta_ms"]["mean"])
                self.assertTrue(math.isfinite(row[f"p{p}_delta_ms"]["sample_stdev"]))
        readme = (EVIDENCE / "README.md").read_text()
        docs = (Path(__file__).resolve().parents[2] / "docs-site/docs/evaluation/performance-overhead.md").read_text()
        for workers in manifest["concurrency"]:
            unstreamed = next(r for r in summary["delta_variance"] if r["concurrency"] == workers and not r["streaming"])
            streamed = next(r for r in summary["delta_variance"] if r["concurrency"] == workers and r["streaming"])
            expected = f"| {workers} | {unstreamed['p50_delta_ms']['mean']:.3f} | {unstreamed['p99_delta_ms']['mean']:.3f} | {streamed['p50_delta_ms']['mean']:.3f} | {streamed['p99_delta_ms']['mean']:.3f} |"
            self.assertIn(expected, readme)
            self.assertIn(expected, docs)

    def test_only_clean_32_decision_captures_are_present(self):
        status = json.loads((EVIDENCE / "phase-status.json").read_text())
        policy = json.loads((EVIDENCE / "policy-summary.json").read_text())
        for mode in ("shadow", "enforce"):
            phase = mode + "-profile-32"
            self.assertEqual(status[phase]["status"], "passed")
            expected = "shadow_recommended" if mode == "shadow" else "selected"
            self.assertEqual(policy[phase], [{"count": 2080, "error_class": "", "outcome": expected}])
            capture = json.loads((EVIDENCE / (mode + "-32/profile-phase.json")).read_text())
            self.assertEqual(capture["concurrency"], 32)
            self.assertEqual(capture["poll_min_active_during_observed_window"], 29)
            self.assertEqual(capture["stop_guard_min_active"], 32)
            self.assertEqual(capture["timed_requests"], 2048)
            for name in ("cpu.pprof", "heap.pprof"):
                self.assertGreater((EVIDENCE / (mode + "-32") / name).stat().st_size, 0)


if __name__ == "__main__":
    unittest.main()
