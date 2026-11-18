# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from check_release_dry_run import validate
from release_ai_review import resolve_git_commit


class CheckReleaseDryRunTest(unittest.TestCase):
    def test_accepts_complete_matching_evidence_and_rejects_tampering(self) -> None:
        version, commit, previous = "v5.0.0-dry-run", "a" * 40, "v4.8.0"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            dist = root / "dist"
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
                "schema": "metrum-ai-router-release-review-artifact/v3",
                "base": previous,
                "base_commit": resolve_git_commit(previous),
                "head": commit,
                "provider": "codex",
                "model": "gpt-6.1-sol",
                "codex_cli_version": "codex-cli 0.169.0",
                "verdict": "PASS",
                "summary": "No findings.",
                "findings": [],
                "file_coverage": [],
            }
            review_artifact["artifact_digest"] = hashlib.sha256(json.dumps(
                review_artifact, ensure_ascii=True, separators=(",", ":"), sort_keys=True
            ).encode("utf-8")).hexdigest()
            (dist / "release-review-artifact.json").write_text(json.dumps(review_artifact), encoding="utf-8")
            (dist / "AI-release-review.md").write_text(
                f"- Decision: **PASS**\n- Reviewed range: `{previous}..{commit}`\n"
                f"- Base tag commit: `{review_artifact['base_commit']}`\n"
                f"- Review artifact SHA-256: `{review_artifact['artifact_digest']}`\n- Model: `gpt-6.1-sol`\n"
                f"- Codex CLI: `codex-cli 0.169.0`\n", encoding="utf-8")
            (dist / "upgrade-rollback-results.json").write_text(json.dumps({
                "schema": "router.release-upgrade-rollback/v1", "previous_version": previous,
                "candidate_version": version, "candidate_commit": commit,
            }), encoding="utf-8")
            with patch("check_release_dry_run.load_committed_artifact", return_value=(review_artifact, commit)), \
                 patch("check_release_dry_run.verify_generated_file_checks"):
                self.assertEqual(validate(dist, version, commit, previous, review_artifact['base_commit']), [])
            (dist / names[0]).write_bytes(b"tampered")
            with patch("check_release_dry_run.load_committed_artifact", return_value=(review_artifact, commit)), \
                 patch("check_release_dry_run.verify_generated_file_checks"):
                self.assertTrue(any("checksum mismatch" in error for error in validate(dist, version, commit, previous, review_artifact['base_commit'])))


if __name__ == "__main__":
    unittest.main()
