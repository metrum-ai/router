#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Create deterministic Windows release ZIPs with Unix executable modes."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import stat
import time
import zipfile


def zip_timestamp() -> tuple[int, int, int, int, int, int]:
    epoch = os.environ.get("SOURCE_DATE_EPOCH")
    if epoch is None:
        return 1980, 1, 1, 0, 0, 0
    timestamp = time.gmtime(int(epoch))
    if timestamp.tm_year < 1980:
        return 1980, 1, 1, 0, 0, 0
    if timestamp.tm_year > 2107:
        return 2107, 12, 31, 23, 59, 58
    return timestamp.tm_year, timestamp.tm_mon, timestamp.tm_mday, timestamp.tm_hour, timestamp.tm_min, timestamp.tm_sec // 2 * 2


def make_release_zip(source_dir: Path, output: Path) -> None:
    source_dir = source_dir.resolve()
    if not source_dir.is_dir():
        raise ValueError(f"package directory does not exist: {source_dir}")
    timestamp = zip_timestamp()
    with zipfile.ZipFile(output, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as package:
        paths = [source_dir, *source_dir.rglob("*")]
        for path in sorted(paths, key=lambda item: item.relative_to(source_dir.parent).as_posix()):
            relative = path.relative_to(source_dir.parent).as_posix()
            is_dir = path.is_dir()
            entry_name = relative + ("/" if is_dir and not relative.endswith("/") else "")
            info = zipfile.ZipInfo(entry_name, date_time=timestamp)
            info.create_system = 3
            if is_dir:
                info.compress_type = zipfile.ZIP_STORED
                info.external_attr = (stat.S_IFDIR | 0o755) << 16 | 0x10
                package.writestr(info, b"")
                continue
            mode = 0o755 if path.parent.name == "bin" and path.suffix == ".exe" else 0o644
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = (stat.S_IFREG | mode) << 16
            package.writestr(info, path.read_bytes())


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source_dir", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    make_release_zip(args.source_dir, args.output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
