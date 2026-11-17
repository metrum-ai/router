#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Create and verify a GitHub release from an already-published payload."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys


_PRERELEASE_ID = r"(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
_BUILD_ID = r"[0-9A-Za-z-]+"
SEMVER_TAG = re.compile(
    rf"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    rf"(?:-(?P<prerelease>{_PRERELEASE_ID}(?:\.{_PRERELEASE_ID})*))?"
    rf"(?:\+(?:{_BUILD_ID}(?:\.{_BUILD_ID})*))?$"
)


def release_is_prerelease(version: str) -> bool:
    match = SEMVER_TAG.fullmatch(version)
    if match is None:
        raise ValueError("release version must be a strict v-prefixed SemVer tag")
    return match.group("prerelease") is not None


def release_create_args(version: str, title: str, notes_file: str, assets: list[str]) -> list[str]:
    prerelease = release_is_prerelease(version)
    command = [
        "release", "create", version,
        "--verify-tag", "--title", title, "--notes-file", notes_file,
    ]
    if prerelease:
        command.extend(("--prerelease", "--latest=false"))
    else:
        command.append("--latest")
    command.extend(assets)
    return command


def publish_release(version: str, title: str, notes_file: str, assets: list[str]) -> None:
    expected_prerelease = release_is_prerelease(version)
    subprocess.run(["gh", *release_create_args(version, title, notes_file, assets)], check=True)
    result = subprocess.run(
        ["gh", "release", "view", version, "--json", "isPrerelease"],
        check=True,
        capture_output=True,
        text=True,
    )
    release = json.loads(result.stdout)
    actual_prerelease = release.get("isPrerelease")
    if actual_prerelease is not expected_prerelease:
        raise RuntimeError(f"GitHub Release prerelease metadata mismatch for {version}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--title", required=True)
    parser.add_argument("--notes-file", required=True)
    parser.add_argument("assets", nargs="+")
    args = parser.parse_args()
    try:
        publish_release(args.version, args.title, args.notes_file, args.assets)
    except (OSError, ValueError, RuntimeError, subprocess.CalledProcessError, json.JSONDecodeError) as exc:
        print(f"release publication failed: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
