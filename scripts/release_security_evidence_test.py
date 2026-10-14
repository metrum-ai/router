#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Regression tests for release security evidence fail-closed behavior."""

from __future__ import annotations

import subprocess
import sys
import tempfile
import zipfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]

sys.path.insert(0, str(ROOT / "scripts"))
import release_security_evidence as security


def main() -> int:
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        good_zip = root / "package.zip"
        with zipfile.ZipFile(good_zip, "w") as package:
            package.writestr("release/bin/router.exe", b"binary")
        extracted = root / "extracted"
        extracted.mkdir()
        security.safe_extract(good_zip, extracted)
        if (extracted / "release/bin/router.exe").read_bytes() != b"binary":
            raise AssertionError("Windows zip package was not extracted for scanning")

        bad_zip = root / "unsafe.zip"
        with zipfile.ZipFile(bad_zip, "w") as package:
            package.writestr("../outside", b"unsafe")
            package.writestr("..\\outside", b"unsafe")
        try:
            security.safe_extract(bad_zip, root / "unsafe-extracted")
        except ValueError:
            pass
        else:
            raise AssertionError("unsafe Windows zip path was accepted")

    with tempfile.TemporaryDirectory() as temporary:
        result = subprocess.run(
            [
                sys.executable,
                str(ROOT / "scripts/release_security_evidence.py"),
                "--dist-dir",
                temporary,
                "--version",
                "test-rc",
                "--syft",
                "/must-not-run/syft",
                "--grype",
                "/must-not-run/grype",
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
    if result.returncode != 2 or "missing artifacts" not in result.stderr:
        raise AssertionError(f"incomplete release matrix did not fail closed: {result.returncode} {result.stderr}")
    print("release security evidence self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
