# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib
import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from release_image_layer import BASE_IMAGE, BINARIES, LEGAL, create_layer


class ReleaseImageLayerTest(unittest.TestCase):
    def make_package(self, root: Path, omit: str | None = None) -> tuple[Path, dict[str, bytes]]:
        package = root / "package.tar.gz"
        files = {f"pkg/bin/{name}": f"binary:{name}".encode() for name in BINARIES}
        files.update({f"pkg/{name}": f"legal:{name}".encode() for name in LEGAL})
        if omit:
            files.pop(omit, None)
        with tarfile.open(package, "w:gz") as archive:
            for name, content in files.items():
                info = tarfile.TarInfo(name)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
        return package, files

    def test_layer_contains_identical_package_binaries(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package, files = self.make_package(root)
            first, second = root / "one.tar.gz", root / "two.tar.gz"
            digests = create_layer(package, first, 123)
            create_layer(package, second, 123)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with tarfile.open(first, "r:gz") as layer:
                image_files = {item.name: layer.extractfile(item).read() for item in layer.getmembers() if item.isfile()}
            for name in BINARIES:
                self.assertEqual(image_files[f"app/bin/{name}"], files[f"pkg/bin/{name}"])
                self.assertEqual(digests[name], hashlib.sha256(files[f"pkg/bin/{name}"]).hexdigest())

    def test_missing_release_binary_fails_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package, _ = self.make_package(root, "pkg/bin/metrum-ai-router")
            with self.assertRaisesRegex(ValueError, "metrum-ai-router"):
                create_layer(package, root / "layer.tar.gz", 0)

    def test_base_is_digest_pinned(self):
        self.assertRegex(BASE_IMAGE, r"@sha256:[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
