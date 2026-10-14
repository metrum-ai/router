#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Regression test that release matrix validation discovers Linux and Windows assets."""

from __future__ import annotations

import tempfile
from pathlib import Path

from validate_release_matrix import list_package_archives


def main() -> int:
    with tempfile.TemporaryDirectory() as temporary:
        dist = Path(temporary)
        names = (
            "metrum-ai-router-v1-linux-amd64.tar.gz",
            "metrum-ai-router-v1-windows-amd64.zip",
            "unrelated.zip",
        )
        for name in names:
            (dist / name).touch()
        actual = [path.name for path in list_package_archives(dist)]
        expected = sorted(names[:2])
        if actual != expected:
            raise AssertionError(f"release archive discovery mismatch: {actual} != {expected}")
    print("release matrix artifact selection self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
