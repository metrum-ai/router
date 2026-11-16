# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Derive a safe immutable artifact name for an AI review commit range."""

from __future__ import annotations

import argparse
import hashlib


def artifact_key(base: str, head: str) -> str:
    if not base or not head or "\n" in base or "\n" in head:
        raise ValueError("review range must contain non-empty single-line base and head values")
    return hashlib.sha256(f"{base}..{head}".encode("utf-8")).hexdigest()[:24]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    args = parser.parse_args()
    try:
        print(artifact_key(args.base, args.head))
    except ValueError as exc:
        parser.error(str(exc))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
