# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import tempfile
import io
import tarfile
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
                first_entry = zipfile.ZipInfo("bin/router.exe", date_time=(2026, 1, 1, 0, 0, 0))
                first_entry.external_attr = 0o100755 << 16
                archive.writestr(first_entry, b"release binary")
                archive.writestr("docs/README.md", b"same")
            with zipfile.ZipFile(second, "w") as archive:
                changed_entry = zipfile.ZipInfo("bin/router.exe", date_time=(2026, 1, 2, 0, 0, 0))
                changed_entry.external_attr = 0o100644 << 16
                archive.writestr(changed_entry, b"rebuilt binary")
                archive.writestr("docs/README.md", b"same")
            report = compare_archive_members(first, second)
            self.assertEqual(report["changed_content"], ["bin/router.exe"])
            self.assertEqual(report["changed_metadata"], ["bin/router.exe"])
            self.assertFalse(report["order_changed"])

    def test_compares_tar_member_metadata_and_order(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            first = Path(temporary) / "first.tar.gz"
            second = Path(temporary) / "second.tar.gz"
            self._write_tar(first, [("bin/router", 1700000000, 0o755), ("docs/README", 1700000000, 0o644)])
            self._write_tar(second, [("docs/README", 1700000000, 0o644), ("bin/router", 1700000002, 0o700)])
            report = compare_archive_members(first, second)
            self.assertEqual(report["changed_content"], [])
            self.assertEqual(report["changed_metadata"], ["bin/router"])
            self.assertTrue(report["order_changed"])

    @staticmethod
    def _write_tar(path: Path, members: list[tuple[str, int, int]]) -> None:
        with tarfile.open(path, "w:gz") as archive:
            for name, mtime, mode in members:
                content = b"same payload"
                info = tarfile.TarInfo(name)
                info.size = len(content)
                info.mtime = mtime
                info.uid = info.gid = 0
                info.mode = mode
                archive.addfile(info, io.BytesIO(content))


if __name__ == "__main__":
    unittest.main()
