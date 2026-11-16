# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import io
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from release_image_layer import BINARIES
from verify_release_image_binaries import verify


class ReleaseImageBinaryTest(unittest.TestCase):
    def fixture(self, root: Path, changed: str | None = None) -> tuple[Path, Path]:
        package = root / "package.tar.gz"
        image = root / "image.tar"
        with tarfile.open(package, "w:gz") as archive:
            for name in BINARIES:
                content = f"release:{name}".encode()
                item = tarfile.TarInfo(f"package/bin/{name}")
                item.size = len(content)
                archive.addfile(item, io.BytesIO(content))
        with tarfile.open(image, "w") as archive:
            for name in BINARIES:
                content = (f"changed:{name}" if name == changed else f"release:{name}").encode()
                item = tarfile.TarInfo(f"app/bin/{name}")
                item.size = len(content)
                archive.addfile(item, io.BytesIO(content))
        return package, image

    def check(self, args, **_kwargs):
        source = Path(args[2])
        destination = Path(args[3])
        destination.write_bytes(source.read_bytes())
        return subprocess.CompletedProcess(args, 0)

    def test_accepts_byte_identical_image_binaries(self):
        with tempfile.TemporaryDirectory() as temporary:
            package, image = self.fixture(Path(temporary))
            with patch("verify_release_image_binaries.subprocess.run", side_effect=self.check):
                result = verify("crane", str(image), package)
            self.assertEqual(set(result), set(BINARIES))

    def test_rejects_one_changed_image_binary(self):
        with tempfile.TemporaryDirectory() as temporary:
            package, image = self.fixture(Path(temporary), changed="metrum-ai-router")
            with patch("verify_release_image_binaries.subprocess.run", side_effect=self.check):
                with self.assertRaisesRegex(ValueError, "differs from release package"):
                    verify("crane", str(image), package)


if __name__ == "__main__":
    unittest.main()
