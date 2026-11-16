# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Create and inspect a local OCI image layout from Crane Docker tarballs."""

from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import tarfile
from typing import Any


LAYOUT = {"imageLayoutVersion": "1.0.0"}
MANIFEST_TYPE = "application/vnd.oci.image.manifest.v1+json"
CONFIG_TYPE = "application/vnd.oci.image.config.v1+json"
LAYER_TYPES = {
    b"\x1f\x8b": "application/vnd.oci.image.layer.v1.tar+gzip",
    b"": "application/vnd.oci.image.layer.v1.tar",
}


def digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def write_blob(layout: Path, data: bytes) -> dict[str, Any]:
    value = digest(data)
    path = layout / "blobs" / "sha256" / value.removeprefix("sha256:")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data)
    return {"digest": value, "size": len(data)}


def read_docker_archive(archive: Path) -> tuple[dict[str, Any], bytes, list[bytes]]:
    with tarfile.open(archive, "r:") as source:
        try:
            entries = json.load(source.extractfile("manifest.json"))
        except (KeyError, json.JSONDecodeError, TypeError) as exc:
            raise ValueError("Crane output is not a Docker image archive") from exc
        if not isinstance(entries, list) or len(entries) != 1 or not isinstance(entries[0], dict):
            raise ValueError("Crane archive must contain exactly one image")
        entry = entries[0]
        config_name = entry.get("Config")
        layers = entry.get("Layers")
        if (not isinstance(config_name, str) or PurePosixPath(config_name).is_absolute()
                or ".." in PurePosixPath(config_name).parts or not isinstance(layers, list) or not layers):
            raise ValueError("Crane archive manifest is incomplete")
        config_stream = source.extractfile(config_name)
        if config_stream is None:
            raise ValueError("Crane archive is missing its image config")
        config_bytes = config_stream.read()
        try:
            config = json.loads(config_bytes)
        except json.JSONDecodeError as exc:
            raise ValueError("Crane image config is invalid") from exc
        layer_bytes = []
        for name in layers:
            if not isinstance(name, str) or PurePosixPath(name).is_absolute() or ".." in PurePosixPath(name).parts:
                raise ValueError("Crane archive contains an unsafe layer path")
            stream = source.extractfile(name)
            if stream is None:
                raise ValueError("Crane archive is missing an image layer")
            layer_bytes.append(stream.read())
    if not isinstance(config, dict):
        raise ValueError("Crane image config must be an object")
    return config, config_bytes, layer_bytes


def append_image(archive: Path, layout: Path, ref_name: str, platform: str) -> None:
    os_name, separator, architecture = platform.partition("/")
    if not separator or not os_name or not architecture:
        raise ValueError("platform must use os/architecture")
    config, config_bytes, layer_contents = read_docker_archive(archive)
    if config.get("os") != os_name or config.get("architecture") != architecture:
        raise ValueError("Crane archive platform does not match its OCI descriptor")
    layout.mkdir(parents=True, exist_ok=True)
    (layout / "oci-layout").write_text(json.dumps(LAYOUT, separators=(",", ":")) + "\n", encoding="utf-8")
    config_descriptor = write_blob(layout, config_bytes)
    config_descriptor["mediaType"] = CONFIG_TYPE
    layer_descriptors = []
    for layer in layer_contents:
        descriptor = write_blob(layout, layer)
        descriptor["mediaType"] = next((kind for magic, kind in LAYER_TYPES.items() if not magic or layer.startswith(magic)), None)
        if descriptor["mediaType"] is None:
            raise ValueError("Crane archive has an unsupported layer encoding")
        layer_descriptors.append(descriptor)
    manifest = {
        "schemaVersion": 2,
        "mediaType": MANIFEST_TYPE,
        "config": config_descriptor,
        "layers": layer_descriptors,
    }
    manifest_bytes = (json.dumps(manifest, separators=(",", ":"), sort_keys=True) + "\n").encode()
    manifest_descriptor = write_blob(layout, manifest_bytes)
    manifest_descriptor.update({
        "mediaType": MANIFEST_TYPE,
        "platform": {"os": os_name, "architecture": architecture},
        "annotations": {"org.opencontainers.image.ref.name": ref_name},
    })
    index_path = layout / "index.json"
    if index_path.exists():
        index = json.loads(index_path.read_text(encoding="utf-8"))
    else:
        index = {"schemaVersion": 2, "manifests": []}
    manifests = index.get("manifests")
    if not isinstance(manifests, list):
        raise ValueError("OCI index has an invalid manifests field")
    if any(item.get("platform") == manifest_descriptor["platform"] for item in manifests if isinstance(item, dict)):
        raise ValueError(f"OCI layout already contains platform {platform}")
    manifests.append(manifest_descriptor)
    index_path.write_text(json.dumps(index, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def image_descriptor(layout: Path, ref_name: str, platform: str | None = None) -> tuple[dict[str, Any], dict[str, Any]]:
    try:
        index = json.loads((layout / "index.json").read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError("OCI layout index is missing or invalid") from exc
    candidates = []
    for descriptor in index.get("manifests", []):
        if not isinstance(descriptor, dict):
            continue
        annotations = descriptor.get("annotations", {})
        item_platform = descriptor.get("platform", {})
        found_platform = f"{item_platform.get('os')}/{item_platform.get('architecture')}"
        if annotations.get("org.opencontainers.image.ref.name") == ref_name and (platform is None or found_platform == platform):
            candidates.append(descriptor)
    if len(candidates) != 1:
        raise ValueError(f"OCI layout does not contain one {ref_name} image for {platform or 'any platform'}")
    descriptor = candidates[0]
    manifest_path = layout / "blobs" / "sha256" / descriptor["digest"].removeprefix("sha256:")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        config_path = layout / "blobs" / "sha256" / manifest["config"]["digest"].removeprefix("sha256:")
        config = json.loads(config_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError, KeyError, TypeError) as exc:
        raise ValueError("OCI image manifest or config is invalid") from exc
    return manifest, config


def image_file(layout: Path, ref_name: str, member_name: str, platform: str) -> bytes:
    manifest, _ = image_descriptor(layout, ref_name, platform)
    for descriptor in reversed(manifest["layers"]):
        blob = layout / "blobs" / "sha256" / descriptor["digest"].removeprefix("sha256:")
        data = blob.read_bytes()
        stream = gzip.GzipFile(fileobj=io.BytesIO(data)) if data.startswith(b"\x1f\x8b") else io.BytesIO(data)
        with tarfile.open(fileobj=stream, mode="r:") as layer:
            try:
                member = layer.getmember(member_name)
            except KeyError:
                continue
            if not member.isfile():
                raise ValueError(f"OCI image member is not a regular file: {member_name}")
            source = layer.extractfile(member)
            if source is None:
                raise ValueError(f"OCI image member cannot be read: {member_name}")
            return source.read()
    raise ValueError(f"OCI image is missing {member_name}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--layout", type=Path, required=True)
    parser.add_argument("--ref", required=True)
    parser.add_argument("--platform", required=True)
    args = parser.parse_args()
    try:
        append_image(args.archive, args.layout, args.ref, args.platform)
    except (OSError, ValueError, tarfile.TarError) as exc:
        parser.error(str(exc))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
