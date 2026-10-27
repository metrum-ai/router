#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline regression for scripts/router_test_time_guard.py (#367)."""
from __future__ import annotations

import io
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GUARD = ROOT / "scripts" / "router_test_time_guard.py"
PKG = "github.com/metrum-ai/router/internal/router"


def run_guard(events: list[dict], env: dict[str, str] | None = None) -> tuple[int, str]:
    payload = "".join(json.dumps(e) + "\n" for e in events)
    proc = subprocess.run(
        [sys.executable, str(GUARD)],
        input=payload,
        text=True,
        capture_output=True,
        env=None if env is None else {**__import__("os").environ, **env},
        check=False,
    )
    return proc.returncode, proc.stdout


def require(ok: bool, message: str) -> None:
    if not ok:
        raise AssertionError(message)


def main() -> int:
    # Under budget: exit 0, lines pass through, no warning.
    code, out = run_guard([
        {"Action": "output", "Package": PKG, "Test": "TestExample", "Output": "hello\n"},
        {"Action": "pass", "Package": PKG, "Test": "TestExample", "Elapsed": 1.0},
        {"Action": "pass", "Package": PKG, "Elapsed": 120.0},
    ])
    require(code == 0, f"under-budget run must pass, got {code}: {out}")
    require('"Output":"hello\\n"' in out.replace(" ", ""), "guard must pass every go test -json line through verbatim")
    require("::warning::" not in out, "under-budget run must not warn")
    require("120.0s" in out, "summary must report the package elapsed time")

    # Over warn but under fail: exit 0 with a warning annotation.
    code, out = run_guard(
        [{"Action": "pass", "Package": PKG, "Elapsed": 400.0}],
        {"ROUTER_TEST_GUARD_WARN_SECONDS": "360", "ROUTER_TEST_GUARD_FAIL_SECONDS": "480"},
    )
    require(code == 0, f"warn-band run must pass, got {code}")
    require("::warning::" in out and "400.0s" in out, "warn band must annotate")

    # Over fail: exit 1 with an error annotation.
    code, out = run_guard(
        [{"Action": "pass", "Package": PKG, "Elapsed": 500.0}],
        {"ROUTER_TEST_GUARD_WARN_SECONDS": "360", "ROUTER_TEST_GUARD_FAIL_SECONDS": "480"},
    )
    require(code == 1, f"over-fail run must fail, got {code}")
    require("::error::" in out and "500.0s" in out, "fail band must annotate")

    # Failed package propagates even under the time budget.
    code, out = run_guard([
        {"Action": "fail", "Package": PKG, "Elapsed": 10.0},
    ])
    require(code == 1 and "FAILED" in out, "failed package must fail the guard")

    # A result line for a different package must not drive the verdict; the
    # guard falls back to wall clock only when no package result exists.
    code, out = run_guard(
        [{"Action": "pass", "Package": "example.com/other", "Elapsed": 99999.0}],
        {"ROUTER_TEST_GUARD_WARN_SECONDS": "1e9", "ROUTER_TEST_GUARD_FAIL_SECONDS": "1e10"},
    )
    require(code == 0, f"other package results must be ignored, got {code}: {out}")

    print("router test time guard tests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
