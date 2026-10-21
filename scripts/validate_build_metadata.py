#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate release/build metadata before shell recipes consume it."""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path


RULES: dict[str, re.Pattern[str]] = {
    "VERSION": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._+/-]{0,127}$"),
    "COMMIT": re.compile(r"^(?:unknown|[A-Fa-f0-9]{7,64})$"),
    "BUILD_DATE": re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$"),
    "GOOS": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$"),
    "GOARCH": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$"),
    "PKG_NAME": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$"),
    "DIST_DIR": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$"),
    "IMAGE_NAME": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$"),
    "IMAGE_TAG": re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$"),
}


def main() -> int:
    errors: list[str] = []
    makefile = Path(__file__).resolve().parents[1] / "Makefile"
    for line in makefile.read_text().splitlines():
        if "go build" in line and ("./cmd/" in line):
            if '$(BUILD_LDFLAGS)' not in line or '-buildvcs=false' not in line:
                errors.append("Makefile go build must use BUILD_LDFLAGS and -buildvcs=false")
    for name, pattern in RULES.items():
        value = os.environ.get(name, "")
        if not value:
            errors.append(f"{name} is empty")
            continue
        if value.startswith("/") or ".." in value.split("/"):
            errors.append(f"{name} contains an unsafe path component")
            continue
        if not pattern.fullmatch(value):
            errors.append(f"{name} contains unsupported characters")

    if errors:
        print("build metadata validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 2

    print("build metadata validation passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
