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
    triggers = workflow.get("on", workflow.get(True))
    assert set(triggers) == {"push", "workflow_dispatch"}
    assert triggers["push"]["tags"] == ["v*"]
    assert "dry_run" in triggers["workflow_dispatch"]["inputs"]
    dispatch = triggers["workflow_dispatch"]["inputs"]["dry_run"]
    assert dispatch["type"] == "boolean" and dispatch["required"] is True and dispatch["default"] is True
    for name in ("hierarchy-matrix", "resolve", "upgrade-and-rollback", "ai-release-review"):
        guard = jobs[name].get("if", "")
        assert "github.ref == 'refs/heads/main'" in guard and "inputs.dry_run" in guard
    ai_review = jobs["ai-release-review"]
    assert ai_review["permissions"] == {"contents": "read"}
    ai_step = next(step for step in ai_review["steps"] if step.get("name") == "Review release diff and documentation")
    assert ai_step["env"]["REVIEW_BASE"] == "${{ needs.resolve.outputs.previous_tag }}"
    resolve_run = next(step["run"] for step in jobs["resolve"]["steps"] if step.get("name") == "Resolve immutable build metadata")
    assert "select_previous_release.py" in resolve_run and "git describe" not in resolve_run
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
    upgrade_step = next(step for step in upgrade["steps"] if step.get("name") == "Run upgrade and rollback checks")
    assert upgrade_step["env"]["PREVIOUS_TAG"] == "${{ needs.resolve.outputs.previous_tag }}"
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
    signing = names.index("Sign Windows release packages after reproducibility comparison")
    rebuild_steps = jobs["reproducible-build"]["steps"]
    rebuild_names = [step.get("name") for step in rebuild_steps]
    assert rebuild_names.index("Rebuild binary packages on a clean runner") < rebuild_names.index("Compare package checksums")
    assert "reproducible-build" in publish["needs"]
    assert signing > names.index("Verify release environment protection before publish")
    assert "release_image_oci_layout.py" in "\n".join(step.get("run", "") for step in jobs["dry-run-verify"]["steps"])
    dry_run = jobs["dry-run-verify"]
    assert "workflow_dispatch" in dry_run["if"] and "inputs.dry_run" in dry_run["if"]
    assert "environment" not in dry_run
    assert dry_run["permissions"] == {"contents": "read"}
    dry_run_runs = "\n".join(step.get("run", "") for step in dry_run["steps"])
    assert "crane push" not in dry_run_runs and "cosign" not in dry_run_runs and "gh release create" not in dry_run_runs
    assert {"package-binaries", "reproducible-build", "ai-release-review", "upgrade-and-rollback"} <= set(dry_run["needs"])
    assert "check_release_dry_run.py" in "\n".join(step.get("run", "") for step in dry_run["steps"])
    assert "release_ai_review.py" in "\n".join(step.get("run", "") for step in ai_review["steps"])
    verify = steps[verify]["run"]
    assert verify.count("--signer-workflow") >= 3 and verify.count("--source-ref") >= 3
    assert "dist/SHA256SUMS" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/release-artifacts.json" in steps[archive_attest]["with"]["subject-path"]
    assert 'ref="${IMAGE}@${digest}"' in steps[sign].get("run", "")


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

    def test_dispatch_is_dry_run_only_and_main_only(self):
        doc = copy.deepcopy(self.workflow)
        trigger_key = "on" if "on" in doc else True
        del doc[trigger_key]["workflow_dispatch"]["inputs"]["dry_run"]
        with self.assertRaises(AssertionError):
            validate(doc)


if __name__ == "__main__":
    unittest.main()
