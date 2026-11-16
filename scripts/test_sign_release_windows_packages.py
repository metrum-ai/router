# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import os
from pathlib import Path
import tempfile
import unittest
import zipfile

import sign_release_windows_packages as signing


class SignReleaseWindowsPackagesTest(unittest.TestCase):
    def test_unsigned_packages_remain_byte_identical_before_inventory(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            dist = Path(temporary)
            original = {}
            for arch in ("amd64", "arm64"):
                name = f"metrum-ai-router-v5.0.0-windows-{arch}.zip"
                path = dist / name
                with zipfile.ZipFile(path, "w") as package:
                    package.writestr(f"metrum-ai-router-v5.0.0-windows-{arch}/bin/router.exe", b"MZ unsigned")
                original[name] = path.read_bytes()
            previous = os.environ.get("ROUTER_SIGN_WINDOWS")
            os.environ["ROUTER_SIGN_WINDOWS"] = "0"
            try:
                self.assertEqual(signing.sign_packages(dist, "v5.0.0"), [])
            finally:
                if previous is None:
                    os.environ.pop("ROUTER_SIGN_WINDOWS", None)
                else:
                    os.environ["ROUTER_SIGN_WINDOWS"] = previous
            self.assertEqual({name: (dist / name).read_bytes() for name in original}, original)

    def test_unsafe_zip_paths_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "unsafe.zip"
            with zipfile.ZipFile(archive, "w") as package:
                package.writestr("../outside.exe", b"MZ")
            with self.assertRaisesRegex(ValueError, "unsafe path"):
                signing.extract_safe(archive, root / "unpacked")


if __name__ == "__main__":
    unittest.main()
