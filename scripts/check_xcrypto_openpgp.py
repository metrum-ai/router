# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Fail when the Go package graph includes x/crypto OpenPGP."""

from __future__ import annotations

import sys


def main() -> int:
    packages = {line.strip() for line in sys.stdin if line.strip()}
    forbidden = sorted(
        package
        for package in packages
        if package == "golang.org/x/crypto/openpgp"
        or package.startswith("golang.org/x/crypto/openpgp/")
    )
    if forbidden:
        print("x/crypto OpenPGP packages remain in the Go dependency graph:", file=sys.stderr)
        print("\n".join(forbidden), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
