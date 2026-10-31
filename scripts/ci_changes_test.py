#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
import unittest
from ci_changes import select


class ChangesTest(unittest.TestCase):
    def test_empty_is_conservative(self):
        self.assertEqual(select([]), (True, True))

    def test_docs_only(self):
        self.assertEqual(select(['README.md', 'docs/guide.md']), (False, False))
        self.assertEqual(select(['examples/case-studies/readme.md']), (False, True))

    def test_mixed_and_ci_changes(self):
        self.assertEqual(select(['README.md', 'internal/router/route.go']), (True, False))
        self.assertEqual(select(['Makefile']), (True, True))
        self.assertEqual(select(['examples/case-studies/test_x.py']), (True, True))
        self.assertEqual(select(['docs-site/config.js']), (True, False))
        self.assertEqual(select(['internal/router/docsdist/embedded.md']), (True, False))
        self.assertEqual(select(['internal/router/code.go', 'docs/code.md']), (True, False))


if __name__ == '__main__':
    unittest.main()
