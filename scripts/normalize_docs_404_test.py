# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Tests for deterministic Docusaurus 404 metadata."""

from __future__ import annotations

import unittest

from normalize_docs_404 import normalize_404_html


class NormalizeDocs404Test(unittest.TestCase):
    def test_normalizes_every_generated_404_url_and_is_idempotent(self) -> None:
        document = '''<!doctype html><html><head><title>Page Not Found</title>
<meta property="og:url" content="https://docs.example/docs/404.html">
<link rel="canonical" href="https://docs.example/docs/404.html">
<link rel="alternate" href="https://docs.example/docs/404.html" hreflang="en">
<link rel="alternate" href="https://docs.example/docs/404.html" hreflang="x-default">
<a href="https://docs.example/docs/404.html?download=1">download</a></head><body></body></html>div>
</body>
</html>'''

        normalized = normalize_404_html(document)

        self.assertEqual(normalized.count("/docs/404\""), 4)
        self.assertNotIn("/docs/404.html\"", normalized)
        self.assertIn("/docs/404.html?download=1", normalized)
        self.assertTrue(normalized.endswith("</html>"))
        self.assertEqual(normalize_404_html(normalized), normalized)

    def test_accepts_already_stable_output(self) -> None:
        document = (
            '<title>Page Not Found</title>'
            '<link rel="canonical" href="https://docs.example/docs/404"></html>'
        )
        self.assertEqual(normalize_404_html(document), document)

    def test_rejects_unexpected_404_shape(self) -> None:
        with self.assertRaisesRegex(ValueError, "missing its title or canonical link"):
            normalize_404_html("<html></html>")


if __name__ == "__main__":
    unittest.main()
