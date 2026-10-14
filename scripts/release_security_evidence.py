#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Generate SBOM and vulnerability evidence for one complete release set."""

from __future__ import annotations

import argparse
import hashlib
import json
import subprocess
import sys
import tarfile
import tempfile
import zipfile
from collections import Counter
from pathlib import Path

from release_artifact_inventory import parse_package, select_complete_set


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run_output(command: list[str]) -> str:
    return subprocess.check_output(command, text=True, stderr=subprocess.STDOUT).strip()


def safe_extract(archive: Path, destination: Path) -> None:
    if archive.name.endswith(".zip"):
        with zipfile.ZipFile(archive) as package:
            for member in package.infolist():
                member_path = Path(member.filename)
                if member_path.is_absolute() or ".." in member_path.parts or "\\" in member.filename:
                    raise ValueError(f"unsafe path in release archive: {member.filename}")
            package.extractall(destination)
        return
    with tarfile.open(archive, "r:gz") as package:
        package.extractall(destination, filter="data")


def scan_source(kind: str, extracted: Path) -> str:
    if kind == "binary":
        return f"dir:{extracted}"
    image_tars = list(extracted.glob("*/images/*.tar"))
    if len(image_tars) != 1:
        raise ValueError(f"expected exactly one saved image, found {len(image_tars)}")
    return f"docker-archive:{image_tars[0]}"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=Path("dist"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--syft", default="syft")
    parser.add_argument("--grype", default="grype")
    parser.add_argument("--fail-on", default="critical")
    args = parser.parse_args()

    output_dir = args.output_dir or args.dist_dir / "security-evidence"
    output_dir.mkdir(parents=True, exist_ok=True)
    try:
        selected = select_complete_set(args.dist_dir, args.version)
        syft_version = run_output([args.syft, "version"])
        grype_version = run_output([args.grype, "version"])
        try:
            database_status = json.loads(run_output([args.grype, "db", "status", "-o", "json"]))
        except (subprocess.CalledProcessError, json.JSONDecodeError):
            database_status = {"status": "unavailable"}
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        print(f"release security evidence failed: {exc}", file=sys.stderr)
        return 2

    records: list[dict[str, object]] = []
    threshold_failures = 0
    for archive, kind, os_name, arch, archive_format in selected:
        stem = archive.name.removesuffix(f".{archive_format}")
        sbom = output_dir / f"{stem}.sbom.cdx.json"
        report = output_dir / f"{stem}.vulnerabilities.json"
        with tempfile.TemporaryDirectory(prefix="release-security-") as temporary:
            extracted = Path(temporary)
            try:
                safe_extract(archive, extracted)
                source = scan_source(kind, extracted)
                subprocess.run([args.syft, "scan", source, "-o", f"cyclonedx-json={sbom}"], check=True)
                grype_result = subprocess.run(
                    [args.grype, f"sbom:{sbom}", "-o", "json", "--file", str(report), "--fail-on", args.fail_on]
                )
                threshold_failures += int(grype_result.returncode != 0)
                bom = json.loads(sbom.read_text(encoding="utf-8"))
                findings = json.loads(report.read_text(encoding="utf-8"))
            except (OSError, ValueError, tarfile.TarError, subprocess.CalledProcessError, json.JSONDecodeError) as exc:
                print(f"release security evidence failed for {archive.name}: {exc}", file=sys.stderr)
                return 2
        if bom.get("bomFormat") != "CycloneDX" or not bom.get("components"):
            print(f"release security evidence failed: invalid or empty CycloneDX SBOM for {archive.name}", file=sys.stderr)
            return 2
        severities = Counter(
            str(match.get("vulnerability", {}).get("severity", "unknown")).lower()
            for match in findings.get("matches", [])
        )
        records.append({
            "artifact": archive.name,
            "artifact_sha256": sha256(archive),
            "kind": kind,
            "os": os_name,
            "arch": arch,
            "format": archive_format,
            "platform": f"{os_name}/{arch}",
            "sbom": sbom.name,
            "sbom_sha256": sha256(sbom),
            "vulnerability_report": report.name,
            "vulnerability_report_sha256": sha256(report),
            "findings_by_severity": dict(sorted(severities.items())),
            "threshold": args.fail_on,
            "threshold_verdict": "fail" if grype_result.returncode else "pass",
        })

    summary = {
        "schema": "metrum-ai-router.release-security-evidence/v1",
        "version": args.version,
        "syft_version": syft_version,
        "grype_version": grype_version,
        "grype_database": database_status,
        "artifacts": records,
    }
    summary_path = output_dir / "release-security-evidence.json"
    summary_path.write_text(json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"wrote {len(records)} artifact SBOMs and vulnerability reports to {output_dir}")
    if threshold_failures:
        print(f"release security evidence failed: {threshold_failures} artifact(s) exceeded {args.fail_on}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
