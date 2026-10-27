#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate a release tag before it reaches any release workflow shell step.

The release workflow passes workflow_dispatch input and tag refs through this
validator (via an environment variable, never expression interpolation) so
that only strict semver tags can influence build, inventory, or publication
commands. Prints the validated tag on stdout; exits non-zero on rejection.
"""

from __future__ import annotations

import re
import sys

# Strict semver 2.0.0 with a mandatory leading "v", matching repository tags
# (v4.3.0) and optional prerelease/build metadata. No whitespace, no shell
# metacharacters, no path or ref traversal sequences can survive this pattern.
RELEASE_TAG_RE = re.compile(
    r"^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)"
    r"(-(0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)"
    r"(\.(0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*)?"
    r"(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$"
)

MAX_TAG_LENGTH = 128


class ReleaseTagError(ValueError):
    """Raised when a candidate release tag fails validation."""


def validate_release_tag(candidate: object) -> str:
    if not isinstance(candidate, str):
        raise ReleaseTagError("release tag must be a string")
    tag = candidate
    if tag != tag.strip() or not tag:
        raise ReleaseTagError("release tag must not be empty or padded with whitespace")
    if len(tag) > MAX_TAG_LENGTH:
        raise ReleaseTagError(f"release tag exceeds {MAX_TAG_LENGTH} characters")
    if any(ord(ch) < 0x21 or ord(ch) > 0x7E for ch in tag):
        raise ReleaseTagError("release tag contains control or non-ASCII characters")
    if not RELEASE_TAG_RE.fullmatch(tag):
        raise ReleaseTagError(f"release tag {tag!r} is not a strict v<semver> tag")
    return tag


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: validate_release_tag.py <tag>", file=sys.stderr)
        return 2
    try:
        tag = validate_release_tag(argv[1])
    except ReleaseTagError as exc:
        print(f"invalid release tag: {exc}", file=sys.stderr)
        return 1
    print(tag)
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
