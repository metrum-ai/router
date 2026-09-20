#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for the public-face docs guardrails, focused on product branding.

The branding policy is that "Metrum AI Router" is the canonical current
product display name everywhere. These tests pin that policy, the narrow
historical/technical exceptions and the path coverage, without depending on
the repository's current branding content.
"""

from __future__ import annotations

import contextlib
import io
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import check_docs_public_face as checker


CANONICAL = "Metrum AI Router"
README = checker.ROOT / "README.md"


def branding(line: str, *, path: Path = README, prev_line: str = "") -> list[str]:
    return list(checker.branding_line_errors(path, 1, line, prev_line))


class CanonicalBrandTest(unittest.TestCase):
    def test_canonical_title_is_accepted(self) -> None:
        self.assertEqual([], branding(CANONICAL))

    def test_canonical_title_keeps_grammar_in_composed_labels(self) -> None:
        for line in (
            "# Metrum AI Router",
            "Metrum AI Router Admin Reports",
            "Metrum AI Router Usage Report",
            "Evaluate Metrum AI Router",
            "title: Metrum AI Router | Metrum AI",
            'Layout title="Metrum AI Router"',
        ):
            with self.subTest(line=line):
                self.assertEqual([], branding(line))

    def test_no_rejection_pattern_matches_the_canonical_title(self) -> None:
        for label, pattern in checker.OBSOLETE_PRODUCT_TITLE_PATTERNS:
            with self.subTest(label=label):
                self.assertIsNone(pattern.search(CANONICAL))
        self.assertIsNone(checker.OBSOLETE_TITLE_SCANNER.search(CANONICAL))

    def test_diagnostic_points_at_the_canonical_title(self) -> None:
        errors = branding("GenAI Smart Router")

        self.assertEqual(1, len(errors))
        self.assertIn("README.md:1:", errors[0])
        self.assertIn("obsolete product title GenAI Smart Router", errors[0])
        self.assertIn(f"use {CANONICAL}", errors[0])
        self.assertNotIn("use Metrum Router", errors[0])


class ObsoleteTitleTest(unittest.TestCase):
    def test_rejects_each_obsolete_display_name(self) -> None:
        for line, expected in (
            ("Metrum Router", "obsolete product title Metrum Router"),
            ("GenAI Smart Router", "obsolete product title GenAI Smart Router"),
            ("Gen AI Smart Router", "obsolete product title GenAI Smart Router"),
            ("Metrum GenAI Smart Router", "obsolete product title Metrum GenAI Smart Router"),
            ("Metrum Smart Router", "obsolete product title Metrum Smart Router"),
            ("Smart LLM Router", "obsolete product title Smart LLM Router"),
            ("Metrum AI Smart Router", "obsolete product title Metrum AI Smart Router"),
        ):
            with self.subTest(line=line):
                errors = branding(line)
                self.assertEqual(1, len(errors), errors)
                self.assertIn(expected, errors[0])

    def test_rejects_case_variants(self) -> None:
        for line in (
            "METRUM ROUTER",
            "metrum router",
            "Metrum ROUTER",
            "genai smart router",
            "GENAI SMART ROUTER",
            "smart llm router",
            "METRUM SMART ROUTER",
        ):
            with self.subTest(line=line):
                self.assertEqual(1, len(branding(line)), line)

    def test_rejects_spacing_variants(self) -> None:
        for line in (
            "Metrum  Router",
            "Metrum\tRouter",
            "Metrum\u00a0Router",
            "GenAI  Smart  Router",
            "Gen  AI Smart Router",
            "GenAI SmartRouter",
            "Metrum  Smart  Router",
            "Smart  LLM\tRouter",
        ):
            with self.subTest(line=line):
                self.assertEqual(1, len(branding(line)), line)

    def test_rejects_partial_replacement_artifacts(self) -> None:
        for line in ("Metrum Metrum Router", "Metrum Metrum AI Router", "Metrum AI AI Router"):
            with self.subTest(line=line):
                errors = branding(line)
                self.assertEqual(1, len(errors), errors)
                self.assertIn("malformed product title", errors[0])

    def test_reports_longest_name_once_per_occurrence(self) -> None:
        errors = branding("Metrum GenAI Smart Router and Smart LLM Router")

        self.assertEqual(2, len(errors), errors)
        self.assertIn("obsolete product title Metrum GenAI Smart Router", errors[0])
        self.assertIn("obsolete product title Smart LLM Router", errors[1])

    def test_reports_obsolete_names_inside_code_and_markup(self) -> None:
        for line in (
            'name = "Metrum Router"',
            "<title>Metrum Smart Router</title>",
            "  description: GenAI Smart Router admin API",
            "    Router[Metrum Router]",
        ):
            with self.subTest(line=line):
                self.assertEqual(1, len(branding(line)), line)


class ExemptPatternTest(unittest.TestCase):
    def test_company_and_legal_identity_is_not_branding(self) -> None:
        for line in (
            "Metrum AI, Inc.",
            "# Copyright 2026 Metrum AI",
            "METRUM AI is a registered trademark of Metrum AI, Inc.",
            '<img src={logoUrl} alt="Metrum AI" />',
            "Metrum AI Product Documentation",
        ):
            with self.subTest(line=line):
                self.assertEqual([], branding(line))

    def test_generic_and_third_party_router_prose_is_not_branding(self) -> None:
        for line in (
            "The router forwards the request to the upstream provider.",
            "Smart routing keeps latency predictable.",
            "Routing policy is evaluated per request.",
            "OpenRouter is one supported upstream provider.",
            "Compare smart routers before buying one.",
            "Metrum AI Router selects a different upstream per request.",
            "An open-source LLM smart router.",
        ):
            with self.subTest(line=line):
                self.assertEqual([], branding(line))

    def test_technical_identifiers_are_not_branding(self) -> None:
        for line in (
            'model_provider = "metrum-ai-router"',
            'Product = "genai-smart-router"',
            "image: metrum-ai/genai-smart-router:v1.3.1",
            "helm install genai-smart-router ./deploy/helm/genai-smart-router",
            "metrum.ai/smartrouter-tenant: acme",
            "smartrouterctl blueprint --out blueprint.md",
            'name": "smart-llmrouter-admin-reports"',
            "ROUTER_TOKEN=... METRUM_ROUTER_URL=...",
            "docs-site/docs/evaluation/evaluate-smart-router.md",
        ):
            with self.subTest(line=line):
                self.assertEqual([], branding(line))

    def test_runtime_paths_accept_canonical_title(self) -> None:
        line = 'realm := "Metrum AI Router"'
        for relative in (
            "internal/router/service.go",
            "cmd/metrum-ai-router/main.go",
            "scripts/local_launch_path_smoke.py",
            "examples/external-routing-policy/prompt_size_policy.py",
            "Makefile",
        ):
            with self.subTest(relative=relative):
                self.assertEqual([], branding(line, path=checker.ROOT / relative))

    def test_no_path_is_exempt_from_obsolete_titles(self) -> None:
        line = "Metrum Smart Router is an open-source product."
        for relative in (
            "README.md",
            "internal/router/service.go",
            "Makefile",
            "NOTICE",
            "TRADEMARKS.md",
            "GOVERNANCE.md",
        ):
            with self.subTest(relative=relative):
                errors = branding(line, path=checker.ROOT / relative)
                self.assertEqual(1, len(errors), errors)
                self.assertIn("obsolete product title Metrum Smart Router", errors[0])

    def test_policy_source_files_may_spell_out_rejected_names(self) -> None:
        for name in sorted(checker.POLICY_SOURCE_FILES):
            with self.subTest(name=str(name)):
                self.assertEqual([], branding("GenAI Smart Router", path=checker.ROOT / name))


class MarkerExceptionTest(unittest.TestCase):
    def test_marker_on_the_same_line_allows_the_old_name(self) -> None:
        line = "GenAI Smart Router v1.0.2 <!-- branding-exception: published release title -->"

        self.assertEqual([], branding(line))
        self.assertEqual("published release title", checker.marker_exception_reason(line))

    def test_marker_on_the_previous_line_allows_the_old_name(self) -> None:
        prev = "<!-- branding-exception: quoted original console output -->"

        self.assertEqual([], branding("Smart LLM Router usage report", prev_line=prev))

    def test_marker_does_not_leak_to_later_lines(self) -> None:
        prev = "<!-- branding-exception: quoted original console output -->"

        self.assertEqual([], branding("Smart LLM Router", prev_line=prev))
        self.assertEqual(1, len(branding("Smart LLM Router", prev_line="unrelated prose")))

    def test_marker_supports_comment_syntaxes(self) -> None:
        for line in (
            "// branding-exception: historical CLI banner",
            "# branding-exception: historical CLI banner",
            "{/* branding-exception: historical CLI banner */}",
        ):
            with self.subTest(line=line):
                self.assertEqual(
                    "historical CLI banner",
                    checker.marker_exception_reason(line),
                )

    def test_marker_without_a_reason_is_an_error(self) -> None:
        for line in (
            "GenAI Smart Router <!-- branding-exception: -->",
            "GenAI Smart Router // branding-exception:",
        ):
            with self.subTest(line=line):
                errors = branding(line)
                self.assertEqual(1, len(errors), errors)
                self.assertIn("branding-exception marker needs a written reason", errors[0])

    def test_unmarked_lines_stay_rejected(self) -> None:
        self.assertEqual(1, len(branding("GenAI Smart Router v1.0.2")))


class ContextualExceptionTest(unittest.TestCase):
    trademarks = checker.ROOT / "TRADEMARKS.md"
    release_notes = checker.ROOT / "docs-site" / "docs" / "release-notes" / "index.md"

    def test_trademark_former_name_enumeration_is_allowed(self) -> None:
        line = (
            'METRUM AI ROUTER, formerly "Metrum Router" or "GenAI Smart Router," '
            'or "Metrum Smart Router," '
            "and the Metrum AI logo are trademarks of Metrum AI, Inc."
        )

        self.assertEqual([], branding(line, path=self.trademarks))

    def test_trademark_exception_does_not_cover_current_product_claims(self) -> None:
        line = '"Metrum Router," formerly also referred to as "GenAI Smart Router,"'

        errors = branding(line, path=self.trademarks)

        self.assertEqual(1, len(errors), errors)
        self.assertIn("obsolete product title Metrum Router 'Metrum Router'", errors[0])

    def test_trademark_exception_does_not_exempt_the_whole_file(self) -> None:
        self.assertEqual(1, len(branding("built on Metrum Router", path=self.trademarks)))

    def test_release_notes_keep_published_release_titles(self) -> None:
        self.assertEqual([], branding("## GenAI Smart Router v1.3.1", path=self.release_notes))

    def test_release_notes_prose_is_still_rejected(self) -> None:
        errors = branding("Metrum Router adds native streaming.", path=self.release_notes)

        self.assertEqual(1, len(errors), errors)

    def test_release_title_exception_is_scoped_to_its_file(self) -> None:
        self.assertEqual(1, len(branding("## GenAI Smart Router v1.3.1")))

    def test_historical_case_study_is_no_longer_branding_exempt(self) -> None:
        for name in sorted(checker.HISTORICAL_FILES):
            with self.subTest(name=str(name)):
                errors = branding("## Related Metrum Router Docs", path=checker.ROOT / name)
                self.assertEqual(1, len(errors), errors)


class CheckSeparationTest(unittest.TestCase):
    """Branding coverage must not add or remove privacy/version coverage."""

    case_study = checker.ROOT / "docs" / "harbor-case-study.md"

    def test_historical_files_remain_exempt_from_privacy_rules(self) -> None:
        line = "curl https://llm-api.metrum.ai/v1/models"

        self.assertEqual([], list(checker.privacy_line_errors(self.case_study, 1, line)))

    def test_privacy_rules_still_fire_on_public_docs(self) -> None:
        errors = list(
            checker.privacy_line_errors(README, 7, "aws sts get-caller-identity 121701826775")
        )

        self.assertEqual(1, len(errors), errors)
        self.assertIn("contains private AWS account", errors[0])

    def test_temporary_docs_origin_is_allowed(self) -> None:
        line = "Hosted docs: https://llm-api.apps.metrum.ai/docs/overview"
        self.assertEqual([], list(checker.privacy_line_errors(README, 1, line)))

    def test_temporary_docs_host_api_path_remains_private(self) -> None:
        errors = list(
            checker.privacy_line_errors(README, 1, "curl https://llm-api.apps.metrum.ai/v1/models")
        )
        self.assertEqual(1, len(errors), errors)
        self.assertIn("contains private production host/IP", errors[0])

    def test_stale_route_rules_still_fire_on_public_docs(self) -> None:
        errors = list(
            checker.privacy_line_errors(README, 9, "The current route is OpenRouter DeepSeek V3.")
        )

        self.assertEqual(1, len(errors), errors)
        self.assertIn("stale active OpenRouter DeepSeek route claim", errors[0])

    def test_rejects_ssh_identity_not_only_pem(self) -> None:
        errors = list(
            checker.privacy_line_errors(
                README, 1, "ssh -i ~/.ssh/id_ed25519 shadeform@64.247.196.20"
            )
        )
        labels = " ".join(errors)
        self.assertTrue(errors, errors)
        self.assertTrue(
            "ssh identity invocation" in labels
            or "user@public-IPv4 SSH target" in labels
            or "public IPv4 address" in labels,
            errors,
        )

    def test_allows_placeholder_ssh_lines(self) -> None:
        line = "ssh -i <operator-ssh-key> <user>@<operator-host>"
        self.assertEqual([], list(checker.privacy_line_errors(README, 1, line)))

    def test_rejects_cloud_instance_id_fields(self) -> None:
        errors = list(
            checker.privacy_line_errors(
                README,
                1,
                '"instance_id": "9963d127-f312-4a37-b72d-91b42607cf8e"',
            )
        )
        self.assertTrue(any("cloud instance id field" in e for e in errors), errors)

    def test_rejects_object_storage_bucket_urls(self) -> None:
        errors = list(checker.privacy_line_errors(README, 1, "restic backup s3://metrum-backups/x"))
        self.assertTrue(any("object-storage bucket URL" in e for e in errors), errors)

    def test_evidence_tree_is_privacy_scanned(self) -> None:
        evidence = checker.ROOT / "docs" / "evidence"
        self.assertIn(evidence, checker.PUBLIC_DOC_PATHS)

    def test_branding_only_files_are_not_privacy_checked(self) -> None:
        errors = list(
            checker.file_errors(
                checker.ROOT / "docs" / "COMPETITIVE_NOTES.md",
                "ssh -i ~/.ssh/router.pem ubuntu@host\n",
                privacy=False,
                branding=True,
            )
        )

        self.assertEqual([], errors)

    def test_branding_only_files_are_not_doc_type_checked(self) -> None:
        errors = list(
            checker.file_errors(
                checker.ROOT / "docs-site" / "docs" / "overview.mdx",
                "no frontmatter here\n",
                privacy=False,
                branding=True,
            )
        )

        self.assertEqual([], errors)

    def test_public_doc_paths_keep_their_privacy_coverage(self) -> None:
        for expected in (
            checker.ROOT / "README.md",
            checker.ROOT / "docs-site" / "docs",
            checker.ROOT / "config.example.yaml",
            checker.ROOT / "deploy" / "Caddyfile.compose",
        ):
            with self.subTest(expected=str(expected)):
                self.assertIn(expected, checker.PUBLIC_DOC_PATHS)

    def test_line_errors_still_reports_both_rule_sets(self) -> None:
        errors = list(
            checker.line_errors(README, 3, "Metrum Router runs on 100.30.225.66 today.")
        )

        joined = "\n".join(errors)
        self.assertGreaterEqual(len(errors), 2, errors)
        self.assertIn("contains private production host/IP", joined)
        self.assertIn("obsolete product title Metrum Router", joined)


class BrandingCoverageTest(unittest.TestCase):
    def setUp(self) -> None:
        self.files = set(checker.iter_branding_files())

    def assert_category_covered(self, *relatives: str) -> None:
        """Every named sample that still exists must be scanned.

        Samples are named so that coverage gaps are obvious, but a rename in
        another branch must not look like a policy regression: it is enough
        that the category is still represented by the samples that remain.
        """

        present = [name for name in relatives if (checker.ROOT / name).exists()]
        self.assertTrue(present, f"no sample of {relatives} exists any more")
        for relative in present:
            with self.subTest(relative=relative):
                self.assertIn(
                    checker.ROOT / relative,
                    self.files,
                    f"{relative} is not covered by the branding scan",
                )

    def test_covers_root_and_community_docs(self) -> None:
        self.assert_category_covered(
            "README.md",
            "CONTRIBUTING.md",
            "CODE_OF_CONDUCT.md",
            "GOVERNANCE.md",
            "SUPPORT.md",
            "SECURITY.md",
            "TRADEMARKS.md",
            "THIRD_PARTY_NOTICES.md",
            "Makefile",
            "config.example.yaml",
        )

    def test_covers_issue_templates_and_shared_github_config(self) -> None:
        self.assert_category_covered(
            ".github/ISSUE_TEMPLATE/bug.yml",
            ".github/ISSUE_TEMPLATE/feature.yml",
            ".github/ISSUE_TEMPLATE/question.yml",
            ".github/PULL_REQUEST_TEMPLATE.md",
            ".github/CODEOWNERS",
        )

    def test_covers_internal_and_package_documentation(self) -> None:
        self.assert_category_covered(
            "docs/COMPETITIVE_NOTES.md",
            "docs/PII_FILTERING.md",
            "docs/specs/kubernetes-deployment.md",
            "docs/compliance/evidence/technology-governance.md",
            "docs/amd-instinct-local-serving-reference-architecture.md",
            "examples/README.md",
            "examples/external-routing-policy/prompt_size_policy.py",
            "deploy/kubernetes/overlays/nvidia-llmd-compat/README.md",
            "deploy/release/production-release-manifest.schema.json",
        )

    def test_covers_docs_site_config_and_components(self) -> None:
        self.assert_category_covered(
            "docs-site/docusaurus.config.js",
            "docs-site/sidebars.js",
            "docs-site/package.json",
            "docs-site/src/pages/index.js",
            "docs-site/src/pages/404.js",
            "docs-site/src/components/RouterEndpoint.js",
            "docs-site/docs/overview.mdx",
        )

    def test_covers_product_facing_runtime_and_generator_sources(self) -> None:
        self.assert_category_covered(
            "internal/router/docs.go",
            "internal/router/service.go",
            "internal/router/config.go",
            "internal/router/admin_reports.go",
            "internal/router/usage_db.go",
            "internal/smartrouterctl/blueprint.go",
            "internal/smartrouterctl/helm.go",
            "internal/smartrouterctl/operator.go",
            "internal/router/admindist/web/src/App.tsx",
            "internal/router/admindist/web/index.html",
        )

    def test_covers_harness_and_generator_scripts(self) -> None:
        self.assert_category_covered(
            "scripts/compose_live_e2e.sh",
            "scripts/live_cli_c_e2e.sh",
            "scripts/reasoning_smoke.py",
            "scripts/local_launch_path_smoke.py",
        )

    def test_covers_tracked_generated_admin_output(self) -> None:
        self.assert_category_covered("internal/router/admindist/index.html")
        generated = sorted(
            (checker.ROOT / "internal" / "router" / "admindist" / "static" / "assets").glob(
                "admin-*.js"
            )
        )
        self.assertTrue(generated, "tracked admin bundle is missing")
        for asset in generated:
            with self.subTest(asset=asset.name):
                self.assertIn(asset, self.files)

    def test_excludes_generated_trees_lockfiles_and_tests(self) -> None:
        for relative in (
            "docs-site/package-lock.json",
            "internal/router/service_test.go",
            "internal/router/usage_db_test.go",
            "scripts/check_docs_public_face_test.py",
            "scripts/test_k8s_nvidia_local_serving.sh",
        ):
            with self.subTest(relative=relative):
                self.assertNotIn(checker.ROOT / relative, self.files)

    def test_is_branding_file_rejects_excluded_paths(self) -> None:
        web = checker.ROOT / "internal" / "router" / "admindist" / "web"
        for path in (
            web / "node_modules" / "pkg" / "index.js",
            checker.ROOT / "internal" / "router" / "docsdist" / "index.html",
            checker.ROOT / "docs-site" / "build" / "index.html",
            checker.ROOT / "docs-site" / "package-lock.json",
            checker.ROOT / "internal" / "router" / "service_test.go",
            checker.ROOT / "scripts" / "reasoning_smoke_test.py",
        ):
            with self.subTest(path=str(path)):
                self.assertFalse(checker.is_branding_file(path))

    def test_branding_paths_all_exist(self) -> None:
        missing = [str(path) for path in checker.BRANDING_PATHS if not path.exists()]

        self.assertEqual([], missing)


class EntryPointTest(unittest.TestCase):
    """Exercise main() end to end over a synthetic tree."""

    def run_main(self, tree: Path) -> tuple[int, str, str]:
        stdout, stderr = io.StringIO(), io.StringIO()
        with mock.patch.object(checker, "ROOT", tree), mock.patch.object(
            checker, "PUBLIC_DOC_PATHS", []
        ), mock.patch.object(checker, "BRANDING_PATHS", [tree / "docs", tree / "README.md"]):
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = checker.main()
        return code, stdout.getvalue(), stderr.getvalue()

    def test_entry_point_fails_on_obsolete_titles_and_passes_once_fixed(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            tree = Path(raw)
            (tree / "docs").mkdir()
            (tree / "README.md").write_text("# Metrum AI Router\n", encoding="utf-8")
            stale = tree / "docs" / "internal-playbook.md"
            stale.write_text(
                "# GenAI Smart Router playbook\n\nThe router routes requests.\n",
                encoding="utf-8",
            )

            code, _, stderr = self.run_main(tree)
            self.assertEqual(1, code)
            self.assertIn("public docs QA failed", stderr)
            self.assertIn(
                "docs/internal-playbook.md:1: contains obsolete product title GenAI Smart Router",
                stderr,
            )

            stale.write_text(
                "# Metrum AI Router playbook\n\nThe router routes requests.\n",
                encoding="utf-8",
            )
            code, stdout, _ = self.run_main(tree)
            self.assertEqual(0, code)
            self.assertIn("public docs QA passed", stdout)


if __name__ == "__main__":
    unittest.main()
