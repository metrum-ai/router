#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Unit tests for release artifact inventory selection and metadata."""

from __future__ import annotations

import tempfile
from pathlib import Path
from unittest.mock import patch

import release_artifact_inventory as inventory


VERSION = "v1.2.3"
NAMES = [
    "metrum-ai-router-v1.2.3-linux-amd64.tar.gz",
    "metrum-ai-router-v1.2.3-linux-arm64.tar.gz",
    "metrum-ai-router-v1.2.3-windows-amd64.zip",
    "metrum-ai-router-v1.2.3-windows-arm64.zip",
    "metrum-ai-router-v1.2.3-docker-linux-amd64.tar.gz",
    "metrum-ai-router-v1.2.3-docker-linux-arm64.tar.gz",
]


def require_raises(message: str, callback) -> None:
    try:
        callback()
    except ValueError:
        return
    raise AssertionError(message)


def main() -> int:
    inventory.validate_metadata(VERSION, "0123456789abcdef", "2026-09-03T12:34:56Z")
    require_raises(
        "dirty version accepted",
        lambda: inventory.validate_metadata("v1.2.3-dirty", "0123456", "2026-09-03T12:34:56Z"),
    )
    require_raises(
        "invalid calendar date accepted",
        lambda: inventory.validate_metadata(VERSION, "0123456", "2026-02-30T12:34:56Z"),
    )

    with tempfile.TemporaryDirectory() as temp:
        root = Path(temp)
        for name in NAMES:
            (root / name).write_bytes(name.encode("utf-8"))

        selected = inventory.select_complete_set(root, VERSION)
        if len(selected) != 6:
            raise AssertionError(f"expected six artifacts, got {selected}")
        binary_only = inventory.select_complete_set(root, VERSION, required=inventory.REQUIRED_BINARY)
        if len(binary_only) != 4:
            raise AssertionError(f"expected four binary artifacts, got {binary_only}")
        expected_binary = {
            ("linux", "amd64", "tar.gz"),
            ("linux", "arm64", "tar.gz"),
            ("windows", "amd64", "zip"),
            ("windows", "arm64", "zip"),
        }
        actual_binary = {(os_name, arch, archive_format) for _, _, os_name, arch, archive_format in binary_only}
        if actual_binary != expected_binary:
            raise AssertionError(f"binary platform/format metadata mismatch: {actual_binary}")

        # The binary set must fail closed when either Windows asset is absent.
        (root / NAMES[2]).unlink()
        require_raises(
            "incomplete binary set accepted without Windows amd64",
            lambda: inventory.select_complete_set(root, VERSION, required=inventory.REQUIRED_BINARY),
        )
        (root / NAMES[2]).write_bytes(NAMES[2].encode("utf-8"))
        (root / NAMES[5]).unlink()
        require_raises("incomplete full set accepted", lambda: inventory.select_complete_set(root, VERSION))
        if len(inventory.select_complete_set(root, VERSION, required=inventory.REQUIRED_BINARY)) != 4:
            raise AssertionError("binary set should remain complete without Docker arm64")

        with patch.object(inventory, "run_content_validator"):
            result, checksums = inventory.create_inventory(
                root,
                VERSION,
                "0123456789abcdef",
                "2026-09-03T12:34:56Z",
                root / "allowlist.txt",
                None,
                artifact_set="binary",
            )
        records = {item["filename"]: item for item in result["artifacts"]}
        windows_record = records["metrum-ai-router-v1.2.3-windows-arm64.zip"]
        if (windows_record["os"], windows_record["arch"], windows_record["format"], windows_record["platform"]) != (
            "windows", "arm64", "zip", "windows/arm64"
        ):
            raise AssertionError(f"Windows inventory metadata mismatch: {windows_record}")
        if len(checksums.splitlines()) != 4 or not all(name in checksums for name in NAMES[:4]):
            raise AssertionError(f"binary assets missing from checksums: {checksums}")

    print("release artifact inventory self-test passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
