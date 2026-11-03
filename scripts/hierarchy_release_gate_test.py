#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline tests for scripts/hierarchy_release_gate.py.

Covers a green matrix, an uncovered red cell, a documented exception, a stale
exception, malformed reports, and malformed exception files. Every case runs
the real CLI so the release step's exit status is what is tested.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "hierarchy_release_gate.py"
SCHEMA = "router-agent-chain-smoke-v1"
EXCEPTION_SCHEMA = "router-hierarchy-release-exceptions-v1"


def report(cells: list[tuple[str, str, int, str]]) -> dict:
    return {
        "schema": SCHEMA,
        "fixture": "local-scripted-sse",
        "hosted_model_calls": 0,
        "results": [
            {"client": client, "surface": "openai-chat", "path": path, "depth": depth, "status": status}
            for client, path, depth, status in cells
        ],
    }


def exceptions(entries: list[dict]) -> dict:
    return {"schema": EXCEPTION_SCHEMA, "exceptions": entries}


def run(work: Path, reports: list[dict], exception_doc: dict | None) -> tuple[int, str, str]:
    args = [sys.executable, str(SCRIPT)]
    for index, document in enumerate(reports):
        path = work / f"report-{index}.json"
        path.write_text(json.dumps(document), encoding="utf-8")
        args.extend(["--report", str(path)])
    exception_path = work / "exceptions.json"
    if exception_doc is None:
        args.extend(["--exceptions", str(exception_path)])
    else:
        exception_path.write_text(json.dumps(exception_doc), encoding="utf-8")
        args.extend(["--exceptions", str(exception_path)])
    proc = subprocess.run(args, capture_output=True, text=True, check=False, cwd=work)
    return proc.returncode, proc.stdout, proc.stderr


def case(work: Path, name: str, reports: list[dict], exception_doc: dict | None, want: int) -> int:
    code, out, err = run(work, reports, exception_doc)
    if code != want:
        print(f"FAIL {name}: exit {code}, want {want}\nstdout:\n{out}\nstderr:\n{err}")
        return 1
    return 0


def main() -> int:
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        work = Path(tmp)
        green = report([("pi", "same-dialect", 0, "pass"), ("codex", "same-dialect", 5, "pass")])
        red = report([("pi", "same-dialect", 0, "pass"), ("pi", "same-dialect", 1, "fail")])
        empty_exceptions = exceptions([])

        failures += case(work, "green matrix", [green], empty_exceptions, 0)
        failures += case(work, "red cell without exception", [red], empty_exceptions, 1)

        documented = exceptions(
            [
                {
                    "cell": "pi/same-dialect/depth-1",
                    "reason": "upstream fixture regression tracked in the linked issue",
                    "issue": "https://github.com/metrum-ai/router/issues/430",
                    "approved": "2026-11-02",
                }
            ]
        )
        failures += case(work, "red cell with documented exception", [red], documented, 0)

        stale = exceptions(
            [
                {
                    "cell": "pi/same-dialect/depth-0",
                    "reason": "kept long after the fix landed",
                    "issue": "https://github.com/metrum-ai/router/issues/430",
                    "approved": "2026-11-02",
                }
            ]
        )
        failures += case(work, "stale exception for a passing cell", [green], stale, 1)

        unknown = exceptions(
            [
                {
                    "cell": "codex/anthropic-to-openai-chat/depth-5",
                    "reason": "cell not in this report",
                    "issue": "https://github.com/metrum-ai/router/issues/430",
                    "approved": "2026-11-02",
                }
            ]
        )
        failures += case(work, "exception for a cell that did not run", [green], unknown, 1)

        bad_issue = exceptions(
            [
                {
                    "cell": "pi/same-dialect/depth-1",
                    "reason": "no link",
                    "issue": "https://example.com/issues/1",
                    "approved": "2026-11-02",
                }
            ]
        )
        failures += case(work, "exception without a repository issue", [red], bad_issue, 2)

        missing_reason = exceptions(
            [
                {
                    "cell": "pi/same-dialect/depth-1",
                    "reason": "",
                    "issue": "https://github.com/metrum-ai/router/issues/430",
                    "approved": "2026-11-02",
                }
            ]
        )
        failures += case(work, "exception without a reason", [red], missing_reason, 2)

        bad_date = exceptions(
            [
                {
                    "cell": "pi/same-dialect/depth-1",
                    "reason": "approved",
                    "issue": "https://github.com/metrum-ai/router/issues/430",
                    "approved": "2026-1-2",
                }
            ]
        )
        failures += case(work, "exception without an ISO approval date", [red], bad_date, 2)

        failures += case(work, "missing exception file", [green], None, 2)
        failures += case(
            work,
            "report with hosted calls recorded",
            [dict(green, hosted_model_calls=3)],
            empty_exceptions,
            2,
        )
        failures += case(
            work,
            "report with an unknown status",
            [report([("pi", "same-dialect", 0, "skipped")])],
            empty_exceptions,
            2,
        )
        failures += case(
            work,
            "report with a duplicate cell",
            [report([("pi", "same-dialect", 0, "pass"), ("pi", "same-dialect", 0, "pass")])],
            empty_exceptions,
            2,
        )
        failures += case(
            work,
            "report with no cells",
            [{"schema": SCHEMA, "hosted_model_calls": 0, "results": []}],
            empty_exceptions,
            2,
        )
        failures += case(
            work,
            "report with a hostile cell name",
            [report([("pi$(id)", "same-dialect", 0, "pass")])],
            empty_exceptions,
            2,
        )
        failures += case(
            work,
            "two suites, one red",
            [green, report([("codex", "same-dialect", 2, "fail")])],
            empty_exceptions,
            1,
        )

    if failures:
        print(f"{failures} hierarchy release gate test(s) failed")
        return 1
    print("ok: hierarchy release gate accepts a green or documented matrix and rejects the rest")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
