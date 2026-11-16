# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from check_release_payload import ARCHIVES, validate
from release_image_layer import BASE_IMAGE, BINARIES


class ReleasePayloadTest(unittest.TestCase):
    def make_payload(self, root: Path) -> None:
        version = "v5.0.0"
        archives = {item.format(version=version) for item in ARCHIVES}
        entries = []
        sums = []
        for name in sorted(archives):
            content = name.encode()
            (root / name).write_bytes(content)
            sums.append(hashlib.sha256(content).hexdigest() + "  " + name)
            base = name.removesuffix(".tar.gz").removesuffix(".zip")
            (root / (base + ".spdx.json")).write_text(json.dumps({"spdxVersion": "SPDX-2.3", "packages": [{"name": base}]}), encoding="utf-8")
            (root / (name + ".sigstore.json")).write_text(json.dumps({"bundle": True}), encoding="utf-8")
            entries.append({"filename": name})
        (root / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8")
        (root / "release-artifacts.json").write_text(json.dumps({"artifact_set": "binary", "artifacts": entries}), encoding="utf-8")
        (root / "upgrade-rollback-results.json").write_text(json.dumps({
            "schema": "router.release-upgrade-rollback/v1",
            "checks": [{"check": "candidate-serving-smoke", "passed": True}],
        }), encoding="utf-8")
        for arch in ("amd64", "arm64"):
            name = f"metrum-ai-router-{version}-linux-{arch}-image.spdx.json"
            (root / name).write_text(json.dumps({"spdxVersion": "SPDX-2.3", "packages": [{"name": name}]}), encoding="utf-8")
        platforms = {
            arch: {
                "ref": f"ghcr.io/metrum-ai/router:{version}-linux-{arch}",
                "digest": "sha256:" + "a" * 64,
                "package_sha256": "b" * 64,
                "binaries": {name: "c" * 64 for name in BINARIES},
            }
            for arch in ("amd64", "arm64")
        }
        (root / "release-images.json").write_text(json.dumps({
            "schema": "metrum-ai-router.release-image/v1", "version": version,
            "base": BASE_IMAGE, "index": {"ref": f"ghcr.io/metrum-ai/router:{version}", "digest": "sha256:" + "d" * 64},
            "platforms": platforms,
        }), encoding="utf-8")
        (root / "AI-release-review.md").write_text("- Decision: **PASS**\n", encoding="utf-8")

    def test_accepts_complete_release_payload(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_payload(root)
            self.assertEqual(validate(root, "v5.0.0"), [])

    def test_rejects_missing_sbom_or_blocked_ai_review(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_payload(root)
            (root / "AI-release-review.md").write_text("- Decision: **BLOCK**\n", encoding="utf-8")
            (root / "metrum-ai-router-v5.0.0-linux-amd64.spdx.json").unlink()
            errors = validate(root, "v5.0.0")
            self.assertTrue(any("missing release assets" in error for error in errors))
            self.assertTrue(any("passing decision" in error for error in errors))

    def test_rejects_missing_image_manifest_or_digest(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_payload(root)
            (root / "release-images.json").unlink()
            errors = validate(root, "v5.0.0")
            self.assertTrue(any("missing release assets" in error for error in errors))

    def test_rejects_malformed_image_binary_checksum(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_payload(root)
            manifest_path = root / "release-images.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["platforms"]["amd64"]["binaries"]["metrum-ai-router"] = "not-a-digest"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            errors = validate(root, "v5.0.0")
            self.assertTrue(any("invalid binary checksum" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
