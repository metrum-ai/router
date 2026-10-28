#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Evaluation report schema validator (metrum-ai/router#330).

EVAL-03: a report records attempts per preregistered cell, pass counts,
confidence intervals, and paired task outcomes against the baseline.
EVAL-05: latency and cost are gated at task level (time to first event,
completion latency, tool turns, cost per verified successful task including
failures).
AGENT-01: provenance pins client versions, task/container digests, and the
sampling seed; an unbounded "latest" is rejected in reproducibility rows.

Usage:
  python3 scripts/eval_report_schema.py report.json [--matrix evaluators/eval-infra/preregistered-matrix.yaml]

Exit 2 with a message when the report is invalid.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_MATRIX = ROOT / "evaluators" / "eval-infra" / "preregistered-matrix.yaml"

REQUIRED_TOP = ("schema_version", "run_id", "started_utc", "cells")
REQUIRED_CELL = ("cell_id", "mode", "attempts", "passes", "task_results")
REQUIRED_TASK = ("task_id", "outcome", "time_to_first_event_ms", "completion_latency_ms", "tool_turns", "cost_usd")
OUTCOMES = ("pass", "fail", "error", "skipped")


class ReportError(ValueError):
    pass


def _is_number(value) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value)


def validate_report(report: dict, matrix_doc: dict | None = None) -> list[str]:
    """Return the list of gate violations; raise ReportError on schema errors."""
    if not isinstance(report, dict):
        raise ReportError("report must be a JSON object")
    if report.get("schema_version") != 1:
        raise ReportError("schema_version must be 1")
    for key in REQUIRED_TOP:
        if key not in report:
            raise ReportError(f"report is missing {key!r}")
    if not isinstance(report["run_id"], str) or not report["run_id"]:
        raise ReportError("run_id must be a non-empty string")

    violations: list[str] = []
    preregistered = {}
    if matrix_doc:
        preregistered = {c["id"]: c for c in matrix_doc["cells"]}

    cells = report["cells"]
    if not isinstance(cells, list) or not cells:
        raise ReportError("report must contain at least one cell")
    seen = set()
    for cell in cells:
        if not isinstance(cell, dict):
            raise ReportError("cell must be an object")
        for key in REQUIRED_CELL:
            if key not in cell:
                raise ReportError(f"cell {cell.get('cell_id', '?')!r} is missing {key!r}")
        cell_id = cell["cell_id"]
        if cell_id in seen:
            raise ReportError(f"duplicate cell {cell_id!r}")
        seen.add(cell_id)
        attempts, passes = cell["attempts"], cell["passes"]
        if not isinstance(attempts, int) or attempts < 1 or not isinstance(passes, int) or passes < 0 or passes > attempts:
            raise ReportError(f"cell {cell_id!r}: need attempts >= 1 and 0 <= passes <= attempts")

        # EVAL-03: confidence interval on the pass rate (Wilson, 95%).
        if "pass_rate_ci95" in cell:
            ci = cell["pass_rate_ci95"]
            if not (isinstance(ci, list) and len(ci) == 2 and all(_is_number(x) for x in ci) and 0 <= ci[0] <= ci[1] <= 1):
                raise ReportError(f"cell {cell_id!r}: pass_rate_ci95 must be [low, high] within [0, 1]")

        # AGENT-01: provenance pins.
        provenance = cell.get("provenance")
        if not isinstance(provenance, dict):
            raise ReportError(f"cell {cell_id!r}: provenance is required")
        for key in ("client_versions", "seed"):
            if key not in provenance:
                raise ReportError(f"cell {cell_id!r}: provenance missing {key!r}")
        for name, version in (provenance.get("client_versions") or {}).items():
            if not isinstance(version, str) or version in ("", "latest"):
                raise ReportError(f"cell {cell_id!r}: client {name!r} must be pinned, got {version!r}")
        for name, digest in (provenance.get("task_image_digests") or {}).items():
            if not isinstance(digest, str) or not digest.startswith("sha256:"):
                raise ReportError(f"cell {cell_id!r}: task image {name!r} must be a sha256 digest")

        # Preregistered sample size must be met (EVAL-03).
        pre = preregistered.get(cell_id)
        if pre is not None and attempts < pre["sample_size"]:
            violations.append(f"{cell_id}: attempts {attempts} below the preregistered sample size {pre['sample_size']}")

        # EVAL-05 task-level latency/cost gates.
        tasks = cell["task_results"]
        if not isinstance(tasks, list) or not tasks:
            raise ReportError(f"cell {cell_id!r}: task_results must be a non-empty list")
        gates = (matrix_doc or {}).get("task_gates", {})
        verified = 0
        total_cost = 0.0
        for task in tasks:
            for key in REQUIRED_TASK:
                if key not in task:
                    raise ReportError(f"cell {cell_id!r} task is missing {key!r}")
            if task["outcome"] not in OUTCOMES:
                raise ReportError(f"cell {cell_id!r}: unknown outcome {task['outcome']!r}")
            for key in ("time_to_first_event_ms", "completion_latency_ms", "tool_turns", "cost_usd"):
                if not _is_number(task[key]) or task[key] < 0:
                    raise ReportError(f"cell {cell_id!r}: {key} must be a non-negative number")
            if task["outcome"] == "pass":
                verified += 1
            total_cost += task["cost_usd"]  # failures included, per EVAL-05
            if gates:
                if task["time_to_first_event_ms"] > gates["max_time_to_first_event_ms"]:
                    violations.append(f"{cell_id}/{task['task_id']}: time-to-first-event {task['time_to_first_event_ms']}ms over gate {gates['max_time_to_first_event_ms']}ms")
                if task["completion_latency_ms"] > gates["max_completion_latency_ms"]:
                    violations.append(f"{cell_id}/{task['task_id']}: completion latency {task['completion_latency_ms']}ms over gate {gates['max_completion_latency_ms']}ms")
                if task["tool_turns"] > gates["max_tool_turns"]:
                    violations.append(f"{cell_id}/{task['task_id']}: tool turns {task['tool_turns']} over gate {gates['max_tool_turns']}")
        if gates and verified:
            per_task = total_cost / verified
            if per_task > gates["max_cost_per_verified_task_usd"]:
                violations.append(f"{cell_id}: cost per verified task ${per_task:.4f} over gate ${gates['max_cost_per_verified_task_usd']:.4f}")

        # EVAL-03 paired outcomes against the baseline are recorded when present.
        if "paired_baseline" in cell:
            paired = cell["paired_baseline"]
            if not isinstance(paired, dict) or not isinstance(paired.get("wins"), int) or not isinstance(paired.get("losses"), int):
                raise ReportError(f"cell {cell_id!r}: paired_baseline must record wins/losses")
    return violations


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("report", type=Path)
    parser.add_argument("--matrix", type=Path, default=DEFAULT_MATRIX)
    args = parser.parse_args()
    try:
        report = json.loads(args.report.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        print(f"eval report unreadable: {exc}", file=sys.stderr)
        return 2
    matrix_doc = None
    if args.matrix.is_file():
        import yaml

        matrix_doc = yaml.safe_load(args.matrix.read_text(encoding="utf-8"))
    try:
        violations = validate_report(report, matrix_doc)
    except ReportError as exc:
        print(f"eval report invalid: {exc}", file=sys.stderr)
        return 2
    if violations:
        for v in violations:
            print(f"gate violation: {v}", file=sys.stderr)
        return 1
    print(f"eval report ok: {len(report['cells'])} cells, schema valid, gates pass")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
