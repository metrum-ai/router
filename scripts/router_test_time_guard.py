#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Time guard for the internal/router Go test package (metrum-ai/router#367).

Reads `go test -json` output on stdin and prints each event's Output field so
the CI log reads like normal `go test` output, plus an `ok <package> <elapsed>`
line at the end. The raw JSON stream can be saved for upload with
`--save-json PATH`.

The package-level result line (the pass/fail event with no Test field) is the
authoritative wall time; the script wall clock is a fallback.

Exits 0 when the package stays under ROUTER_TEST_GUARD_FAIL_SECONDS (default
480 s), 1 when it exceeds it, and emits a ::warning:: annotation when it
exceeds ROUTER_TEST_GUARD_WARN_SECONDS (default 360 s, the #367 budget). A
failed package result propagates as exit 1 as well.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time

PACKAGE = "github.com/metrum-ai/router/internal/router"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--save-json", type=str, default="", help="optional path that receives the raw go test -json stream")
    args = parser.parse_args()
    json_out = open(args.save_json, "w", encoding="utf-8") if args.save_json else None

    warn_seconds = float(os.environ.get("ROUTER_TEST_GUARD_WARN_SECONDS", "360"))
    fail_seconds = float(os.environ.get("ROUTER_TEST_GUARD_FAIL_SECONDS", "480"))
    started = time.monotonic()
    package_elapsed: float | None = None
    package_failed = False
    try:
        for line in sys.stdin:
            if json_out is not None:
                json_out.write(line)
            try:
                event = json.loads(line)
            except ValueError:
                sys.stdout.write(line)
                sys.stdout.flush()
                continue
            if isinstance(event, dict) and event.get("Action") == "output":
                sys.stdout.write(str(event.get("Output", "")))
                sys.stdout.flush()
            if not isinstance(event, dict) or event.get("Package") != PACKAGE or "Test" in event:
                continue
            if event.get("Action") == "fail":
                package_failed = True
            if "Elapsed" in event and event.get("Action") in ("pass", "fail"):
                package_elapsed = float(event["Elapsed"])
                # No synthetic ok/FAIL line: go test already emits it as an
                # Output event, so printing one here would duplicate it.
    finally:
        if json_out is not None:
            json_out.close()

    wall = time.monotonic() - started
    elapsed = package_elapsed if package_elapsed is not None else wall
    source = "go test package result" if package_elapsed is not None else "guard wall clock"
    summary = f"internal/router tests took {elapsed:.1f}s ({source}; budget {warn_seconds:.0f}s, fail at {fail_seconds:.0f}s)"
    if package_failed:
        print(f"::error::{summary} and the package FAILED")
        return 1
    if elapsed > fail_seconds:
        print(f"::error::{summary}")
        return 1
    if elapsed > warn_seconds:
        print(f"::warning::{summary} — exceeds the #367 budget")
    print(summary)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
