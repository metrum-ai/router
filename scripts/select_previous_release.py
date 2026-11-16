# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Select the latest published, non-prerelease GitHub release from JSON stdin."""

from __future__ import annotations

import argparse
import json
import re
import sys
from typing import Any


TAG = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:\+([A-Za-z0-9.-]+))?$")


def select_previous(releases: Any, current: str) -> str:
    if not isinstance(releases, list):
        raise ValueError("release listing must be a JSON array")
    candidates: list[tuple[tuple[int, int, int, str], str]] = []
    current_match = TAG.fullmatch(current)
    current_version = tuple(map(int, current_match.groups()[:3])) if current_match else None
    for release in releases:
        if not isinstance(release, dict):
            continue
        tag = release.get("tagName")
        if (
            isinstance(tag, str)
            and TAG.fullmatch(tag)
            and tag != current
            and release.get("isPrerelease") is False
            and release.get("isDraft") is False
        ):
            match = TAG.fullmatch(tag)
            assert match is not None
            version = tuple(map(int, match.groups()[:3]))
            if current_version is not None and version >= current_version:
                continue
            candidates.append(((version[0], version[1], version[2], tag), tag))
    if not candidates:
        raise ValueError("no previous published non-prerelease release found")
    return max(candidates)[1]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--current", required=True)
    args = parser.parse_args()
    try:
        print(select_previous(json.load(sys.stdin), args.current))
    except (json.JSONDecodeError, ValueError) as exc:
        print(f"release selection failed: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
