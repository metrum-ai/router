#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline regression for the eval-infra matrix and report schema (#330)."""
from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def load(name: str):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts" / f"{name}.py")
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


M = load("eval_infra_matrix")
R = load("eval_report_schema")


def require(ok: bool, message: str) -> None:
    if not ok:
        raise AssertionError(message)


def main() -> int:
    doc = M.load_matrix(M.DEFAULT_MATRIX)
    combos = M.validate(doc)
    print(f"matrix: {len(combos)} combinations")
    require(("stream", "tools") in combos, "pairwise coverage must include stream+tools")
    require(("reasoning", "stream", "tools") in combos, "mandatory triple stream+tools+reasoning must be covered")
    require(("image", "reasoning") not in combos, "excluded pair must stay out")
    require(("continuation", "stream") not in combos, "excluded pair must stay out")

    # Every pair of non-excluded factor values is covered by some combination.
    values = M.factor_values(doc)
    excluded = {tuple(sorted(e["combo"])) for e in doc["excluded"]}
    covered = {tuple(sorted(p)) for combo in combos for p in __import__("itertools").combinations(combo, 2)}
    import itertools

    for pair in itertools.combinations(values, 2):
        pair = tuple(sorted(pair))
        if pair in excluded:
            continue
        require(pair in covered, f"pair {pair} neither covered nor explicitly excluded")

    # Broken matrix inputs fail loudly.
    bad = dict(doc)
    bad["cells"] = [dict(doc["cells"][0], mode="nope")]
    try:
        M.validate(bad)
    except M.MatrixError as exc:
        require("undeclared mode" in str(exc), str(exc))
    else:
        raise AssertionError("undeclared mode must fail")

    # Report schema: a valid report passes.
    report = {
        "schema_version": 1,
        "run_id": "test-run",
        "started_utc": "2026-10-27T00:00:00Z",
        "cells": [
            {
                "cell_id": "api-compat-dynamic",
                "mode": "dynamic-group",
                "attempts": 2,
                "passes": 2,
                "pass_rate_ci95": [0.34, 1.0],
                "provenance": {"client_versions": {"openai": "7.45.1"}, "seed": "s-1", "task_image_digests": {"task": "sha256:" + "ab" * 32}},
                "task_results": [
                    {"task_id": "t1", "outcome": "pass", "time_to_first_event_ms": 800, "completion_latency_ms": 5000, "tool_turns": 3, "cost_usd": 0.01},
                    {"task_id": "t2", "outcome": "fail", "time_to_first_event_ms": 700, "completion_latency_ms": 4000, "tool_turns": 5, "cost_usd": 0.02},
                ],
            }
        ],
    }
    require(R.validate_report(report, doc) == [], "valid report must pass")
    require(R.validate_report(report, None) == [], "valid report must pass without a matrix")

    # Gate violation: cost per verified task above the gate (failures count).
    report2 = json.loads(json.dumps(report))
    report2["cells"][0]["task_results"][1]["cost_usd"] = 10.0
    violations = R.validate_report(report2, doc)
    require(any("cost per verified task" in v for v in violations), f"cost gate must fire: {violations}")

    # Schema errors: unpinned client, bad CI, duplicate cells, pass>attempts.
    def rejects(mutate, text):
        broken = json.loads(json.dumps(report))
        mutate(broken)
        try:
            R.validate_report(broken, doc)
        except R.ReportError as exc:
            require(text in str(exc), f"{text!r} not in {exc}")
            return
        raise AssertionError(f"expected rejection containing {text!r}")

    rejects(lambda r: r["cells"][0]["provenance"]["client_versions"].update({"openai": "latest"}), "must be pinned")
    rejects(lambda r: r["cells"][0].update({"pass_rate_ci95": [0.9, 0.1]}), "pass_rate_ci95")
    rejects(lambda r: r["cells"].append(r["cells"][0]), "duplicate cell")
    rejects(lambda r: r["cells"][0].update({"passes": 5}), "passes <= attempts")
    rejects(lambda r: r["cells"][0]["task_results"][0].pop("cost_usd"), "missing 'cost_usd'")

    # CLI smoke on the real matrix file.
    out = subprocess.run([sys.executable, str(ROOT / "scripts" / "eval_infra_matrix.py")], capture_output=True, text=True)
    require(out.returncode == 0 and "pairwise" in out.stdout, f"matrix CLI failed: {out.stderr}")
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
        json.dump(report, fh)
        path = fh.name
    out = subprocess.run([sys.executable, str(ROOT / "scripts" / "eval_report_schema.py"), path], capture_output=True, text=True)
    require(out.returncode == 0, f"report CLI failed: {out.stderr}")
    print("eval-infra matrix/schema tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
