#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline regression for scripts/router_test_time_guard.py (#367)."""
from __future__ import annotations

import json
import tempfile
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GUARD = ROOT / "scripts" / "router_test_time_guard.py"
PKG = "github.com/metrum-ai/router/internal/router"


def run_guard(events: list[dict], env: dict[str, str] | None = None, save_json: Path | None = None) -> tuple[int, str]:
    payload = "".join(json.dumps(e) + "\n" for e in events)
    argv = [sys.executable, str(GUARD)]
    if save_json is not None:
        argv += ["--save-json", str(save_json)]
    proc = subprocess.run(
        argv,
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
        {"Action": "output", "Package": PKG, "Output": f"ok  \t{PKG}\t120.000s\n"},
        {"Action": "pass", "Package": PKG, "Elapsed": 120.0},
    ])
    require(code == 0, f"under-budget run must pass, got {code}: {out}")
    require("hello\n" in out, "guard must print each event's Output field")
    require('{"Action":"output"' not in out.replace(" ", ""), "raw go test -json lines must not reach the log")
    require(f"ok  \t{PKG}\t120.000s" in out, "go test Output line must reach the log; the guard must not synthesize a duplicate")
    require("::warning::" not in out, "under-budget run must not warn")
    require("120.0s" in out, "summary must report the package elapsed time")

    # --save-json retains the raw stream for upload.
    with tempfile.TemporaryDirectory() as tmp:
        raw = Path(tmp) / "raw.json"
        events = [{"Action": "output", "Package": PKG, "Output": "x\n"}, {"Action": "pass", "Package": PKG, "Elapsed": 5.0}]
        code, out = run_guard(events, save_json=raw)
        require(code == 0, f"save-json run must pass, got {code}")
        lines = [json.loads(line) for line in raw.read_text(encoding="utf-8").splitlines() if line.strip()]
        require(lines == events, "--save-json must retain the raw stream exactly")
        require("x\n" in out, "Output field must still print with --save-json")

    # --save-json to an unwritable path fails fast and loudly.
    code, out = run_guard([{"Action": "pass", "Package": PKG, "Elapsed": 1.0}], save_json=Path("/nonexistent-dir/router_guard.json"))
    require(code != 0, "--save-json to an unwritable path must fail")

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
