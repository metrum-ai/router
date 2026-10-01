#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate one complete release package set and write checksum inventory evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

import canonical_product as product


ROOT = Path(__file__).resolve().parents[1]
# Keep a literal canonical slug so contract scanners can verify packaging scripts.
CANONICAL_ARCHIVE_PREFIX = "metrum-ai-router"
assert CANONICAL_ARCHIVE_PREFIX == product.PRODUCT_SLUG
PACKAGE_RE = product.RELEASE_ARCHIVE_RE
REQUIRED_FULL = {
    ("binary", "amd64"),
    ("binary", "arm64"),
    ("docker", "amd64"),
    ("docker", "arm64"),
}
REQUIRED_BINARY = {
    ("binary", "amd64"),
    ("binary", "arm64"),
}
REQUIRED_SETS = {
    "full": REQUIRED_FULL,
    "binary": REQUIRED_BINARY,
}
COMMIT_RE = re.compile(r"^[0-9a-f]{7,64}$")
BUILD_DATE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_package(path: Path) -> tuple[str, str, str]:
    match = PACKAGE_RE.fullmatch(path.name)
    if not match:
        raise ValueError(f"unexpected release artifact filename: {path.name}")
    kind = "docker" if match.group("docker") else "binary"
    return match.group("version"), kind, match.group("arch")


def select_complete_set(
    dist_dir: Path,
    version: str,
    required: set[tuple[str, str]] | None = None,
) -> list[tuple[Path, str, str]]:
    required_keys = required if required is not None else REQUIRED_FULL
    selected: dict[tuple[str, str], Path] = {}
    errors: list[str] = []
    for path in sorted(dist_dir.glob(f"{product.PKG_NAME}-*.tar.gz")):
        try:
            found_version, kind, arch = parse_package(path)
        except ValueError as exc:
            errors.append(str(exc))
            continue
        if found_version != version:
            continue
        key = (kind, arch)
        if key not in required_keys:
            continue
        if key in selected:
            errors.append(f"duplicate {kind} linux-{arch} artifact for {version}")
        selected[key] = path
    missing = required_keys - set(selected)
    if missing:
        errors.append("missing artifacts: " + ", ".join(f"{kind} linux-{arch}" for kind, arch in sorted(missing)))
    if errors:
        raise ValueError("; ".join(errors))
    return [(selected[key], key[0], key[1]) for key in sorted(required_keys)]


def atomic_write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as stream:
        stream.write(content)
        temp_path = Path(stream.name)
    os.replace(temp_path, path)


def validate_metadata(version: str, commit: str, build_date: str) -> None:
    if not version or version.endswith("-dirty") or ".." in version or "/" in version:
        raise ValueError("version must be a non-dirty path-safe release identifier")
    if not COMMIT_RE.fullmatch(commit):
        raise ValueError("commit must be a 7-64 character lowercase hexadecimal commit ID")
    if not BUILD_DATE_RE.fullmatch(build_date):
        raise ValueError("build date must use UTC YYYY-MM-DDTHH:MM:SSZ format")
    try:
        parsed = dt.datetime.strptime(build_date, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError as exc:
        raise ValueError("build date is not a valid UTC timestamp") from exc
    if parsed.strftime("%Y-%m-%dT%H:%M:%SZ") != build_date:
        raise ValueError("build date is not canonical")


def run_content_validator(paths: list[Path], allowlist: Path) -> None:
    subprocess.run(
        [
            sys.executable,
            str(ROOT / "scripts/validate_package_contents.py"),
            "--allowlist",
            str(allowlist),
            *[str(path) for path in paths],
        ],
        cwd=ROOT,
        check=True,
    )


def create_inventory(
    dist_dir: Path,
    version: str,
    commit: str,
    build_date: str,
    allowlist: Path,
    release_url: str | None,
    artifact_set: str = "full",
) -> tuple[dict[str, object], str]:
    validate_metadata(version, commit, build_date)
    try:
        required = REQUIRED_SETS[artifact_set]
    except KeyError as exc:
        raise ValueError(f"unknown artifact set: {artifact_set}") from exc
    selected = select_complete_set(dist_dir, version, required=required)
    run_content_validator([path for path, _, _ in selected], allowlist)
    artifacts: list[dict[str, object]] = []
    checksum_lines: list[str] = []
    for path, kind, arch in selected:
        digest = sha256(path)
        checksum_lines.append(f"{digest}  {path.name}")
        item: dict[str, object] = {
            "filename": path.name,
            "size_bytes": path.stat().st_size,
            "sha256": digest,
            "kind": kind,
            "platform": f"linux/{arch}",
            "package_content_validation": "passed",
        }
        if release_url:
            item["release_url"] = release_url.rstrip("/") + "/" + path.name
        artifacts.append(item)
    if artifact_set == "binary":
        build_commands = [
            f"VERSION={version} COMMIT={commit} BUILD_DATE={build_date} make package-all",
            (
                f"VERSION={version} COMMIT={commit} BUILD_DATE={build_date} "
                "make release-artifact-inventory RELEASE_ARTIFACT_SET=binary"
            ),
        ]
    else:
        build_commands = [
            f"VERSION={version} COMMIT={commit} BUILD_DATE={build_date} make package-all package-docker-all",
            f"VERSION={version} COMMIT={commit} BUILD_DATE={build_date} make release-artifact-inventory",
        ]
    inventory: dict[str, object] = {
        "schema": f"{product.PRODUCT_SLUG}.release-artifact-inventory/v1",
        "version": version,
        "commit": commit,
        "build_date": build_date,
        "artifact_set": artifact_set,
        "reproducible_commands": build_commands,
        "artifacts": artifacts,
    }
    return inventory, "\n".join(checksum_lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=Path(os.environ.get("DIST_DIR", "dist")))
    parser.add_argument("--version", default=os.environ.get("VERSION", ""))
    parser.add_argument("--commit", default=os.environ.get("COMMIT", ""))
    parser.add_argument("--build-date", default=os.environ.get("BUILD_DATE", ""))
    parser.add_argument(
        "--set",
        dest="artifact_set",
        choices=sorted(REQUIRED_SETS),
        default=os.environ.get("RELEASE_ARTIFACT_SET", "full"),
        help="full = binary+docker (local handoff); binary = GitHub Actions self-hosted release",
    )
    parser.add_argument("--allowlist", type=Path, default=ROOT / "scripts/package_docs_allowlist.txt")
    parser.add_argument("--release-url", help="Optional public asset URL prefix; omit before publication")
    parser.add_argument("--inventory", type=Path)
    parser.add_argument("--checksums", type=Path)
    args = parser.parse_args()

    dist_dir = args.dist_dir if args.dist_dir.is_absolute() else ROOT / args.dist_dir
    inventory_path = args.inventory or dist_dir / "release-artifacts.json"
    checksums_path = args.checksums or dist_dir / "SHA256SUMS"
    try:
        inventory, checksums = create_inventory(
            dist_dir,
            args.version,
            args.commit,
            args.build_date,
            args.allowlist,
            args.release_url,
            artifact_set=args.artifact_set,
        )
    except (ValueError, subprocess.CalledProcessError) as exc:
        print(f"release artifact inventory failed: {exc}", file=sys.stderr)
        return 2
    atomic_write(inventory_path, json.dumps(inventory, indent=2, sort_keys=True) + "\n")
    atomic_write(checksums_path, checksums)
    print(f"release artifact inventory passed: {len(inventory['artifacts'])} artifacts")
    print(f"wrote {inventory_path} and {checksums_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
