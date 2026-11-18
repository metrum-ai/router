# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Stage four unsigned binary archives and their archive-only release evidence."""

from __future__ import annotations

import argparse
import shutil
import sys
from pathlib import Path

ARCHIVE_PATTERNS = (
    "metrum-ai-router-{version}-linux-amd64.tar.gz",
    "metrum-ai-router-{version}-linux-arm64.tar.gz",
    "metrum-ai-router-{version}-windows-amd64.zip",
    "metrum-ai-router-{version}-windows-arm64.zip",
)
UNSIGNED_PAYLOAD_PATTERNS = ARCHIVE_PATTERNS + (
    "SHA256SUMS",
    "release-artifacts.json",
    "upgrade-rollback-results.json",
)


def archive_sbom_names(version: str) -> set[str]:
    return {
        pattern.format(version=version).removesuffix(".tar.gz").removesuffix(".zip") + ".spdx.json"
        for pattern in ARCHIVE_PATTERNS
    }


def payload_names(version: str, include_upgrade_results: bool = True) -> tuple[str, ...]:
    names = tuple(pattern.format(version=version) for pattern in UNSIGNED_PAYLOAD_PATTERNS)
    if not include_upgrade_results:
        names = tuple(name for name in names if name != "upgrade-rollback-results.json")
    return names


def stage(source: Path, destination: Path, version: str, include_upgrade_results: bool = True) -> tuple[str, ...]:
    names = payload_names(version, include_upgrade_results)
    if not source.is_dir():
        raise ValueError(f"source payload directory is missing: {source}")
    if source.resolve() == destination.resolve() or source.resolve() in destination.resolve().parents:
        raise ValueError("staging destination must not be inside the source directory")
    destination.mkdir(parents=True, exist_ok=True)
    if any(destination.iterdir()):
        raise ValueError(f"staging destination is not empty: {destination}")
    for name in names:
        source_file = source / name
        if source_file.is_symlink() or not source_file.is_file():
            raise ValueError(f"required release payload file is missing or not a regular file: {name}")
    for name in names:
        shutil.copy2(source / name, destination / name)
    return names


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path("dist"))
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--without-upgrade-results", action="store_true")
    args = parser.parse_args()
    try:
        names = stage(args.source, args.destination, args.version, not args.without_upgrade_results)
    except (OSError, ValueError) as exc:
        print(f"release payload staging failed: {exc}", file=sys.stderr)
        return 1
    print("staged unsigned release payload: " + ", ".join(names))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
