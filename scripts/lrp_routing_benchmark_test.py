#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Unit tests for the offline LRP routing-benchmark harness."""

from __future__ import annotations

import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "lrp_routing_benchmark.py"


def load_module():
    spec = importlib.util.spec_from_file_location("lrp_routing_benchmark", SCRIPT)
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MODULE = load_module()


class RoutingBenchmarkHarnessTest(unittest.TestCase):
    def test_config_patches_abc_and_d_stub(self) -> None:
        a = MODULE.config_patch("A")
        self.assertEqual(a["models"]["lrp-bench"]["strategy"], "static")
        self.assertNotIn("external_policy", a["models"]["lrp-bench"])

        b = MODULE.config_patch("B")
        self.assertEqual(b["models"]["lrp-bench"]["external_policy"]["mode"], "shadow")

        c = MODULE.config_patch("C")
        self.assertEqual(c["models"]["lrp-bench"]["external_policy"]["mode"], "enforce")

        d = MODULE.config_patch("D")
        self.assertTrue(d["stub"])
        self.assertEqual(d["issue"], 162)

    def test_seed_and_harbor_plans(self) -> None:
        seed = MODULE.seed_load_plan(turn_ids=["t1", "t2"], concurrency=32)
        self.assertTrue(seed["synthetic_upstream"])
        self.assertEqual(seed["synthetic_upstream_mark"], "synthetic_upstream")

        harbor = MODULE.harbor_load_plan(
            task_set="aider/polyglot_python_two-bucket",
            task_revision="abc123",
            concurrency=4,
        )
        self.assertEqual(harbor["task_set"], "aider/polyglot_python_two-bucket")
        self.assertEqual(harbor["task_revision"], "abc123")
        self.assertEqual(harbor["harbor_pass_rate_role"], "load_shape_only")

        with self.assertRaises(MODULE.HarnessError):
            MODULE.seed_load_plan(turn_ids=["t1"], concurrency=2)
        with self.assertRaises(MODULE.HarnessError):
            MODULE.harbor_load_plan(task_set="", task_revision="x", concurrency=1)

    def test_run_matrix_shapes(self) -> None:
        cells = MODULE.run_matrix()
        seed_cells = [c for c in cells if c.get("driver") == "seed_replay" and not c.get("stub")]
        harbor_cells = [c for c in cells if c.get("driver") == "harbor"]
        self.assertEqual(len(seed_cells), 3 * 4)
        self.assertEqual(len(harbor_cells), 3 * 3)
        self.assertTrue(any(c.get("config_id") == "D" and c.get("stub") for c in cells))

    def test_three_run_median_spread(self) -> None:
        runs = [
            {"lrp_decisions_per_sec": 10.0, "timeout_rate": 0.1},
            {"lrp_decisions_per_sec": 12.0, "timeout_rate": 0.2},
            {"lrp_decisions_per_sec": 11.0, "timeout_rate": 0.0},
        ]
        agg = MODULE.aggregate_three_runs(runs)
        self.assertEqual(agg["metrics"]["lrp_decisions_per_sec"]["median"], 11.0)
        self.assertEqual(agg["metrics"]["lrp_decisions_per_sec"]["spread"], 2.0)
        self.assertEqual(agg["metrics"]["timeout_rate"]["median"], 0.1)

    def test_metric_families_forbid_savings_percent(self) -> None:
        overhead = MODULE.overhead_metrics(
            decisions_per_sec=100.0,
            router_added_ms=[1.0, 2.0, 3.0],
            sidecar_ms=[4.0, 5.0, 6.0],
            timeout_rate=0.0,
            error_rate_by_class={"timeout": 0.0, "upstream_5xx": 0.01},
            e2e_ttfb_ms=[10.0, 12.0],
            e2e_total_ms=[20.0, 30.0],
        )
        self.assertEqual(overhead["family"], "overhead")
        self.assertIn("router_added_latency_source", overhead)
        self.assertTrue(overhead["e2e_ttfb"]["upstream_dependent"])
        self.assertTrue(overhead["e2e_total"]["upstream_dependent"])
        self.assertEqual(overhead["error_rate_by_class"]["upstream_5xx"], 0.01)

        baseline = MODULE.overhead_metrics(
            decisions_per_sec=120.0,
            router_added_ms=[1.0],
            sidecar_ms=[1.0],
            timeout_rate=0.0,
        )
        candidate = MODULE.overhead_metrics(
            decisions_per_sec=100.0,
            router_added_ms=[3.0],
            sidecar_ms=[5.0],
            timeout_rate=0.1,
        )
        deltas = MODULE.overhead_deltas(
            baseline_a=baseline,
            candidate=candidate,
            candidate_config_id="B",
        )
        self.assertEqual(deltas["family"], "overhead_delta")
        self.assertEqual(deltas["deltas"]["lrp_decisions_per_sec_delta"], -20.0)
        self.assertEqual(deltas["deltas"]["router_added_latency_delta"], 2.0)

        effectiveness = MODULE.effectiveness_metrics(
            verifier_pass_rate=0.8,
            verifier_column_rates={"exact_match": 0.7},
            target_mix={"gpt-5.6": 0.2, "qwen": 0.8},
            abstention_rate=0.05,
        )
        self.assertEqual(effectiveness["primary_baseline"], "gpt-5.6-only")

        cost = MODULE.cost_token_metrics(
            tokens_by_category={k: 1.0 for k in MODULE.TOKEN_CATEGORIES},
            usd_by_category={k: 0.01 for k in MODULE.TOKEN_CATEGORIES},
            total_usd=1.0,
            gpt56_total_usd=3.0,
            volume_effect_usd={"input": -0.5},
            mix_effect_usd={"input": -1.5},
        )
        self.assertEqual(cost["absolute_usd_vs_gpt56"], 2.0)
        self.assertIn("savings_percent", cost["forbidden"])
        raw = json.dumps(cost)
        self.assertNotIn('"savings_percent":', raw)

    def test_volume_mix_decomposition(self) -> None:
        parts = MODULE.volume_mix_decomposition(
            baseline_tokens=100.0,
            selected_tokens=80.0,
            baseline_rate_per_token=0.01,
            selected_rate_per_token=0.005,
        )
        self.assertAlmostEqual(parts["volume_effect_usd"], -0.2)
        self.assertAlmostEqual(parts["mix_effect_usd"], -0.4)
        self.assertAlmostEqual(parts["sum_usd"], -0.6)

    def test_schema_and_placeholder_roundtrip(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            schema_path = out / "routing-benchmark.schema.json"
            MODULE.write_json(schema_path, MODULE.schema_document())
            paths = MODULE.write_placeholder_artifacts(out, router_commit="deadbeef")
            doc = MODULE.load_json(paths["json"])
            schema = MODULE.load_json(schema_path)
            errors = MODULE.validate_against_schema(doc, schema)
            self.assertEqual(errors, [])
            self.assertFalse(doc["live_runs_executed"])
            self.assertEqual(doc["configuration_d_issue"], 162)
            summary = paths["summary"].read_text(encoding="utf-8")
            self.assertIn("Router-added latency", summary)
            self.assertIn("router_upstream_wrote_request", summary)
            self.assertIn("internal/router/service.go", summary)

            live = dict(doc)
            live["live_runs_executed"] = True
            live["status"] = "live_abc_overhead_complete"
            self.assertEqual(MODULE.validate_against_schema(live, schema), [])

            percent_doc = dict(doc)
            percent_doc["cells"] = [{"savings_percent": 12.0}]
            self.assertIn("forbidden_savings_percent", MODULE.validate_against_schema(percent_doc, schema))

    def test_cli_plan_and_validate(self) -> None:
        import contextlib
        import io

        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            self.assertEqual(MODULE.main(["plan", "--no-harbor"]), 0)
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            with contextlib.redirect_stdout(buf):
                self.assertEqual(MODULE.main(["write-schema", "--out", str(out / "schema.json")]), 0)
                self.assertEqual(
                    MODULE.main(
                        [
                            "write-placeholder",
                            "--out-dir",
                            str(out),
                            "--router-commit",
                            "test",
                        ]
                    ),
                    0,
                )
                self.assertEqual(
                    MODULE.main(
                        [
                            "validate",
                            "--document",
                            str(out / "routing-benchmark.json"),
                            "--schema",
                            str(out / "schema.json"),
                        ]
                    ),
                    0,
                )


if __name__ == "__main__":
    unittest.main()
