#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Credential-free tests for the cost helpers in scripts/d3_live_eval.py (issue #287).

Covers finish_reason extraction, the per-run cost summary (measured cost,
output-cap hits by difficulty) and the enforce vs always-strong / always-cheap
comparison. Stdlib unittest only; no router, d3 or upstream is contacted.

Copyright 2026 Metrum AI.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("d3_live_eval", ROOT / "scripts" / "d3_live_eval.py")
ev = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(ev)

SOL = "gpt-6.1-sol"
CHEAP = "accounts/fireworks/models/deepseek-v4p1-flash"


def row(difficulty: str, model: str, cost: float, out: int, finish: str = "stop") -> dict:
    return {"difficulty_label": difficulty, "served_model": model, "cost_usd": cost, "input_tokens": 50,
            "output_tokens": out, "reasoning_tokens": 0, "cached_input_tokens": 0, "finish_reason": finish}


def run(group: str, rows: list[dict], strong_share: float | None = None) -> dict:
    out = {"group": group, "prompts": len(rows), "http_status": {"200": len(rows)},
           "served_models": {}, "e2e_ms": {"p50": 1.0}, "policy_duration_ms": {"p50": None},
           "cost": ev.cost_summary(rows)}
    if strong_share is not None:
        out["enforce"] = {"strong_share": strong_share, "hard_prompts_to_strong": "1/1",
                          "easy_prompts_to_cheap": "1/1"}
    return out


class LiveEvalCostTest(unittest.TestCase):
    def test_finish_reason_keeps_only_a_short_scalar(self) -> None:
        self.assertEqual(ev.finish_reason(b'{"choices":[{"finish_reason":"length","message":{"content":"x"}}]}'),
                         "length")
        self.assertIsNone(ev.finish_reason(b"not json"))
        self.assertIsNone(ev.finish_reason(b'{"choices":[]}'))
        self.assertIsNone(ev.finish_reason(json.dumps({"choices": [{"finish_reason": "x" * 64}]}).encode()))

    def test_strong_upstream_maps_fireworks_refs(self) -> None:
        self.assertEqual(ev.strong_upstream(SOL), SOL)
        self.assertEqual(ev.strong_upstream("glm-5p3"), "accounts/fireworks/models/glm-5p3")

    def test_cost_summary_counts_cap_hits_by_difficulty(self) -> None:
        rows = [row("easy", CHEAP, 0.001, 100), row("hard", SOL, 0.04, 4096, "length"),
                {"difficulty_label": "hard", "status": 0, "finish_reason": None}]
        s = ev.cost_summary(rows)
        self.assertEqual(s["served"], 2)
        self.assertAlmostEqual(s["actual_cost_usd"], 0.041)
        self.assertAlmostEqual(s["cost_per_request_usd"], 0.0205)
        self.assertEqual(s["by_difficulty"]["hard"]["hit_max_tokens"], 1)
        self.assertEqual(s["by_difficulty"]["easy"]["hit_max_tokens"], 0)
        self.assertEqual(s["finish_reasons"], {"stop": 1, "length": 1, "none": 1})

    def test_compare_reports_saving_and_extra_cost(self) -> None:
        enforce = run("d3-auto", [row("easy", CHEAP, 0.001, 10), row("hard", SOL, 0.02, 10)], 0.5)
        strong = run("d3-auto-strong", [row("easy", SOL, 0.01, 10), row("hard", SOL, 0.03, 10)])
        cheap = run("d3-auto-cheap", [row("easy", CHEAP, 0.001, 10), row("hard", CHEAP, 0.002, 10)])
        with tempfile.TemporaryDirectory() as tmp:
            paths = []
            for name, data in (("e", enforce), ("s", strong), ("c", cheap)):
                path = Path(tmp) / f"{name}.json"
                path.write_text(json.dumps(data), encoding="utf-8")
                paths.append(str(path))
            out = ev.compare(argparse.Namespace(enforce=paths[0], always_strong=paths[1], always_cheap=paths[2]))
        self.assertAlmostEqual(out["saving_vs_always_strong_usd"], 0.019)
        self.assertEqual(out["saving_vs_always_strong_pct"], 47.5)
        self.assertAlmostEqual(out["extra_vs_always_cheap_usd"], 0.018)
        self.assertEqual(out["extra_vs_always_cheap_x"], 7.0)
        self.assertEqual(out["strong_share"], 0.5)
        self.assertEqual(out["runs"]["always_strong"]["cost_usd"], 0.04)


if __name__ == "__main__":
    unittest.main()
