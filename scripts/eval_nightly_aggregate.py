#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Build a rolling, sample-aware aggregate from sanitized eval run reports."""

from __future__ import annotations

import argparse
import json
import math
import sys
from collections import defaultdict
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import eval_report_schema  # noqa: E402


class AggregateError(ValueError):
    pass


HISTORY_GATE_VIOLATION_MAX_AGE_DAYS = 30


def report_is_historical(report: dict[str, Any], cutoff: datetime) -> bool:
    value = report.get("started_utc")
    if not isinstance(value, str):
        raise AggregateError("run report is missing started_utc")
    try:
        started = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise AggregateError("run report started_utc is invalid") from exc
    if started.tzinfo is None:
        started = started.replace(tzinfo=timezone.utc)
    return started.astimezone(timezone.utc) < cutoff


def wilson_interval(passes: int, attempts: int, z: float = 1.959963984540054) -> list[float]:
    if attempts < 1 or passes < 0 or passes > attempts:
        raise AggregateError("Wilson interval needs attempts >= 1 and 0 <= passes <= attempts")
    rate = passes / attempts
    den = 1 + z * z / attempts
    center = (rate + z * z / (2 * attempts)) / den
    radius = z * math.sqrt(rate * (1 - rate) / attempts + z * z / (4 * attempts * attempts)) / den
    return [round(max(0.0, center - radius), 6), round(min(1.0, center + radius), 6)]


def aggregate_reports(reports: list[dict[str, Any]], matrix: dict[str, Any]) -> dict[str, Any]:
    if not reports:
        raise AggregateError("at least one run report is required")
    expected = {
        cell["id"]: cell
        for cell in matrix.get("cells", [])
        if cell.get("environment") == "nightly-live"
    }
    if not expected:
        raise AggregateError("matrix has no nightly-live cells")

    seen_runs: set[str] = set()
    rows: dict[str, list[tuple[str, dict[str, Any]]]] = defaultdict(list)
    violations: list[str] = []
    historical_violations: list[str] = []
    historical_run_ids: set[str] = set()
    cutoff = datetime.now(timezone.utc) - timedelta(days=HISTORY_GATE_VIOLATION_MAX_AGE_DAYS)
    paired_outcomes: dict[str, int] = defaultdict(int)
    cost_totals: dict[str, float] = defaultdict(float)
    for report in reports:
        run_id = report.get("run_id")
        if not isinstance(run_id, str) or not run_id:
            raise AggregateError("run report is missing run_id")
        historical = report_is_historical(report, cutoff)
        if historical:
            historical_run_ids.add(run_id)
        errors = eval_report_schema.validate_report(report, matrix)
        # Per-run reports intentionally accumulate below the preregistered
        # sample size; that condition is decided over the rolling window.
        errors = [error for error in errors if "below the preregistered sample size" not in error]
        if errors:
            target = historical_violations if historical else violations
            target.extend(f"{run_id}: {error}" for error in errors)
        for error in report.get("gate_violations", []):
            if isinstance(error, str):
                target = historical_violations if historical else violations
                target.append(f"{run_id}: {error}")
        for name, count in ((report.get("canary") or {}).get("classification") or {}).items():
            if isinstance(count, int) and not isinstance(count, bool) and count >= 0:
                paired_outcomes[name] += count
        nightly_cost = report.get("nightly_cost") or {}
        for name in ("reserved_usd", "charged_usd", "router_usage_rows_usd", "direct_provider_usage_estimate_usd"):
            value = nightly_cost.get(name)
            if isinstance(value, (int, float)) and not isinstance(value, bool):
                cost_totals[name] += float(value)
        if run_id in seen_runs:
            raise AggregateError(f"duplicate run_id {run_id!r}")
        seen_runs.add(run_id)
        report_cells = {cell["cell_id"]: cell for cell in report["cells"]}
        unknown = sorted(set(report_cells) - set(expected))
        if unknown:
            raise AggregateError(f"{run_id}: report contains non-nightly cells: {unknown}")
        for cell_id, cell in report_cells.items():
            rows[cell_id].append((run_id, cell))

    aggregate_cells = []
    missing_cells = []
    expected_missing_cells = []
    for cell_id, preregistered in expected.items():
        samples = rows.get(cell_id, [])
        if not samples:
            if preregistered.get("nightly_status") == "expected-missing":
                expected_missing_cells.append(cell_id)
            else:
                missing_cells.append(cell_id)
            continue
        modes = {cell["mode"] for _, cell in samples}
        if len(modes) != 1:
            raise AggregateError(f"{cell_id}: mode changed across runs")
        task_results = []
        attempts = passes = paired_wins = paired_losses = 0
        has_pairs = False
        for run_id, cell in samples:
            attempts += cell["attempts"]
            passes += cell["passes"]
            for task in cell["task_results"]:
                task_results.append({**task, "task_id": f"{run_id}/{task['task_id']}"})
            paired = cell.get("paired_baseline")
            if paired is not None:
                has_pairs = True
                paired_wins += paired["wins"]
                paired_losses += paired["losses"]
        aggregate = {
            "cell_id": cell_id,
            "mode": next(iter(modes)),
            "attempts": attempts,
            "passes": passes,
            "pass_rate_ci95": wilson_interval(passes, attempts),
            "provenance": {
                **samples[0][1]["provenance"],
                "seed": f"rolling:{len(seen_runs)}-runs",
            },
            "task_results": task_results,
        }
        if has_pairs:
            aggregate["paired_baseline"] = {"wins": paired_wins, "losses": paired_losses}
        aggregate_cells.append(aggregate)

    # Keep preregistered cells visible even when this workflow only has the
    # credentials and runner support for a subset. Expected-missing cells are
    # reported without failing the nightly gate; unexpected gaps still fail.

    under_sampled = [
        cell["cell_id"] for cell in aggregate_cells
        if cell["attempts"] < expected[cell["cell_id"]]["sample_size"]
    ]
    if missing_cells:
        violations.extend(f"{cell_id}: no nightly reports" for cell_id in missing_cells)
    # Preserve the schema's task-level gates over the joined rolling sample.
    candidate = {
        "schema_version": 1,
        "run_id": "nightly-aggregate-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ"),
        "started_utc": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "cells": aggregate_cells,
    }
    try:
        candidate_errors = eval_report_schema.validate_report(candidate, matrix)
        # Sample size is a certification refusal, not a nightly gate failure.
        candidate_errors = [error for error in candidate_errors if "below the preregistered sample size" not in error]
        for error in candidate_errors:
            if any(f"/{run_id}/" in error for run_id in historical_run_ids):
                historical_violations.append(error)
            else:
                violations.append(error)
    except eval_report_schema.ReportError as exc:
        raise AggregateError(f"aggregated report is invalid: {exc}") from exc

    sample_complete = not missing_cells and not expected_missing_cells and not under_sampled
    candidate["aggregate"] = {
        "run_count": len(seen_runs),
        "run_ids": sorted(seen_runs),
        "sample_complete": sample_complete,
        "certification": "sample_complete_not_certified" if sample_complete else "not_available",
        "missing_cells": missing_cells,
        "expected_missing_cells": expected_missing_cells,
        "under_sampled_cells": under_sampled,
        "gate_violations": sorted(set(violations)),
        "historical_gate_violations": sorted(set(historical_violations)),
        "historical_gate_violation_max_age_days": HISTORY_GATE_VIOLATION_MAX_AGE_DAYS,
        "paired_outcomes": dict(sorted(paired_outcomes.items())),
        "cost_totals_usd": {name: round(value, 8) for name, value in sorted(cost_totals.items())},
        "cost_basis": {
            "router_usage_rows_usd": "measured request_usage rows",
            "direct_provider_usage_estimate_usd": "list-rate estimate",
        },
    }
    return candidate


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--matrix", type=Path, default=ROOT / "evaluators/eval-infra/preregistered-matrix.yaml")
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("reports", nargs="+", type=Path)
    args = parser.parse_args()
    try:
        import yaml

        matrix = yaml.safe_load(args.matrix.read_text(encoding="utf-8"))
        reports = [json.loads(path.read_text(encoding="utf-8")) for path in args.reports]
        result = aggregate_reports(reports, matrix)
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    except (OSError, json.JSONDecodeError, AggregateError, eval_report_schema.ReportError, ImportError) as exc:
        print(f"evaluation aggregate failed: {type(exc).__name__}: {exc}", file=sys.stderr)
        return 2
    aggregate = result["aggregate"]
    print(f"runs={aggregate['run_count']} sample_complete={aggregate['sample_complete']} "
          f"certification={aggregate['certification']} violations={len(aggregate['gate_violations'])}")
    return 1 if aggregate["gate_violations"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
