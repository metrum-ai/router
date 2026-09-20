#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Require a valid DCO Signed-off-by trailer on every commit in a range."""

from __future__ import annotations

import re
import subprocess
import sys


TRAILER = re.compile(r"(?im)^Signed-off-by:\s+[^<\n]+\s+<[^<>\s]+@[^<>\s]+>\s*$")


def commit_messages(revision_range: str) -> list[tuple[str, str]]:
    # Skip merge commits: the merge queue tip is an unsigned merge that would
    # otherwise fail this check even when every pull-request commit is signed off.
    output = subprocess.check_output(
        ["git", "log", "--no-merges", "--format=%H%x00%B%x00", revision_range],
        text=True,
    )
    fields = output.split("\0")
    return [(fields[index], fields[index + 1]) for index in range(0, len(fields) - 1, 2)]


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check_dco.py <base>..<head>", file=sys.stderr)
        return 2
    commits = commit_messages(sys.argv[1])
    if not commits:
        print("DCO check failed: revision range contains no commits", file=sys.stderr)
        return 1
    missing = [sha[:12] for sha, message in commits if not TRAILER.search(message)]
    if missing:
        print("DCO check failed; commits without a valid Signed-off-by trailer:", file=sys.stderr)
        for sha in missing:
            print(f"- {sha}", file=sys.stderr)
        return 1
    print(f"DCO check passed for {len(commits)} commit(s)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
