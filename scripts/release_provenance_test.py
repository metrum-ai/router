#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline contract tests for release asset provenance coverage."""

import copy
import glob
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/release.yml"
PIN = "96278af6caaf10aea03fd8d33a09a777ca52d62f"  # attest-build-provenance v3.2.0
SUBJECTS = {
    "dist/metrum-ai-router-*.tar.gz",
    "dist/metrum-ai-router-*.zip",
    "dist/SHA256SUMS",
    "dist/release-artifacts.json",
}


def validate(workflow):
    job = workflow["jobs"]["package"]
    permissions = job["permissions"]
    assert permissions.get("contents") == "write"
    assert permissions.get("id-token") == "write"
    assert permissions.get("attestations") == "write"
    steps = job["steps"]
    names = [step.get("name") for step in steps]
    inventory = names.index("Inventory and checksums")
    attest = names.index("Attest release asset provenance")
    upload = names.index("Upload inventory-validated release assets")
    publish = names.index("Publish GitHub Release with assets")
    assert inventory < attest < upload < publish
    step = steps[attest]
    assert step["uses"] == f"actions/attest-build-provenance@{PIN}"
    assert step.get("if") == "startsWith(github.ref, 'refs/tags/')"
    assert not step.get("continue-on-error", False)
    subjects = set(step["with"]["subject-path"].splitlines())
    assert subjects == SUBJECTS
    assert set(steps[upload]["with"]["path"].splitlines()) == subjects
    release_script = steps[publish]["run"]
    assert steps[publish].get("if") == step["if"]
    # The publisher selects archives via find and two metadata files via
    # printf. Check both selectors so additions cannot bypass attestation.
    archive_patterns = set(re.findall(r"-name '([^']+)'", release_script))
    assert archive_patterns == {"metrum-ai-router-*.tar.gz", "metrum-ai-router-*.zip"}
    selection = release_script.split("mapfile -t ASSETS < <(", 1)[1].split("} | sort -u", 1)[0]
    selection_lines = [line.strip() for line in selection.splitlines() if line.strip()]
    assert selection_lines == [
        "{",
        "find dist -maxdepth 1 -type f \\( -name 'metrum-ai-router-*.tar.gz' -o -name 'metrum-ai-router-*.zip' \\)",
        "printf '%s\\n' dist/SHA256SUMS dist/release-artifacts.json",
    ]
    assert '"${ASSETS[@]}"' in release_script
    assert "gh release create" in release_script


class ProvenanceWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))

    def test_release_contract(self):
        validate(self.workflow)

    def test_synthetic_release_asset_selection_matches_subjects(self):
        steps = self.workflow["jobs"]["package"]["steps"]
        publish = next(s for s in steps if s.get("name") == "Publish GitHub Release with assets")
        attest = next(s for s in steps if s.get("name") == "Attest release asset provenance")
        selector = publish["run"].split("mapfile -t ASSETS < <(", 1)[1].split("} | sort -u", 1)[0]
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            dist = root / "dist"
            dist.mkdir()
            for name in (
                "metrum-ai-router-v1.2.3-linux-amd64.tar.gz",
                "metrum-ai-router-v1.2.3-linux-arm64.tar.gz",
                "metrum-ai-router-v1.2.3-windows-amd64.zip",
                "metrum-ai-router-v1.2.3-windows-arm64.zip",
                "SHA256SUMS", "release-artifacts.json", "unrelated.txt",
            ):
                (dist / name).write_text("fixture", encoding="utf-8")
            # Execute only the checked, fixed selection block; no gh invocation.
            command = "set -euo pipefail\n" + selector + "} | sort -u\n"
            published = set(subprocess.check_output(["bash", "-c", command], cwd=root, text=True).splitlines())
            attested = set()
            for pattern in attest["with"]["subject-path"].splitlines():
                attested.update(glob.glob(pattern, root_dir=root))
            self.assertEqual(published, attested)
            self.assertEqual(len(published), 6)

    def test_missing_zip_attestation_is_rejected(self):
        doc = copy.deepcopy(self.workflow)
        step = next(s for s in doc["jobs"]["package"]["steps"] if s.get("name") == "Attest release asset provenance")
        step["with"]["subject-path"] = step["with"]["subject-path"].replace("dist/metrum-ai-router-*.zip\n", "")
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_missing_checksum_attestation_is_rejected(self):
        doc = copy.deepcopy(self.workflow)
        step = next(s for s in doc["jobs"]["package"]["steps"] if s.get("name") == "Attest release asset provenance")
        step["with"]["subject-path"] = step["with"]["subject-path"].replace("dist/SHA256SUMS\n", "")
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_unattested_new_archive_selector_is_rejected(self):
        doc = copy.deepcopy(self.workflow)
        step = next(s for s in doc["jobs"]["package"]["steps"] if s.get("name") == "Publish GitHub Release with assets")
        step["run"] += "\nfind dist -name 'metrum-ai-router-*.msi'\n"
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_missing_permission_is_rejected(self):
        doc = copy.deepcopy(self.workflow)
        del doc["jobs"]["package"]["permissions"]["id-token"]
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_attestation_cannot_be_skipped_on_tag(self):
        doc = copy.deepcopy(self.workflow)
        step = next(s for s in doc["jobs"]["package"]["steps"] if s.get("name") == "Attest release asset provenance")
        step["if"] = "false"
        with self.assertRaises(AssertionError):
            validate(doc)


if __name__ == "__main__":
    unittest.main()
