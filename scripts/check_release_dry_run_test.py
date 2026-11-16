# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from check_release_dry_run import validate
from release_image_layer import BINARIES
from release_image_oci_layout import append_image


def archive(path: Path, architecture: str, version: str, commit: str) -> None:
    config = {"os": "linux", "architecture": architecture, "config": {"Labels": {
        "org.opencontainers.image.version": version,
        "org.opencontainers.image.revision": commit,
    }}}
    config_bytes = json.dumps(config).encode()
    layer_buffer = io.BytesIO()
    with tarfile.open(fileobj=layer_buffer, mode="w") as layer:
        for name in BINARIES:
            data = f"binary:{architecture}:{name}".encode()
            info = tarfile.TarInfo(f"app/bin/{name}")
            info.size = len(data)
            layer.addfile(info, io.BytesIO(data))
    manifest = json.dumps([{"Config": "config.json", "Layers": ["layer.tar"]}]).encode()
    with tarfile.open(path, "w") as output:
        for name, data in (("config.json", config_bytes), ("layer.tar", layer_buffer.getvalue()), ("manifest.json", manifest)):
            info = tarfile.TarInfo(name)
            info.size = len(data)
            output.addfile(info, io.BytesIO(data))


class CheckReleaseDryRunTest(unittest.TestCase):
    def test_accepts_complete_matching_evidence_and_rejects_tampering(self) -> None:
        version, commit, previous = "v5.0.0-dry-run", "a" * 40, "v4.8.0"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            dist = root / "dist"
            layout = root / "oci"
            dist.mkdir()
            names = []
            for arch in ("amd64", "arm64"):
                name = f"metrum-ai-router-{version}-linux-{arch}.tar.gz"
                path = dist / name
                with tarfile.open(path, "w:gz"):
                    pass
                names.append(name)
            # The inventory requires all four release packages; the Windows fixtures are ordinary package files.
            for arch in ("amd64", "arm64"):
                name = f"metrum-ai-router-{version}-windows-{arch}.zip"
                (dist / name).write_bytes(f"windows:{arch}".encode())
                names.append(name)
            entries = [{"filename": name} for name in names]
            (dist / "release-artifacts.json").write_text(json.dumps({
                "version": version, "commit": commit, "artifact_set": "binary", "artifacts": entries,
            }), encoding="utf-8")
            checksums = []
            for name in names:
                checksums.append(f"{hashlib.sha256((dist / name).read_bytes()).hexdigest()}  {name}")
            (dist / "SHA256SUMS").write_text("\n".join(checksums) + "\n", encoding="utf-8")
            review_artifact = {
                "schema": "metrum-ai-router-release-review-artifact/v1",
                "base": previous,
                "head": commit,
                "summary": "No findings.",
                "findings": [],
            }
            review_artifact["artifact_digest"] = hashlib.sha256(json.dumps(
                review_artifact, ensure_ascii=True, separators=(",", ":"), sort_keys=True
            ).encode("utf-8")).hexdigest()
            (dist / "release-review-artifact.json").write_text(json.dumps(review_artifact), encoding="utf-8")
            (dist / "AI-release-review.md").write_text(
                f"- Decision: **PASS**\n- Reviewed range: `{previous}..{commit}`\n"
                f"- Review artifact SHA-256: `{review_artifact['artifact_digest']}`\n", encoding="utf-8")
            (dist / "upgrade-rollback-results.json").write_text(json.dumps({
                "schema": "router.release-upgrade-rollback/v1", "previous_version": previous,
                "candidate_version": version, "candidate_commit": commit,
            }), encoding="utf-8")
            for arch in ("amd64", "arm64"):
                image = root / f"{arch}.tar"
                archive(image, arch, version, commit)
                append_image(image, layout, f"ghcr.io/metrum-ai/router:{version}", f"linux/{arch}")

            self.assertEqual(validate(dist, layout, version, commit, previous), [])
            (dist / names[0]).write_bytes(b"tampered")
            self.assertTrue(any("checksum mismatch" in error for error in validate(dist, layout, version, commit, previous)))


if __name__ == "__main__":
    unittest.main()
