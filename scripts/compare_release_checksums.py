# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail unless two release builds produce identical checked package digests."""

from __future__ import annotations

import argparse
import hashlib
import re
import sys
import tarfile
from pathlib import Path
import zipfile


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


def archive_members(path: Path) -> list[dict[str, object]]:
    """Return ordered content and metadata fingerprints without extracting paths."""
    if path.name.endswith(".tar.gz"):
        with tarfile.open(path, "r:gz") as archive:
            return [
                {
                    "name": item.name,
                    "sha256": (
                        hashlib.sha256(archive.extractfile(item).read()).hexdigest()
                        if item.isfile()
                        else ""
                    ),
                    "metadata": (
                        item.type.hex(), item.mtime, item.uid, item.gid, item.mode,
                        item.uname, item.gname, item.linkname,
                        tuple(sorted(item.pax_headers.items())),
                    ),
                }
                for item in archive.getmembers()
            ]
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as archive:
            return [
                {
                    "name": item.filename,
                    "sha256": hashlib.sha256(archive.read(item)).hexdigest() if not item.is_dir() else "",
                    "metadata": (
                        item.date_time, item.external_attr, item.compress_type,
                        item.create_system, item.flag_bits,
                    ),
                }
                for item in archive.infolist()
            ]
    raise ValueError(f"unsupported release archive format: {path.name}")


def compare_archive_members(primary: Path, rebuild: Path) -> dict[str, object]:
    before_members = archive_members(primary)
    after_members = archive_members(rebuild)
    before = {member["name"]: member for member in before_members}
    after = {member["name"]: member for member in after_members}
    return {
        "changed_content": sorted(
            member
            for member in before.keys() & after.keys()
            if before[member]["sha256"] != after[member]["sha256"]
        ),
        "changed_metadata": sorted(
            member
            for member in before.keys() & after.keys()
            if before[member]["metadata"] != after[member]["metadata"]
        ),
        "missing": sorted(set(before) - set(after)),
        "added": sorted(set(after) - set(before)),
        "order_changed": [member["name"] for member in before_members]
        != [member["name"] for member in after_members],
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("primary", type=Path)
    parser.add_argument("rebuild", type=Path)
    parser.add_argument("--primary-dist", type=Path, help="directory containing primary archives")
    parser.add_argument("--rebuild-dist", type=Path, help="directory containing rebuilt archives")
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
        if args.primary_dist is not None and args.rebuild_dist is not None:
            for name in changed:
                try:
                    member_report = compare_archive_members(
                        args.primary_dist / name,
                        args.rebuild_dist / name,
                    )
                except (OSError, tarfile.TarError, zipfile.BadZipFile, ValueError) as exc:
                    print(f"archive member comparison failed for {name}: {exc}", file=sys.stderr)
                    continue
                labels = [
                    f"{label}=" + "|".join(member_report[label])
                    for label in ("changed_content", "changed_metadata", "missing", "added")
                    if member_report[label]
                ]
                labels.append("order=" + ("changed" if member_report["order_changed"] else "same"))
                has_member_diff = any(
                    member_report[label]
                    for label in ("changed_content", "changed_metadata", "missing", "added")
                ) or member_report["order_changed"]
                if not has_member_diff:
                    labels.append("members_identical; container_bytes_differ")
                print(
                    f"archive members {name}: " + ", ".join(labels),
                    file=sys.stderr,
                )
        return 1
    print(f"release rebuild matches {len(primary)} package checksums")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
