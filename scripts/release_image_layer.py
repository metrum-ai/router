# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Build a deterministic OCI layer from an already validated Linux package."""

from __future__ import annotations

import argparse
import hashlib
import gzip
import io
import tarfile
from pathlib import Path


BASE_IMAGE = "cgr.dev/chainguard/static:latest@sha256:fc42af33e643f20d08baf03cddcc1c02ba57052dfb02edb0d79f3fc47bcea8d9"
BINARIES = (
    "metrum-ai-router",
    "metrum-ai-router-token-gen",
    "metrum-ai-router-usage-report",
    "metrum-ai-router-migrate",
    "metrum-ai-routerctl",
)
LEGAL = ("LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "MODEL_LICENSES.md")


def package_members(archive: Path) -> dict[str, bytes]:
    found: dict[str, bytes] = {}
    with tarfile.open(archive, "r:gz") as source:
        for member in source.getmembers():
            path = Path(member.name)
            if path.is_absolute() or ".." in path.parts or not member.isfile():
                continue
            relative = Path(*path.parts[1:]) if len(path.parts) > 1 else Path()
            if relative.parts[:1] == ("bin",) and relative.name in BINARIES:
                stream = source.extractfile(member)
                if stream is None:
                    raise ValueError(f"cannot read package member {member.name}")
                found[f"app/bin/{relative.name}"] = stream.read()
            elif len(relative.parts) == 1 and relative.name in LEGAL:
                stream = source.extractfile(member)
                if stream is None:
                    raise ValueError(f"cannot read package member {member.name}")
                found[relative.name] = stream.read()
    required = {f"app/bin/{name}" for name in BINARIES} | set(LEGAL)
    missing = sorted(required - found.keys())
    if missing:
        raise ValueError("release package is missing image files: " + ", ".join(missing))
    return found


def create_layer(archive: Path, output: Path, epoch: int) -> dict[str, str]:
    content = package_members(archive)
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode="w", format=tarfile.PAX_FORMAT) as layer:
                for name, data in sorted(content.items()):
                    item = tarfile.TarInfo(name)
                    item.size = len(data)
                    item.mtime = epoch
                    item.uid = item.gid = 0
                    item.uname = item.gname = ""
                    item.mode = 0o755 if name.startswith("app/bin/") else 0o644
                    layer.addfile(item, io.BytesIO(data))
    return {name.removeprefix("app/bin/"): hashlib.sha256(data).hexdigest()
            for name, data in sorted(content.items()) if name.startswith("app/bin/")}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--epoch", type=int, default=0)
    parser.add_argument("--print-base-image", action="store_true")
    args = parser.parse_args()
    if args.print_base_image:
        print(BASE_IMAGE)
        return 0
    if args.package is None or args.output is None:
        parser.error("--package and --output are required")
    hashes = create_layer(args.package, args.output, args.epoch)
    for name, digest in hashes.items():
        print(f"{digest}  {name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
