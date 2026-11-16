# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Sign Windows executables inside release ZIPs, preserving deterministic packages."""

from __future__ import annotations

import argparse
import hashlib
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile

import make_release_zip


def extract_safe(archive: Path, destination: Path) -> Path:
    with zipfile.ZipFile(archive) as package:
        members = package.infolist()
        if not members:
            raise ValueError(f"Windows release package is empty: {archive.name}")
        roots: set[str] = set()
        for member in members:
            path = PurePosixPath(member.filename)
            if path.is_absolute() or ".." in path.parts or not path.parts:
                raise ValueError("Windows release package contains an unsafe path")
            mode = (member.external_attr >> 16) & 0xFFFF
            if stat.S_ISLNK(mode):
                raise ValueError("Windows release package contains a symbolic link")
            roots.add(path.parts[0])
            target = (destination / Path(*path.parts)).resolve()
            if destination.resolve() not in target.parents and target != destination.resolve():
                raise ValueError("Windows release package contains an unsafe path")
            if member.is_dir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with package.open(member) as source, target.open("wb") as output:
                    shutil.copyfileobj(source, output)
        if len(roots) != 1:
            raise ValueError("Windows release package must have exactly one top-level directory")
        return destination / next(iter(roots))


def binary_hashes(package_dir: Path) -> dict[str, str]:
    return {
        path.name: hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted((package_dir / "bin").glob("*.exe"))
    }


def sign_packages(dist: Path, version: str) -> list[str]:
    changed: list[str] = []
    for arch in ("amd64", "arm64"):
        archive = dist / f"metrum-ai-router-{version}-windows-{arch}.zip"
        if not archive.is_file():
            raise ValueError(f"missing Windows release package: {archive.name}")
        with tempfile.TemporaryDirectory(prefix="router-windows-sign-") as temporary:
            root = Path(temporary)
            package_dir = extract_safe(archive, root / "unpacked")
            before = binary_hashes(package_dir)
            if not before:
                raise ValueError(f"Windows release package has no executables: {archive.name}")
            subprocess.run([sys.executable, str(Path(__file__).with_name("sign_windows_exes.py")), str(package_dir / "bin")], check=True)
            if binary_hashes(package_dir) == before:
                continue
            replacement = root / archive.name
            make_release_zip.make_release_zip(package_dir, replacement)
            archive.write_bytes(replacement.read_bytes())
            changed.append(archive.name)
    return changed


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=Path("dist"))
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        changed = sign_packages(args.dist_dir, args.version)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        print(f"Windows release signing failed: {exc}", file=sys.stderr)
        return 2
    print("Windows packages signed: " + (", ".join(changed) if changed else "no signing change requested"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
