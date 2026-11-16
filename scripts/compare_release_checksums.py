# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail unless two release builds produce identical checked package digests."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


CHECKSUM = re.compile(r"^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._+-]{0,255})$")


def read_checksums(path: Path) -> dict[str, str]:
    entries: dict[str, str] = {}
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        match = CHECKSUM.fullmatch(line)
        if not match:
            raise ValueError(f"{path}: invalid checksum record on line {line_number}")
        digest, name = match.groups()
        if name in entries:
            raise ValueError(f"{path}: duplicate artifact name {name}")
        entries[name] = digest
    if not entries:
        raise ValueError(f"{path}: no release artifacts listed")
    return entries


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("primary", type=Path)
    parser.add_argument("rebuild", type=Path)
    args = parser.parse_args()
    try:
        primary = read_checksums(args.primary)
        rebuild = read_checksums(args.rebuild)
    except (OSError, ValueError) as exc:
        print(f"release reproducibility check failed: {exc}", file=sys.stderr)
        return 2
    if primary != rebuild:
        missing = sorted(set(primary) - set(rebuild))
        extra = sorted(set(rebuild) - set(primary))
        changed = sorted(name for name in primary.keys() & rebuild.keys() if primary[name] != rebuild[name])
        differences = [
            label + "=" + "|".join(names)
            for label, names in (("missing", missing), ("extra", extra), ("changed", changed))
            if names
        ]
        print("release rebuild differs: " + ", ".join(differences), file=sys.stderr)
        return 1
    print(f"release rebuild matches {len(primary)} package checksums")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
