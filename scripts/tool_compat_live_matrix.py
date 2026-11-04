#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Turn a live gate run into matrix results, and guard the monthly live spend.

This is the results path for the nightly tool-call matrix (#572). It does not
dispatch anything and it does not hold a budget reservation of its own: the gate
(`scripts/parallel_tool_live.py`) reserves before every dispatch under its own
`--cap`, and this tool only (a) converts its per-cell report into the ingest
format that `scripts/tool_compat_matrix.py --ingest` accepts, and (b) records and
enforces the monthly ceiling across runs through one small ledger file.

Costs come from the gate's own router usage rows (`total_cost_usd`), never from
list-rate arithmetic; direct-provider cells are billed outside the router and
are labelled `provider_invoiced`.

Commands:

    report --gate-report raw.json --out report.json --commit SHA
    guard  --ledger spend.json --monthly-usd 10 --run-cap 1
    record --ledger spend.json --cost-usd 0.0012
"""

from __future__ import annotations

import argparse
import json
import sys
from decimal import Decimal, InvalidOperation
from pathlib import Path

# Gate cell -> matrix cell id. A cell name that is not listed here fails the
# conversion instead of inventing an id or silently dropping a result.
CELL_MAP = {
    "openai-chat-direct": "parallel-gate-chat-direct",
    "openai-chat-routed": "parallel-gate-chat-through-router",
    "anthropic-messages-direct": "parallel-gate-messages-direct",
    "anthropic-messages-routed": "parallel-gate-messages-through-router",
    "chat-caller-to-messages-target": "parallel-gate-chat-to-messages",
    "messages-caller-to-chat-target": "parallel-gate-messages-to-chat",
}

# Cells that run through the local router and therefore have router usage rows.
ROUTED_CELLS = {name for name in CELL_MAP if name.endswith("-routed")} | {
    "chat-caller-to-messages-target",
    "messages-caller-to-chat-target",
}


class ReportError(ValueError):
    pass


def router_cost_usd(raw: dict) -> Decimal:
    rows = raw.get("router_usage_rows")
    if not isinstance(rows, list):
        raise ReportError("gate report has no router_usage_rows list")
    total = Decimal("0")
    for row in rows:
        if not isinstance(row, dict):
            raise ReportError("router usage row is not an object")
        value = row.get("total_cost_usd")
        if value is None:
            continue
        try:
            total += Decimal(str(value))
        except InvalidOperation as exc:
            raise ReportError(f"unparseable total_cost_usd: {value!r}") from exc
    return total


DIRECT_CELL_CHARGE_LABEL = "usage rows + list-rate estimate for direct cells"


def charged_cost_usd(raw: dict) -> tuple[Decimal, str]:
    """Cost to charge for one run.

    Router usage rows cover the routed cells only; the direct cells are invoiced
    by the provider outside the router and are priced by the gate at list rates.
    The run is therefore charged the larger of the two, so the monthly guard
    cannot undercount. When any turn has unknown usage, or the gate halted, the
    reservation is charged instead: an unknown outcome must never be cheap.
    """
    router_usage = router_cost_usd(raw)
    try:
        list_rate = Decimal(str(raw.get("list_rate_usage_usd") or 0))
    except InvalidOperation as exc:
        raise ReportError(f"unparseable list_rate_usage_usd: {raw.get('list_rate_usage_usd')!r}") from exc
    unknown = raw.get("unknown_usage_turns") or 0
    if isinstance(unknown, bool) or not isinstance(unknown, int) or unknown < 0:
        raise ReportError(f"unknown_usage_turns must be a nonnegative int, got {unknown!r}")
    halted = bool(raw.get("halted"))
    if unknown > 0 or halted:
        try:
            reserved = Decimal(str(raw.get("reserved_usd") or 0))
        except InvalidOperation as exc:
            raise ReportError(f"unparseable reserved_usd: {raw.get('reserved_usd')!r}") from exc
        reason = "reserved (unknown usage or halted run)"
        return max(reserved, router_usage), reason
    charged = max(router_usage, list_rate)
    if list_rate > router_usage:
        return charged, DIRECT_CELL_CHARGE_LABEL
    return charged, "router usage rows"


def build_report(raw: dict, date: str, commit: str, runner: str = "agent-tool-compat-live") -> dict:
    cells = raw.get("cells")
    if not isinstance(cells, list) or not cells:
        raise ReportError("gate report has no cells")
    usage = router_cost_usd(raw)
    charged, charge_reason = charged_cost_usd(raw)
    out_cells = []
    seen = set()
    for cell in cells:
        name = cell.get("cell")
        if name not in CELL_MAP:
            raise ReportError(f"unknown gate cell {name!r}")
        if name in seen:
            raise ReportError(f"duplicate gate cell {name!r}")
        seen.add(name)
        problems = cell.get("problems") or []
        detail = "; ".join(str(problem) for problem in problems)
        if name in ROUTED_CELLS:
            cost = {"usd": float(usage), "source": "usage_rows", "scope": "run"}
        else:
            cost = {"usd": None, "source": "provider_invoiced", "scope": "cell"}
        entry = {
            "id": CELL_MAP[name],
            "status": "fail" if problems else "pass",
            "detail": detail or "nightly live gate",
            "cost": cost,
        }
        out_cells.append(entry)
    report = {
        "schema_version": 1,
        "date": date,
        "commit": commit,
        "runner": runner,
        "cost": {
            "usd": float(charged),
            "source": "usage_rows",
            "scope": "run",
            "note": charge_reason,
            "router_usage_rows_usd": float(usage),
        },
        "cells": out_cells,
    }
    return report


def load_ledger(path: Path) -> dict:
    if not path.exists():
        return {"monthly": {}, "runs": []}
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ReportError(f"cannot read ledger {path}: {exc}") from exc
    if not isinstance(data, dict) or not isinstance(data.get("monthly"), dict) or not isinstance(data.get("runs"), list):
        raise ReportError(f"ledger {path} has an unexpected shape")
    return data


def charge_for_recorded_run(report_path: Path, gate_path: Path, run_cap: Decimal) -> tuple[Decimal, str]:
    """Cost to put in the ledger, even when the run did not finish.

    A run that produced a report charges its report cost; a run that only
    produced a raw gate report charges that; a run that produced neither charges
    the full per-run cap, so a crashed or killed job cannot spend unrecorded.
    """
    if report_path.exists():
        try:
            data = json.loads(report_path.read_text(encoding="utf-8"))
            return Decimal(str(data["cost"]["usd"])), str(data["cost"].get("note") or "report cost")
        except (OSError, json.JSONDecodeError, KeyError, TypeError, InvalidOperation) as exc:
            return run_cap, f"report unreadable ({type(exc).__name__}): charging the run cap"
    if gate_path.exists():
        try:
            raw = json.loads(gate_path.read_text(encoding="utf-8"))
            charged, reason = charged_cost_usd(raw)
            return charged, f"gate report only: {reason}"
        except (OSError, json.JSONDecodeError, ReportError) as exc:
            return run_cap, f"gate report unreadable ({type(exc).__name__}): charging the run cap"
    return run_cap, "no report produced after the gate started: charging the run cap"


def ledger_miss_warning(ledger_path: Path, month: str) -> str:
    if not ledger_path.exists():
        return (
            f"ledger not restored (cache miss or eviction): starting {month} from 0;"
            " an evicted ledger entry undercounts this month, so review the month's runs"
            " before assuming the ceiling is intact"
        )
    return ""


def guard(ledger: dict, month: str, monthly_usd: Decimal, run_cap: Decimal) -> tuple[bool, Decimal, str]:
    spent = Decimal(str(ledger["monthly"].get(month, 0)))
    if spent + run_cap > monthly_usd:
        return (
            False,
            spent,
            f"monthly live budget exhausted for {month}: spent ${spent} + ${run_cap} run cap > ${monthly_usd}",
        )
    return True, spent, f"monthly live budget allows ${run_cap} for {month} (spent ${spent} of ${monthly_usd})"


def record(ledger: dict, month: str, cost_usd: Decimal, run_id: str) -> dict:
    spent = Decimal(str(ledger["monthly"].get(month, 0))) + cost_usd
    ledger["monthly"][month] = float(spent)
    ledger["runs"] = (ledger["runs"] + [{"month": month, "run": run_id, "usd": float(cost_usd)}])[-200:]
    return ledger


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    report_parser = sub.add_parser("report", help="convert a gate report into matrix ingest input")
    report_parser.add_argument("--gate-report", type=Path, required=True)
    report_parser.add_argument("--out", type=Path, required=True)
    report_parser.add_argument("--date", required=True)
    report_parser.add_argument("--commit", required=True)
    report_parser.add_argument("--runner", default="agent-tool-compat-live")

    guard_parser = sub.add_parser("guard", help="refuse to start when the monthly budget is spent")
    guard_parser.add_argument("--ledger", type=Path, required=True)
    guard_parser.add_argument("--month", required=True)
    guard_parser.add_argument("--monthly-usd", required=True)
    guard_parser.add_argument("--run-cap", required=True)

    record_run_parser = sub.add_parser(
        "record-run", help="charge a run to the ledger from its report, its gate report, or the run cap"
    )
    record_run_parser.add_argument("--ledger", type=Path, required=True)
    record_run_parser.add_argument("--month", required=True)
    record_run_parser.add_argument("--report", type=Path, required=True)
    record_run_parser.add_argument("--gate", type=Path, required=True)
    record_run_parser.add_argument("--cap", required=True)
    record_run_parser.add_argument("--run-id", default="local")

    record_parser = sub.add_parser("record", help="add a run's actual cost to the ledger")
    record_parser.add_argument("--ledger", type=Path, required=True)
    record_parser.add_argument("--month", required=True)
    record_parser.add_argument("--cost-usd", required=True)
    record_parser.add_argument("--run-id", default="local")

    args = parser.parse_args()
    try:
        if args.command == "report":
            raw = json.loads(args.gate_report.read_text(encoding="utf-8"))
            report = build_report(raw, args.date, args.commit, args.runner)
            args.out.parent.mkdir(parents=True, exist_ok=True)
            args.out.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
            failed = [cell["id"] for cell in report["cells"] if cell["status"] == "fail"]
            print(json.dumps({"cells": len(report["cells"]), "failed": failed,
                              "router_cost_usd": report["cost"]["usd"]}), file=sys.stderr)
            return 0
        if args.command == "guard":
            warning = ledger_miss_warning(args.ledger, args.month)
            if warning:
                print(f"::warning::{warning}", file=sys.stderr)
            ledger = load_ledger(args.ledger)
            allowed, spent, message = guard(ledger, args.month, Decimal(args.monthly_usd), Decimal(args.run_cap))
            print(message)
            print(json.dumps({"allowed": allowed, "month": args.month, "cap": args.run_cap,
                              "spent": float(spent), "monthly": args.monthly_usd}))
            return 0 if allowed else 1
        if args.command == "record-run":
            charged, reason = charge_for_recorded_run(args.report, args.gate, Decimal(args.cap))
            ledger = load_ledger(args.ledger)
            ledger = record(ledger, args.month, charged, args.run_id)
            args.ledger.parent.mkdir(parents=True, exist_ok=True)
            args.ledger.write_text(json.dumps(ledger, indent=2) + "\n", encoding="utf-8")
            print(json.dumps({"month": args.month, "charged_usd": float(charged),
                              "reason": reason, "spent": ledger["monthly"][args.month]}))
            return 0
        ledger = load_ledger(args.ledger)
        ledger = record(ledger, args.month, Decimal(args.cost_usd), args.run_id)
        args.ledger.parent.mkdir(parents=True, exist_ok=True)
        args.ledger.write_text(json.dumps(ledger, indent=2) + "\n", encoding="utf-8")
        print(json.dumps({"month": args.month, "spent": ledger["monthly"][args.month]}))
        return 0
    except (ReportError, InvalidOperation, OSError, json.JSONDecodeError) as exc:
        print(f"live matrix result handling stopped: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
