# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from check_release_payload import ARCHIVES, validate, validate_unsigned
from stage_release_payload import payload_names, stage


ROOT = Path(__file__).resolve().parents[1]
REAL_UPGRADE_REPORT = ROOT / "scripts/testdata/upgrade-rollback-results-real-v4.8.0.json"


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
        for name in {path.name for path in root.glob("*.spdx.json")}:
            (root / (name + ".sigstore.json")).write_text(json.dumps({"bundle": True}), encoding="utf-8")
        review_artifact = {
            "schema": "metrum-ai-router-release-review-artifact/v3",
            "base": "v4.8.0",
            "base_commit": "b" * 40,
            "head": "a" * 40,
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
        (root / "release-review-artifact.json").write_text(json.dumps(review_artifact), encoding="utf-8")
        (root / "AI-release-review.md").write_text(
            f"- Review artifact SHA-256: `{review_artifact['artifact_digest']}`\n- Decision: **PASS**\n- Model: `gpt-6.1-sol`\n- Codex CLI: `codex-cli 0.169.0`\n", encoding="utf-8"
        )

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

    def test_rejects_missing_sbom_signature_and_tampered_review_artifact(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_payload(root)
            (root / "metrum-ai-router-v5.0.0-linux-amd64.spdx.json.sigstore.json").unlink()
            artifact_path = root / "release-review-artifact.json"
            artifact = json.loads(artifact_path.read_text(encoding="utf-8"))
            artifact["summary"] = "Changed after review."
            artifact_path.write_text(json.dumps(artifact), encoding="utf-8")
            errors = validate(root, "v5.0.0")
            self.assertTrue(any("missing release assets" in error for error in errors))
            self.assertTrue(any("artifact digest is invalid" in error for error in errors))

    def test_rejects_obsolete_image_assets(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_payload(root)
            self.assertEqual(len(list(root.iterdir())), 21)
            for name in ("release-images.json", "metrum-ai-router-v5.0.0-linux-amd64-image.spdx.json"):
                with self.subTest(name=name):
                    (root / name).touch()
                    self.assertIn("unexpected release assets: " + name, validate(root, "v5.0.0"))
                    (root / name).unlink()

    def make_unsigned_payload(self, root: Path, version: str = "v5.0.0-rc.3") -> None:
        archives = {item.format(version=version) for item in ARCHIVES}
        sums = []
        for name in sorted(archives):
            content = name.encode()
            (root / name).write_bytes(content)
            sums.append(hashlib.sha256(content).hexdigest() + "  " + name)
        (root / "SHA256SUMS").write_text("\n".join(sums) + "\n", encoding="utf-8")
        (root / "release-artifacts.json").write_text(json.dumps({
            "artifact_set": "binary", "artifacts": [{"filename": name} for name in sorted(archives)],
        }), encoding="utf-8")
        report = json.loads(REAL_UPGRADE_REPORT.read_text(encoding="utf-8"))
        report["candidate_version"] = version
        (root / "upgrade-rollback-results.json").write_text(json.dumps(report), encoding="utf-8")

    def test_unsigned_stage_accepts_exact_payload_and_rejects_extra_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_unsigned_payload(root)
            self.assertEqual(validate_unsigned(root, "v5.0.0-rc.3"), [])
            (root / "engine-advisories.json").write_text("{}\n", encoding="utf-8")
            errors = validate_unsigned(root, "v5.0.0-rc.3")
            self.assertTrue(any("unexpected unsigned release assets: engine-advisories.json" in error for error in errors), errors)

    def test_shared_stager_excludes_pkg_and_stages_same_unsigned_payload(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "dist"
            destination = root / "unsigned-payload"
            source.mkdir()
            self.make_unsigned_payload(source, "v5.0.0-dry-run")
            (source / "pkg").mkdir()
            (source / "engine-advisories.json").write_text("{}\n", encoding="utf-8")
            names = stage(source, destination, "v5.0.0-dry-run")
            self.assertEqual(set(names), set(payload_names("v5.0.0-dry-run")))
            self.assertEqual({path.name for path in destination.iterdir()}, set(names))
            self.assertEqual(validate_unsigned(destination, "v5.0.0-dry-run"), [])

    def test_stager_binary_upload_uses_same_canonical_list_without_upgrade_report(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "dist"
            destination = root / "release-binaries"
            source.mkdir()
            self.make_unsigned_payload(source, "v5.0.0-rc.3")
            names = stage(source, destination, "v5.0.0-rc.3", include_upgrade_results=False)
            self.assertNotIn("upgrade-rollback-results.json", names)
            self.assertEqual({path.name for path in destination.iterdir()}, set(names))

    def test_unsigned_report_rejects_failed_candidate_check(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_unsigned_payload(root, "v5.0.0-dry-run")
            report_path = root / "upgrade-rollback-results.json"
            report = json.loads(report_path.read_text(encoding="utf-8"))
            candidate_check = next(item for item in report["checks"] if item["check"] == "candidate-resume")
            candidate_check["compatible"] = False
            report_path.write_text(json.dumps(report), encoding="utf-8")
            errors = validate_unsigned(root, "v5.0.0-dry-run")
            self.assertTrue(any("incomplete or contains a failed check" in error for error in errors), errors)

    def test_unsigned_report_rejects_missing_candidate_outcome(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.make_unsigned_payload(root, "v5.0.0-dry-run")
            report_path = root / "upgrade-rollback-results.json"
            report = json.loads(report_path.read_text(encoding="utf-8"))
            candidate_check = next(item for item in report["checks"] if item["check"] == "candidate-resume")
            candidate_check.pop("compatible")
            report_path.write_text(json.dumps(report), encoding="utf-8")
            errors = validate_unsigned(root, "v5.0.0-dry-run")
            self.assertTrue(any("incomplete or contains a failed check" in error for error in errors), errors)


if __name__ == "__main__":
    unittest.main()
