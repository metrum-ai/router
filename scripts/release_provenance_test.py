#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Offline contract tests for protected, daemonless release publication."""

from __future__ import annotations

import copy
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path
from zipfile import ZipFile

import yaml
from publish_github_release import publish_release, release_create_args, release_is_prerelease
from release_publish_tool_contract import verify_java_and_jsign

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/release.yml"
DRY_RUN_WORKFLOW = ROOT / ".github/workflows/release-dry-run.yml"
PIN = "4d101475d8b20a2381f78447822ac1eab6504dd8"
SUBJECTS = {
    "dist/metrum-ai-router-*.tar.gz",
    "dist/metrum-ai-router-*.zip",
    "dist/upgrade-rollback-results.json",
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
    resolve_steps = jobs["resolve"]["steps"]
    assert [step.get("name") for step in resolve_steps].index("Install pinned GitHub CLI") < [step.get("name") for step in resolve_steps].index("Resolve immutable build metadata")
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
    assert "packages" not in publish["permissions"]
    assert publish["permissions"].get("id-token") == "write"
    assert publish["permissions"].get("attestations") == "write"
    assert set(publish["needs"]) == {"resolve", "package-binaries", "reproducible-build", "ai-release-review", "upgrade-and-rollback"}
    assert upgrade["needs"] == ["resolve", "package-binaries"]
    assert "release_upgrade_rollback.py" in "\n".join(step.get("run", "") for step in upgrade["steps"])
    assert any(step.get("name") == "Download and verify previous production package" for step in upgrade["steps"])
    upgrade_names = [step.get("name") for step in upgrade["steps"]]
    assert upgrade_names.index("Install pinned GitHub CLI") < upgrade_names.index("Download and verify previous production package")
    assert "docker-runner-preflight" not in jobs and "package-images" not in jobs
    steps = publish["steps"]
    names = [step.get("name") for step in steps]
    assert "Publish tool contract" in names
    assert names.index("Publish tool contract") < names.index("Verify release environment protection before publish")
    environment = names.index("Verify release environment protection before publish")
    sbom_sign = names.index("Sign and verify SPDX SBOM assets")
    archives = names.index("Sign release archives with Cosign")
    verify = names.index("Verify attestations and require complete payload")
    publish_release = names.index("Confirm payload and publish immutable release")
    archive_attest = names.index("Attest release archives")
    assert SUBJECTS <= set(steps[archive_attest]["with"]["subject-path"].splitlines())
    all_runs = "\n".join(step.get("run", "") for step in steps)
    assert "cosign sign-blob" in steps[sbom_sign]["run"] and "cosign verify-blob" in steps[sbom_sign]["run"]
    assert "docker.sock" not in all_runs and "docker build" not in all_runs
    assert environment < sbom_sign < archives < archive_attest < verify < publish_release
    assert steps[archive_attest]["uses"] == f"actions/attest-build-provenance@{PIN}"
    assert "if" not in steps[archive_attest]
    assert "ghcr.io" not in str(publish) and "crane" not in all_runs.lower()
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
    assert verify.count("--signer-workflow") >= 2 and verify.count("--source-ref") >= 2
    assert "dist/SHA256SUMS" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/release-artifacts.json" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/AI-release-review.md" in steps[archive_attest]["with"]["subject-path"]
    assert "dist/release-review-artifact.json" in steps[archive_attest]["with"]["subject-path"]
    assert "release-review-artifact.json" in steps[names.index("Confirm payload and publish immutable release")]["run"]
    assert "-eq 21" in steps[names.index("Confirm payload and publish immutable release")]["run"]
    release_command = steps[publish_release]["run"]
    assert "scripts/publish_github_release.py" in release_command
    assert 'gh release create "${VERSION}"' not in release_command
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


def validate_advisory_snapshot_staging(workflow):
    jobs = workflow["jobs"]
    for job in jobs.values():
        for step in job.get("steps", []):
            run = step.get("run", "")
            if "engine_advisory_gate.py" in run and "--snapshot" in run:
                assert "--snapshot dist/" not in run
            snapshot_path = step.get("env", {}).get("ENGINE_ADVISORY_SNAPSHOT", "")
            assert "dist" not in snapshot_path
    if "package-binaries" in jobs:
        package_steps = jobs["package-binaries"]["steps"]
        binary_upload = next(step for step in package_steps if step.get("with", {}).get("name") == "release-binaries")
        assert "engine-advisories.json" not in binary_upload["with"]["path"]
        snapshot_upload = next(step for step in package_steps if step.get("with", {}).get("name") == "release-engine-advisories")
        assert "runner.temp" in snapshot_upload["with"]["path"]
        assert "dist" not in snapshot_upload["with"]["path"]
        for job_name, step_name in (("reproducible-build", "Rebuild binary packages on a clean runner"), ("publish-release", "Validate binary inventory")):
            job_steps = jobs[job_name]["steps"]
            download = next(step for step in job_steps if step.get("with", {}).get("name") == "release-engine-advisories")
            assert "runner.temp" in download["with"]["path"]
            consumer = next(step for step in job_steps if step.get("name") == step_name)
            assert "ENGINE_ADVISORY_SNAPSHOT" in consumer.get("env", {})
            assert "dist" not in consumer["env"]["ENGINE_ADVISORY_SNAPSHOT"]
    else:
        dry_run = jobs["dry-run"]["steps"]
        snapshot = next(step for step in dry_run if step.get("name") == "Generate engine advisory snapshot for release packages")
        assert "dist" not in snapshot["run"]
        for step in dry_run:
            assert "dist/engine-advisories.json" not in str(step)


def validate_unsigned_payload_gate(workflow, job_name):
    steps = workflow["jobs"][job_name]["steps"]
    names = [step.get("name") for step in steps]
    gate_name = "Validate exact unsigned release payload"
    assert gate_name in names
    gate = steps[names.index(gate_name)]
    assert "check_release_payload.py --stage unsigned" in gate.get("run", "")
    stage_name = "Stage exact unsigned release payload"
    assert stage_name in names
    stage = steps[names.index(stage_name)]
    assert "scripts/stage_release_payload.py" in stage.get("run", "")
    assert '--destination "${RUNNER_TEMP}/unsigned-payload"' in stage.get("run", "")
    assert '"${RUNNER_TEMP}/unsigned-payload"' in gate.get("run", "")
    if job_name == "publish-release":
        gate_index = names.index(gate_name)
        downloads = {
            step.get("with", {}).get("name"): index
            for index, step in enumerate(steps)
            if step.get("uses", "").startswith("actions/download-artifact@")
        }
        assert downloads["release-binaries"] < gate_index
        assert downloads["release-upgrade-rollback"] < gate_index
        assert gate_index < names.index("Sign Windows release packages after reproducibility comparison")
    else:
        assert names.index("Run upgrade and rollback checks") < names.index(gate_name)
        assert names.index(gate_name) < names.index("Review the previous production release through main")


def validate_clean_rebuild_cache_env(workflow):
    expected = {
        "GOCACHE": "${{ runner.temp }}/repro-go-build",
        "GOMODCACHE": "${{ runner.temp }}/repro-go-mod",
    }
    rebuilds = (
        [("reproducible-build", "Rebuild binary packages on a clean runner")]
        if "reproducible-build" in workflow["jobs"]
        else [("dry-run", "Rebuild with clean Go caches")]
    )
    for job_name, step_name in rebuilds:
        job = workflow["jobs"][job_name]
        step = next(step for step in job["steps"] if step.get("name") == step_name)
        assert {key: step.get("env", {}).get(key) for key in expected} == expected
        run = step.get("run", "")
        assert 'rm -rf "${GOCACHE}" "${GOMODCACHE}"' in run
        assert run.index('rm -rf "${GOCACHE}" "${GOMODCACHE}"') < run.index("make package")
        assert "GITHUB_ENV" not in step.get("run", "")
        assert not any(
            "GITHUB_ENV" in candidate.get("run", "")
            and ("GOCACHE" in candidate.get("run", "") or "GOMODCACHE" in candidate.get("run", ""))
            for candidate in job["steps"]
        )


def validate_cosign_installation(workflow, job_name):
    steps = workflow["jobs"][job_name]["steps"]
    names = [step.get("name") for step in steps]
    provide = next((step for step in steps if step.get("name") == "Provide envsubst for Cosign installer"), None)
    install = next((step for step in steps if step.get("name") == "Install pinned Cosign"), None)
    verify = next((step for step in steps if step.get("name") == "Verify Cosign installation"), None)
    assert provide is not None and install is not None and verify is not None
    assert names.index("Provide envsubst for Cosign installer") < names.index("Install pinned Cosign") < names.index("Verify Cosign installation")
    assert "command -v envsubst" in provide["run"]
    assert "scripts/envsubst_compat.py" in provide["run"]
    assert '"${GITHUB_PATH}"' in provide["run"]
    assert install.get("uses", "").startswith("sigstore/cosign-installer@")
    assert install["with"].get("cosign-release") == "v3.1.4"
    assert verify.get("run") == "cosign version"


def validate_publish_tool_contract(workflow, job_name):
    steps = workflow["jobs"][job_name]["steps"]
    names = [step.get("name") for step in steps]
    contract = next((step for step in steps if step.get("name") == "Publish tool contract"), None)
    assert contract is not None
    assert "scripts/release_publish_tool_contract.py" in contract.get("run", "")
    assert '--gh "${RUNNER_TEMP}/gh-cli/bin/gh"' in contract["run"]
    assert "--crane" not in contract["run"]
    assert '--syft "${SYFT_COMMAND}"' in contract["run"]
    assert '--jsign-jar "${JSIGN_JAR}"' in contract["run"]
    required_steps = (
        "Install pinned GitHub CLI",
        "Provide envsubst for Cosign installer",
        "Install pinned Cosign",
        "Verify Cosign installation",
        "Install pinned Syft",
        "Set up pinned Java for jsign",
        "Fetch pinned jsign (checksum-verified)",
    )
    for name in required_steps:
        assert name in names, f"missing publish-tool setup step: {name}"
        assert names.index(name) < names.index("Publish tool contract"), name
    gh_install = next(step for step in steps if step.get("name") == "Install pinned GitHub CLI")
    assert 'scripts/install_github_cli.sh "${RUNNER_TEMP}/gh-cli/bin"' in gh_install.get("run", "")
    cosign_install = next(step for step in steps if step.get("name") == "Install pinned Cosign")
    assert cosign_install.get("uses", "").startswith("sigstore/cosign-installer@")
    assert cosign_install.get("with", {}).get("cosign-release") == "v3.1.4"
    syft_install = next(step for step in steps if step.get("name") == "Install pinned Syft")
    assert syft_install.get("uses", "").startswith("anchore/sbom-action/download-syft@")
    assert syft_install.get("with", {}).get("syft-version") == "v1.57.1"
    java_setup = next(step for step in steps if step.get("name") == "Set up pinned Java for jsign")
    assert java_setup.get("with", {}).get("java-version") == "21"
    jsign_fetch = next(step for step in steps if step.get("name") == "Fetch pinned jsign (checksum-verified)")
    assert "from release_publish_tool_contract import JSIGN_SHA256" in jsign_fetch.get("run", "")
    assert "sha256sum -c -" in jsign_fetch.get("run", "")
    assert not re.search(r"\b[0-9a-f]{64}\b", jsign_fetch.get("run", ""))
    if job_name == "publish-release":
        assert names.index("Publish tool contract") < names.index("Verify release environment protection before publish")
    else:
        assert names.index("Publish tool contract") < names.index("Resolve the main commit and latest production release")
    return contract


class ProvenanceWorkflowTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.workflow = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
        cls.dry_run_workflow = yaml.safe_load(DRY_RUN_WORKFLOW.read_text(encoding="utf-8"))

    def test_release_contract(self):
        validate(self.workflow)
        validate_binary_inventory_snapshot(self.workflow)
        validate_clean_rebuild_cache_env(self.workflow)
        validate_cosign_installation(self.workflow, "publish-release")
        validate_publish_tool_contract(self.workflow, "publish-release")
        validate_advisory_snapshot_staging(self.workflow)
        validate_unsigned_payload_gate(self.workflow, "publish-release")
        package_steps = self.workflow["jobs"]["package-binaries"]["steps"]
        stage = next(step for step in package_steps if step.get("name") == "Stage exact binary release payload")
        assert "scripts/stage_release_payload.py" in stage.get("run", "")
        assert "--without-upgrade-results" in stage["run"]
        binary_upload = next(step for step in package_steps if step.get("with", {}).get("name") == "release-binaries")
        assert "${{ runner.temp }}/release-binaries" == binary_upload["with"]["path"]

    def test_release_job_requires_environment_protection(self):
        doc = copy.deepcopy(self.workflow)
        del doc["jobs"]["publish-release"]["environment"]
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_release_payload_requires_upgrade_evidence(self):
        doc = copy.deepcopy(self.workflow)
        publish = doc["jobs"]["publish-release"]
        publish["needs"].remove("upgrade-and-rollback")
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_unsigned_payload_gate_required_in_both_workflows(self):
        release = copy.deepcopy(self.workflow)
        release["jobs"]["publish-release"]["steps"] = [
            step for step in release["jobs"]["publish-release"]["steps"]
            if step.get("name") != "Validate exact unsigned release payload"
        ]
        with self.assertRaises(AssertionError):
            validate_unsigned_payload_gate(release, "publish-release")
        dry_run = copy.deepcopy(self.dry_run_workflow)
        dry_run["jobs"]["dry-run"]["steps"] = [
            step for step in dry_run["jobs"]["dry-run"]["steps"]
            if step.get("name") != "Validate exact unsigned release payload"
        ]
        with self.assertRaises(AssertionError):
            validate_unsigned_payload_gate(dry_run, "dry-run")

    def test_release_publish_workflow_has_no_manual_dispatch(self):
        doc = copy.deepcopy(self.workflow)
        trigger_key = "on" if "on" in doc else True
        doc[trigger_key]["workflow_dispatch"] = {"inputs": {"dry_run": {"type": "boolean"}}}
        with self.assertRaises(AssertionError):
            validate(doc)

    def test_full_dry_run_is_main_only_read_only_and_nonpublishing(self):
        workflow = self.dry_run_workflow
        validate_binary_inventory_snapshot(workflow)
        validate_clean_rebuild_cache_env(workflow)
        validate_cosign_installation(workflow, "dry-run")
        validate_publish_tool_contract(workflow, "dry-run")
        validate_advisory_snapshot_staging(workflow)
        validate_unsigned_payload_gate(workflow, "dry-run")
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
        self.assertNotIn("ghcr.io", str(self.dry_run_workflow))
        self.assertIn("check_release_dry_run.py", all_runs)
        self.assertNotIn("crane push", all_runs)
        self.assertIn("cosign version", all_runs)
        self.assertNotIn("cosign sign", all_runs)
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

    def test_publish_tool_contract_is_required_and_identical_in_both_workflows(self):
        release_contract = validate_publish_tool_contract(self.workflow, "publish-release")
        dry_run_contract = validate_publish_tool_contract(self.dry_run_workflow, "dry-run")
        self.assertEqual(release_contract["run"], dry_run_contract["run"])
        for source, job_name in ((self.workflow, "publish-release"), (self.dry_run_workflow, "dry-run")):
            doc = copy.deepcopy(source)
            doc["jobs"][job_name]["steps"] = [
                step for step in doc["jobs"][job_name]["steps"]
                if step.get("name") != "Publish tool contract"
            ]
            with self.assertRaises(AssertionError):
                validate_publish_tool_contract(doc, job_name)

        contract_source = (ROOT / "scripts/release_publish_tool_contract.py").read_text(encoding="utf-8")
        for required in (
            "--signer-workflow", "--source-ref", "--verify-tag", "--notes-file", "--pattern",
            "--storetype", "--tsaurl", "--certificate-oidc-issuer",
            "--prerelease", "--latest",
        ):
            with self.subTest(required=required):
                self.assertIn(required, contract_source)
        installer = (ROOT / "scripts/install_github_cli.sh").read_text(encoding="utf-8")
        self.assertIn("version=2.104.0", installer)
        self.assertIn('checksum_asset="gh_${version}_checksums.txt"', installer)
        self.assertIn("sha256sum --check", installer)
        self.assertIn('>> "$GITHUB_PATH"', installer)
        self.assertEqual(contract_source.count("JSIGN_SHA256 = "), 1)
        self.assertIn("hashlib.sha256(jsign_jar.read_bytes()).hexdigest()", contract_source)
        self.assertIn('archive.read("META-INF/MANIFEST.MF")', contract_source)

    def test_release_creation_marks_rc_and_ga_correctly(self):
        rc_args = release_create_args("v5.0.0-rc.2", "Metrum AI Router v5.0.0-rc.2", "notes.md", ["asset.tgz"])
        self.assertIn("--prerelease", rc_args)
        self.assertIn("--latest=false", rc_args)
        self.assertNotIn("--latest", rc_args)

        ga_args = release_create_args("v5.0.0", "Metrum AI Router v5.0.0", "notes.md", ["asset.tgz"])
        self.assertNotIn("--prerelease", ga_args)
        self.assertIn("--latest", ga_args)
        self.assertNotIn("--latest=false", ga_args)

    def test_release_creation_rejects_non_semver_tags_and_build_metadata_hyphens_are_not_prereleases(self):
        for invalid in ("5.0.0", "v05.0.0", "v5.0.0-01", "v5.0.0-", "v5.0"):
            with self.subTest(version=invalid), self.assertRaises(ValueError):
                release_is_prerelease(invalid)
        self.assertFalse(release_is_prerelease("v5.0.0+build-foo"))

    def test_release_creation_verifies_published_prerelease_metadata(self):
        for version, metadata in (("v5.0.0-rc.2", True), ("v5.0.0", False)):
            with self.subTest(version=version), mock.patch("publish_github_release.subprocess.run") as run:
                run.side_effect = [
                    subprocess.CompletedProcess(["gh"], 0),
                    subprocess.CompletedProcess(["gh"], 0, stdout=json.dumps({"isPrerelease": metadata})),
                ]
                publish_release(version, f"Router {version}", "notes.md", ["asset.tgz"])
                self.assertEqual(run.call_args_list[1].args[0], ["gh", "release", "view", version, "--json", "isPrerelease"])

        with mock.patch("publish_github_release.subprocess.run") as run:
            run.side_effect = [
                subprocess.CompletedProcess(["gh"], 0),
                subprocess.CompletedProcess(["gh"], 0, stdout=json.dumps({"isPrerelease": False})),
            ]
            with self.assertRaisesRegex(RuntimeError, "prerelease metadata mismatch"):
                publish_release("v5.0.0-rc.2", "Router RC", "notes.md", ["asset.tgz"])

    def test_jsign_contract_checks_pinned_jar_manifest_and_help_without_help_version(self):
        help_output = "\n".join(("--storetype", "--keystore", "--storepass", "--alias", "--tsaurl", "--tsmode"))
        with tempfile.TemporaryDirectory(dir=ROOT) as temporary:
            jsign_jar = Path(temporary) / "jsign.jar"
            with ZipFile(jsign_jar, "w") as archive:
                archive.writestr("META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nImplementation-Version: 7.6\r\n\r\n")
            expected_sha256 = hashlib.sha256(jsign_jar.read_bytes()).hexdigest()
            with mock.patch("release_publish_tool_contract.JSIGN_SHA256", expected_sha256), mock.patch("release_publish_tool_contract.subprocess.run") as run:
                run.side_effect = [
                    subprocess.CompletedProcess(["java", "-version"], 0, stdout="", stderr='openjdk version "21.0.3"'),
                    subprocess.CompletedProcess(["java", "-jar", str(jsign_jar), "--help"], 0, stdout=help_output, stderr=""),
                ]
                verify_java_and_jsign(["java"], jsign_jar)
            self.assertEqual(run.call_args_list[1].args[0][-1], "--help")
            self.assertNotIn("7.6", help_output)
            self.assertTrue(all("--version" not in call.args[0] for call in run.call_args_list))

            with mock.patch("release_publish_tool_contract.JSIGN_SHA256", "0" * 64), mock.patch("release_publish_tool_contract.subprocess.run") as run:
                run.return_value = subprocess.CompletedProcess(["java", "-version"], 0, stdout="", stderr='openjdk version "21.0.3"')
                with self.assertRaisesRegex(RuntimeError, "SHA-256 mismatch"):
                    verify_java_and_jsign(["java"], jsign_jar)

            wrong_version_jar = Path(temporary) / "wrong-version.jar"
            with ZipFile(wrong_version_jar, "w") as archive:
                archive.writestr("META-INF/MANIFEST.MF", "Manifest-Version: 1.0\r\nImplementation-Version: 7.5\r\n\r\n")
            wrong_version_sha256 = hashlib.sha256(wrong_version_jar.read_bytes()).hexdigest()
            with mock.patch("release_publish_tool_contract.JSIGN_SHA256", wrong_version_sha256), mock.patch("release_publish_tool_contract.subprocess.run") as run:
                run.return_value = subprocess.CompletedProcess(["java", "-version"], 0, stdout="", stderr='openjdk version "21.0.3"')
                with self.assertRaisesRegex(RuntimeError, "manifest must declare Implementation-Version: 7.6"):
                    verify_java_and_jsign(["java"], wrong_version_jar)

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

    def test_clean_rebuild_requires_step_scoped_fresh_go_caches(self):
        for source in (self.workflow, self.dry_run_workflow):
            doc = copy.deepcopy(source)
            job_name = "reproducible-build" if source is self.workflow else "dry-run"
            step_name = "Rebuild binary packages on a clean runner" if job_name == "reproducible-build" else "Rebuild with clean Go caches"
            step = next(step for step in doc["jobs"][job_name]["steps"] if step.get("name") == step_name)
            step["env"].pop("GOCACHE")
            with self.assertRaises(AssertionError):
                validate_clean_rebuild_cache_env(doc)

    def test_envsubst_compatibility_expands_environment_variables(self):
        result = subprocess.run(
            [sys.executable, str(ROOT / "scripts/envsubst_compat.py")],
            input="image=${COSIGN_TEST_IMAGE}\n",
            capture_output=True,
            text=True,
            env={**os.environ, "COSIGN_TEST_IMAGE": "registry.example/router:v5"},
            check=True,
        )
        self.assertEqual(result.stdout, "image=registry.example/router:v5\n")

    def test_cosign_installer_contract_is_required_in_both_workflows(self):
        for source, job_name in ((self.workflow, "publish-release"), (self.dry_run_workflow, "dry-run")):
            doc = copy.deepcopy(source)
            steps = doc["jobs"][job_name]["steps"]
            steps[:] = [step for step in steps if step.get("name") != "Verify Cosign installation"]
            with self.assertRaises(AssertionError):
                validate_cosign_installation(doc, job_name)

    def test_release_tool_paths_are_outside_the_checkout_in_both_workflows(self):
        for source, job_name in ((self.workflow, "publish-release"), (self.dry_run_workflow, "dry-run")):
            with self.subTest(workflow=source.get("name")):
                job = source["jobs"][job_name]
                steps = job["steps"]
                self.assertNotIn("CRANE", job.get("env", {}))
                cosign = next(step for step in steps if step.get("name") == "Install pinned Cosign")
                self.assertEqual(cosign["with"]["install-dir"], "${{ runner.temp }}/cosign-bin")
                envsubst = next(step for step in steps if step.get("name") == "Provide envsubst for Cosign installer")
                self.assertIn('shim_dir="${RUNNER_TEMP}/cosign-envsubst-bin"', envsubst["run"])
                fetch_jsign = next(step for step in steps if step.get("name") == "Fetch pinned jsign (checksum-verified)")
                self.assertIn('"${RUNNER_TEMP}/signing/jsign.jar"', fetch_jsign["run"])
                contract = next(step for step in steps if step.get("name") == "Publish tool contract")
                self.assertEqual(contract["env"]["JSIGN_JAR"], "${{ runner.temp }}/signing/jsign.jar")
                self.assertEqual(contract["env"]["SYFT_COMMAND"], "${{ steps.syft.outputs.cmd }}")
                syft = next(step for step in steps if step.get("name") == "Install pinned Syft")
                self.assertEqual(syft["uses"], "anchore/sbom-action/download-syft@d8a2c0130026bf585de5c176ab8f7ce62d75bf04")

                tool_paths = [
                    cosign["with"]["install-dir"], envsubst["run"],
                    fetch_jsign["run"], contract["env"]["JSIGN_JAR"],
                    contract["env"]["SYFT_COMMAND"], contract["run"],
                ]
                for candidate in source["jobs"].values():
                    for step in candidate.get("steps", []):
                        if step.get("name") == "Install pinned GitHub CLI":
                            tool_paths.append(step["run"])
                            self.assertIn('${RUNNER_TEMP}/gh-cli/bin', step["run"])
                self.assertNotIn("${{ github.workspace }}", "\n".join(tool_paths))

    def test_runner_context_is_not_used_in_workflow_or_job_level_env(self):
        for source in (self.workflow, self.dry_run_workflow):
            with self.subTest(workflow=source.get("name")):
                self.assertNotIn("runner.", str(source.get("env", {})))
                for job_name, job in source["jobs"].items():
                    with self.subTest(job=job_name):
                        self.assertNotIn("runner.", str(job.get("env", {})))

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
        }
        for command in shared_validation_steps:
            with self.subTest(command=command):
                self.assertIn(command, release_runs)
                self.assertIn(command, dry_runs)

if __name__ == "__main__":
    unittest.main()
