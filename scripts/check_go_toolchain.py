#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Fail loudly if a build is using an obsolete Go patch floor."""
import re
import subprocess
import sys

MINIMUM = (1, 27, 3)


def supported(version):
    match = re.fullmatch(r"go(\d+)\.(\d+)\.(\d+)", version.strip())
    return match is not None and tuple(map(int, match.groups())) >= MINIMUM


def main():
    output = subprocess.check_output(["go", "version"], text=True).strip()
    print(output)
    fields = output.split()
    version = fields[2] if len(fields) >= 3 and fields[:2] == ["go", "version"] else "unknown"
    if not supported(version):
        print(f"Go patch floor violation: {version}; require stable Go >= 1.27.3", file=sys.stderr)
        return 1
    print(f"Go patch floor verified: {version} (minimum 1.27.3)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
