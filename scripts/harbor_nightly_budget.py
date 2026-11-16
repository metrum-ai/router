# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Fail-closed same-day reservation accounting for the paid Harbor job."""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

MAX_NIGHTLY_USD = 0.50


def remaining_budget(reports: list[Path], ceiling: float = MAX_NIGHTLY_USD) -> float:
    reserved = 0.0
    seen: set[str] = set()
    for path in reports:
        try:
            report = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise ValueError(f"unreadable Harbor cost report: {path}") from exc
        if not isinstance(report, dict):
            raise ValueError(f"invalid Harbor cost report: {path}")
        run_id = report.get("run_id")
        value = report.get("reserved_upper_usd")
        if not isinstance(run_id, str) or not run_id:
            raise ValueError(f"Harbor cost report has no run identity: {path}")
        if run_id in seen:
            raise ValueError(f"duplicate Harbor cost report identity: {run_id}")
        if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
            raise ValueError(f"Harbor cost report has invalid reservation: {path}")
        seen.add(run_id)
        reserved += float(value)
    if reserved > ceiling:
        raise ValueError("same-day Harbor reservation already exceeds the spend ceiling")
    return round(ceiling - reserved, 8)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("reports", nargs="*", type=Path)
    args = parser.parse_args()
    try:
        remaining = remaining_budget(args.reports)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps({"ceiling_usd": MAX_NIGHTLY_USD,
                                    "remaining_usd": remaining}, sort_keys=True) + "\n",
                        encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
