#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Compare a pinned compatibility tester's summary with expected failures.

The tester's exit code and its complete summary are both checked: a crash or
partial report must never be confused with an expected compatibility failure.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path
from urllib.parse import urlparse

SUMMARY = re.compile(r"^  \[(PASS|FAIL)\] ([a-z][a-z0-9_]*)(?:: .*)?$")
TOTAL = re.compile(r"^(\d+) of (\d+) suites passed$")
NAME = re.compile(r"[a-z][a-z0-9_]*\Z")


def compare(output: str, exit_code: int, suites: list[str], expected: dict) -> list[str]:
    if not suites or len(set(suites)) != len(suites) or any(not NAME.fullmatch(s) for s in suites):
        raise ValueError("selected suite names must be nonempty and unique")
    if not isinstance(expected, dict):
        raise ValueError("expected failures must be an object")
    for name, entry in expected.items():
        if name not in suites or not isinstance(entry, dict) or set(entry) != {"reason", "issue"}:
            raise ValueError(f"invalid or unselected expected failure: {name}")
        reason, issue = entry["reason"], entry["issue"]
        if not isinstance(reason, str) or not reason.strip() or not isinstance(issue, str):
            raise ValueError(f"missing reason or issue for {name}")
        url = urlparse(issue)
        if url.scheme != "https" or url.netloc != "github.com" or not re.fullmatch(r"/[^/]+/[^/]+/issues/[1-9][0-9]*", url.path) or url.query or url.fragment:
            raise ValueError(f"invalid issue URL for {name}")
    lines = output.splitlines()
    totals = [(i, TOTAL.fullmatch(line)) for i, line in enumerate(lines) if TOTAL.fullmatch(line)]
    if len(totals) != 1:
        raise ValueError("tester must produce exactly one summary")
    index, match = totals[0]
    results = {}
    previous_status = None
    for line in lines[index + 1:]:
        m = SUMMARY.fullmatch(line)
        if not m:
            # SDK errors can contain multi-line response bodies. Only a failed
            # result can own continuation lines; malformed result lines cannot.
            if previous_status == "FAIL" and not line.startswith("  ["):
                continue
            raise ValueError(f"invalid summary line: {line!r}")
        status, name = m.groups()
        previous_status = status
        if name in results:
            raise ValueError(f"duplicate result: {name}")
        results[name] = status
    passed = sum(status == "PASS" for status in results.values())
    if set(results) != set(suites) or int(match[1]) != passed or int(match[2]) != len(suites):
        raise ValueError("summary does not match selected suites or totals")
    if exit_code not in (0, 1) or exit_code != (0 if passed == len(suites) else 1):
        raise ValueError("tester exit code is inconsistent with summary")
    errors = []
    for name in suites:
        failed = results[name] == "FAIL"
        if failed and name not in expected:
            errors.append(f"new failure: {name}")
        if not failed and name in expected:
            errors.append(f"expected failure now passes: {name}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--exit-code", type=int, required=True)
    parser.add_argument("--suites", required=True, help="comma-separated exact suite names")
    parser.add_argument("--expected", type=Path, required=True)
    args = parser.parse_args()
    try:
        errors = compare(args.output.read_text(encoding="utf-8"), args.exit_code,
                         args.suites.split(","), json.loads(args.expected.read_text(encoding="utf-8")))
    except (ValueError, OSError) as exc:
        print(f"invalid compatibility report: {exc}", file=sys.stderr)
        return 2
    for error in errors:
        print(error, file=sys.stderr)
    if not errors:
        print("compatibility results match expected failures")
    return int(bool(errors))


if __name__ == "__main__":
    raise SystemExit(main())
