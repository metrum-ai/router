# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Validate the non-publishing release dry-run evidence bundle."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import sys
import subprocess

from release_image_oci_layout import image_descriptor
from release_ai_review import evaluate, load_committed_artifact, verify_generated_file_checks


def validate(dist: Path, layout: Path, version: str, commit: str, previous: str, previous_commit: str) -> list[str]:
    errors: list[str] = []
    try:
        inventory = json.loads((dist / "release-artifacts.json").read_text(encoding="utf-8"))
        if inventory.get("version") != version or inventory.get("commit") != commit or inventory.get("artifact_set") != "binary":
            errors.append("binary inventory metadata does not match dry-run inputs")
        entries = inventory.get("artifacts", [])
        names = {entry.get("filename") for entry in entries if isinstance(entry, dict)}
        if len(names) != 4:
            errors.append("binary inventory does not contain four release packages")
        sums = {}
        for line in (dist / "SHA256SUMS").read_text(encoding="utf-8").splitlines():
            digest, name = line.split("  ", 1)
            if not re.fullmatch(r"[0-9a-f]{64}", digest):
                raise ValueError("invalid checksum")
            sums[name] = digest
        if set(sums) != names:
            errors.append("checksum list does not match binary inventory")
        for name, expected in sums.items():
            if hashlib.sha256((dist / name).read_bytes()).hexdigest() != expected:
                errors.append(f"release package checksum mismatch: {name}")
    except (OSError, json.JSONDecodeError, ValueError, AttributeError, TypeError) as exc:
        errors.append(f"binary inventory or checksums are invalid: {exc}")

    review_artifact_digest = None
    review_head = commit
    artifact: dict[str, object] = {}
    try:
        artifact = json.loads((dist / "release-review-artifact.json").read_text(encoding="utf-8"))
        expected_artifact, review_head = load_committed_artifact(
            Path("docs/release-reviews"), previous, previous_commit, commit
        )
        verify_generated_file_checks(expected_artifact, review_head)
        if artifact != expected_artifact:
            errors.append("AI review artifact differs from the committed range artifact")
        responses = json.loads(Path("docs/release-review-responses.json").read_text(encoding="utf-8"))
        acceptances = responses.get("acceptances", {}) if isinstance(responses, dict) else {}
        evaluated, unanswered = evaluate(expected_artifact, acceptances, previous, review_head)
        if evaluated["decision"] != "pass" or unanswered:
            errors.append("AI review artifact has unanswered blocking findings")
        digest = artifact.pop("artifact_digest")
        expected = hashlib.sha256(json.dumps(artifact, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode("utf-8")).hexdigest()
        if artifact.get("schema") != "metrum-ai-router-release-review-artifact/v3" or artifact.get("base") != previous or artifact.get("base_commit") != previous_commit or artifact.get("head") != review_head:
            errors.append("AI review artifact does not match the requested production range")
        if artifact.get("provider") != "codex" or artifact.get("model") != "gpt-6.1-sol" or not artifact.get("codex_cli_version"):
            errors.append("AI review artifact does not identify the selected Codex review tool")
        if artifact.get("verdict") not in {"PASS", "BLOCK"}:
            errors.append("AI review artifact verdict is invalid")
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or digest != expected:
            errors.append("AI review artifact digest is invalid")
        review_artifact_digest = digest
    except (OSError, json.JSONDecodeError, ValueError, AttributeError, TypeError, KeyError):
        errors.append("AI review artifact is missing or invalid")

    try:
        report = (dist / "AI-release-review.md").read_text(encoding="utf-8")
        if "- Decision: **PASS**" not in report or f"- Reviewed range: `{previous}..{review_head}`" not in report:
            errors.append("AI review does not pass for the requested production range")
        elif f"- Base tag commit: `{previous_commit}`" not in report:
            errors.append("AI review report does not pin the previous release commit")
        elif review_artifact_digest and f"Review artifact SHA-256: `{review_artifact_digest}`" not in report:
            errors.append("AI review report does not match the stored artifact digest")
        elif f"- Model: `{artifact.get('model')}`" not in report:
            errors.append("AI review report does not identify the artifact model")
        elif f"- Codex CLI: `{artifact.get('codex_cli_version')}`" not in report:
            errors.append("AI review report does not identify the Codex CLI version")
    except ValueError as exc:
        errors.append(f"AI review validation failed: {exc}")
    except subprocess.CalledProcessError:
        errors.append("AI review Git or generated-check validation command failed")
    except (OSError, json.JSONDecodeError, ValueError, TypeError, AttributeError):
        errors.append("AI release review artifact or report could not be validated")

    try:
        upgrade = json.loads((dist / "upgrade-rollback-results.json").read_text(encoding="utf-8"))
        if upgrade.get("schema") != "router.release-upgrade-rollback/v1":
            errors.append("upgrade/rollback report schema is invalid")
        if upgrade.get("previous_version") != previous or upgrade.get("candidate_version") != version or upgrade.get("candidate_commit") != commit:
            errors.append("upgrade/rollback report does not match dry-run inputs")
    except (OSError, json.JSONDecodeError, AttributeError):
        errors.append("upgrade/rollback report is missing or invalid")

    ref = f"ghcr.io/metrum-ai/router:{version}"
    for architecture in ("amd64", "arm64"):
        try:
            _, config = image_descriptor(layout, ref, f"linux/{architecture}")
            runtime = config.get("config", {})
            labels = runtime.get("Labels", {})
            if labels.get("org.opencontainers.image.version") != version or labels.get("org.opencontainers.image.revision") != commit:
                errors.append(f"{architecture} OCI image metadata does not match the dry-run")
        except (OSError, ValueError, KeyError, TypeError):
            errors.append(f"local OCI layout is missing linux/{architecture}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dist-dir", type=Path, default=Path("dist"))
    parser.add_argument("--oci-layout", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--previous", required=True)
    parser.add_argument("--previous-commit", required=True)
    args = parser.parse_args()
    errors = validate(args.dist_dir, args.oci_layout, args.version, args.commit, args.previous, args.previous_commit)
    if errors:
        print("release dry-run verification failed: " + "; ".join(errors), file=sys.stderr)
        return 1
    print("release dry-run artifacts, review range, upgrade report and local OCI index verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
