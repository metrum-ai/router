#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Unit tests for release artifact inventory selection and metadata."""

from __future__ import annotations

import tempfile
from pathlib import Path

import release_artifact_inventory as inventory


def require_raises(message: str, callback) -> None:
    try:
        callback()
    except ValueError:
        return
    raise AssertionError(message)


def main() -> int:
    inventory.validate_metadata("v1.2.3", "0123456789abcdef", "2026-09-03T12:34:56Z")
    require_raises(
        "dirty version accepted",
        lambda: inventory.validate_metadata("v1.2.3-dirty", "0123456", "2026-09-03T12:34:56Z"),
    )
    require_raises(
        "invalid calendar date accepted",
        lambda: inventory.validate_metadata("v1.2.3", "0123456", "2026-02-30T12:34:56Z"),
    )

    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        names = [
            "metrum-ai-router-v1.2.3-linux-amd64.tar.gz",
            "metrum-ai-router-v1.2.3-linux-arm64.tar.gz",
            "metrum-ai-router-v1.2.3-docker-linux-amd64.tar.gz",
            "metrum-ai-router-v1.2.3-docker-linux-arm64.tar.gz",
        ]
        for name in names:
            (root / name).write_bytes(name.encode("utf-8"))
        selected = inventory.select_complete_set(root, "v1.2.3")
        if len(selected) != 4:
            raise AssertionError(f"expected four artifacts, got {selected}")
        binary_only = inventory.select_complete_set(root, "v1.2.3", required=inventory.REQUIRED_BINARY)
        if len(binary_only) != 2:
            raise AssertionError(f"expected two binary artifacts, got {binary_only}")
        (root / names[-1]).unlink()
        require_raises("incomplete release set accepted", lambda: inventory.select_complete_set(root, "v1.2.3"))
        # Binary set still complete without the missing docker arm64 package.
        still_binary = inventory.select_complete_set(root, "v1.2.3", required=inventory.REQUIRED_BINARY)
        if len(still_binary) != 2:
            raise AssertionError(f"expected binary set to remain complete, got {still_binary}")
        (root / names[0]).unlink()
        require_raises(
            "incomplete binary set accepted",
            lambda: inventory.select_complete_set(root, "v1.2.3", required=inventory.REQUIRED_BINARY),
        )

    print("release artifact inventory self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
