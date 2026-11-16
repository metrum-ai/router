# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Compare every executable in a published image with its release package."""

from __future__ import annotations

import argparse
import hashlib
import subprocess
import tarfile
import tempfile
from pathlib import Path

from release_image_layer import BINARIES
from release_image_oci_layout import image_file


def package_binaries(package: Path) -> dict[str, bytes]:
    result = {}
    with tarfile.open(package, "r:gz") as archive:
        for name in BINARIES:
            member = next((item for item in archive.getmembers() if item.name.endswith(f"/bin/{name}")), None)
            if member is None:
                raise ValueError(f"package is missing {name}")
            stream = archive.extractfile(member)
            if stream is None:
                raise ValueError(f"cannot read {name} from package")
            result[name] = stream.read()
    return result


def verify(crane: str, image: str, package: Path) -> dict[str, str]:
    expected = package_binaries(package)
    with tempfile.TemporaryDirectory(prefix="router-image-binary-check-") as temporary:
        exported = Path(temporary) / "image.tar"
        subprocess.run([crane, "export", image, str(exported)], check=True)
        actual = {}
        with tarfile.open(exported) as archive:
            for name in BINARIES:
                member = next((item for item in archive.getmembers() if item.name == f"app/bin/{name}"), None)
                if member is None:
                    raise ValueError(f"image is missing {name}")
                stream = archive.extractfile(member)
                if stream is None:
                    raise ValueError(f"cannot read image binary {name}")
                data = stream.read()
                if data != expected[name]:
                    raise ValueError(f"image binary differs from release package: {name}")
                actual[name] = hashlib.sha256(data).hexdigest()
        return actual


def verify_layout(layout: Path, image_tag: str, platform: str, package: Path) -> dict[str, str]:
    expected = package_binaries(package)
    actual = {}
    for name in BINARIES:
        data = image_file(layout, image_tag, f"app/bin/{name}", platform)
        if data != expected[name]:
            raise ValueError(f"image binary differs from release package: {name}")
        actual[name] = hashlib.sha256(data).hexdigest()
    return actual


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--crane", required=True)
    parser.add_argument("--image")
    parser.add_argument("--image-tag")
    parser.add_argument("--oci-layout", type=Path)
    parser.add_argument("--platform")
    parser.add_argument("--package", required=True, type=Path)
    args = parser.parse_args()
    if args.oci_layout is not None:
        if not args.image_tag or not args.platform:
            parser.error("--oci-layout requires --image-tag and --platform")
        digests = verify_layout(args.oci_layout, args.image_tag, args.platform, args.package)
    else:
        if not args.image:
            parser.error("--image is required without --oci-layout")
        digests = verify(args.crane, args.image, args.package)
    for name, digest in digests.items():
        print(f"{digest}  {name}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
