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
from unittest.mock import patch
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

    def test_direct_timeout_is_a_scalar_error_without_prompt_or_key(self) -> None:
        prompt = "private synthetic prompt"

        class Response:
            headers = {}

            def read(self) -> bytes:
                return b'{"status":"ok","model":"d3"}'

        class Opener:
            def __init__(self) -> None:
                self.calls = 0

            def open(self, *_args, **_kwargs):
                self.calls += 1
                if self.calls == 1:
                    return Response()
                raise TimeoutError("private prompt and bearer secret must not escape")

        args = argparse.Namespace(url="http://offline.invalid", config=None, api_key_env="D3_TEST_KEY",
                                  model="d3-test", model_label="offline", revision="", device="",
                                  warmup=0, timeout=0.01, limit=1, per_cell=None)
        with patch.object(ev, "seed_prompts", return_value=[{"id": "sample-1", "prompt": prompt,
                                                             "difficulty": "easy", "domain": "code"}]), \
                patch.object(ev, "opener", return_value=Opener()), \
                patch.dict("os.environ", {"D3_TEST_KEY": "private-bearer-token"}):
            result = ev.direct(args)
        serialized = json.dumps(result)
        self.assertEqual(result["errors"], {"unreachable_or_timeout": 1})
        self.assertEqual(result["answered"], 0)
        self.assertNotIn(prompt, serialized)
        self.assertNotIn("private-bearer-token", serialized)

    def test_router_timeout_is_reported_without_token_or_prompt(self) -> None:
        prompt = "private synthetic prompt"

        class Opener:
            def open(self, *_args, **_kwargs):
                raise TimeoutError("private prompt and bearer secret must not escape")

        with tempfile.TemporaryDirectory() as tmp:
            token_path = Path(tmp) / "token"
            token_path.write_text("private-bearer-token", encoding="utf-8")
            db_path = Path(tmp) / "usage.sqlite"
            db_path.touch()
            args = argparse.Namespace(token_file=str(token_path), limit=1, per_cell=None,
                                      base_url="http://offline.invalid", group="d3-test", run_marker="",
                                      max_tokens=32, timeout=0.01, mode_label="baseline",
                                      strong_model="gpt-test", strong_input_price=1.0, strong_output_price=1.0,
                                      cheap_input_price=0.1, cheap_output_price=0.1, usage_db=str(db_path))
            with patch.object(ev, "seed_prompts", return_value=[{"id": "sample-1", "prompt": prompt,
                                                                 "difficulty": "easy", "domain": "code"}]), \
                    patch.object(ev, "opener", return_value=Opener()), patch.object(ev.time, "sleep"):
                result = ev.router(args)
        serialized = json.dumps(result)
        self.assertEqual(result["http_status"], {0: 1})
        self.assertEqual(result["requests_failed_due_to_policy"], 0)
        self.assertNotIn(prompt, serialized)
        self.assertNotIn("private-bearer-token", serialized)


if __name__ == "__main__":
    unittest.main()
