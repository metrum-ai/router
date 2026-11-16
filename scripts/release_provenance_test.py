#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline contract tests for protected, daemonless release publication."""

from __future__ import annotations

import copy
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/release.yml"
PIN = "4d101475d8b20a2381f78447822ac1eab6504dd8"
SUBJECTS = {
    "dist/metrum-ai-router-*.tar.gz",
    "dist/metrum-ai-router-*.zip",
    "dist/upgrade-rollback-results.json",
}


def validate(workflow):
    jobs = workflow["jobs"]
    dispatch_guard = "github.event_name != 'workflow_dispatch' || github.ref == 'refs/heads/main'"
    assert jobs["hierarchy-matrix"].get("if") == dispatch_guard
    assert jobs["resolve"].get("if") == dispatch_guard
    publish = jobs["publish-release"]
    assert publish.get("environment") == "release"
    assert publish.get("if", "").startswith("always() && startsWith(github.ref, 'refs/tags/')")
    assert publish["permissions"].get("packages") == "write"
    assert publish["permissions"].get("id-token") == "write"
    assert publish["permissions"].get("attestations") == "write"
    assert set(publish["needs"]) == {"resolve", "package-binaries", "reproducible-build", "ai-release-review", "upgrade-and-rollback"}
    upgrade = jobs["upgrade-and-rollback"]
    assert upgrade["needs"] == ["resolve", "package-binaries"]
    assert "release_upgrade_rollback.py" in "\n".join(step.get("run", "") for step in upgrade["steps"])
    assert any(step.get("name") == "Download and verify previous production package" for step in upgrade["steps"])
    assert "docker-runner-preflight" not in jobs and "package-images" not in jobs
    steps = publish["steps"]
    names = [step.get("name") for step in steps]
    environment = names.index("Verify release environment protection before publish")
    assemble = names.index("Assemble and publish multi-architecture image from release packages")
    smoke = names.index("Smoke the Crane-built image without a daemon")
    sbom = names.index("Generate image SPDX SBOMs and attach them")
    sign = names.index("Sign images keylessly")
    archives = names.index("Sign release archives with Cosign")
    image_attest = names.index("Attest OCI image index")
    arch_attest = [names.index("Attest linux/amd64 image"), names.index("Attest linux/arm64 image")]
    verify = names.index("Verify attestations and require complete payload")
    publish_release = names.index("Confirm payload and publish immutable release")
    assert environment < assemble < smoke < sbom < sign < archives < image_attest < verify < publish_release
    assert all(image_attest < index < verify for index in arch_attest)
    assert steps[image_attest]["uses"] == f"actions/attest-build-provenance@{PIN}"
    assert steps[image_attest]["with"].get("push-to-registry") is True
    assert steps[image_attest]["with"].get("subject-name") == "${{ env.IMAGE }}"
    archive_attest = names.index("Attest release archives")
    assert SUBJECTS <= set(steps[archive_attest]["with"]["subject-path"].splitlines())
    assert "release_image_layer.py" in steps[assemble]["run"]
    assert '"${CRANE}" mutate "$base" --append "$layer"' in steps[assemble]["run"]
    assert "digest --tarball" in steps[assemble]["run"]
    assert "existing immutable image ref" in steps[assemble]["run"]
    assert '"${CRANE}" index append' in steps[assemble]["run"]
    assert "release_image_smoke.py" in steps[smoke]["run"]
    all_runs = "\n".join(step.get("run", "") for step in steps)
    assert "cosign sign --yes" in all_runs and 'gh attestation verify "oci://' in all_runs
    assert "cosign attach sbom" in all_runs
    assert "docker.sock" not in all_runs and "docker build" not in all_runs
    assert "install_crane.sh" in all_runs
    assert "upgrade-rollback-results.json" in all_runs
    rebuild = jobs["reproducible-build"]
    assert "compare_release_checksums.py" in "\n".join(step.get("run", "") for step in rebuild["steps"])


class ProvenanceWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))

    def test_release_contract(self):
        validate(self.workflow)

    def test_release_job_requires_environment_protection(self):
        doc = copy.deepcopy(self.workflow)
        del doc["jobs"]["publish-release"]["environment"]
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_image_attestation_is_mandatory(self):
        doc = copy.deepcopy(self.workflow)
        doc["jobs"]["publish-release"]["steps"] = [
            step for step in doc["jobs"]["publish-release"]["steps"]
            if step.get("name") != "Attest OCI image index"
        ]
        with self.assertRaises(ValueError):
            validate(doc)

    def test_daemonless_image_build_and_smoke_are_mandatory(self):
        doc = copy.deepcopy(self.workflow)
        doc["jobs"]["publish-release"]["steps"] = [
            step for step in doc["jobs"]["publish-release"]["steps"]
            if step.get("name") != "Smoke the Crane-built image without a daemon"
        ]
        with self.assertRaises(ValueError):
            validate(doc)

    def test_release_payload_requires_upgrade_evidence(self):
        doc = copy.deepcopy(self.workflow)
        publish = doc["jobs"]["publish-release"]
        publish["needs"].remove("upgrade-and-rollback")
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_manual_release_dispatch_is_restricted_to_main(self):
        doc = copy.deepcopy(self.workflow)
        del doc["jobs"]["resolve"]["if"]
        with self.assertRaises(AssertionError):
            validate(doc)


if __name__ == "__main__":
    unittest.main()
