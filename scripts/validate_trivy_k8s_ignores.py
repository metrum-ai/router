#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Reject broad, unexplained Trivy suppressions for rendered manifests."""
import json
from pathlib import Path
import re
import sys


IGNORE = Path(__file__).resolve().parents[1] / ".github/trivy-k8s-ignore.yaml"


def validate(text: str) -> None:
    # JSON is a YAML subset. Leading copyright/comments are allowed; no YAML
    # parser is needed in CI, and every permitted field is checked below.
    data = json.loads("\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#")))
    if not isinstance(data, dict) or set(data) != {"misconfigurations"} or not isinstance(data["misconfigurations"], list):
        raise ValueError("only a misconfigurations list is permitted")
    for entry in data["misconfigurations"]:
        if not isinstance(entry, dict) or not {"id", "paths", "statement"} <= set(entry) or set(entry) - {"id", "paths", "statement", "expired_at"}:
            raise ValueError("ignore must specify only id, paths, statement and optional expired_at")
        if not isinstance(entry["id"], str) or not re.fullmatch(r"AVD-(?:KSV|DS)-[0-9]{4}", entry["id"]):
            raise ValueError("ignore must identify a Kubernetes or Dockerfile rule")
        if not isinstance(entry["statement"], str) or not entry["statement"].strip():
            raise ValueError("ignore must have a reason")
        paths = entry["paths"]
        if not isinstance(paths, list) or not paths:
            raise ValueError("ignore must specify exact rendered files")
        if any(not isinstance(path, str) or not re.fullmatch(r"(?:[a-z0-9][a-z0-9_.-]*\.yaml|Dockerfile)", path) or ".." in path for path in paths):
            raise ValueError("ignore paths must be exact rendered filenames, not globs or directories")
        if "expired_at" in entry and (not isinstance(entry["expired_at"], str) or not re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}", entry["expired_at"])):
            raise ValueError("invalid expiry date")


def main() -> int:
    try:
        validate(IGNORE.read_text(encoding="utf-8"))
    except (ValueError, OSError) as exc:
        print(f"invalid rendered-manifest ignore file: {exc}", file=sys.stderr)
        return 1
    print("Rendered-manifest ignores are scoped and explained")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
