# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import tempfile
import unittest
import zipfile
from pathlib import Path

from compare_release_checksums import compare_archive_members, read_checksums


class ReleaseChecksumTest(unittest.TestCase):
    def test_accepts_identical_artifact_sets(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            first = Path(temporary) / "first"
            second = Path(temporary) / "second"
            content = "a" * 64 + "  metrum-ai-router-v5.0.0-linux-amd64.tar.gz\n"
            first.write_text(content, encoding="utf-8")
            second.write_text(content, encoding="utf-8")
            self.assertEqual(read_checksums(first), read_checksums(second))

    def test_rejects_duplicate_and_empty_manifests(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            manifest = Path(temporary) / "checksums"
            record = "a" * 64 + "  artifact.tar.gz\n"
            for content in ("", record + record):
                with self.subTest(content=content):
                    manifest.write_text(content, encoding="utf-8")
                    with self.assertRaises(ValueError):
                        read_checksums(manifest)

    def test_rejects_malformed_and_path_like_names(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            manifest = Path(temporary) / "checksums"
            for content in (
                "a" * 63 + "  artifact.tar.gz\n",
                "a" * 64 + "  ../artifact.tar.gz\n",
            ):
                with self.subTest(content=content):
                    manifest.write_text(content, encoding="utf-8")
                    with self.assertRaises(ValueError):
                        read_checksums(manifest)

    def test_compares_archive_member_payloads(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            first = Path(temporary) / "first.zip"
            second = Path(temporary) / "second.zip"
            with zipfile.ZipFile(first, "w") as archive:
                archive.writestr("bin/router.exe", b"release binary")
                archive.writestr("docs/README.md", b"same")
            with zipfile.ZipFile(second, "w") as archive:
                archive.writestr("bin/router.exe", b"rebuilt binary")
                archive.writestr("docs/README.md", b"same")
            self.assertEqual(
                compare_archive_members(first, second),
                (["bin/router.exe"], [], []),
            )


if __name__ == "__main__":
    unittest.main()
