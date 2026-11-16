# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from release_image_layer import BINARIES
from release_image_oci_layout import append_image, image_descriptor, image_file
from verify_release_image_binaries import verify_layout


def docker_archive(path: Path, architecture: str) -> None:
    config = {"architecture": architecture, "os": "linux", "config": {
        "Entrypoint": ["/app/bin/metrum-ai-router"], "Cmd": ["--config", "/app/config/config.yaml"],
        "User": "65532:65532", "Labels": {"org.opencontainers.image.version": "v5.0.0-dry-run"},
    }}
    config_bytes = json.dumps(config).encode()
    files = {f"app/bin/{name}": f"binary:{name}".encode() for name in BINARIES}
    config_name = "config.json"
    layer_name = "layer.tar"
    layer = io.BytesIO()
    with tarfile.open(fileobj=layer, mode="w") as archive:
        for name, data in files.items():
            info = tarfile.TarInfo(name)
            info.mode = 0o755
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
    manifest = json.dumps([{"Config": config_name, "RepoTags": [], "Layers": [layer_name]}]).encode()
    with tarfile.open(path, "w") as output:
        for name, data in ((config_name, config_bytes), (layer_name, layer.getvalue()), ("manifest.json", manifest)):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            output.addfile(info, io.BytesIO(data))


class ReleaseImageOCILayoutTest(unittest.TestCase):
    def test_assembles_and_reads_multi_arch_oci_layout(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            layout = root / "oci"
            for architecture in ("amd64", "arm64"):
                archive = root / f"{architecture}.tar"
                docker_archive(archive, architecture)
                append_image(archive, layout, "router:v5.0.0-dry-run", f"linux/{architecture}")
                _, config = image_descriptor(layout, "router:v5.0.0-dry-run", f"linux/{architecture}")
                self.assertEqual(config["config"]["User"], "65532:65532")
                self.assertEqual(image_file(layout, "router:v5.0.0-dry-run", "app/bin/metrum-ai-router", f"linux/{architecture}"), b"binary:metrum-ai-router")
            index = json.loads((layout / "index.json").read_text(encoding="utf-8"))
            self.assertEqual({item["platform"]["architecture"] for item in index["manifests"]}, {"amd64", "arm64"})

    def test_verifies_layout_binary_bytes_against_package(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            layout = root / "oci"
            archive = root / "amd64.tar"
            docker_archive(archive, "amd64")
            append_image(archive, layout, "router:v5.0.0-dry-run", "linux/amd64")
            package = root / "package.tar.gz"
            with tarfile.open(package, "w:gz") as output:
                for name in BINARIES:
                    data = f"binary:{name}".encode()
                    info = tarfile.TarInfo(f"package/bin/{name}")
                    info.size = len(data)
                    output.addfile(info, io.BytesIO(data))
            self.assertEqual(set(verify_layout(layout, "router:v5.0.0-dry-run", "linux/amd64", package)), set(BINARIES))

    def test_rejects_archive_platform_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "amd64.tar"
            docker_archive(archive, "amd64")
            with self.assertRaisesRegex(ValueError, "platform"):
                append_image(archive, root / "oci", "router:test", "linux/arm64")


if __name__ == "__main__":
    unittest.main()
