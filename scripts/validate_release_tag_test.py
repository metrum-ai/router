#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Offline tests for scripts/validate_release_tag.py.

Accepts stable, prerelease, and build-metadata release tags; rejects shell
metacharacters, whitespace/newlines, ref traversal, and malformed versions so
workflow_dispatch input can never become shell source in the release workflow.
"""

from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "validate_release_tag.py"

_spec = importlib.util.spec_from_file_location("validate_release_tag", SCRIPT)
_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)
validate_release_tag = _mod.validate_release_tag
ReleaseTagError = _mod.ReleaseTagError

VALID = [
    "v0.0.0",
    "v1.2.3",
    "v4.3.0",
    "v10.20.30",
    "v2.0.0-rc.1",
    "v2.0.0-alpha",
    "v2.0.0-alpha.1.2",
    "v2.0.0+build.5",
    "v2.0.0-rc.1+build.5",
    "v" + "1" * 120 + ".0.0".rjust(128 - 121, "0")[: 128 - 1 - 120 + 1],  # long but <= 128
]

INVALID = [
    "",
    " ",
    "v1.2",
    "1.2.3",
    "v01.2.3",
    "v1.2.3 ",
    " v1.2.3",
    "v1.2.3\n",
    "v1.2.3\r\nmalicious",
    "v1.2.3;rm -rf /",
    "v1.2.3$(id)",
    "v1.2.3`id`",
    "v1.2.3|cat",
    "v1.2.3&&id",
    "v1.2.3>out",
    "v1.2.3'\"",
    "v1.2.3/../../etc/passwd",
    "v1.2.3~1",
    "v1.2.3^{commit}",
    "v1.2.3*",
    "v1.2.3?",
    "v1.2.3[0]",
    "v1.2.3:x",
    "v1.2.3\\x",
    "v1.2.3\ttab",
    "v1.2.3é",
    "-v1.2.3",
    "--upload-pack=evil v1.2.3",
    "v1.2.3-",
    "v1.2.3-01",
    "v1.2.3+",
    "v" + "1" * 200,
    "HEAD",
    "refs/tags/v1.2.3",
]


def run(candidate: str) -> tuple[int, str, str]:
    proc = subprocess.run(
        [sys.executable, str(SCRIPT), candidate],
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode, proc.stdout.strip(), proc.stderr.strip()


def main() -> int:
    failures = 0
    for tag in VALID:
        code, out, err = run(tag)
        if code != 0 or out != tag:
            print(f"FAIL valid tag rejected: {tag!r} (exit {code}, stderr {err!r})")
            failures += 1
    for tag in INVALID:
        code, out, err = run(tag)
        if code == 0:
            print(f"FAIL invalid tag accepted: {tag!r} (stdout {out!r})")
            failures += 1
        if out and out == tag:
            print(f"FAIL invalid tag echoed on stdout: {tag!r}")
            failures += 1
    # Newlines must never survive: a crafted value must produce a single-line
    # error on stderr and nothing on stdout.
    code, out, _ = run("v1.2.3\nid")
    if code == 0 or out:
        print("FAIL newline payload produced stdout or success")
        failures += 1
    # NUL bytes cannot be passed through argv; exercise them in-process.
    for tag in ["v1.2.3\x00nul", "\x00", 42, None]:
        try:
            validate_release_tag(tag)
        except ReleaseTagError:
            continue
        print(f"FAIL in-process validator accepted {tag!r}")
        failures += 1
    if failures:
        print(f"{failures} release-tag validation test(s) failed")
        return 1
    print(f"ok: {len(VALID)} valid tags accepted, {len(INVALID)} invalid tags rejected")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
