# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Require a complete signed release payload before publication."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

from stage_release_payload import ARCHIVE_PATTERNS, payload_names


ARCHIVES = set(ARCHIVE_PATTERNS)
PASS = re.compile(r"^\- Decision: \*\*PASS\*\*$", re.MULTILINE)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def validate(dist: Path, version: str) -> list[str]:
    errors: list[str] = []
    expected_archives = {item.format(version=version) for item in ARCHIVES}
    expected_sboms = {name.removesuffix(".tar.gz").removesuffix(".zip") + ".spdx.json" for name in expected_archives}
    expected_sboms |= {
        f"metrum-ai-router-{version}-linux-amd64-image.spdx.json",
        f"metrum-ai-router-{version}-linux-arm64-image.spdx.json",
    }
    expected_bundles = {name + ".sigstore.json" for name in expected_archives | expected_sboms}
    expected_names = expected_archives | expected_sboms | expected_bundles | {
        "SHA256SUMS", "release-artifacts.json", "release-images.json", "release-review-artifact.json",
        "upgrade-rollback-results.json", "AI-release-review.md"
    }
    if not dist.is_dir():
        return ["release payload directory is missing"]
    found_names = {path.name for path in dist.iterdir() if path.is_file()}
    missing = sorted(expected_names - found_names)
    extra = sorted(found_names - expected_names)
    if missing:
        errors.append("missing release assets: " + ", ".join(missing))
    if extra:
        errors.append("unexpected release assets: " + ", ".join(extra))
    try:
        inventory = json.loads((dist / "release-artifacts.json").read_text(encoding="utf-8"))
        inventory_items = inventory.get("artifacts", [])
        inventory_names = {item["filename"] for item in inventory_items}
        if (
            inventory.get("artifact_set") != "binary"
            or len(inventory_items) != len(expected_archives)
            or inventory_names != expected_archives
        ):
            errors.append("release inventory does not describe the complete binary package set")
    except (OSError, json.JSONDecodeError, KeyError, TypeError):
        errors.append("release inventory is missing or malformed")
    try:
        checksums = set()
        for line in (dist / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
            digest, name = line.split("  ", 1)
            if not re.fullmatch(r"[0-9a-f]{64}", digest):
                raise ValueError
            checksums.add(name)
        if checksums != expected_archives:
            errors.append("checksum file does not cover the complete release package set")
    except (OSError, ValueError):
        errors.append("checksum file is missing or malformed")
    for name in expected_sboms & found_names:
        try:
            sbom = json.loads((dist / name).read_text(encoding="utf-8"))
            if not str(sbom.get("spdxVersion", "")).startswith("SPDX-2.") or not sbom.get("packages"):
                errors.append(f"invalid SPDX SBOM: {name}")
        except (OSError, json.JSONDecodeError):
            errors.append(f"invalid SPDX SBOM: {name}")
    for name in expected_bundles & found_names:
        try:
            bundle = json.loads((dist / name).read_text(encoding="utf-8"))
            if not isinstance(bundle, dict) or not bundle:
                errors.append(f"invalid Cosign bundle: {name}")
        except (OSError, json.JSONDecodeError):
            errors.append(f"invalid Cosign bundle: {name}")
    try:
        rollback = json.loads((dist / "upgrade-rollback-results.json").read_text(encoding="utf-8"))
        if rollback.get("schema") != "router.release-upgrade-rollback/v1":
            errors.append("upgrade and rollback evidence has an unknown schema")
    except (OSError, json.JSONDecodeError, AttributeError):
        errors.append("upgrade and rollback evidence is missing or malformed")
    try:
        images = json.loads((dist / "release-images.json").read_text(encoding="utf-8"))
        if images.get("schema") != "metrum-ai-router.release-image/v1":
            errors.append("release image manifest has an unknown schema")
        if images.get("version") != version:
            errors.append("release image manifest version does not match the release")
        if not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", images.get("base", "")):
            errors.append("release image base is not digest pinned")
        platforms = images.get("platforms", {})
        if set(platforms) != {"amd64", "arm64"}:
            errors.append("release image manifest must include amd64 and arm64")
        for arch, item in platforms.items():
            if not re.fullmatch(r"sha256:[0-9a-f]{64}", item.get("digest", "")):
                errors.append(f"invalid {arch} image digest")
            if not re.fullmatch(r"[0-9a-f]{64}", item.get("package_sha256", "")):
                errors.append(f"invalid {arch} release package digest")
            if set(item.get("binaries", {})) != {
                "metrum-ai-router", "metrum-ai-router-token-gen", "metrum-ai-router-usage-report",
                "metrum-ai-router-migrate", "metrum-ai-routerctl",
            }:
                errors.append(f"{arch} image manifest does not cover each release binary")
            elif any(not re.fullmatch(r"[0-9a-f]{64}", digest) for digest in item["binaries"].values()):
                errors.append(f"invalid binary checksum in {arch} image manifest")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", images.get("index", {}).get("digest", "")):
            errors.append("invalid multi-architecture image index digest")
    except (OSError, json.JSONDecodeError, AttributeError, TypeError):
        errors.append("release image manifest is missing or malformed")
    review_artifact_digest = None
    try:
        review_artifact = json.loads((dist / "release-review-artifact.json").read_text(encoding="utf-8"))
        digest = review_artifact.pop("artifact_digest")
        expected_digest = hashlib.sha256(
            json.dumps(review_artifact, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode("utf-8")
        ).hexdigest()
        if review_artifact.get("schema") != "metrum-ai-router-release-review-artifact/v3":
            errors.append("AI release review artifact has an unknown schema")
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or digest != expected_digest:
            errors.append("AI release review artifact digest is invalid")
        review_artifact_digest = digest
        if not review_artifact.get("base") or not review_artifact.get("head") or not isinstance(review_artifact.get("findings"), list):
            errors.append("AI release review artifact does not identify a commit range and findings")
        if review_artifact.get("provider") != "codex" or review_artifact.get("model") != "gpt-6.1-sol" or not review_artifact.get("codex_cli_version"):
            errors.append("AI release review artifact does not identify the selected Codex provider and CLI")
        if review_artifact.get("verdict") not in {"PASS", "BLOCK"}:
            errors.append("AI release review artifact has an invalid verdict")
        if not re.fullmatch(r"[0-9a-f]{40}", review_artifact.get("base_commit", "")) or not re.fullmatch(r"[0-9a-f]{40}", review_artifact.get("head", "")):
            errors.append("AI release review artifact does not pin full Git commits")
    except (OSError, json.JSONDecodeError, KeyError, TypeError, AttributeError):
        errors.append("AI release review artifact is missing or malformed")
    try:
        report = (dist / "AI-release-review.md").read_text(encoding="utf-8")
        if not PASS.search(report):
            errors.append("AI release review is missing a passing decision")
        elif review_artifact_digest and f"Review artifact SHA-256: `{review_artifact_digest}`" not in report:
            errors.append("AI release review report does not match the recorded artifact digest")
        elif f"- Model: `{review_artifact.get('model')}`" not in report:
            errors.append("AI release review report does not identify the artifact model")
        elif f"- Codex CLI: `{review_artifact.get('codex_cli_version')}`" not in report:
            errors.append("AI release review report does not identify the Codex CLI version")
    except OSError:
        errors.append("AI release review is missing")
    return errors


def validate_unsigned(dist: Path, version: str) -> list[str]:
    """Require the complete pre-sign payload and reject every extra entry."""
    errors: list[str] = []
    expected_names = set(payload_names(version))
    expected_archives = {item.format(version=version) for item in ARCHIVES}
    if not dist.is_dir():
        return ["release payload directory is missing"]
    entries = list(dist.iterdir())
    found_names = {path.name for path in entries}
    missing = sorted(expected_names - found_names)
    extra = sorted(found_names - expected_names)
    if missing:
        errors.append("missing unsigned release assets: " + ", ".join(missing))
    if extra:
        errors.append("unexpected unsigned release assets: " + ", ".join(extra))
    if any(not path.is_file() for path in entries):
        errors.append("unsigned release payload contains a non-file entry")
    try:
        inventory = json.loads((dist / "release-artifacts.json").read_text(encoding="utf-8"))
        inventory_items = inventory.get("artifacts", [])
        inventory_names = {item["filename"] for item in inventory_items}
        if (
            inventory.get("artifact_set") != "binary"
            or len(inventory_items) != len(expected_archives)
            or inventory_names != expected_archives
        ):
            errors.append("release inventory does not describe the complete binary package set")
    except (OSError, json.JSONDecodeError, KeyError, TypeError, AttributeError):
        errors.append("release inventory is missing or malformed")
    try:
        checksums: dict[str, str] = {}
        for line in (dist / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
            digest, name = line.split("  ", 1)
            if not re.fullmatch(r"[0-9a-f]{64}", digest) or name in checksums:
                raise ValueError
            checksums[name] = digest
        if set(checksums) != expected_archives:
            errors.append("checksum file does not cover the complete release package set")
        else:
            for name, digest in checksums.items():
                if (dist / name).is_file() and sha256_file(dist / name) != digest:
                    errors.append(f"checksum mismatch: {name}")
    except (OSError, ValueError):
        errors.append("checksum file is missing or malformed")
    try:
        rollback = json.loads((dist / "upgrade-rollback-results.json").read_text(encoding="utf-8"))
        if rollback.get("schema") != "router.release-upgrade-rollback/v1":
            errors.append("upgrade and rollback evidence has an unknown schema")
        checks = rollback.get("checks")
        failed_check = (
            not isinstance(checks, list)
            or not checks
            or not re.fullmatch(r"[0-9a-f]{40}", str(rollback.get("candidate_commit", "")))
            or not isinstance(rollback.get("previous_version"), str)
        )
        if isinstance(checks, list):
            check_names = {
                item.get("check") for item in checks if isinstance(item, dict)
            }
            required_checks = {
                "previous-release-json", "previous-serving-smoke",
                "kubernetes-migration-job-plan", "kubernetes-migration-job-apply",
                "candidate-resume",
                "candidate-verify-serving", "candidate-serving-smoke", "rollback-serving-smoke",
            }
            schema_checks = {
                name for name in check_names
                if isinstance(name, str) and re.fullmatch(r"kubernetes-migration-job-router-config-schema-\d+", name)
            }
            if not required_checks.issubset(check_names) or len(schema_checks) != 1:
                failed_check = True
            for item in checks:
                if not isinstance(item, dict) or not isinstance(item.get("check"), str) or not item["check"]:
                    failed_check = True
                    break
                # v4.8.0 is the known previous-release JSON regression that this
                # report records explicitly; all candidate/rollback gates must pass.
                if item["check"] == "previous-release-json":
                    expected_baseline_result = rollback.get("previous_version") == "v4.8.0"
                    if item.get("strict_json") is not (not expected_baseline_result):
                        failed_check = True
                elif any(key in item and item[key] is not True for key in ("passed", "compatible", "strict_json")):
                    failed_check = True
                if "scope_count" in item and (type(item["scope_count"]) is not int or item["scope_count"] < 1):
                    failed_check = True
        if rollback.get("candidate_version") != version or failed_check:
            errors.append("upgrade and rollback evidence is incomplete or contains a failed check")
    except (OSError, json.JSONDecodeError, AttributeError, TypeError):
        errors.append("upgrade and rollback evidence is missing or malformed")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=Path("dist"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--stage", choices=("unsigned", "signed"), default="signed")
    args = parser.parse_args()
    errors = validate_unsigned(args.dist_dir, args.version) if args.stage == "unsigned" else validate(args.dist_dir, args.version)
    if errors:
        print("release payload check failed: " + "; ".join(errors), file=sys.stderr)
        return 1
    print("release payload passed its unsigned check" if args.stage == "unsigned" else "release payload has four binary packages, signed archive and image SPDX SBOMs, checksums, manifests and digest-bound AI review")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
