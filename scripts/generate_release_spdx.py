# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Generate one SPDX JSON SBOM for each binary or Docker release package."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from pathlib import Path

from release_artifact_inventory import REQUIRED_SETS, select_complete_set
from release_security_evidence import safe_extract, scan_source


def generate(dist_dir: Path, version: str, output_dir: Path, syft: str, artifact_set: str = "full") -> list[Path]:
    selected = select_complete_set(dist_dir, version, required=REQUIRED_SETS[artifact_set])
    output_dir.mkdir(parents=True, exist_ok=True)
    reports: list[Path] = []
    for archive, _, _, _, archive_format in selected:
        stem = archive.name.removesuffix(f".{archive_format}")
        output = output_dir / f"{stem}.spdx.json"
        with tempfile.TemporaryDirectory(prefix="release-spdx-") as temporary:
            extracted = Path(temporary)
            safe_extract(archive, extracted)
            source = scan_source("binary" if "-docker-" not in archive.name else "docker", extracted)
            result = subprocess.run(
                [syft, "scan", source, "-o", f"spdx-json={output}"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
                check=False,
            )
            if result.returncode:
                raise RuntimeError(f"Syft failed for {archive.name}")
        sbom = json.loads(output.read_text(encoding="utf-8"))
        if not str(sbom.get("spdxVersion", "")).startswith("SPDX-2.") or not sbom.get("packages"):
            raise ValueError(f"Syft produced an empty or invalid SPDX SBOM for {archive.name}")
        reports.append(output)
    return reports


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=Path("dist"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path, default=Path("dist"))
    parser.add_argument("--syft", default="syft")
    parser.add_argument("--artifact-set", choices=("full", "binary"), default="full")
    args = parser.parse_args()
    try:
        reports = generate(args.dist_dir, args.version, args.output_dir, args.syft, args.artifact_set)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, tarfile.TarError, zipfile.BadZipFile) as exc:
        print(f"release SPDX generation failed: {exc}", file=sys.stderr)
        return 2
    print(f"generated {len(reports)} SPDX SBOMs for release packages")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
