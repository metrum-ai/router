#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import tempfile
import unittest
from pathlib import Path

from check_docs_eligibility_errors import check


class EligibilityDocsCheckTest(unittest.TestCase):
    def test_rejects_stale_current_claim_and_allows_historical_release_notes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "release-notes").mkdir()
            (root / "release-notes" / "old.md").write_text("502 no-eligible-target for tools\n")
            (root / "current.md").write_text("502 no-eligible-target for tools\n")
            self.assertEqual(check(root), ["current.md:1: unapproved no-eligible-target mention"])

    def test_approved_line_cannot_be_extended(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "faq.md").write_text("An empty group or a later contract-filter failure can return `502 no-eligible-target`. Also for tools.\n")
            self.assertEqual(check(root), ["faq.md:1: unapproved no-eligible-target mention"])

    def test_repository_docs_pass(self):
        root = Path(__file__).resolve().parent.parent / "docs-site" / "docs"
        self.assertEqual(check(root), [])


if __name__ == "__main__":
    unittest.main()
