# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Select the latest published, non-prerelease GitHub release from JSON stdin."""

from __future__ import annotations

import argparse
import json
import re
import sys
from typing import Any


TAG = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$")


def select_previous(releases: Any, current: str) -> str:
    if not isinstance(releases, list):
        raise ValueError("release listing must be a JSON array")
    candidates = []
    for release in releases:
        if not isinstance(release, dict):
            continue
        tag = release.get("tagName")
        published = release.get("publishedAt")
        if (
            isinstance(tag, str)
            and TAG.fullmatch(tag)
            and tag != current
            and release.get("isPrerelease") is False
            and release.get("isDraft") is False
            and isinstance(published, str)
            and published
        ):
            candidates.append((published, tag))
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
