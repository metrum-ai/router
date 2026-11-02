#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Decide whether the router hierarchy compatibility matrix may release.

The release workflow runs the local mock chain matrices (agent clients and the
sandboxed Codex Responses chain) and then calls this script. It reads the
per-cell JSON reports, prints one row per cell, and exits non-zero when any cell
failed and is not covered by an explicitly documented exception.

The matrix exit status of the runners is deliberately not trusted here: a
runner that crashes still writes partial results, so this script requires a
well-formed report for every expected suite and treats a missing report as a
failure rather than an empty matrix.

Exceptions live in tests/agent_compat/release-exceptions.json and are reviewed
like code. An exception must name the exact cell, a reason, an issue URL on the
metrum-ai/router repository, and an approval date. An exception for a cell that
already passes is stale and fails the gate so the file cannot silently rot.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from urllib.parse import urlparse

SCHEMA = "router-agent-chain-smoke-v1"
EXCEPTIONS_SCHEMA = "router-hierarchy-release-exceptions-v1"
STATUSES = {"pass", "fail"}
CLIENT = re.compile(r"[a-z][a-z0-9-]*\Z")
PATH_NAME = re.compile(r"[a-z][a-z0-9-]*\Z")
DATE = re.compile(r"\d{4}-\d{2}-\d{2}\Z")


class GateError(Exception):
    """A report, exception file, or cell that cannot be evaluated."""


def cell_id(client: str, path: str, depth: int) -> str:
    return f"{client}/{path}/depth-{depth}"


def load_json(path: Path) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise GateError(f"{path}: cannot read report: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise GateError(f"{path}: invalid JSON: {exc}") from exc


def report_cells(path: Path) -> dict[str, str]:
    """Return cell id -> status for one matrix report, validating its shape."""
    report = load_json(path)
    if not isinstance(report, dict):
        raise GateError(f"{path}: report must be an object")
    if report.get("schema") != SCHEMA:
        raise GateError(f"{path}: unsupported report schema {report.get('schema')!r}")
    if report.get("hosted_model_calls") not in (0, None):
        raise GateError(f"{path}: matrix report must record hosted_model_calls=0")
    results = report.get("results")
    if not isinstance(results, list) or not results:
        raise GateError(f"{path}: report contains no cells")
    cells: dict[str, str] = {}
    for index, cell in enumerate(results):
        if not isinstance(cell, dict):
            raise GateError(f"{path}: cell {index} must be an object")
        client, path_name, depth, status = (
            cell.get("client"),
            cell.get("path"),
            cell.get("depth"),
            cell.get("status"),
        )
        if not isinstance(client, str) or not CLIENT.fullmatch(client):
            raise GateError(f"{path}: cell {index} has an invalid client")
        if not isinstance(path_name, str) or not PATH_NAME.fullmatch(path_name):
            raise GateError(f"{path}: cell {index} has an invalid path")
        if not isinstance(depth, int) or isinstance(depth, bool) or depth < 0 or depth > 32:
            raise GateError(f"{path}: cell {index} has an invalid depth")
        if status not in STATUSES:
            raise GateError(f"{path}: cell {index} has an invalid status")
        name = cell_id(client, path_name, depth)
        if name in cells:
            raise GateError(f"{path}: duplicate cell {name}")
        cells[name] = status
    return cells


def load_exceptions(path: Path) -> dict[str, dict[str, str]]:
    """Return cell -> exception entry, validating every documented exception."""
    if not path.exists():
        raise GateError(f"{path}: exception file is missing")
    document = load_json(path)
    if not isinstance(document, dict) or set(document) != {"schema", "exceptions"}:
        raise GateError(f"{path}: exception file must contain only schema and exceptions")
    if document["schema"] != EXCEPTIONS_SCHEMA:
        raise GateError(f"{path}: unsupported exception schema {document['schema']!r}")
    entries = document["exceptions"]
    if not isinstance(entries, list):
        raise GateError(f"{path}: exceptions must be a list")
    exceptions: dict[str, dict[str, str]] = {}
    for index, entry in enumerate(entries):
        if not isinstance(entry, dict) or set(entry) != {"cell", "reason", "issue", "approved"}:
            raise GateError(f"{path}: exception {index} must have exactly cell, reason, issue, approved")
        cell, reason, issue, approved = (entry[k] for k in ("cell", "reason", "issue", "approved"))
        if not isinstance(cell, str) or not re.fullmatch(r"[a-z][a-z0-9-]*/[a-z][a-z0-9-]*/depth-[0-9]+", cell):
            raise GateError(f"{path}: exception {index} has an invalid cell id")
        if not isinstance(reason, str) or not reason.strip() or len(reason) > 500:
            raise GateError(f"{path}: exception {index} needs a reason of at most 500 characters")
        if not isinstance(issue, str):
            raise GateError(f"{path}: exception {index} needs an issue URL")
        url = urlparse(issue)
        if url.scheme != "https" or url.netloc != "github.com" or not re.fullmatch(
            r"/[^/]+/[^/]+/issues/[1-9][0-9]*", url.path
        ) or url.query or url.fragment:
            raise GateError(f"{path}: exception {index} needs an https://github.com/<owner>/<repo>/issues/<n> URL")
        if not isinstance(approved, str) or not DATE.fullmatch(approved):
            raise GateError(f"{path}: exception {index} needs an approved date (YYYY-MM-DD)")
        if cell in exceptions:
            raise GateError(f"{path}: duplicate exception for {cell}")
        exceptions[cell] = entry
    return exceptions


def evaluate(reports: dict[Path, dict[str, str]], exceptions: dict[str, dict[str, str]]) -> tuple[list[str], list[str]]:
    """Return (rows, failures). A row is one rendered cell line."""
    rows: list[str] = []
    failures: list[str] = []
    seen: set[str] = set()
    for path, cells in reports.items():
        for name in sorted(cells, key=lambda n: (n.split("/")[0], int(n.rsplit("-", 1)[1]), n)):
            status = cells[name]
            seen.add(name)
            if status == "pass":
                note = ""
                if name in exceptions:
                    note = " stale exception (cell passes; remove it)"
                    failures.append(f"stale exception for {name}: the cell passes, remove it from the exception file")
                rows.append(f"{status:4} {name}{note}")
                continue
            entry = exceptions.get(name)
            if entry is None:
                rows.append(f"{status:4} {name}  uncovered failure")
                failures.append(f"failed cell without a documented exception: {name}")
                continue
            rows.append(f"{status:4} {name}  exception {entry['issue']} approved {entry['approved']}")
    for name in sorted(text for text in exceptions if text not in seen):
        rows.append(f"{'n/a':4} {name}  exception names a cell that did not run")
        failures.append(f"exception for {name} does not match a cell in any report")
    return rows, failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path, action="append", required=True, help="per-suite matrix report (repeatable)")
    parser.add_argument(
        "--exceptions",
        type=Path,
        default=Path("tests/agent_compat/release-exceptions.json"),
        help="documented, reviewed exceptions",
    )
    args = parser.parse_args()
    try:
        reports = {path: report_cells(path) for path in args.report}
        exceptions = load_exceptions(args.exceptions)
        rows, failures = evaluate(reports, exceptions)
    except GateError as exc:
        print(f"hierarchy gate error: {exc}", file=sys.stderr)
        return 2
    total = sum(len(cells) for cells in reports.values())
    passed = sum(status == "pass" for cells in reports.values() for status in cells.values())
    for row in rows:
        print(row)
    print(f"\nhierarchy matrix: {passed}/{total} cells pass, {len(failures)} problem(s)")
    if failures:
        for failure in failures:
            print(f"FAIL: {failure}", file=sys.stderr)
        return 1
    print("hierarchy matrix gate: PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
