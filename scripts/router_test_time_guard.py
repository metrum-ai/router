#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Time guard for the internal/router Go test package (metrum-ai/router#367).

Reads `go test -json` output on stdin and passes every line through so the CI
log keeps full detail. Uses the package-level result line (the pass/fail event
with no Test field) as the authoritative wall time for the package, and also
tracks script wall time as a fallback.

Exits 0 when the package stays under ROUTER_TEST_GUARD_FAIL_SECONDS (default
480 s), 1 when it exceeds it, and emits a ::warning:: annotation when it
exceeds ROUTER_TEST_GUARD_WARN_SECONDS (default 360 s, the #367 budget). A
failed package result propagates as exit 1 as well.
"""

from __future__ import annotations

import json
import os
import sys
import time

PACKAGE = "github.com/metrum-ai/router/internal/router"


def main() -> int:
    warn_seconds = float(os.environ.get("ROUTER_TEST_GUARD_WARN_SECONDS", "360"))
    fail_seconds = float(os.environ.get("ROUTER_TEST_GUARD_FAIL_SECONDS", "480"))
    started = time.monotonic()
    package_elapsed: float | None = None
    package_failed = False
    for line in sys.stdin:
        sys.stdout.write(line)
        sys.stdout.flush()
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict) or event.get("Package") != PACKAGE or "Test" in event:
            continue
        if event.get("Action") == "fail":
            package_failed = True
        if "Elapsed" in event and event.get("Action") in ("pass", "fail"):
            package_elapsed = float(event["Elapsed"])
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
