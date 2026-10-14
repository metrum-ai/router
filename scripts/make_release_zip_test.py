#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Self-test deterministic release ZIP creation and file permissions."""

from __future__ import annotations

import os
from pathlib import Path
import stat
import tempfile
import zipfile

import make_release_zip


def main() -> int:
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        source = root / "metrum-ai-router-v1.0.0-windows-amd64"
        (source / "bin").mkdir(parents=True)
        (source / "config").mkdir()
        (source / "bin/router.exe").write_bytes(b"exe")
        (source / "config/settings.yaml").write_text("server: {}\n", encoding="utf-8")
        first = root / "first.zip"
        second = root / "second.zip"
        previous_epoch = os.environ.get("SOURCE_DATE_EPOCH")
        os.environ["SOURCE_DATE_EPOCH"] = "1700000001"
        try:
            make_release_zip.make_release_zip(source, first)
            make_release_zip.make_release_zip(source, second)
        finally:
            if previous_epoch is None:
                os.environ.pop("SOURCE_DATE_EPOCH", None)
            else:
                os.environ["SOURCE_DATE_EPOCH"] = previous_epoch
        if first.read_bytes() != second.read_bytes():
            raise AssertionError("identical inputs did not produce identical ZIP bytes")
        with zipfile.ZipFile(first) as package:
            entries = package.infolist()
            names = [entry.filename for entry in entries]
            if names != sorted(names):
                raise AssertionError(f"ZIP entries are not sorted: {names!r}")
            if any(entry.date_time != (2023, 11, 14, 22, 13, 20) for entry in entries):
                raise AssertionError(f"ZIP entries do not use SOURCE_DATE_EPOCH timestamp: {entries!r}")
            modes = {entry.filename: (entry.external_attr >> 16) & 0o777 for entry in entries}
            if modes["metrum-ai-router-v1.0.0-windows-amd64/bin/router.exe"] != 0o755:
                raise AssertionError(f"executable mode is wrong: {modes!r}")
            if modes["metrum-ai-router-v1.0.0-windows-amd64/config/settings.yaml"] != 0o644:
                raise AssertionError(f"data mode is wrong: {modes!r}")
            if stat.S_IFMT((entries[0].external_attr >> 16) & 0xFFFF) != stat.S_IFDIR:
                raise AssertionError("top-level directory entry is missing its directory type")
    print("release ZIP self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
