#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline contract tests for protected, daemonless release publication."""

from __future__ import annotations

import copy
import unittest
from pathlib import Path

import yaml
from release_image_smoke import smoke_caller_credentials

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/release.yml"
DRY_RUN_WORKFLOW = ROOT / ".github/workflows/release-dry-run.yml"
PIN = "4d101475d8b20a2381f78447822ac1eab6504dd8"
SUBJECTS = {
    "dist/metrum-ai-router-*.tar.gz",
    "dist/metrum-ai-router-*.zip",
    "dist/upgrade-rollback-results.json",
    "dist/release-images.json",
    "dist/AI-release-review.md",
    "dist/release-review-artifact.json",
}


def validate(workflow):
    jobs = workflow["jobs"]
    triggers = workflow.get("on", workflow.get(True))
    assert set(triggers) == {"push"}
    assert triggers["push"]["tags"] == ["v*"]
    ai_review = jobs["ai-release-review"]
    assert ai_review["timeout-minutes"] >= 20
    assert ai_review["permissions"] == {"actions": "read", "checks": "read", "contents": "read"}
    assert ai_review.get("environment") is None
    ai_step = next(step for step in ai_review["steps"] if step.get("name") == "Validate the committed release review artifact")
    assert ai_step["env"]["REVIEW_BASE"] == "${{ needs.resolve.outputs.previous_tag }}"
    assert "--committed-artifact-dir docs/release-reviews" in ai_step["run"]
    assert "ANTHROPIC_API_KEY" not in str(ai_step)
    assert not any("cache" in step.get("name", "").lower() for step in ai_review["steps"])
    resolve_run = next(step["run"] for step in jobs["resolve"]["steps"] if step.get("name") == "Resolve immutable build metadata")
    assert "select_previous_release.py" in resolve_run and "git describe" not in resolve_run
    assert 'git merge-base --is-ancestor "${commit}" origin/main' in resolve_run
    assert 'git rev-parse --verify "${previous_tag}^{commit}"' in resolve_run
    assert 'printf \'previous_commit=%s\\n\' "${previous_commit}"' in resolve_run
    upgrade = jobs["upgrade-and-rollback"]
    upgrade_step = next(step for step in upgrade["steps"] if step.get("name") == "Run upgrade and rollback checks")
    assert upgrade_step["env"]["PREVIOUS_TAG"] == "${{ needs.resolve.outputs.previous_tag }}"
    publish = jobs["publish-release"]
    assert publish.get("environment") == "release"
    assert publish.get("if", "").startswith("always() && startsWith(github.ref, 'refs/tags/')")
    assert publish["permissions"].get("packages") == "write"
    assert publish["permissions"].get("id-token") == "write"
    assert publish["permissions"].get("attestations") == "write"
    assert set(publish["needs"]) == {"resolve", "package-binaries", "reproducible-build", "ai-release-review", "upgrade-and-rollback"}
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
    sbom_sign = names.index("Sign and verify SPDX SBOM assets")
    sign = names.index("Sign images keylessly")
    archives = names.index("Sign release archives with Cosign")
    image_attest = names.index("Attest OCI image index")
    arch_attest = [names.index("Attest linux/amd64 image"), names.index("Attest linux/arm64 image")]
    verify = names.index("Verify attestations and require complete payload")
    publish_release = names.index("Confirm payload and publish immutable release")
    assert environment < assemble < smoke < sbom < sbom_sign < sign < archives < image_attest < verify < publish_release
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
    assert "cosign sign-blob" in steps[sbom_sign]["run"] and "cosign verify-blob" in steps[sbom_sign]["run"]
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
    verify = steps[verify]["run"]
    assert verify.count("--signer-workflow") >= 3 and verify.count("--source-ref") >= 3
    assert "dist/SHA256SUMS" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/release-artifacts.json" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/release-images.json" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/AI-release-review.md" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/release-review-artifact.json" in steps[archive_attest]["with"]["subject-path"]
    assert "release-review-artifact.json" in steps[names.index("Confirm payload and publish immutable release")]["run"]
    assert "-eq 26" in steps[names.index("Confirm payload and publish immutable release")]["run"]
    assert 'ref="${IMAGE}@${digest}"' in steps[sign].get("run", "")
    release_command = steps[publish_release]["run"]
    assert 'gh release create "${VERSION}" --verify-tag' in release_command
    assert not any(token in all_runs for token in ("git tag -a", "git tag -m", "git tag --annotate"))
    assert "git push origin refs/tags" not in all_runs


def validate_binary_inventory_snapshot(workflow):
    calls = [
        line.strip()
        for job in workflow["jobs"].values()
        for step in job.get("steps", [])
        for line in step.get("run", "").splitlines()
        if "scripts/release_artifact_inventory.py" in line and "--set binary" in line
    ]
    assert calls, "workflow must validate binary release inventories"
    assert all("--advisories-snapshot" in call for call in calls), calls


class ProvenanceWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
        cls.dry_run_workflow = yaml.safe_load(DRY_RUN_WORKFLOW.read_text(encoding="utf-8"))

    def test_release_contract(self):
        validate(self.workflow)
        validate_binary_inventory_snapshot(self.workflow)

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

    def test_release_publish_workflow_has_no_manual_dispatch(self):
        doc = copy.deepcopy(self.workflow)
        trigger_key = "on" if "on" in doc else True
        doc[trigger_key]["workflow_dispatch"] = {"inputs": {"dry_run": {"type": "boolean"}}}
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_full_dry_run_is_main_only_read_only_and_nonpublishing(self):
        workflow = self.dry_run_workflow
        validate_binary_inventory_snapshot(workflow)
        triggers = workflow.get("on", workflow.get(True))
        dispatch = triggers["workflow_dispatch"]["inputs"]["dry_run"]
        self.assertEqual(dispatch, {"description": "Run the full release validation without publishing", "type": "boolean", "required": True, "default": True})
        jobs = workflow["jobs"]
        dry_run = jobs["dry-run"]
        self.assertIn("hierarchy-agent-chain", dry_run["needs"])
        self.assertIn("hierarchy-codex-chain", dry_run["needs"])
        all_runs = "\n".join(step.get("run", "") for job in jobs.values() for step in job.get("steps", []))
        self.assertIn("make package", all_runs)
        dry_run_steps = jobs["dry-run"]["steps"]
        dry_run_names = [step.get("name") for step in dry_run_steps]
        advisory_step = next(step for step in dry_run_steps if step.get("name") == "Generate engine advisory snapshot for release packages")
        self.assertEqual(advisory_step["env"], {"GITHUB_TOKEN": "${{ github.token }}"})
        self.assertIn("engine_advisory_gate.py --check --snapshot", advisory_step["run"])
        self.assertIn("ENGINE_ADVISORY_SNAPSHOT=%s", advisory_step["run"])
        package_build = next(step for step in dry_run_steps if step.get("name") == "Build release packages")
        self.assertEqual(package_build["run"], "make package")
        first_inventory = next(step for step in dry_run_steps if step.get("name") == "Record the first build inventory")
        self.assertIn("--advisories-snapshot", first_inventory["run"])
        rebuild_inventory = next(step for step in dry_run_steps if step.get("name") == "Record rebuilt inventory and compare packages")
        self.assertIn("--advisories-snapshot", rebuild_inventory["run"])
        review_step = next(step for step in dry_run_steps if step.get("id") == "review")
        self.assertIn("--committed-artifact-dir docs/release-reviews", review_step["run"])
        preserve = dry_run_names.index("Preserve first build output")
        reset = dry_run_names.index("Reset generated package outputs before clean-cache rebuild")
        rebuild = dry_run_names.index("Rebuild with clean Go caches")
        self.assertLess(preserve, reset)
        self.assertLess(reset, rebuild)
        reset_run = dry_run_steps[reset]["run"]
        self.assertIn("git restore --worktree -- .", reset_run)
        self.assertIn("git clean -fd", reset_run)
        self.assertIn("rm -rf dist", reset_run)
        self.assertIn("validate_release_clean.py", reset_run)
        self.assertIn("compare_release_checksums.py", all_runs)
        self.assertIn("release_ai_review.py", all_runs)
        self.assertIn("release_upgrade_rollback.py", all_runs)
        self.assertIn("release_image_oci_layout.py", all_runs)
        self.assertIn("check_release_dry_run.py", all_runs)
        self.assertNotIn("crane push", all_runs)
        self.assertNotIn("cosign", all_runs)
        self.assertNotIn("gh release create", all_runs)
        self.assertIsNone(dry_run.get("environment"))
        self.assertEqual(dry_run["permissions"], {"actions": "read", "checks": "read", "contents": "read"})
        review_step = next(step for step in dry_run_steps if step.get("id") == "review")
        self.assertEqual(review_step.get("env"), {"GH_TOKEN": "${{ github.token }}"})
        for step in dry_run_steps:
            if step is not review_step:
                self.assertNotIn("ANTHROPIC_API_KEY", step.get("env", {}))
        self.assertNotIn("ANTHROPIC_API_KEY", all_runs)
        for name, job in jobs.items():
            self.assertEqual(job.get("if"), "github.ref == 'refs/heads/main' && inputs.dry_run")
            expected_permissions = {"actions": "read", "checks": "read", "contents": "read"} if name == "dry-run" else {"contents": "read"}
            self.assertEqual(job.get("permissions"), expected_permissions)
            if name != "dry-run":
                self.assertNotIn("environment", job)
            for step in job.get("steps", []):
                if step.get("uses", "").startswith("actions/checkout@"):
                    self.assertEqual(step.get("with", {}).get("ref"), "main")

    def test_binary_inventory_validation_requires_advisory_snapshot_everywhere(self):
        for source in (self.workflow, self.dry_run_workflow):
            doc = copy.deepcopy(source)
            target = next(
                step
                for job in doc["jobs"].values()
                for step in job.get("steps", [])
                if "scripts/release_artifact_inventory.py" in step.get("run", "")
                and "--set binary" in step.get("run", "")
            )
            target["run"] = target["run"].replace(" --advisories-snapshot", "")
            with self.assertRaises(AssertionError):
                validate_binary_inventory_snapshot(doc)

    def test_dry_run_checkout_does_not_follow_workflow_dispatch_ref(self):
        doc = copy.deepcopy(self.dry_run_workflow)
        checkout = next(step for job in doc["jobs"].values() for step in job.get("steps", []) if step.get("uses", "").startswith("actions/checkout@"))
        checkout["with"]["ref"] = "${{ github.ref }}"
        with self.assertRaises(AssertionError):
            for job in doc["jobs"].values():
                for step in job.get("steps", []):
                    if step.get("uses", "").startswith("actions/checkout@"):
                        assert step.get("with", {}).get("ref") == "main"

    def test_release_and_dry_run_validation_steps_do_not_drift(self):
        release_runs = "\n".join(
            step.get("run", "")
            for job in self.workflow["jobs"].values()
            for step in job.get("steps", [])
        )
        dry_runs = "\n".join(
            step.get("run", "")
            for job in self.dry_run_workflow["jobs"].values()
            for step in job.get("steps", [])
        )
        shared_validation_steps = {
            "make package",
            "release_artifact_inventory.py",
            "compare_release_checksums.py",
            "release_upgrade_rollback.py",
            "release_ai_review.py",
            "release_image_layer.py",
            "verify_release_image_binaries.py",
            "release_image_smoke.py",
        }
        for command in shared_validation_steps:
            with self.subTest(command=command):
                self.assertIn(command, release_runs)
                self.assertIn(command, dry_runs)

    def test_release_image_smoke_uses_salted_caller_config(self):
        token_hash, token_id = smoke_caller_credentials()
        smoke_source = (ROOT / "scripts/release_image_smoke.py").read_text(encoding="utf-8")
        self.assertTrue(token_hash.startswith("pbkdf2-sha3-512:i=20000:"))
        self.assertEqual(token_id, "rtr_metrum_release_image_smoke_test")
        self.assertIn('"    token_hash: {token_hash}\\n"', smoke_source)
        self.assertIn('"    token_id: {token_id}\\n"', smoke_source)
        self.assertNotIn('token_sha256:', smoke_source)


if __name__ == "__main__":
    unittest.main()
