#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Tests for the Metrum AI Router public docs origin check."""

from __future__ import annotations

import json
import shutil
import subprocess
import unittest
from pathlib import Path

import canonical_product as product
import check_docs_origin as checker


TEMPORARY = "https://llm-api.apps.metrum.ai"
PERMANENT = "https://docs.metrum.ai"
README = Path("README.md")
CHANGELOG = Path("CHANGELOG.md")

PENDING = checker.OriginPolicy(
    canonical=TEMPORARY, allowed=(TEMPORARY,), temporary=TEMPORARY, permanent=PERMANENT
)
SWITCHED = checker.OriginPolicy(
    canonical=PERMANENT, allowed=(PERMANENT,), temporary=TEMPORARY, permanent=PERMANENT
)


class DocsOriginCheckTest(unittest.TestCase):
    def errors(self, line: str, policy: checker.OriginPolicy, rel: Path = README) -> list[str]:
        return list(checker.line_errors(rel, 1, line, policy))

    def test_current_policy_matches_canonical_product(self) -> None:
        policy = checker.current_policy()
        self.assertEqual(product.DOCS_SITE_ORIGIN, policy.canonical)
        self.assertEqual(product.allowed_docs_origins(), policy.allowed)

    def test_pending_allows_temporary_and_rejects_permanent(self) -> None:
        self.assertEqual([], self.errors(f"[Overview]({TEMPORARY}/docs/overview#top)", PENDING))
        errors = self.errors(f"See {PERMANENT}/docs/overview.", PENDING)
        self.assertEqual(1, len(errors), errors)
        self.assertIn("does not serve /docs/ yet", errors[0])
        self.assertIn(f"'{PERMANENT}/docs/overview'", errors[0])

    def test_switched_rejects_temporary_outside_history(self) -> None:
        self.assertEqual([], self.errors(f"{PERMANENT}/docs/routing/learned-routing-policy", SWITCHED))
        errors = self.errors(f"- [LRP]({TEMPORARY}/docs/routing/learned-routing-policy)", SWITCHED)
        self.assertEqual(1, len(errors), errors)
        self.assertIn("non-canonical docs origin", errors[0])
        self.assertEqual([], self.errors(f"Docs URL remains `{TEMPORARY}/docs`", SWITCHED, CHANGELOG))

    def test_rejects_other_docs_origins(self) -> None:
        for url in (
            "https://llm-api.metrum.ai/docs/overview",
            "http://llm-api.apps.metrum.ai/docs/overview",
            "https://docs.staging.metrum.ai/overview",
            "https://metrum-ai.github.io/router/",
        ):
            with self.subTest(url=url):
                self.assertEqual(1, len(self.errors(url, PENDING)), url)
                self.assertEqual(1, len(self.errors(url, SWITCHED)), url)

    def test_ignores_non_docs_urls_and_exceptions(self) -> None:
        for line in (
            "curl https://<router-host>/docs/overview",
            "open http://localhost:8080/docs/",
            "schema https://metrum.ai/schemas/lrp-routing-benchmark/v1.json",
            "see https://www.metrum.ai/llms.txt and https://metrum.ai/router",
            f"{PERMANENT}/docs/overview <!-- docs-origin-exception: example -->",
        ):
            with self.subTest(line=line):
                self.assertEqual([], self.errors(line, PENDING))

    def test_rewrite_keeps_paths_and_fragments(self) -> None:
        text = (
            f"[a]({TEMPORARY}/docs/configuration/dynamic-score-routing#weights)\n"
            f"bare {TEMPORARY}/docs.\n"
            f"api {TEMPORARY}/v1/models\n"
        )
        rewritten = checker.rewrite_text(README, text, SWITCHED)
        self.assertIn(f"[a]({PERMANENT}/docs/configuration/dynamic-score-routing#weights)", rewritten)
        self.assertIn(f"bare {PERMANENT}/docs.\n", rewritten)
        self.assertIn(f"api {TEMPORARY}/v1/models", rewritten)
        self.assertEqual([], checker.scan_text(README, rewritten, SWITCHED))
        self.assertEqual(text, checker.rewrite_text(CHANGELOG, text, SWITCHED))

    def test_repository_passes(self) -> None:
        policy = checker.current_policy()
        errors: list[str] = []
        for path in checker.scan_paths(checker.ROOT):
            errors.extend(
                checker.scan_text(path.relative_to(checker.ROOT), path.read_text(encoding="utf-8"), policy)
            )
        self.assertEqual([], errors)


@unittest.skipIf(shutil.which("node") is None, "node is required for docs-site helper tests")
class RouterEndpointHostTest(unittest.TestCase):
    """Exercise docs-site/src/components/docsHost.mjs used by RouterEndpoint.js."""

    def classify(self, hostname: str, pathname: str, extra: list[str] | None = None) -> bool:
        helper = checker.ROOT / "docs-site" / "src" / "components" / "docsHost.mjs"
        script = """
const [helper, configPath, hostname, pathname, extra] = process.argv.slice(1);
const { readFileSync } = await import("node:fs");
const { isEmbeddedRouterDocsLocation } = await import(helper);
const config = JSON.parse(readFileSync(configPath, "utf8"));
const result = isEmbeddedRouterDocsLocation({ hostname, pathname }, config, JSON.parse(extra));
process.stdout.write(JSON.stringify(result));
"""
        result = subprocess.run(
            [
                "node",
                "--input-type=module",
                "-e",
                script,
                helper.as_uri(),
                str(product.DOCS_ORIGIN_CONFIG_PATH),
                hostname,
                pathname,
                json.dumps(extra or []),
            ],
            check=True,
            text=True,
            capture_output=True,
        )
        return json.loads(result.stdout)

    def test_standalone_hosts_keep_placeholder(self) -> None:
        self.assertFalse(self.classify("docs.metrum.ai", "/docs/overview"))
        self.assertFalse(self.classify("llm-api.apps.metrum.ai", "/docs/overview"))
        self.assertFalse(self.classify("DOCS.METRUM.AI", "/docs"))
        self.assertFalse(self.classify("staging.example.com", "/docs/", ["https://staging.example.com"]))

    def test_embedded_router_docs_use_browser_origin(self) -> None:
        self.assertTrue(self.classify("router.customer.example", "/docs/overview"))
        self.assertTrue(self.classify("127.0.0.1", "/docs"))
        self.assertFalse(self.classify("router.customer.example", "/documentation"))
        self.assertFalse(self.classify("", "/docs/"))


if __name__ == "__main__":
    raise SystemExit(unittest.main())
