#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential-free tests for scripts/d3_demo_panel.py (issue #278 demo panel).

Builds a throwaway SQLite DB with the two router tables the panel joins and
checks row formatting, tier mapping, the summary and the all-strong estimate.
Stdlib unittest only.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import contextlib
import importlib.util
import io
import sqlite3
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("d3_demo_panel", ROOT / "scripts" / "d3_demo_panel.py")
panel = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(panel)

TIERS = ["deepseek=cheap", "gpt-6-luna=strong"]
CHEAP = "accounts/fireworks/models/deepseek-v4p1-flash"


def make_db(path: Path) -> None:
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE request_usage (request_id TEXT, ts TEXT, resolved_group TEXT, target_provider TEXT,
          target_model TEXT, status INTEGER, latency_ms INTEGER, total_cost_usd REAL, input_tokens INTEGER,
          output_tokens INTEGER, input_price_per_million_usd REAL, output_price_per_million_usd REAL,
          cached_input_tokens INTEGER, cached_input_price_per_million_usd REAL);
        CREATE TABLE request_policy_executions (request_id TEXT, seq INTEGER, class_label TEXT,
          outcome TEXT, duration_ms INTEGER);
        """
    )
    rows = [
        ("r1", "2026-10-11T16:00:01.000000000Z", "d3-auto", "fireworks", CHEAP, 200, 1500, 0.00066, 2000, 100, 0.22, 0.66, 1000, None),
        ("r2", "2026-10-11T16:00:03.000000000Z", "d3-auto", "openai", "gpt-6-luna", 200, 1900, 0.00041, 3000, 200, 0.10, 0.50, 0, 0.01),
        ("r3", "2026-10-11T16:00:05.000000000Z", "big-coder", "openai", "gpt-6-luna", 200, 900, 0.1, 1, 1, 0.10, 0.50, 0, 0.01),
        ("r0", "2026-10-11T15:00:00.000000000Z", "d3-auto", "openai", "gpt-6-luna", 200, 900, 0.1, 1, 1, 0.10, 0.50, 0, 0.01),
    ]
    conn.executemany("INSERT INTO request_usage VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", rows)
    conn.executemany(
        "INSERT INTO request_policy_executions VALUES (?,?,?,?,?)",
        [("r1", 1, "d3:trivial:code:r0.18", "selected", 160), ("r2", 1, "d3:hard:code:r0.82", "selected", 162)],
    )
    conn.commit()
    conn.close()


class PanelTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.db = Path(self.tmp.name) / "usage.sqlite"
        make_db(self.db)
        self.since = "2026-10-11T15:30:00Z"

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def run_main(self, *extra: str) -> str:
        args = ["--db", str(self.db), "--since", self.since, "--no-color"]
        for t in TIERS:
            args += ["--tier", t]
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            self.assertEqual(panel.main(args + list(extra)), 0)
        return buf.getvalue()

    def test_once_prints_group_rows_since_start_in_order(self) -> None:
        lines = self.run_main("--once").splitlines()
        self.assertEqual(len(lines), 2, lines)  # r0 is before --since, r3 is another group
        self.assertIn("turn 1", lines[0])
        self.assertIn("trivial·code·r0.18", lines[0])
        self.assertIn("→ cheap", lines[0])
        self.assertIn("deepseek-v4p1-flash", lines[0])
        self.assertNotIn("accounts/fireworks", lines[0])
        self.assertIn("hard·code·r0.82", lines[1])
        self.assertIn("→ strong", lines[1])
        self.assertIn("1.9 s", lines[1])
        self.assertIn("$0.00041", lines[1])
        for line in lines:
            self.assertLessEqual(len(line), 100)

    def test_summary_split_cost_and_all_strong_estimate(self) -> None:
        tiers = panel.parse_tiers(TIERS)
        s = panel.summarize(panel.fetch(self.db, "d3-auto", self.since), tiers)
        self.assertEqual(s["turns"], 2)
        self.assertEqual(s["by_tier"], {"cheap": 1, "strong": 1})
        self.assertAlmostEqual(s["cheap_share"], 0.5)
        self.assertAlmostEqual(s["total_cost_usd"], 0.00107)
        # the strong row keeps its recorded cost; the cheap row's tokens are priced at the strong
        # row's list price, with its 1000 cached input tokens at the strong cached rate
        expected = (1000 * 0.10 + 1000 * 0.01 + 100 * 0.50) / 1e6 + 0.00041
        self.assertAlmostEqual(s["all_strong_cost_usd_estimate"], expected)
        self.assertEqual(s["policy_ms_p50"], 161)
        text = self.run_main("--summary")
        self.assertIn("2 turns · 1 cheap / 1 strong (50% cheap)", text)
        self.assertIn(f"all-strong est. ${expected:.4f}", text)
        self.assertIn("% more)", text)  # luna was cheaper than deepseek in v1
        for line in text.splitlines():
            self.assertLessEqual(len(line), 100)

    def test_strong_price_flag_when_no_strong_turn(self) -> None:
        tiers = panel.parse_tiers(["deepseek=cheap", "gpt-6.1-sol=strong"])
        rows = [r for r in panel.fetch(self.db, "d3-auto", self.since) if "deepseek" in r["target_model"]]
        self.assertIsNone(panel.summarize(rows, tiers)["all_strong_cost_usd_estimate"])
        s = panel.summarize(rows, tiers, panel.parse_price("2/0.1/10"))
        expected = (1000 * 2 + 1000 * 0.1 + 100 * 10) / 1e6
        self.assertAlmostEqual(s["all_strong_cost_usd_estimate"], expected)
        text = panel.format_summary(s)
        self.assertIn(f"cost $0.0007 · all-strong est. ${expected:.4f} (79% less)", text)
        with self.assertRaises(SystemExit):
            panel.parse_price("2/10")

    def test_label_and_tier_helpers(self) -> None:
        self.assertEqual(panel.label_parts("d3:hard:math:r0.71"), "hard·math·r0.71")
        self.assertEqual(panel.label_parts("d3:no-request-content"), "no-request-content")
        self.assertEqual(panel.label_parts(None), "-")
        self.assertEqual(panel.label_parts("other-policy"), "other-policy")
        self.assertEqual(panel.tier_for("unknown-model", panel.parse_tiers(TIERS)), "?")
        with self.assertRaises(SystemExit):
            panel.parse_tiers(["no-equals"])

    def test_non_200_status_is_shown(self) -> None:
        conn = sqlite3.connect(self.db)
        conn.execute(
            "INSERT INTO request_usage VALUES ('r4','2026-10-11T16:00:09.000000000Z','d3-auto','','',502,400,0,0,0,NULL,NULL,0,NULL)"
        )
        conn.commit()
        conn.close()
        lines = self.run_main("--once").splitlines()
        self.assertIn("HTTP 502", lines[-1])

    def test_missing_db_once_fails_cleanly(self) -> None:
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            rc = panel.main(["--db", str(Path(self.tmp.name) / "nope.sqlite"), "--once"])
        self.assertEqual(rc, 1)
        self.assertIn("not readable", err.getvalue())


if __name__ == "__main__":
    unittest.main()
